package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"argon-watch-go/internal/alerts"
	"argon-watch-go/internal/api"
	"argon-watch-go/internal/assets"
	"argon-watch-go/internal/config"
	"argon-watch-go/internal/monitor"
	"argon-watch-go/internal/realtime"
	"argon-watch-go/internal/storage"
)

func main() {
	// 1. Load Configuration
	configPath := "config.json"
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		configPath = "../config/config.json"
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			log.Println("Config file not found in ./config.json or ../config/config.json")
		}
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// 2. Setup Storage
	store := storage.NewStorage(cfg.Storage)

	// 3. Setup Realtime Hub
	hub := realtime.NewHub()
	go hub.Run()

	// Setup message handler for incoming WebSocket messages
	hub.SetMessageHandler(func(client *realtime.Client, msg realtime.Message) {
		switch msg.Type {
		case "GET_HISTORICAL_DATA":
			// Get duration from message data (default to 1h)
			duration := "1h"
			if data, ok := msg.Data.(map[string]interface{}); ok {
				if d, ok := data["duration"].(string); ok {
					duration = d
				}
			}

			// Get historical data from storage
			histData := store.GetAllHistory(duration)

			// Send to requesting client
			client.SendMessage("HISTORICAL_DATA", histData)
		}
	})

	// 4. Setup Alerts
	alertEngine := alerts.NewAlertEngine(cfg.Alerts, cfg.Notifications, hub.Broadcast)

	// 5. Setup System Monitor
	sysMon := monitor.NewSystemMonitor(
		time.Duration(cfg.Monitoring.SystemInterval)*time.Millisecond,
		hub.Broadcast,
		store,
		alertEngine,
	)
	sysMon.Start()

	// 6. Setup Service Monitor
	svcMon := monitor.NewServiceMonitor(
		cfg.Services,
		time.Duration(cfg.Monitoring.ServicesInterval)*time.Millisecond,
		hub.Broadcast,
	)
	svcMon.Start()

	// 7. Setup Database Monitor
	dbInterval := cfg.Monitoring.ServicesInterval
	if dbInterval == 0 {
		dbInterval = 30000
	}
	dbMon := monitor.NewDatabaseMonitor(
		cfg.Databases,
		time.Duration(dbInterval)*time.Millisecond,
		hub.Broadcast,
	)
	dbMon.Start()

	// 8. Setup PM2 Monitor
	pm2Mon := monitor.NewPM2Monitor(
		time.Duration(cfg.Monitoring.PM2Interval)*time.Millisecond,
		hub.Broadcast,
	)
	pm2Mon.Start()

	// 9. Setup Router
	r := api.NewRouter(cfg, hub, store, alertEngine)

	// 10. Serve Embedded Frontend
	frontendFS, err := assets.GetFrontendAssets()
	if err != nil {
		log.Fatalf("Failed to get frontend assets: %v", err)
	}

	fileServer := http.FileServer(http.FS(frontendFS))
	r.PathPrefix("/").Handler(fileServer)

	// 11. Start Server
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("Starting server on http://%s", addr)

	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
