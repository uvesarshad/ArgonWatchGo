package api

import (
	"encoding/json"
	"net/http"

	"argon-watch-go/internal/github"
)

// listGithubRunsHandler returns the most recent merged snapshot — same
// shape that flows over the WS as GH_WORKFLOW_RUNS, so the UI can use
// one renderer for the initial paint and live updates.
func listGithubRunsHandler(p *github.Poller) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p.Snapshot())
	}
}

// listGithubReposHandler returns the configured repo list + per-repo
// fetch status. Lighter than the full snapshot; the settings view can
// pull this without dragging the whole runs array.
func listGithubReposHandler(p *github.Poller) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		snap := p.Snapshot()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"repos": snap.Repos,
			"rate":  snap.Rate,
		})
	}
}
