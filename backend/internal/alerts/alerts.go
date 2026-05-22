package alerts

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"argon-watch-go/internal/config"
)

type AlertEngine struct {
	config       config.AlertsConfig
	notifier     *Notifier
	alertStates  map[string]*AlertState
	alertHistory []AlertHistory
	mu           sync.Mutex
	broadcast    func(string, interface{})
	diagnoser    DiagnosticsHook // Phase 7; nil means no AI diagnosis
}

// DiagnosticsHook is the seam the AI package fulfils to add proactive
// incident analysis without creating an import cycle (the alerts
// package would otherwise need to depend on internal/ai which itself
// depends on internal/alerts.GetHistory via the tool surface).
type DiagnosticsHook interface {
	OnAlertTriggered(a AlertHistory)
}

// SetDiagnoser is called from main.go after both engines are built.
func (e *AlertEngine) SetDiagnoser(h DiagnosticsHook) {
	e.mu.Lock()
	e.diagnoser = h
	e.mu.Unlock()
}

type AlertState struct {
	Triggered      bool
	Since          time.Time
	Value          interface{}
	Alerted        bool
	Acknowledged   bool
	LastNotifiedAt time.Time // Phase 5 rate-limit gate
}

// DefaultCooldown is applied to rules that don't set CooldownMs. 60s
// matches the plan's "1 per minute per rule" budget; operators can
// shorten or zero it per rule.
const DefaultCooldown = 60 * time.Second

type AlertHistory struct {
	ID        string      `json:"id"`
	ServerID  string      `json:"serverId,omitempty"` // Phase 5: which server tripped the rule
	RuleID    string      `json:"ruleId"`
	RuleName  string      `json:"ruleName"`
	Metric    string      `json:"metric"`
	Value     interface{} `json:"value"`
	Threshold float64     `json:"threshold"`
	Severity  string      `json:"severity"`
	Timestamp time.Time   `json:"timestamp"`
	Status    string      `json:"status"` // "triggered", "resolved"
}

func NewAlertEngine(cfg config.AlertsConfig, notifications config.NotificationsConfig, broadcast func(string, interface{})) *AlertEngine {
	return &AlertEngine{
		config:       cfg,
		notifier:     NewNotifier(notifications),
		alertStates:  make(map[string]*AlertState),
		broadcast:    broadcast,
		alertHistory: make([]AlertHistory, 0),
	}
}

// CheckMetrics evaluates every enabled rule against a metrics map for
// the implicit local server. Kept for v1 callers that haven't been
// taught about server scoping yet.
func (e *AlertEngine) CheckMetrics(metrics interface{}) {
	e.CheckMetricsForServer("local", metrics)
}

// CheckMetricsForServer evaluates every enabled rule against a metrics
// map scoped to one server. Rule state is keyed by (ruleID, serverID)
// so the same threshold can be tracked independently per server — a
// CPU spike on server A doesn't suppress an alert from server B.
func (e *AlertEngine) CheckMetricsForServer(serverID string, metrics interface{}) {
	if !e.config.Enabled {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now()
	for _, rule := range e.config.Rules {
		if !rule.Enabled {
			continue
		}
		// Phase 5: per-server allow-list. Empty list means "all servers".
		if len(rule.ServerIDs) > 0 && !containsString(rule.ServerIDs, serverID) {
			continue
		}

		val := getMetricValue(metrics, rule.Metric)
		if val == nil {
			continue
		}

		valFloat, ok := toFloat(val)
		if !ok {
			continue
		}

		triggered := evaluateCondition(valFloat, rule.Condition, rule.Threshold)

		stateKey := rule.ID + "@" + serverID
		state, exists := e.alertStates[stateKey]
		if !exists {
			state = &AlertState{}
			e.alertStates[stateKey] = state
		}

		if triggered {
			if !state.Triggered {
				// Just triggered
				state.Triggered = true
				state.Since = now
				state.Value = valFloat
				state.Acknowledged = false

				// Check instant trigger
				if rule.Duration == 0 && e.cooldownExpired(state, rule, now) {
					e.triggerAlert(rule, serverID, valFloat)
					state.Alerted = true
					state.LastNotifiedAt = now
				}
			} else {
				// Still triggered, check duration
				if !state.Alerted &&
					now.Sub(state.Since) >= time.Duration(rule.Duration)*time.Millisecond &&
					e.cooldownExpired(state, rule, now) {
					e.triggerAlert(rule, serverID, valFloat)
					state.Alerted = true
					state.LastNotifiedAt = now
				}
			}
		} else {
			if state.Triggered {
				// Resolved — surfaces even while in cooldown so the UI
				// flips back to "ok" promptly.
				e.resolveAlert(rule, serverID, valFloat)
				delete(e.alertStates, stateKey)
			}
		}
	}
}

// cooldownExpired returns true when enough time has passed since the
// last notification fired for this (rule, server). Rules can set
// CooldownMs=-1 to opt out entirely (e.g. test alerts).
func (e *AlertEngine) cooldownExpired(state *AlertState, rule config.AlertRule, now time.Time) bool {
	if rule.CooldownMs < 0 {
		return true
	}
	cd := DefaultCooldown
	if rule.CooldownMs > 0 {
		cd = time.Duration(rule.CooldownMs) * time.Millisecond
	}
	if state.LastNotifiedAt.IsZero() {
		return true
	}
	return now.Sub(state.LastNotifiedAt) >= cd
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func (e *AlertEngine) triggerAlert(rule config.AlertRule, serverID string, value float64) {
	alert := AlertHistory{
		ID:        fmt.Sprintf("%s-%s-%d", rule.ID, serverID, time.Now().UnixNano()),
		ServerID:  serverID,
		RuleID:    rule.ID,
		RuleName:  rule.Name,
		Metric:    rule.Metric,
		Value:     value,
		Threshold: rule.Threshold,
		Severity:  rule.Severity,
		Timestamp: time.Now(),
		Status:    "triggered",
	}

	e.alertHistory = append(e.alertHistory, alert)
	// Keep history small
	if len(e.alertHistory) > 100 {
		e.alertHistory = e.alertHistory[1:]
	}

	log.Printf("🚨 ALERT: %s [%s] - %s = %v (threshold: %v)", rule.Name, serverID, rule.Metric, value, rule.Threshold)

	e.broadcast("ALERT_TRIGGERED", alert)
	go e.notifier.Notify(alert, rule)
	// Phase 7: fire-and-forget AI diagnosis. The hook owns its own
	// budget + timeout; we never block the alert path on it.
	if e.diagnoser != nil {
		go e.diagnoser.OnAlertTriggered(alert)
	}
}

func (e *AlertEngine) resolveAlert(rule config.AlertRule, serverID string, value float64) {
	alert := AlertHistory{
		ID:        fmt.Sprintf("%s-%s-resolved-%d", rule.ID, serverID, time.Now().UnixNano()),
		ServerID:  serverID,
		RuleID:    rule.ID,
		RuleName:  rule.Name,
		Metric:    rule.Metric,
		Value:     value,
		Threshold: rule.Threshold,
		Severity:  rule.Severity,
		Timestamp: time.Now(),
		Status:    "resolved",
	}

	e.alertHistory = append(e.alertHistory, alert)
	log.Printf("✅ RESOLVED: %s [%s] - %s = %v", rule.Name, serverID, rule.Metric, value)

	e.broadcast("ALERT_RESOLVED", alert)
	go e.notifier.Notify(alert, rule)
}

func (e *AlertEngine) GetActiveAlerts() []AlertHistory {
	e.mu.Lock()
	defer e.mu.Unlock()

	var active []AlertHistory
	for key, state := range e.alertStates {
		if !state.Triggered || !state.Alerted {
			continue
		}
		// Keys are "<ruleID>@<serverID>" after Phase 5; split to surface
		// the originating server in the response.
		ruleID, serverID := splitStateKey(key)

		var rule config.AlertRule
		for _, r := range e.config.Rules {
			if r.ID == ruleID {
				rule = r
				break
			}
		}
		if rule.ID == "" {
			continue
		}
		active = append(active, AlertHistory{
			ServerID:  serverID,
			RuleID:    rule.ID,
			RuleName:  rule.Name,
			Metric:    rule.Metric,
			Value:     state.Value,
			Threshold: rule.Threshold,
			Severity:  rule.Severity,
			Timestamp: state.Since,
			Status:    "triggered",
		})
	}
	return active
}

// splitStateKey reverses the "<ruleID>@<serverID>" packing used in
// CheckMetricsForServer. Falls back to (key, "") for any legacy state
// entries that predate the migration.
func splitStateKey(k string) (ruleID, serverID string) {
	for i := len(k) - 1; i >= 0; i-- {
		if k[i] == '@' {
			return k[:i], k[i+1:]
		}
	}
	return k, ""
}

func (e *AlertEngine) GetHistory() []AlertHistory {
	e.mu.Lock()
	defer e.mu.Unlock()
	// Return copy/slice
	return e.alertHistory
}

// Helper functions

func getMetricValue(data interface{}, path string) interface{} {
	parts := strings.Split(path, ".")
	current := data

	for _, part := range parts {
		if m, ok := current.(map[string]interface{}); ok {
			if val, exists := m[part]; exists {
				current = val
			} else {
				return nil
			}
		} else {
			// struct traversal via reflection could be added if needed,
			// but better to rely on map marshaling for generic paths
			// For now, assume data is converted to map[string]interface{} or similar
			// Or we use reflection.
			// Given Go's strict typing, it's easier if we marshal/unmarshal to map
			// or use reflection.
			return nil
		}
	}
	return current
}

// evaluateCondition checks: val condition threshold
func evaluateCondition(val float64, cond string, threshold float64) bool {
	switch cond {
	case ">", "greater_than":
		return val > threshold
	case "<", "less_than":
		return val < threshold
	case ">=", "greater_equal":
		return val >= threshold
	case "<=", "less_equal":
		return val <= threshold
	case "==", "equals":
		return val == threshold
	case "!=", "not_equals":
		return val != threshold
	}
	return false
}

func toFloat(v interface{}) (float64, bool) {
	switch i := v.(type) {
	case float64:
		return i, true
	case float32:
		return float64(i), true
	case int:
		return float64(i), true
	case int64:
		return float64(i), true
	case int32:
		return float64(i), true
	case string:
		f, err := strconv.ParseFloat(i, 64)
		return f, err == nil
	}
	return 0, false
}
