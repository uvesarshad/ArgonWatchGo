package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"argon-watch-go/internal/storage"
)

// Diagnoser is the proactive incident analyser. When an alert fires,
// the engine fires-and-forgets a Diagnose() call; this package builds
// a small context window (last 15 minutes of metrics for the affected
// server + the alert itself), asks the configured AI provider for a
// 2-3 sentence diagnosis, and surfaces the result through whatever
// sink the operator wires up (broadcast envelope, Telegram, etc).
//
// Phase 7 keeps the diagnoser strictly read-only — it never proposes
// commands, never executes anything. The interactive AI sidebar is
// where actionable suggestions belong; this is the cheap, automatic
// "what just happened?" annotation that rides on the alert itself.
type Diagnoser struct {
	store     *Store
	tools     *ToolExecutor
	metrics   *storage.Storage
	dailyCap  int            // tokens per day; 0 disables the cap
	tokensToday atomic.Int64
	resetDate atomic.Int64    // day-of-year boundary
	mu        sync.Mutex
	sinks     []DiagnosisSink
}

// DiagnosisSink is anything that wants notified when a diagnosis lands.
// Two ship in Phase 7: the realtime hub (broadcasts ALERT_DIAGNOSIS to
// browsers) and the Telegram notifier (appends a second line to the
// outbound alert message).
type DiagnosisSink interface {
	OnDiagnosis(d Diagnosis)
}

// Diagnosis is the payload sinks receive. AlertID lets sinks tie the
// diagnosis back to the originating AlertHistory row (frontend stores
// alerts by ID in a ring buffer).
type Diagnosis struct {
	AlertID   string    `json:"alertId"`
	ServerID  string    `json:"serverId"`
	RuleName  string    `json:"ruleName"`
	Summary   string    `json:"summary"`
	Tokens    int       `json:"tokens"`
	CreatedAt time.Time `json:"createdAt"`
}

// AlertContext is what we pass to Diagnose. Mirror of what the alert
// engine has on hand — kept here as a local struct so the alerts
// package doesn't need to import ai (which would create a cycle since
// the diagnoser uses alerts.History via the tool surface).
type AlertContext struct {
	AlertID   string
	ServerID  string
	RuleID    string
	RuleName  string
	Metric    string
	Value     float64
	Threshold float64
	Severity  string
	Status    string
	TS        time.Time
}

func NewDiagnoser(store *Store, tools *ToolExecutor, metrics *storage.Storage, dailyTokenCap int) *Diagnoser {
	return &Diagnoser{
		store:    store,
		tools:    tools,
		metrics:  metrics,
		dailyCap: dailyTokenCap,
	}
}

// Subscribe registers a sink. Sinks fire synchronously inside the
// diagnoser goroutine so they should be quick — broadcast to a channel,
// not block on a slow HTTP POST.
func (d *Diagnoser) Subscribe(s DiagnosisSink) {
	d.mu.Lock()
	d.sinks = append(d.sinks, s)
	d.mu.Unlock()
}

// Diagnose runs one AI call in a background goroutine. Returns
// immediately; consumers receive results via subscribed sinks. Safe to
// call when no provider is configured (silently no-ops).
func (d *Diagnoser) Diagnose(parent context.Context, ctx AlertContext) {
	// Only diagnose triggers — resolutions just generate noise.
	if ctx.Status != "triggered" {
		return
	}
	// Daily cap. Without this, a flapping alert could burn through the
	// provider's monthly budget in an afternoon.
	if !d.hasBudget() {
		return
	}

	go d.run(parent, ctx)
}

func (d *Diagnoser) run(parent context.Context, alert AlertContext) {
	// Hard cap on diagnoser work: 30s. The alert UI already shows the
	// raw event immediately; diagnosis is a follow-up annotation.
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	provider, providerName, model, err := pickAnyProvider(d.store)
	if err != nil {
		return // no key configured — skip silently
	}
	_ = providerName // reserved for future per-provider routing

	// Build the context window: alert payload + last 15 min of relevant
	// metrics for the affected server. We keep it small (≤ 60 points
	// per metric down-sampled) so the diagnoser stays cheap.
	contextJSON := d.buildContext(alert)

	prompt := fmt.Sprintf(
		"An alert just fired:\n%s\n\nLast 15 minutes of telemetry for server %q (down-sampled):\n%s\n\n"+
			"Diagnose this in 2-3 sentences. Be specific: name the likely cause, cite the data point that supports it, "+
			"and recommend ONE next-step investigation. Do NOT propose commands — the operator has a separate command-suggestion path.",
		mustJSON(alert), alert.ServerID, contextJSON,
	)

	resp, err := provider.Chat(ctx, []Message{
		{Role: RoleUser, Content: prompt},
	}, nil, ChatOptions{
		Model:     model,
		MaxTokens: 400, // short — we want a paragraph, not an essay
	})
	if err != nil {
		log.Printf("diagnoser: chat: %v", err)
		return
	}

	used := resp.Usage.InputTokens + resp.Usage.OutputTokens
	d.tokensToday.Add(int64(used))

	out := Diagnosis{
		AlertID:   alert.AlertID,
		ServerID:  alert.ServerID,
		RuleName:  alert.RuleName,
		Summary:   resp.Content,
		Tokens:    used,
		CreatedAt: time.Now(),
	}
	log.Printf("📋 diagnosed %s [%s]: %s", alert.RuleName, alert.ServerID, truncate(resp.Content, 120))

	d.mu.Lock()
	sinks := append([]DiagnosisSink(nil), d.sinks...)
	d.mu.Unlock()
	for _, s := range sinks {
		// Each sink in its own goroutine so a slow one (Telegram POST)
		// doesn't block the others.
		go s.OnDiagnosis(out)
	}
}

func (d *Diagnoser) buildContext(alert AlertContext) string {
	if d.metrics == nil {
		return "(no telemetry available)"
	}
	// Pull a fixed set of useful series; we don't dynamically discover
	// the metric the rule references because the alert payload already
	// names it and the LLM can see it from the alert JSON.
	cpu := d.metrics.GetHistoryScoped(alert.ServerID, "cpu", "1h")
	mem := d.metrics.GetHistoryScoped(alert.ServerID, "memory", "1h")
	// 15 minutes of context is enough; trim if we have more.
	cutoff := time.Now().Add(-15 * time.Minute).UnixMilli()
	cpu = trimSince(cpu, cutoff)
	mem = trimSince(mem, cutoff)

	cpu = downsample(cpu, 30)
	mem = downsample(mem, 30)

	b, _ := json.Marshal(map[string]interface{}{
		"cpu":    cpu,
		"memory": mem,
	})
	return string(b)
}

func trimSince(points []storage.DataPoint, cutoffMs int64) []storage.DataPoint {
	// Storage already returns in ascending order; binary-search for the
	// first point newer than cutoff.
	lo, hi := 0, len(points)
	for lo < hi {
		mid := (lo + hi) / 2
		if points[mid].Timestamp < cutoffMs {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return points[lo:]
}

func downsample(points []storage.DataPoint, target int) []storage.DataPoint {
	if len(points) <= target {
		return points
	}
	stride := len(points) / target
	out := make([]storage.DataPoint, 0, target)
	for i := 0; i < len(points); i += stride {
		out = append(out, points[i])
	}
	return out
}

// hasBudget enforces the daily token cap. dailyCap=0 disables the cap.
// Reset key is the (UTC) ordinal day so a process spanning midnight
// resets correctly without a separate cron.
func (d *Diagnoser) hasBudget() bool {
	if d.dailyCap <= 0 {
		return true
	}
	today := int64(time.Now().UTC().YearDay() + time.Now().UTC().Year()*1000)
	if d.resetDate.Swap(today) != today {
		d.tokensToday.Store(0)
	}
	return int(d.tokensToday.Load()) < d.dailyCap
}

// pickAnyProvider returns the first configured provider. We don't honor
// a per-user preference here — diagnoser runs without a user context.
func pickAnyProvider(store *Store) (Provider, string, string, error) {
	providers, err := store.ListProviders()
	if err != nil {
		return nil, "", "", err
	}
	if len(providers) == 0 {
		return nil, "", "", ErrNoProvider
	}
	pick := providers[0].Provider
	apiKey, model, _, err := store.GetKey(pick)
	if err != nil {
		return nil, "", "", err
	}
	switch pick {
	case "claude":
		if model == "" {
			model = defaultClaudeSummary // cheaper model for batch jobs
		}
		return NewClaude(apiKey), pick, model, nil
	case "gemini":
		if model == "" {
			model = defaultGeminiSummary
		}
		return NewGemini(apiKey), pick, model, nil
	}
	return nil, "", "", fmt.Errorf("unknown provider: %s", pick)
}

func mustJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}
