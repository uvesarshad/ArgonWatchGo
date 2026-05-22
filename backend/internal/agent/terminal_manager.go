package agent

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"sync"

	"argon-watch-go/internal/agent/terminal"
	"argon-watch-go/internal/config"
	"argon-watch-go/internal/transport"
)

// terminalManager owns every live PTY session for the agent process.
// One agent can run multiple concurrent sessions (e.g. two browser tabs
// open against the same server) — each keyed by the hub-minted sessionID.
type terminalManager struct {
	mu       sync.Mutex
	sessions map[string]*terminal.Session
	cfg      config.TerminalConfig
	perms    config.PermissionsConfig
	send     func(transport.Envelope)
}

func newTerminalManager(cfg config.TerminalConfig, perms config.PermissionsConfig, send func(transport.Envelope)) *terminalManager {
	return &terminalManager{
		sessions: make(map[string]*terminal.Session),
		cfg:      cfg,
		perms:    perms,
		send:     send,
	}
}

// Handle dispatches a terminal-related envelope coming in from the hub.
// Returns true if the envelope was consumed (so the caller doesn't try
// to interpret it further).
func (m *terminalManager) Handle(env transport.Envelope) bool {
	switch env.Type {
	case transport.MsgTerminalOpen:
		var p transport.TerminalOpenPayload
		if !decode(env.Payload, &p) {
			return true
		}
		m.open(p)
		return true
	case transport.MsgTerminalIn:
		var p transport.TerminalDataPayload
		if !decode(env.Payload, &p) {
			return true
		}
		m.input(p)
		return true
	case transport.MsgTerminalResize:
		var p transport.TerminalResizePayload
		if !decode(env.Payload, &p) {
			return true
		}
		m.resize(p)
		return true
	case transport.MsgTerminalClose:
		var p transport.TerminalClosePayload
		if !decode(env.Payload, &p) {
			return true
		}
		m.close(p.SessionID, p.Reason)
		return true
	}
	return false
}

// CloseAll terminates every live session — used on agent shutdown so we
// don't orphan PTY processes when the hub WS drops.
func (m *terminalManager) CloseAll(reason string) {
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.close(id, reason)
	}
}

func (m *terminalManager) open(p transport.TerminalOpenPayload) {
	if !m.cfg.Enabled {
		m.fail(p.SessionID, "terminal not enabled on this agent")
		return
	}
	if p.SessionID == "" {
		m.fail("", "missing sessionId")
		return
	}

	m.mu.Lock()
	if _, exists := m.sessions[p.SessionID]; exists {
		m.mu.Unlock()
		// Duplicate OPEN — the hub thinks the session already exists.
		// Treat it as a no-op rather than an error.
		return
	}
	m.mu.Unlock()

	sess, err := terminal.NewSession(p.SessionID, terminal.Options{
		Shell:       m.cfg.Shell,
		RunAsUser:   m.perms.RunAsUser,
		AllowRoot:   false, // toggle on once permissions.allowRoot is added; safer default
		InitialCols: p.Cols,
		InitialRows: p.Rows,
	})
	if err != nil {
		m.fail(p.SessionID, err.Error())
		return
	}

	m.mu.Lock()
	m.sessions[p.SessionID] = sess
	m.mu.Unlock()

	// Pump output back to the hub. Reads in 4KB chunks — enough for a
	// typical screen redraw without forcing JSON envelope overhead on
	// every single byte.
	go m.pumpOutput(sess)
	log.Printf("terminal %s: opened", p.SessionID)
}

func (m *terminalManager) input(p transport.TerminalDataPayload) {
	m.mu.Lock()
	s, ok := m.sessions[p.SessionID]
	m.mu.Unlock()
	if !ok {
		return
	}
	if _, err := s.Write(p.Data); err != nil {
		if !errors.Is(err, io.ErrClosedPipe) {
			log.Printf("terminal %s: write: %v", p.SessionID, err)
		}
	}
}

func (m *terminalManager) resize(p transport.TerminalResizePayload) {
	m.mu.Lock()
	s, ok := m.sessions[p.SessionID]
	m.mu.Unlock()
	if !ok {
		return
	}
	if err := s.Resize(p.Cols, p.Rows); err != nil {
		log.Printf("terminal %s: resize: %v", p.SessionID, err)
	}
}

func (m *terminalManager) close(sessionID, reason string) {
	m.mu.Lock()
	s, ok := m.sessions[sessionID]
	if ok {
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	bytesIn, bytesOut := s.BytesIn(), s.BytesOut()
	_ = s.Close()
	log.Printf("terminal %s: closed (reason=%q, in=%d, out=%d)", sessionID, reason, bytesIn, bytesOut)
	m.send(transport.New("", transport.MsgTerminalExit, transport.TerminalExitPayload{
		SessionID: sessionID,
		Code:      0,
	}))
}

func (m *terminalManager) fail(sessionID, msg string) {
	log.Printf("terminal %s: open failed: %s", sessionID, msg)
	m.send(transport.New("", transport.MsgTerminalExit, transport.TerminalExitPayload{
		SessionID: sessionID,
		Code:      -1,
		Error:     msg,
	}))
}

func (m *terminalManager) pumpOutput(s *terminal.Session) {
	buf := make([]byte, 4096)
	for {
		n, err := s.Read(buf)
		if n > 0 {
			// Copy the slice — the underlying buf gets overwritten on
			// the next Read, but the envelope may sit in the outbound
			// queue past that point.
			cp := make([]byte, n)
			copy(cp, buf[:n])
			m.send(transport.New("", transport.MsgTerminalOut, transport.TerminalDataPayload{
				SessionID: s.ID(),
				Data:      cp,
			}))
		}
		if err != nil {
			// EOF means the shell exited (user typed `exit`, killed by
			// signal, etc.). Anything else is an unexpected error.
			m.close(s.ID(), "shell exited")
			return
		}
	}
}

// decode best-effort unmarshals a generic envelope payload into a typed
// struct. JSON round-trip via map[string]interface{} is the lowest-
// common-denominator approach that works without dragging gjson or
// similar into the agent.
func decode(payload interface{}, into interface{}) bool {
	b, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, into) == nil
}
