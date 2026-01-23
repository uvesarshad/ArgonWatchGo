package api

import (
	"encoding/json"
	"net/http"

	"argon-watch-go/internal/alerts"
	"argon-watch-go/internal/config"
	"argon-watch-go/internal/realtime"
	"argon-watch-go/internal/storage"

	"github.com/gorilla/mux"
)

func NewRouter(cfg *config.Config, hub *realtime.Hub, store *storage.Storage, ae *alerts.AlertEngine) *mux.Router {
	r := mux.NewRouter()

	// WebSocket
	r.HandleFunc("/ws", hub.ServeWS)

	// API Routes
	api := r.PathPrefix("/api").Subrouter()
	api.HandleFunc("/config", getConfigHandler(cfg)).Methods("GET")
	api.HandleFunc("/history/{type}", getHistoryHandler(store)).Methods("GET")
	api.HandleFunc("/alerts/active", getAlertsHandler(ae)).Methods("GET")
	api.HandleFunc("/alerts/history", getAlertHistoryHandler(ae)).Methods("GET")

	return r
}

func getConfigHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg)
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
