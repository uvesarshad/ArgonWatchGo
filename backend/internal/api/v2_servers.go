package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"argon-watch-go/internal/hub"

	"github.com/gorilla/mux"
)

// v2ServerView is the JSON shape returned to the browser. The bcrypt token
// hash never leaves the server.
type v2ServerView struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	OS       string            `json:"os,omitempty"`
	Version  string            `json:"version,omitempty"`
	Tags     map[string]string `json:"tags,omitempty"`
	Status   string            `json:"status"`
	LastSeen int64             `json:"lastSeen"`
}

func toView(s *hub.Server) v2ServerView {
	return v2ServerView{
		ID: s.ID, Name: s.Name, OS: s.OS, Version: s.Version,
		Tags: s.Tags, Status: s.Status, LastSeen: s.LastSeen.UnixMilli(),
	}
}

// listServersHandler returns every registered server.
func listServersHandler(reg *hub.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		all := reg.List()
		out := make([]v2ServerView, 0, len(all))
		for _, s := range all {
			out = append(out, toView(s))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

// createServerHandler mints a new server token. The plaintext token is
// returned exactly once; the caller is expected to copy it into the install
// command they paste on the target host.
func createServerHandler(reg *hub.Registry, hubBaseURL func(r *http.Request) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string            `json:"name"`
			Tags map[string]string `json:"tags,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}

		s, token, err := reg.Mint(body.Name, body.Tags)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		base := hubBaseURL(r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"server":          toView(s),
			"token":           token, // shown once — never returned again
			"installLinux":    fmt.Sprintf("curl -sSL %s/install.sh?id=%s\\&token=%s | sudo bash", base, s.ID, token),
			"installWindows":  fmt.Sprintf("iwr -useb '%s/install.ps1?id=%s&token=%s' | iex", base, s.ID, token),
			"manualHubUrl":    wsBaseURL(base) + "/agent",
			"manualServerId":  s.ID,
			"manualToken":     token,
		})
	}
}

// deleteServerHandler revokes a server token and removes it from the registry.
// Local server cannot be deleted.
func deleteServerHandler(reg *hub.Registry, conns *hub.Connections) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]
		// Kill any active connection first so the agent reconnect attempt
		// hits an "invalid credentials" wall and exits cleanly.
		conns.Detach(id)
		if err := reg.Delete(id); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// hubBaseURL derives the public-facing base URL for install command strings.
// Honors X-Forwarded-Proto/Host when behind a reverse proxy.
func hubBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	host := r.Host
	if fwdHost := r.Header.Get("X-Forwarded-Host"); fwdHost != "" {
		host = fwdHost
	}
	return scheme + "://" + host
}

// wsBaseURL flips http(s) → ws(s) for the agent dial URL.
func wsBaseURL(base string) string {
	if strings.HasPrefix(base, "https://") {
		return "wss://" + strings.TrimPrefix(base, "https://")
	}
	return "ws://" + strings.TrimPrefix(base, "http://")
}
