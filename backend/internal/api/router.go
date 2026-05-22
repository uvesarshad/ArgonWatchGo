package api

import (
	"encoding/json"
	"io/fs"
	"net/http"

	"argon-watch-go/internal/alerts"
	"argon-watch-go/internal/auth"
	"argon-watch-go/internal/config"
	"argon-watch-go/internal/github"
	"argon-watch-go/internal/hub"
	"argon-watch-go/internal/realtime"
	"argon-watch-go/internal/storage"

	"github.com/gorilla/mux"
)

// Deps groups every dependency the router needs. v1 routes used positional
// args; v2 adds the multi-server registry + connection manager so the
// helper signature now has enough arguments to deserve a struct.
type Deps struct {
	Config        *config.Config
	Hub           *realtime.Hub
	Store         *storage.Storage
	Alerts        *alerts.AlertEngine
	AuthManager   *auth.Manager
	FrontendFS    fs.FS
	Registry      *hub.Registry
	Connections   *hub.Connections
	TerminalProxy *hub.TerminalProxy
	GitHubPoller  *github.Poller
}

func NewRouter(d Deps) *mux.Router {
	r := mux.NewRouter()

	// Public auth routes
	authRoutes := r.PathPrefix("/api/auth").Subrouter()
	authRoutes.HandleFunc("/check-setup", d.AuthManager.HandleCheckSetup).Methods("GET")
	authRoutes.HandleFunc("/setup", d.AuthManager.HandleSetup).Methods("POST")
	authRoutes.HandleFunc("/login", d.AuthManager.HandleLogin).Methods("POST")
	authRoutes.HandleFunc("/verify-2fa", d.AuthManager.HandleVerify2FA).Methods("POST")
	authRoutes.HandleFunc("/logout", d.AuthManager.HandleLogout).Methods("POST")

	r.HandleFunc("/setup", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, d.FrontendFS, "setup.html")
	}).Methods("GET")

	r.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, d.FrontendFS, "login.html")
	}).Methods("GET")

	// Agent WebSocket: auth is per-agent token (query string), not the
	// JWT browser session. Lives outside the /api auth subtree.
	r.Handle("/agent", hub.AgentWSHandler(d.Registry, d.Connections, d.Hub, d.Store, d.TerminalProxy))

	// Terminal browser WS. Auth is via JWT (cookie/header/query) checked
	// inside the proxy handler — same surface as the agent WS so the
	// reverse-proxy WebSocket config in the README keeps working.
	// Terminals are auth-gated by design: refusing to mount when auth
	// is disabled is the safer default (an unauthenticated PTY on the
	// open internet would be catastrophic).
	if d.TerminalProxy != nil && d.AuthManager != nil {
		r.Handle("/ws/terminal/{id}", d.TerminalProxy.HandleRoute(d.AuthManager.GetJWTManager()))
	}

	// Install scripts: served unauthenticated because the URL itself
	// contains the secret (token query param) and is only ever produced
	// by an authenticated /api/v2/servers POST. Treat the URL like a
	// short-lived bearer.
	r.HandleFunc("/install.sh", installShHandler()).Methods("GET")
	r.HandleFunc("/install.ps1", installPs1Handler()).Methods("GET")

	if d.Config.Auth.Enabled {
		// Browser WS with JWT auth
		r.Handle("/ws", auth.Middleware(d.AuthManager.GetJWTManager())(http.HandlerFunc(d.Hub.ServeWS)))

		api := r.PathPrefix("/api").Subrouter()
		api.Use(func(next http.Handler) http.Handler {
			return auth.Middleware(d.AuthManager.GetJWTManager())(next)
		})
		mountAPIRoutes(api, d)
	} else {
		r.HandleFunc("/ws", d.Hub.ServeWS)
		api := r.PathPrefix("/api").Subrouter()
		mountAPIRoutes(api, d)
	}

	return r
}

func mountAPIRoutes(api *mux.Router, d Deps) {
	// v1 endpoints (unchanged shape; back-compat for any external consumer)
	api.HandleFunc("/config", getConfigHandler(d.Config)).Methods("GET")
	api.HandleFunc("/history/{type}", getHistoryHandler(d.Store)).Methods("GET")
	api.HandleFunc("/alerts/active", getAlertsHandler(d.Alerts)).Methods("GET")
	api.HandleFunc("/alerts/history", getAlertHistoryHandler(d.Alerts)).Methods("GET")
	api.HandleFunc("/auth/me", d.AuthManager.HandleGetMe).Methods("GET")
	api.HandleFunc("/auth/enable-2fa", d.AuthManager.HandleEnable2FA).Methods("POST")
	api.HandleFunc("/auth/disable-2fa", d.AuthManager.HandleDisable2FA).Methods("POST")

	// v2 endpoints — multi-server
	v2 := api.PathPrefix("/v2").Subrouter()
	v2.HandleFunc("/servers", listServersHandler(d.Registry)).Methods("GET")
	v2.HandleFunc("/servers", createServerHandler(d.Registry, hubBaseURL)).Methods("POST")
	v2.HandleFunc("/servers/{id}", deleteServerHandler(d.Registry, d.Connections)).Methods("DELETE")
	v2.HandleFunc("/servers/{id}/history/{type}", getScopedHistoryHandler(d.Store)).Methods("GET")
	v2.HandleFunc("/servers/{id}/history", getScopedAllHistoryHandler(d.Store)).Methods("GET")

	// v2 terminal endpoints — require an auth manager so the role check
	// in the handler has a non-nil JWT claims pipeline.
	if d.TerminalProxy != nil && d.AuthManager != nil {
		v2.HandleFunc("/servers/{id}/terminal", mintTerminalSessionHandler(d.TerminalProxy)).Methods("POST")
		v2.HandleFunc("/terminal/sessions", terminalAuditHandler(d.TerminalProxy, nil)).Methods("GET")
	}

	// v2 GitHub Actions endpoints — mount only when a poller is wired
	// (i.e. github.enabled=true with at least one repo configured).
	if d.GitHubPoller != nil {
		v2.HandleFunc("/github/runs", listGithubRunsHandler(d.GitHubPoller)).Methods("GET")
		v2.HandleFunc("/github/repos", listGithubReposHandler(d.GitHubPoller)).Methods("GET")
	}
}

func getConfigHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg.Sanitize())
	}
}

func getHistoryHandler(store *storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		metricType := vars["type"]
		duration := r.URL.Query().Get("duration")
		if duration == "" {
			duration = "1h"
		}

		data := store.GetHistory(metricType, duration)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(data)
	}
}

func getScopedHistoryHandler(store *storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		serverID := vars["id"]
		metricType := vars["type"]
		duration := r.URL.Query().Get("duration")
		if duration == "" {
			duration = "1h"
		}
		data := store.GetHistoryScoped(serverID, metricType, duration)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(data)
	}
}

func getScopedAllHistoryHandler(store *storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		serverID := vars["id"]
		duration := r.URL.Query().Get("duration")
		if duration == "" {
			duration = "1h"
		}
		data := store.GetAllHistoryScoped(serverID, duration)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(data)
	}
}

func getAlertsHandler(ae *alerts.AlertEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := ae.GetActiveAlerts()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(data)
	}
}

func getAlertHistoryHandler(ae *alerts.AlertEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := ae.GetHistory()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(data)
	}
}
