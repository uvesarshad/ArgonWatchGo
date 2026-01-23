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
}

type AlertState struct {
	Triggered    bool
	Since        time.Time
	Value        interface{}
	Alerted      bool
	Acknowledged bool
}

type AlertHistory struct {
	ID        string      `json:"id"`
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

// CheckMetrics takes a metrics map (flattened or nested) and evaluates rules
func (e *AlertEngine) CheckMetrics(metrics interface{}) {
	if !e.config.Enabled {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	for _, rule := range e.config.Rules {
		if !rule.Enabled {
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

		state, exists := e.alertStates[rule.ID]
		if !exists {
			state = &AlertState{}
			e.alertStates[rule.ID] = state
		}

		now := time.Now()

		if triggered {
			if !state.Triggered {
				// Just triggered
				state.Triggered = true
				state.Since = now
				state.Value = valFloat
				state.Acknowledged = false

				// Check instant trigger
				if rule.Duration == 0 {
					e.triggerAlert(rule, valFloat)
					state.Alerted = true
				}
			} else {
				// Still triggered, check duration
				if !state.Alerted && now.Sub(state.Since) >= time.Duration(rule.Duration)*time.Millisecond {
					e.triggerAlert(rule, valFloat)
					state.Alerted = true
				}
			}
		} else {
			if state.Triggered {
				// Resolved
				e.resolveAlert(rule, valFloat)
				// Reset state
				delete(e.alertStates, rule.ID)
			}
		}
	}
}

func (e *AlertEngine) triggerAlert(rule config.AlertRule, value float64) {
	alert := AlertHistory{
		ID:        fmt.Sprintf("%s-%d", rule.ID, time.Now().UnixNano()),
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

	log.Printf("🚨 ALERT: %s - %s = %v (threshold: %v)", rule.Name, rule.Metric, value, rule.Threshold)

	e.broadcast("ALERT_TRIGGERED", alert)
	go e.notifier.Notify(alert, rule)
}

func (e *AlertEngine) resolveAlert(rule config.AlertRule, value float64) {
	alert := AlertHistory{
		ID:        fmt.Sprintf("%s-resolved-%d", rule.ID, time.Now().UnixNano()),
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
	log.Printf("✅ RESOLVED: %s - %s = %v", rule.Name, rule.Metric, value)

	e.broadcast("ALERT_RESOLVED", alert)
	go e.notifier.Notify(alert, rule)
}

func (e *AlertEngine) GetActiveAlerts() []AlertHistory {
	e.mu.Lock()
	defer e.mu.Unlock()

	var active []AlertHistory
	for id, state := range e.alertStates {
		if state.Triggered && state.Alerted {
			// Find rule
			var rule config.AlertRule
			for _, r := range e.config.Rules {
				if r.ID == id {
					rule = r
					break
				}
			}

			// If rule found (it should be)
			if rule.ID != "" {
				active = append(active, AlertHistory{
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
		}
	}
	return active
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
