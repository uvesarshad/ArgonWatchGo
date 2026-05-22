package main

import (
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"argon-watch-go/internal/agent"
	"argon-watch-go/internal/alerts"
	"argon-watch-go/internal/api"
	"argon-watch-go/internal/assets"
	"argon-watch-go/internal/auth"
	"argon-watch-go/internal/config"
	githubapi "argon-watch-go/internal/github"
	"argon-watch-go/internal/hub"
	"argon-watch-go/internal/monitor"
	"argon-watch-go/internal/realtime"
	"argon-watch-go/internal/storage"

	"context"
)

// Mode is the runtime mode. Defaults to "hub" which preserves the v1
// single-binary behavior (UI + in-process self-agent). "agent" runs as a
// headless metrics pusher that connects to a remote hub.
var (
	modeFlag       = flag.String("mode", "hub", "run mode: hub | agent")
	configFlag     = flag.String("config", "", "path to config file (defaults: config.json for hub, agent.config.json for agent)")
	agentHubFlag   = flag.String("hub", "", "hub WSS URL (agent mode)")
	agentTokenFlag = flag.String("token", "", "agent enrollment token (agent mode)")
)

func main() {
	flag.Parse()

	if *modeFlag == "agent" {
		runAgent()
		return
	}

	// Hub mode (default). Loads the v1 single-binary stack; in Phase 1 this
	// will also start the hub-side agent listener.

	// 1. Load Configuration
	configPath := "config.json"
	if *configFlag != "" {
		configPath = *configFlag
	}

	// Check if config exists, if not create default
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		log.Println("Config file not found, creating default config.json...")
		defaultCfg := config.GenerateDefaultConfig()
		if err := config.SaveConfig(defaultCfg, configPath); err != nil {
			log.Printf("Warning: Failed to create default config: %v", err)
			// Try fallback location
			configPath = "../config/config.json"
		} else {
			log.Println("✓ Created default config.json - Please review and update the jwtSecret!")
		}
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// 2. Setup Storage
	store := storage.NewStorage(cfg.Storage)

	// 3. Setup Realtime Hub
	realtimeHub := realtime.NewHub()
	go realtimeHub.Run()

	// Setup message handler for incoming WebSocket messages
	realtimeHub.SetMessageHandler(func(client *realtime.Client, msg realtime.Message) {
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
	alertEngine := alerts.NewAlertEngine(cfg.Alerts, cfg.Notifications, realtimeHub.Broadcast)

	// 5. Setup System Monitor
	sysInterval := cfg.Monitoring.SystemInterval
	if sysInterval <= 0 {
		sysInterval = 2000
	}
	// All in-process monitors publish as the implicit local server. When
	// remote agents land (Phase 1) they push from their own serverID.
	const localID = realtime.LocalServerID
	sysMon := monitor.NewSystemMonitor(
		localID,
		time.Duration(sysInterval)*time.Millisecond,
		realtimeHub.BroadcastFor,
		store,
		alertEngine,
	)
	sysMon.Start()

	// 6. Setup Service Monitor
	svcInterval := cfg.Monitoring.ServicesInterval
	if svcInterval <= 0 {
		svcInterval = 30000
	}
	svcMon := monitor.NewServiceMonitor(
		localID,
		cfg.Services,
		time.Duration(svcInterval)*time.Millisecond,
		realtimeHub.BroadcastFor,
	)
	svcMon.Start()

	// 7. Setup Database Monitor
	dbInterval := cfg.Monitoring.ServicesInterval
	if dbInterval <= 0 {
		dbInterval = 30000
	}
	dbMon := monitor.NewDatabaseMonitor(
		localID,
		cfg.Databases,
		time.Duration(dbInterval)*time.Millisecond,
		realtimeHub.BroadcastFor,
	)
	dbMon.Start()

	// 8. Setup PM2 Monitor
	pm2Interval := cfg.Monitoring.PM2Interval
	if pm2Interval <= 0 {
		pm2Interval = 5000
	}
	pm2Mon := monitor.NewPM2Monitor(
		localID,
		time.Duration(pm2Interval)*time.Millisecond,
		realtimeHub.BroadcastFor,
	)
	pm2Mon.Start()

	// 9. Setup Auth Manager
	var authManager *auth.Manager
	if cfg.Auth.Enabled {
		// Env Var Overrides
		if envSecret := os.Getenv("JWT_SECRET"); envSecret != "" {
			cfg.Auth.JWTSecret = envSecret
		}

		// Check for default or weak secret
		if cfg.Auth.JWTSecret == "CHANGE-THIS-TO-A-SECURE-RANDOM-SECRET-KEY" || cfg.Auth.JWTSecret == "change-this-secret-key-in-production" {
			// Generate a new secure secret
			log.Println("⚠️  Default JWT secret detected. Generating a new secure secret...")
			randomBytes := make([]byte, 32)
			_, err := rand.Read(randomBytes)
			if err != nil {
				log.Fatalf("Failed to generate random secret: %v", err)
			}
			cfg.Auth.JWTSecret = base64.StdEncoding.EncodeToString(randomBytes)

			// Save the new secret so valid tokens persist across restarts
			if err := config.SaveConfig(cfg, configPath); err != nil {
				log.Printf("Warning: Failed to save config with new secret: %v", err)
			} else {
				log.Println("✓ Updated config.json with new secure JWT secret")
			}
		}

		// SMTP Password from Env
		if smtpPass := os.Getenv("SMTP_PASSWORD"); smtpPass != "" {
			cfg.Notifications.Email.SMTP.Auth.Pass = smtpPass
		}

		if cfg.Auth.TokenExpiration == 0 {
			cfg.Auth.TokenExpiration = 24 // 24 hours default
		}
		if cfg.Auth.UsersFile == "" {
			cfg.Auth.UsersFile = "../data/users.json"
		}

		am, err := auth.NewManager(cfg.Auth.UsersFile, cfg.Auth.JWTSecret, cfg.Auth.TokenExpiration)
		if err != nil {
			log.Fatalf("Failed to initialize auth manager: %v", err)
		}
		authManager = am

		// Check if setup is required
		if !authManager.GetUserStore().HasUsers() {
			log.Println("⚠️  No users found. Please complete initial setup at /setup")
		}
	}

	// 10. Load Frontend Assets
	frontendFS, err := assets.GetFrontendAssets()
	if err != nil {
		log.Fatalf("Failed to get frontend assets: %v", err)
	}

	// 11. Setup hub registry + connection manager. The SQLite store is
	// required for v2 multi-server. If storage is disabled (rare), the
	// hub still runs but cannot mint/list remote agents.
	var registry *hub.Registry
	var connections *hub.Connections
	var terminalProxy *hub.TerminalProxy
	if sqliteStore := store.SQLiteStore(); sqliteStore != nil {
		reg, regErr := hub.NewRegistry(sqliteStore.DB())
		if regErr != nil {
			log.Fatalf("hub registry: %v", regErr)
		}
		registry = reg
		connections = hub.NewConnections(registry)
		terminalProxy = hub.NewTerminalProxy(connections, sqliteStore.DB())
	} else {
		log.Println("⚠️  storage disabled — multi-server registry unavailable")
	}

	// GitHub Actions poller. Opt-in via config.github.enabled + at least
	// one repo. Phase 4 ships PAT-only; App auth lands in Phase 4.5.
	var githubPoller *githubapi.Poller
	if cfg.GitHub.Enabled && len(cfg.GitHub.Repos) > 0 {
		var ghAuth githubapi.Auth
		switch cfg.GitHub.Auth.Type {
		case "pat", "":
			ghAuth = &githubapi.PATAuth{Token: cfg.GitHub.Auth.Token}
		default:
			log.Printf("github: auth type %q not supported in Phase 4 (use \"pat\")", cfg.GitHub.Auth.Type)
		}
		if ghAuth != nil {
			client := githubapi.New(ghAuth, githubapi.Options{})
			interval := time.Duration(cfg.GitHub.PollInterval) * time.Millisecond
			if interval <= 0 {
				interval = 60 * time.Second
			}
			poller, err := githubapi.NewPoller(client, cfg.GitHub.Repos, interval, realtimeHub.BroadcastFor)
			if err != nil {
				log.Printf("github: poller init failed: %v", err)
			} else {
				githubPoller = poller
				go poller.Start(context.Background())
				log.Printf("github: poller started for %d repo(s) @ %s", len(cfg.GitHub.Repos), interval)
			}
		}
	}

	// 12. Setup Router
	r := api.NewRouter(api.Deps{
		Config:        cfg,
		Hub:           realtimeHub,
		Store:         store,
		Alerts:        alertEngine,
		AuthManager:   authManager,
		FrontendFS:    frontendFS,
		Registry:      registry,
		Connections:   connections,
		TerminalProxy: terminalProxy,
		GitHubPoller:  githubPoller,
	})

	// Serve static files (CSS, JS, images, etc.)
	fileServer := http.FileServer(http.FS(frontendFS))
	r.PathPrefix("/").Handler(fileServer)

	// 13. Start Server
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("Starting server on http://%s", addr)

	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// runAgent loads the agent config from --config (or --hub / --token flags
// for ad-hoc runs) and blocks driving the agent loop.
func runAgent() {
	path := *configFlag
	if path == "" {
		path = "agent.config.json"
	}

	// Prefer the config file; fall back to flags for first-run sanity checks
	// where the operator hasn't installed a config yet.
	var cfg config.AgentConfig
	if _, err := os.Stat(path); err == nil {
		loaded, err := config.LoadAgentConfig(path)
		if err != nil {
			log.Fatalf("agent: load config %s: %v", path, err)
		}
		cfg = *loaded
	}
	if *agentHubFlag != "" {
		cfg.HubURL = *agentHubFlag
	}
	if *agentTokenFlag != "" {
		cfg.Token = *agentTokenFlag
	}
	if cfg.ServerID == "" {
		cfg.ServerID = os.Getenv("AGENT_SERVER_ID")
	}

	if err := agent.Run(cfg); err != nil {
		log.Fatalf("agent: %v", err)
	}
}
