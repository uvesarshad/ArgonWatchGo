package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"argon-watch-go/internal/ai"
	"argon-watch-go/internal/auth"

	"github.com/gorilla/mux"
)

// AIDeps lets main.go inject the AI store + service without coupling
// the api package to the wiring details. Optional — when nil, AI routes
// are omitted from the mux.
type AIDeps struct {
	Service  *ai.Service
	Store    *ai.Store
	Reporter *ai.Reporter // Phase 7
}

func mountAIRoutes(v2 *mux.Router, d AIDeps) {
	if d.Service == nil || d.Store == nil {
		return
	}
	v2.HandleFunc("/ai/chat", chatHandler(d.Service)).Methods("POST")
	v2.HandleFunc("/ai/providers", listProvidersHandler(d.Store)).Methods("GET")
	v2.HandleFunc("/ai/providers", setProviderKeyHandler(d.Store)).Methods("POST")
	v2.HandleFunc("/ai/providers/{name}", deleteProviderHandler(d.Store)).Methods("DELETE")
	v2.HandleFunc("/ai/providers/{name}/test", testProviderHandler()).Methods("POST")
	v2.HandleFunc("/ai/conversations", listConversationsHandler(d.Store)).Methods("GET")
	v2.HandleFunc("/ai/conversations/{id}", getConversationHandler(d.Store)).Methods("GET")
	v2.HandleFunc("/ai/conversations/{id}", deleteConversationHandler(d.Store)).Methods("DELETE")
	v2.HandleFunc("/ai/conversations/{id}/export", exportConversationHandler(d.Store)).Methods("GET")

	// Phase 7: ad-hoc weekly report.
	if d.Reporter != nil {
		v2.HandleFunc("/ai/report/generate", generateReportHandler(d.Reporter)).Methods("POST")
		v2.HandleFunc("/ai/report/status", reportStatusHandler(d.Reporter)).Methods("GET")
	}
}

func generateReportHandler(r *ai.Reporter) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		claims, ok := auth.GetUserFromContext(req)
		if !ok || (claims.Role != auth.RoleAdmin && claims.Role != "") {
			http.Error(w, "forbidden: admin only", http.StatusForbidden)
			return
		}
		// 2-min hard timeout — report generation needs the same window
		// as a long tool-use turn.
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Minute)
		defer cancel()
		if err := r.GenerateNow(ctx); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func reportStatusHandler(r *ai.Reporter) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if _, ok := auth.GetUserFromContext(req); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		last := r.LastReport()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"lastReportAt": last.UnixMilli(),
		})
	}
}

func chatHandler(svc *ai.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.GetUserFromContext(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req ai.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		// 2-minute hard timeout — long enough for multi-tool turns but
		// short enough that a hung provider won't tie up a handler.
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		out, err := svc.Chat(ctx, claims, req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

func listProvidersHandler(store *ai.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.GetUserFromContext(r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		providers, err := store.ListProviders()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"providers": providers})
	}
}

func setProviderKeyHandler(store *ai.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.GetUserFromContext(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if claims.Role != auth.RoleAdmin && claims.Role != "" {
			http.Error(w, "forbidden: admin only", http.StatusForbidden)
			return
		}
		var body struct {
			Provider     string `json:"provider"`
			APIKey       string `json:"apiKey"`
			ChatModel    string `json:"chatModel"`
			SummaryModel string `json:"summaryModel"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := store.SetKey(body.Provider, body.APIKey, body.ChatModel, body.SummaryModel); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func deleteProviderHandler(store *ai.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.GetUserFromContext(r)
		if !ok || (claims.Role != auth.RoleAdmin && claims.Role != "") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		name := mux.Vars(r)["name"]
		if err := store.DeleteKey(name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func testProviderHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.GetUserFromContext(r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var body struct {
			Provider string `json:"provider"`
			APIKey   string `json:"apiKey"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// 30 s hard cap — a real key validates in 2-3s, an invalid one
		// errors fast. We don't want the UI to spin on a hung provider.
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := ai.TestProvider(ctx, body.Provider, body.APIKey); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func listConversationsHandler(store *ai.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.GetUserFromContext(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		out, err := store.ListConversations(claims.Username, 30)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"conversations": out})
	}
}

func getConversationHandler(store *ai.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.GetUserFromContext(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id := mux.Vars(r)["id"]
		c, err := store.GetConversation(id, claims.Username)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(c)
	}
}

func deleteConversationHandler(store *ai.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.GetUserFromContext(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id := mux.Vars(r)["id"]
		if err := store.DeleteConversation(id, claims.Username); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func exportConversationHandler(store *ai.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.GetUserFromContext(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id := mux.Vars(r)["id"]
		c, err := store.GetConversation(id, claims.Username)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", "attachment; filename=\"conversation-"+id+".json\"")
		_ = json.NewEncoder(w).Encode(c)
	}
}
