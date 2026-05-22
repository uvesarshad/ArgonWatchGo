package api

import (
	"encoding/json"
	"net/http"

	"argon-watch-go/internal/auth"
	"argon-watch-go/internal/hub"

	"github.com/gorilla/mux"
)

// mintTerminalSessionHandler creates a session token a browser can use
// to open WS /ws/terminal/{id}. Caller must be authenticated and at
// least operator role.
func mintTerminalSessionHandler(proxy *hub.TerminalProxy) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.GetUserFromContext(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if claims.Role == auth.RoleViewer {
			http.Error(w, "forbidden: terminal requires operator role", http.StatusForbidden)
			return
		}
		serverID := mux.Vars(r)["id"]
		if serverID == "" {
			http.Error(w, "server id required", http.StatusBadRequest)
			return
		}

		sessionID, err := proxy.Mint(serverID, claims.Username)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"sessionId": sessionID,
			"wsPath":    "/ws/terminal/" + sessionID,
		})
	}
}

// terminalAuditHandler lists recent terminal sessions for the audit view.
// Admin only.
func terminalAuditHandler(proxy *hub.TerminalProxy, getDB func() interface{}) http.HandlerFunc {
	// proxy unused for now; the DB query lives on the proxy struct in a
	// future refactor. Phase 3 ships a simple list-recent query inline.
	_ = proxy
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.GetUserFromContext(r)
		if !ok || claims.Role != auth.RoleAdmin {
			http.Error(w, "forbidden: admin only", http.StatusForbidden)
			return
		}
		// Reserved for Phase 3.5 — list endpoint comes when the audit
		// UI lands. For now, a stub so the route exists.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"note":"audit list endpoint stubbed in Phase 3; ships in 3.5"}`))
	}
}
