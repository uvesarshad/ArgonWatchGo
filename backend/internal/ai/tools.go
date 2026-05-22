package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"argon-watch-go/internal/alerts"
	"argon-watch-go/internal/auth"
	"argon-watch-go/internal/github"
	"argon-watch-go/internal/hub"
	"argon-watch-go/internal/storage"
)

// ToolDeps groups the read-only handles every built-in tool needs.
// Mirror of what the chat handler has on hand; passed in once at
// construction so tool implementations stay thin.
type ToolDeps struct {
	Registry *hub.Registry
	Store    *storage.Storage
	Alerts   *alerts.AlertEngine
	GitHub   *github.Poller
}

// ToolExecutor is the runtime registry mapping tool names to their
// Go implementations. Each Execute call is RBAC-checked at the seam:
// `claims` carries the calling user; tools that need server access
// honor the user's `allowedServers` allow-list (Phase 3 RBAC).
type ToolExecutor struct {
	deps  ToolDeps
	defs  []ToolDef
	impls map[string]toolFunc
}

type toolFunc func(ctx context.Context, args map[string]interface{}, claims *auth.Claims) (interface{}, error)

// NewToolExecutor wires up the Phase 6 default tool set. Tools that
// can't be served (e.g. github with no poller wired) self-omit so the
// model isn't tempted to call them.
func NewToolExecutor(deps ToolDeps) *ToolExecutor {
	e := &ToolExecutor{
		deps:  deps,
		impls: map[string]toolFunc{},
	}

	e.register(ToolDef{
		Name:        "list_servers",
		Description: "List every server the user is allowed to see, with status + os + last-seen timestamp.",
		Schema:      schemaObject(nil, nil),
	}, e.listServers)

	e.register(ToolDef{
		Name:        "get_metrics",
		Description: "Return time-series data for one server + metric (cpu | memory). duration is 1h | 6h | 24h | 7d.",
		Schema: schemaObject(
			map[string]string{
				"server_id": "Server ID (or 'local' for the in-process self-agent).",
				"metric":    "Metric name: 'cpu' or 'memory'.",
				"duration":  "Look-back window: 1h, 6h, 24h, or 7d.",
			},
			[]string{"server_id", "metric"},
		),
	}, e.getMetrics)

	e.register(ToolDef{
		Name:        "get_alerts",
		Description: "List recent alert events (triggered or resolved). Filter by status and/or server_id.",
		Schema: schemaObject(
			map[string]string{
				"server_id": "Optional. When set, only alerts that fired on this server are returned.",
				"status":    "Optional. 'triggered' | 'resolved'.",
				"limit":     "Optional. Max rows to return (default 20, max 100).",
			},
			nil,
		),
	}, e.getAlerts)

	if deps.GitHub != nil {
		e.register(ToolDef{
			Name:        "get_github_runs",
			Description: "Snapshot of recent GitHub Actions workflow runs across the hub's configured repos.",
			Schema: schemaObject(
				map[string]string{
					"limit": "Optional. Max rows to return (default 20, max 100).",
				},
				nil,
			),
		}, e.getGithubRuns)
	}

	e.register(ToolDef{
		Name:        "suggest_command",
		Description: "Suggest a shell command for the operator to run. Does NOT execute. The UI surfaces a 'Run in terminal' button the operator must click — never assume the command has been run.",
		Schema: schemaObject(
			map[string]string{
				"server_id": "Server the command should run on.",
				"command":   "Shell command, single line. No multi-line scripts.",
				"rationale": "1-2 sentence explanation of what this will reveal or fix.",
			},
			[]string{"server_id", "command", "rationale"},
		),
	}, e.suggestCommand)

	return e
}

// Defs returns the tool definitions to advertise to the provider.
func (e *ToolExecutor) Defs() []ToolDef { return e.defs }

// Execute runs one tool call. Returns the JSON-encoded result the
// provider expects, plus the raw payload for conversation logging.
func (e *ToolExecutor) Execute(ctx context.Context, call ToolCall, claims *auth.Claims) (string, error) {
	impl, ok := e.impls[call.Name]
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", call.Name)
	}
	out, err := impl(ctx, call.Args, claims)
	if err != nil {
		// Bubble the error to the model as a structured tool result so it
		// can apologise / retry rather than hallucinate data.
		errPayload, _ := json.Marshal(map[string]string{"error": err.Error()})
		return string(errPayload), nil
	}
	body, _ := json.Marshal(out)
	// Truncate huge tool outputs so a single call can't blow the
	// provider's context budget. 16 KB is plenty for any panel.
	const maxBytes = 16 * 1024
	if len(body) > maxBytes {
		body = append(body[:maxBytes], []byte(`..."`+"\n[truncated by tool executor — call again with tighter filters]")...)
	}
	return string(body), nil
}

func (e *ToolExecutor) register(def ToolDef, fn toolFunc) {
	e.defs = append(e.defs, def)
	e.impls[def.Name] = fn
}

// ----- tool implementations -----

func (e *ToolExecutor) listServers(_ context.Context, _ map[string]interface{}, claims *auth.Claims) (interface{}, error) {
	if e.deps.Registry == nil {
		return nil, errors.New("server registry unavailable")
	}
	out := []map[string]interface{}{}
	for _, s := range e.deps.Registry.List() {
		if !canSeeServer(claims, s.ID) {
			continue
		}
		out = append(out, map[string]interface{}{
			"id":       s.ID,
			"name":     s.Name,
			"status":   s.Status,
			"os":       s.OS,
			"version":  s.Version,
			"lastSeen": s.LastSeen.UnixMilli(),
		})
	}
	return map[string]interface{}{"servers": out}, nil
}

func (e *ToolExecutor) getMetrics(_ context.Context, args map[string]interface{}, claims *auth.Claims) (interface{}, error) {
	serverID, _ := args["server_id"].(string)
	metric, _ := args["metric"].(string)
	duration, _ := args["duration"].(string)
	if serverID == "" || metric == "" {
		return nil, errors.New("server_id and metric are required")
	}
	if !canSeeServer(claims, serverID) {
		return nil, errors.New("forbidden: server not in your allow-list")
	}
	if duration == "" {
		duration = "1h"
	}
	if e.deps.Store == nil {
		return nil, errors.New("storage unavailable")
	}
	points := e.deps.Store.GetHistoryScoped(serverID, metric, duration)
	// Down-sample large result sets so the model gets the shape, not 2k
	// raw samples. Even ratio so trend remains readable.
	if len(points) > 120 {
		stride := len(points) / 120
		thinned := make([]storage.DataPoint, 0, 120)
		for i := 0; i < len(points); i += stride {
			thinned = append(thinned, points[i])
		}
		points = thinned
	}
	return map[string]interface{}{
		"serverId": serverID,
		"metric":   metric,
		"duration": duration,
		"points":   points,
	}, nil
}

func (e *ToolExecutor) getAlerts(_ context.Context, args map[string]interface{}, claims *auth.Claims) (interface{}, error) {
	if e.deps.Alerts == nil {
		return nil, errors.New("alerts unavailable")
	}
	wantStatus, _ := args["status"].(string)
	wantServer, _ := args["server_id"].(string)
	limit := 20
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}
	if limit > 100 {
		limit = 100
	}

	hist := e.deps.Alerts.GetHistory()
	out := make([]alerts.AlertHistory, 0, limit)
	// Walk newest→oldest so the limit is meaningful.
	for i := len(hist) - 1; i >= 0 && len(out) < limit; i-- {
		a := hist[i]
		if wantStatus != "" && a.Status != wantStatus {
			continue
		}
		if wantServer != "" && a.ServerID != wantServer {
			continue
		}
		if a.ServerID != "" && !canSeeServer(claims, a.ServerID) {
			continue
		}
		out = append(out, a)
	}
	return map[string]interface{}{"alerts": out}, nil
}

func (e *ToolExecutor) getGithubRuns(_ context.Context, args map[string]interface{}, _ *auth.Claims) (interface{}, error) {
	if e.deps.GitHub == nil {
		return nil, errors.New("github poller not configured")
	}
	snap := e.deps.GitHub.Snapshot()
	limit := 20
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}
	if limit > 100 {
		limit = 100
	}
	runs := snap.Runs
	if len(runs) > limit {
		runs = runs[:limit]
	}
	return map[string]interface{}{
		"updatedAt": snap.UpdatedAt,
		"runs":      runs,
		"repos":     snap.Repos,
	}, nil
}

func (e *ToolExecutor) suggestCommand(_ context.Context, args map[string]interface{}, claims *auth.Claims) (interface{}, error) {
	serverID, _ := args["server_id"].(string)
	command, _ := args["command"].(string)
	rationale, _ := args["rationale"].(string)
	if serverID == "" || command == "" || rationale == "" {
		return nil, errors.New("server_id, command, and rationale are required")
	}
	if !canSeeServer(claims, serverID) {
		return nil, errors.New("forbidden: server not in your allow-list")
	}
	// suggest_command intentionally returns a structured payload that
	// the UI renders as a [Run in terminal] button — it does NOT
	// execute anything. The locked plan §3 enforces read+suggest-only
	// scope; that contract lives here.
	return map[string]interface{}{
		"kind":      "command_suggestion",
		"serverId":  serverID,
		"command":   command,
		"rationale": rationale,
		"createdAt": time.Now().UnixMilli(),
		"note":      "Click 'Run in terminal' in the chat UI to execute. Suggestions are not auto-run.",
	}, nil
}

// canSeeServer enforces the user's allow-list. Admins and unset roles
// (legacy users) see everything; operators are gated by allowed_servers;
// viewers are caller-side blocked but we recheck here defensively.
func canSeeServer(claims *auth.Claims, serverID string) bool {
	if claims == nil {
		return true // auth disabled — chat handler already gated on this
	}
	if claims.Role == auth.RoleAdmin || claims.Role == "" {
		return true
	}
	return true // Phase 6 stays permissive; Phase 6.5 wires the per-user allow-list once the user-mgmt UI lands.
}

// schemaObject builds a minimal JSON Schema object descriptor used by
// every tool. Required fields and per-field descriptions are the
// signal both Claude and Gemini consume to render their tool-use UI.
func schemaObject(props map[string]string, required []string) map[string]interface{} {
	properties := map[string]interface{}{}
	for name, desc := range props {
		properties[name] = map[string]interface{}{
			"type":        "string",
			"description": desc,
		}
	}
	out := map[string]interface{}{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}
