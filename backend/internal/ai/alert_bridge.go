package ai

import (
	"context"
	"time"

	"argon-watch-go/internal/alerts"
)

// AlertBridge satisfies alerts.DiagnosticsHook by adapting the engine's
// AlertHistory shape to the diagnoser's AlertContext. Lives here (not in
// alerts/) because it imports the ai package — the bridge inverts the
// dependency arrow so the alerts package stays AI-agnostic.
type AlertBridge struct {
	diag *Diagnoser
}

func NewAlertBridge(d *Diagnoser) *AlertBridge { return &AlertBridge{diag: d} }

// OnAlertTriggered is called by the alert engine immediately after a
// trigger broadcasts. Synchronous wrt the goroutine the engine spawns;
// the diagnoser itself is async.
func (b *AlertBridge) OnAlertTriggered(a alerts.AlertHistory) {
	val := 0.0
	if v, ok := a.Value.(float64); ok {
		val = v
	}
	b.diag.Diagnose(context.Background(), AlertContext{
		AlertID:   a.ID,
		ServerID:  a.ServerID,
		RuleID:    a.RuleID,
		RuleName:  a.RuleName,
		Metric:    a.Metric,
		Value:     val,
		Threshold: a.Threshold,
		Severity:  a.Severity,
		Status:    a.Status,
		TS:        a.Timestamp,
	})
}

// BroadcastSink wires a Diagnoser to a realtime broadcast function so
// browsers see new diagnoses live. Pass realtime.Hub.BroadcastFor at
// wiring time.
type BroadcastSink struct {
	broadcast func(serverID, msgType string, data interface{})
}

func NewBroadcastSink(broadcast func(serverID, msgType string, data interface{})) *BroadcastSink {
	return &BroadcastSink{broadcast: broadcast}
}

func (s *BroadcastSink) OnDiagnosis(d Diagnosis) {
	if s.broadcast == nil {
		return
	}
	// Cap browser updates to ALERT_DIAGNOSIS so the frontend can
	// patch the existing alert row in-place. Bound to the server the
	// alert fired on so per-server filters route correctly.
	target := d.ServerID
	if target == "" {
		target = "local"
	}
	_ = time.Now() // touchpoint for future "wallclock vs created_at" logic
	s.broadcast(target, "ALERT_DIAGNOSIS", d)
}
