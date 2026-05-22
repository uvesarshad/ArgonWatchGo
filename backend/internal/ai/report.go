package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"argon-watch-go/internal/alerts"
	"argon-watch-go/internal/hub"
	"argon-watch-go/internal/storage"
)

// Reporter generates scheduled health summaries — weekly by default,
// daily if explicitly enabled. The build path is:
//
//   1. Snapshot the registry + alerts ring + recent metrics per server
//   2. Compress to a JSON brief (small enough for one provider call)
//   3. Ask the AI to write a 1-page narrative
//   4. Store the result + post via subscribed sinks (Telegram, etc.)
//
// The same DiagnosisSink interface is reused — sinks treat a report
// like a long-form diagnosis with AlertID="" so the frontend can route
// it to a "reports" view instead of patching an alert row.
type Reporter struct {
	store    *Store
	metrics  *storage.Storage
	alerts   *alerts.AlertEngine
	registry *hub.Registry

	interval time.Duration
	mu       sync.Mutex
	sinks    []DiagnosisSink
	stop     chan struct{}

	lastReport time.Time
}

func NewReporter(store *Store, metrics *storage.Storage, alertsEng *alerts.AlertEngine, registry *hub.Registry, interval time.Duration) *Reporter {
	if interval <= 0 {
		interval = 7 * 24 * time.Hour
	}
	return &Reporter{
		store:    store,
		metrics:  metrics,
		alerts:   alertsEng,
		registry: registry,
		interval: interval,
		stop:     make(chan struct{}),
	}
}

func (r *Reporter) Subscribe(s DiagnosisSink) {
	r.mu.Lock()
	r.sinks = append(r.sinks, s)
	r.mu.Unlock()
}

// Start launches the scheduling loop. Stop with Stop(). The first
// report fires `interval` after start; manual ad-hoc generation is
// available via GenerateNow.
func (r *Reporter) Start(parent context.Context) {
	go r.loop(parent)
}

func (r *Reporter) Stop() {
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
}

func (r *Reporter) loop(parent context.Context) {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-parent.Done():
			return
		case <-r.stop:
			return
		case <-t.C:
			if err := r.GenerateNow(parent); err != nil {
				log.Printf("reporter: %v", err)
			}
		}
	}
}

// GenerateNow runs one report immediately. Returns the rendered
// Diagnosis (treating the report-as-diagnosis with AlertID="") so
// callers can surface it without subscribing to a sink.
func (r *Reporter) GenerateNow(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()

	provider, _, model, err := pickAnyProvider(r.store)
	if err != nil {
		return fmt.Errorf("no provider configured")
	}

	brief, err := r.buildBrief()
	if err != nil {
		return err
	}

	prompt := fmt.Sprintf(
		"You are writing the weekly ops summary for an ArgonWatchGo install.\n\n"+
			"Compiled brief (JSON):\n%s\n\n"+
			"Write a 5-section markdown summary:\n"+
			"1) Fleet status (one line per server)\n"+
			"2) Top 5 alerts this week with counts\n"+
			"3) Notable anomalies or trends\n"+
			"4) Recommended follow-ups (read-only — never propose commands)\n"+
			"5) One paragraph 'what changed since last week'.\n\n"+
			"Be concise and concrete. Use bullets. No filler.",
		brief,
	)

	resp, err := provider.Chat(ctx, []Message{
		{Role: RoleUser, Content: prompt},
	}, nil, ChatOptions{Model: model, MaxTokens: 1200})
	if err != nil {
		return err
	}

	out := Diagnosis{
		AlertID:   "",
		ServerID:  "report",
		RuleName:  "Weekly Health Report",
		Summary:   resp.Content,
		Tokens:    resp.Usage.InputTokens + resp.Usage.OutputTokens,
		CreatedAt: time.Now(),
	}
	r.lastReport = out.CreatedAt
	log.Printf("📊 health report generated (%d tokens)", out.Tokens)

	// Persist the report as a single-message conversation so users can
	// find it later in the chat history.
	_ = r.store.SaveConversation(&Conversation{
		User:  "system",
		Title: fmt.Sprintf("Weekly report — %s", time.Now().UTC().Format("2006-01-02")),
		Model: model,
		Messages: []Message{
			{Role: RoleAssistant, Content: resp.Content},
		},
	})

	r.mu.Lock()
	sinks := append([]DiagnosisSink(nil), r.sinks...)
	r.mu.Unlock()
	for _, s := range sinks {
		go s.OnDiagnosis(out)
	}
	return nil
}

// LastReport returns the wall-clock time of the last successful report
// build. Used by the /api/v2/ai/report-status endpoint so the UI can
// render "next run in 3 days".
func (r *Reporter) LastReport() time.Time { return r.lastReport }

// buildBrief assembles the raw facts the LLM works from. Kept small on
// purpose — token cost matters when this runs on a cron.
func (r *Reporter) buildBrief() (string, error) {
	type serverBrief struct {
		ID      string  `json:"id"`
		Name    string  `json:"name"`
		Status  string  `json:"status"`
		OS      string  `json:"os,omitempty"`
		CPUAvg  float64 `json:"cpuAvg7d"`
		MemAvg  float64 `json:"memAvg7d"`
	}
	type alertBucket struct {
		RuleName string `json:"ruleName"`
		Count    int    `json:"count"`
	}

	briefs := []serverBrief{}
	if r.registry != nil {
		for _, s := range r.registry.List() {
			cpu := r.metrics.GetHistoryScoped(s.ID, "cpu", "7d")
			mem := r.metrics.GetHistoryScoped(s.ID, "memory", "7d")
			briefs = append(briefs, serverBrief{
				ID: s.ID, Name: s.Name, Status: s.Status, OS: s.OS,
				CPUAvg: avgValue(cpu),
				MemAvg: avgValue(mem),
			})
		}
	}

	buckets := map[string]int{}
	if r.alerts != nil {
		for _, a := range r.alerts.GetHistory() {
			if a.Status == "triggered" {
				buckets[a.RuleName]++
			}
		}
	}
	top := topNBuckets(buckets, 5)
	tops := make([]alertBucket, 0, len(top))
	for _, kv := range top {
		tops = append(tops, alertBucket{RuleName: kv.k, Count: kv.v})
	}

	payload := map[string]interface{}{
		"period":   "last 7 days",
		"servers":  briefs,
		"topAlerts": tops,
		"updatedAt": time.Now().UTC(),
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func avgValue(points []storage.DataPoint) float64 {
	if len(points) == 0 {
		return 0
	}
	var sum float64
	for _, p := range points {
		sum += p.Value
	}
	return sum / float64(len(points))
}

type kv struct {
	k string
	v int
}

func topNBuckets(m map[string]int, n int) []kv {
	// Simple O(n*k) selection — n is 5, k is at most a few dozen.
	if n <= 0 {
		return nil
	}
	taken := map[string]bool{}
	out := make([]kv, 0, n)
	for i := 0; i < n; i++ {
		best := kv{k: "", v: -1}
		for k, v := range m {
			if taken[k] {
				continue
			}
			if v > best.v {
				best = kv{k: k, v: v}
			}
		}
		if best.v <= 0 {
			break
		}
		taken[best.k] = true
		out = append(out, best)
	}
	return out
}
