// Package agent implements the v2 agent-mode runtime: a lightweight process
// that runs the existing monitor stack and pushes envelopes over WSS to a
// remote hub. The hub fans those envelopes out to connected browsers.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/url"
	"os"
	"runtime"
	"sync"
	"time"

	"argon-watch-go/internal/agent/ghrunner"
	"argon-watch-go/internal/config"
	"argon-watch-go/internal/monitor"
	"argon-watch-go/internal/transport"

	"github.com/gorilla/websocket"
)

// Version is stamped into the heartbeat so the hub can render it in the
// server tab. Bump when shipping a breaking protocol change.
const Version = "v2.0.0-phase1"

// Run blocks the calling goroutine, connecting to the hub and serving
// metrics until the process is killed. Reconnects automatically with
// jittered exponential backoff.
func Run(cfg config.AgentConfig) error {
	if cfg.HubURL == "" || cfg.Token == "" || cfg.ServerID == "" {
		return errors.New("agent: hubUrl, token, and serverId are all required")
	}

	// Build the dial URL with auth query params. Append, don't overwrite,
	// so users can experiment with custom hub paths.
	dialURL, err := url.Parse(cfg.HubURL)
	if err != nil {
		return fmt.Errorf("parse hubUrl: %w", err)
	}
	q := dialURL.Query()
	q.Set("id", cfg.ServerID)
	q.Set("token", cfg.Token)
	dialURL.RawQuery = q.Encode()

	log.Printf("agent %s: connecting to %s", cfg.ServerID, dialURL.Host)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	backoff := time.Second
	const maxBackoff = 60 * time.Second
	for {
		if err := runOnce(ctx, cfg, dialURL.String()); err != nil {
			log.Printf("agent: session ended: %v", err)
		}
		// Jittered backoff so a hub restart doesn't get hammered by a
		// thundering herd of agents reconnecting at the same instant.
		jitter := time.Duration(rand.Int63n(int64(backoff / 2)))
		sleep := backoff + jitter
		log.Printf("agent: reconnecting in %s", sleep)
		select {
		case <-time.After(sleep):
		case <-ctx.Done():
			return ctx.Err()
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// runOnce is one full life of a hub connection: dial → handshake → run
// monitors → loop until the connection drops. Returns whatever error tore
// the session down.
func runOnce(ctx context.Context, cfg config.AgentConfig, dial string) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	conn, _, err := dialer.DialContext(ctx, dial, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	log.Printf("agent %s: connected", cfg.ServerID)

	// Single writer goroutine — gorilla/websocket connections are not safe
	// for concurrent writes, so every outbound message goes through this
	// buffered channel.
	outbound := make(chan transport.Envelope, 64)
	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-sessionCtx.Done():
				return
			case env := <-outbound:
				if err := conn.WriteJSON(env); err != nil {
					log.Printf("agent: write: %v", err)
					sessionCancel()
					return
				}
			}
		}
	}()

	// broadcast is the function passed to every monitor. It enqueues an
	// envelope; if the buffer is full (hub is slow or stalled) we drop
	// the message rather than block the monitor goroutine.
	broadcast := func(serverID, msgType string, data interface{}) {
		env := transport.New(serverID, msgType, data)
		select {
		case outbound <- env:
		default:
			log.Printf("agent: outbound buffer full, dropped %s", msgType)
		}
	}

	// Start monitors. Storage is nil in agent mode — the hub owns retention.
	// Alerts are also nil; the hub evaluates rules against the incoming
	// metric stream centrally so per-server-rule wiring is consistent.
	sysInterval := cfg.Monitoring.SystemInterval
	if sysInterval <= 0 {
		sysInterval = 2000
	}
	sysMon := monitor.NewSystemMonitor(cfg.ServerID,
		time.Duration(sysInterval)*time.Millisecond,
		broadcast, nil, nil)
	sysMon.Start()
	defer sysMon.Stop()

	if len(cfg.Services) > 0 {
		svcInterval := cfg.Monitoring.ServicesInterval
		if svcInterval <= 0 {
			svcInterval = 30000
		}
		svcMon := monitor.NewServiceMonitor(cfg.ServerID, cfg.Services,
			time.Duration(svcInterval)*time.Millisecond, broadcast)
		svcMon.Start()
		defer svcMon.Stop()
	}
	if len(cfg.Databases) > 0 {
		dbInterval := cfg.Monitoring.ServicesInterval
		if dbInterval <= 0 {
			dbInterval = 30000
		}
		dbMon := monitor.NewDatabaseMonitor(cfg.ServerID, cfg.Databases,
			time.Duration(dbInterval)*time.Millisecond, broadcast)
		dbMon.Start()
		defer dbMon.Stop()
	}
	pm2Interval := cfg.Monitoring.PM2Interval
	if pm2Interval <= 0 {
		pm2Interval = 5000
	}
	pm2Mon := monitor.NewPM2Monitor(cfg.ServerID,
		time.Duration(pm2Interval)*time.Millisecond, broadcast)
	pm2Mon.Start()
	defer pm2Mon.Stop()

	// Terminal manager: owns every live PTY for this agent process.
	// Goes through the same single-writer outbound queue as the monitors
	// so we never race the WS connection.
	termSend := func(env transport.Envelope) {
		env.ServerID = cfg.ServerID
		select {
		case outbound <- env:
		default:
			log.Printf("agent: outbound full, dropped terminal envelope %s", env.Type)
		}
	}
	termMgr := newTerminalManager(cfg.Terminal, cfg.Permissions, termSend)
	defer termMgr.CloseAll("agent shutdown")

	// Self-hosted runner probe. Only spins up when the operator has
	// pointed githubRunner.* at something — otherwise the dashboard
	// shows "no runner installed" for hosts that aren't runners.
	if ghrunner.Configured(cfg.GithubRunner) {
		ghrPush := func(msgType string, payload interface{}) {
			broadcast(cfg.ServerID, msgType, payload)
		}
		ghr := ghrunner.NewPoller(cfg.GithubRunner, 15*time.Second, ghrPush)
		ghr.Start(sessionCtx)
		defer ghr.Stop()
	}

	// Heartbeat tells the hub "I'm alive" + bumps last-seen. 10s is the
	// spec'd cadence; the hub's read deadline is 60s so we can miss 5+
	// heartbeats before being marked offline.
	wg.Add(1)
	go func() {
		defer wg.Done()
		hostname, _ := os.Hostname()
		hb := func() {
			broadcast(cfg.ServerID, transport.MsgHeartbeat, map[string]interface{}{
				"hostname": hostname,
				"os":       runtime.GOOS,
				"arch":     runtime.GOARCH,
				"version":  Version,
			})
		}
		hb() // first heartbeat is also the handshake
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-sessionCtx.Done():
				return
			case <-t.C:
				hb()
			}
		}
	}()

	// Read pump runs inline so we block until the connection drops.
	// We never expect inbound messages from the hub in Phase 1; future
	// phases will use this for terminal input + exec commands.
	conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})
	for {
		var env transport.Envelope
		if err := conn.ReadJSON(&env); err != nil {
			sessionCancel()
			wg.Wait()
			return err
		}
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		// Terminal envelopes are the only inbound traffic we handle in
		// Phase 3. Phase 4+ will fan out to exec / reload-config here.
		if termMgr.Handle(env) {
			continue
		}
	}
}
