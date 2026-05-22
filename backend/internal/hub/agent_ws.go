package hub

import (
	"log"
	"net/http"
	"time"

	"argon-watch-go/internal/alerts"
	"argon-watch-go/internal/realtime"
	"argon-watch-go/internal/storage"
	"argon-watch-go/internal/transport"

	"github.com/gorilla/websocket"
)

// agentUpgrader is separate from the browser upgrader so we can apply
// stricter origin checks later. For Phase 1 it accepts any origin (agents
// dial directly without a browser).
var agentUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// AgentWSHandler returns an http.HandlerFunc that owns the hub side of the
// agent ↔ hub WebSocket. Agents connect with ?id=<serverId>&token=<token>;
// successfully authenticated, every envelope they send is rebroadcast to
// browser clients via the realtime hub (transparently scoped to their ID).
func AgentWSHandler(reg *Registry, conns *Connections, rt *realtime.Hub, store *storage.Storage, terms *TerminalProxy, ae *alerts.AlertEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		serverID := r.URL.Query().Get("id")
		token := r.URL.Query().Get("token")
		if serverID == "" || token == "" {
			http.Error(w, "missing id or token", http.StatusUnauthorized)
			return
		}
		if _, err := reg.Authenticate(serverID, token); err != nil {
			// Constant-ish error to avoid leaking which arg was wrong.
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}

		conn, err := agentUpgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("agent_ws: upgrade %s: %v", serverID, err)
			return
		}
		log.Printf("agent connected: %s", serverID)

		agent := conns.Attach(serverID, conn)
		defer conns.Detach(serverID)

		// Read deadline reset on pong + on every received message so a quiet
		// agent doesn't hold an idle connection forever. 60s is comfortable
		// at the spec'd 10s heartbeat cadence.
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			return nil
		})

		for {
			env, err := agent.Read()
			if err != nil {
				log.Printf("agent disconnected: %s (%v)", serverID, err)
				return
			}
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))

			// Trust the connection's authenticated serverID over whatever
			// the agent claims in the envelope — prevents one agent from
			// publishing under another's identity.
			env.ServerID = serverID

			switch env.Type {
			case transport.MsgHeartbeat:
				if hb, ok := env.Payload.(map[string]interface{}); ok {
					osName, _ := hb["os"].(string)
					version, _ := hb["version"].(string)
					reg.MarkOnline(serverID, osName, version)
				} else {
					reg.MarkOnline(serverID, "", "")
				}
				continue
			case transport.MsgSystemMetrics:
				// Persist the same data points that the local self-agent
				// writes through monitor/system.go. Without this, the
				// scoped history endpoint returns null for remote agents.
				if store != nil {
					storeRemoteSystemMetrics(store, serverID, env.Payload)
				}
				// Phase 5: also run the alert engine against this server's
				// metrics. Without this, alert rules with serverIds
				// pointing at remote agents would never fire.
				if ae != nil {
					if m, ok := env.Payload.(map[string]interface{}); ok {
						ae.CheckMetricsForServer(serverID, m)
					}
				}
			case transport.MsgTerminalOut, transport.MsgTerminalExit:
				// Terminal output rides the same agent connection as
				// metrics but routes through the proxy, not the
				// browser hub fan-out.
				if terms != nil {
					terms.DeliverFromAgent(env)
				}
				continue
			}

			// Default: route to browsers as a scoped broadcast.
			rt.BroadcastFor(serverID, env.Type, env.Payload)
		}
	}
}

// storeRemoteSystemMetrics pulls the CPU + memory percentages out of a
// SYSTEM_METRICS payload coming from a remote agent and persists them with
// the agent's serverID. Mirrors monitor/system.go::collectMetrics so the
// scoped history endpoint returns data for remote agents too.
//
// The payload is whatever JSON the agent serialized — for our own agents
// that's the CompleteSystemMetrics struct; for hypothetical third-party
// agents we still do best-effort extraction and silently skip what we
// can't find.
func storeRemoteSystemMetrics(store *storage.Storage, serverID string, payload interface{}) {
	m, ok := payload.(map[string]interface{})
	if !ok {
		return
	}
	if cpu, ok := m["cpu"].(map[string]interface{}); ok {
		if load, ok := cpu["load"].(float64); ok {
			store.AddDataPointScoped(serverID, "cpu", load)
		}
	}
	if mem, ok := m["memory"].(map[string]interface{}); ok {
		if pct, ok := mem["percentage"].(float64); ok {
			store.AddDataPointScoped(serverID, "memory", pct)
		}
	}
}
