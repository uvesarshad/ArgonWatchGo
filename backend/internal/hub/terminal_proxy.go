package hub

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"argon-watch-go/internal/auth"
	"argon-watch-go/internal/transport"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

// TerminalProxy owns the hub side of browser↔agent PTY plumbing.
//
// Each browser session is a short-lived (typically minutes-to-hours)
// record minted by POST /api/v2/servers/:id/terminal. The browser then
// connects to /ws/terminal/:sessionId, the proxy sends TERMINAL_OPEN to
// the agent, and bytes flow:
//
//     browser ──TERMINAL_INPUT──> hub ──TERMINAL_INPUT──> agent (PTY stdin)
//     browser <──TERMINAL_OUTPUT── hub <──TERMINAL_OUTPUT── agent (PTY stdout)
//
// The proxy persists session start/end + byte counts to the
// terminal_sessions table for the audit log.
type TerminalProxy struct {
	mu       sync.RWMutex
	sessions map[string]*terminalSession
	conns    *Connections
	db       *sql.DB
}

type terminalSession struct {
	id        string
	serverID  string
	user      string
	startedAt time.Time

	mu        sync.Mutex
	browser   *websocket.Conn
	bytesIn   int64
	bytesOut  int64
	closed    bool
}

func NewTerminalProxy(conns *Connections, db *sql.DB) *TerminalProxy {
	return &TerminalProxy{
		sessions: make(map[string]*terminalSession),
		conns:    conns,
		db:       db,
	}
}

// Mint creates a new pending session. Returns the sessionID the browser
// will use to dial /ws/terminal/:sessionId. The session is held until
// either the browser connects (within 30s) or the proxy expires it.
func (t *TerminalProxy) Mint(serverID, user string) (string, error) {
	// Local server runs its monitors in-process but has no agent WS
	// attached, so there's no PTY-spawning peer to talk to. Phase 3.5
	// will add an in-process terminal handler for "local"; until then,
	// users opening the local terminal get a clear refusal instead of
	// a session that silently goes nowhere.
	if !t.conns.IsConnected(serverID) {
		if serverID == LocalServerID {
			return "", errors.New("terminal for the local server requires a separate --mode=agent process (Phase 3.5)")
		}
		return "", errors.New("server is not connected")
	}
	id, err := generateSessionID()
	if err != nil {
		return "", err
	}
	now := time.Now()
	s := &terminalSession{
		id:        id,
		serverID:  serverID,
		user:      user,
		startedAt: now,
	}
	t.mu.Lock()
	t.sessions[id] = s
	t.mu.Unlock()

	// Persist the start record now so an audit trail exists even if the
	// browser never actually connects.
	if t.db != nil {
		_, err := t.db.Exec(
			`INSERT INTO terminal_sessions (id, server_id, user, started_at) VALUES (?, ?, ?, ?)`,
			id, serverID, user, now.UnixMilli(),
		)
		if err != nil {
			log.Printf("terminal_proxy: write start row: %v", err)
		}
	}

	// 30-second grace: if the browser never opens the WS, reap the session.
	go func() {
		time.Sleep(30 * time.Second)
		t.mu.RLock()
		s, ok := t.sessions[id]
		t.mu.RUnlock()
		if ok && s.browser == nil {
			t.endSession(id, "browser never connected")
		}
	}()

	return id, nil
}

// HandleRoute is the WebSocket handler the router mounts at
// /ws/terminal/{id}. JWT auth + RBAC check happen here.
func (t *TerminalProxy) HandleRoute(jwtMgr *auth.JWTManager) http.HandlerFunc {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := mux.Vars(r)["id"]
		if sessionID == "" {
			http.Error(w, "missing session id", http.StatusBadRequest)
			return
		}

		// Auth: same token sources as the metrics WS (cookie, header,
		// query). Caller must be at least operator.
		token := extractToken(r)
		if token == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		claims, err := jwtMgr.ValidateToken(token)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if claims.Role == auth.RoleViewer {
			http.Error(w, "forbidden: terminal requires operator role", http.StatusForbidden)
			return
		}

		// Validate session and ownership.
		t.mu.RLock()
		sess, ok := t.sessions[sessionID]
		t.mu.RUnlock()
		if !ok {
			http.Error(w, "unknown or expired session", http.StatusNotFound)
			return
		}
		if sess.user != claims.Username {
			http.Error(w, "forbidden: session belongs to another user", http.StatusForbidden)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		sess.mu.Lock()
		sess.browser = conn
		sess.mu.Unlock()

		// Tell the agent to spawn the PTY.
		t.conns.SendTo(sess.serverID, transport.Envelope{
			Type:     transport.MsgTerminalOpen,
			ServerID: sess.serverID,
			Ts:       time.Now().UnixMilli(),
			Payload: transport.TerminalOpenPayload{
				SessionID: sessionID,
				Cols:      80,
				Rows:      24,
			},
		})

		t.pumpBrowser(sess, conn)
	}
}

// pumpBrowser reads from the browser WS, forwards input/resize/close to
// the agent. Output flows the other way through DeliverFromAgent.
func (t *TerminalProxy) pumpBrowser(sess *terminalSession, conn *websocket.Conn) {
	defer t.endSession(sess.id, "browser disconnected")
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data,omitempty"`
			Cols int             `json:"cols,omitempty"`
			Rows int             `json:"rows,omitempty"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "input":
			// Browser sends data as a base64 string OR a raw JSON string
			// — accept both. JSON string is simpler from xterm.js;
			// base64 matches the wire shape used elsewhere.
			var bytes []byte
			if err := json.Unmarshal(msg.Data, &bytes); err == nil {
				// Successfully decoded as base64 ([]byte JSON form).
			} else {
				var s string
				if err := json.Unmarshal(msg.Data, &s); err == nil {
					bytes = []byte(s)
				}
			}
			sess.mu.Lock()
			sess.bytesIn += int64(len(bytes))
			sess.mu.Unlock()
			t.conns.SendTo(sess.serverID, transport.Envelope{
				Type:     transport.MsgTerminalIn,
				ServerID: sess.serverID,
				Ts:       time.Now().UnixMilli(),
				Payload: transport.TerminalDataPayload{
					SessionID: sess.id,
					Data:      bytes,
				},
			})
		case "resize":
			t.conns.SendTo(sess.serverID, transport.Envelope{
				Type:     transport.MsgTerminalResize,
				ServerID: sess.serverID,
				Ts:       time.Now().UnixMilli(),
				Payload: transport.TerminalResizePayload{
					SessionID: sess.id,
					Cols:      msg.Cols,
					Rows:      msg.Rows,
				},
			})
		}
	}
}

// DeliverFromAgent is called by the agent_ws handler whenever a
// TERMINAL_OUTPUT or TERMINAL_EXIT envelope arrives. Routes to the
// owning browser session, if any.
func (t *TerminalProxy) DeliverFromAgent(env transport.Envelope) {
	// Best-effort payload decode — the envelope's payload is whatever
	// the agent serialized; we round-trip via JSON to recover the typed
	// shape without coupling this code to gjson or reflection.
	switch env.Type {
	case transport.MsgTerminalOut:
		var p transport.TerminalDataPayload
		if !decode(env.Payload, &p) {
			return
		}
		t.mu.RLock()
		sess, ok := t.sessions[p.SessionID]
		t.mu.RUnlock()
		if !ok || sess.browser == nil {
			return
		}
		sess.mu.Lock()
		sess.bytesOut += int64(len(p.Data))
		conn := sess.browser
		sess.mu.Unlock()
		// Browser receives `{"type":"output","data":"<base64>"}`. xterm.js
		// gets a Uint8Array on the JS side.
		payload, _ := json.Marshal(map[string]interface{}{
			"type": "output",
			"data": p.Data, // JSON encoding turns []byte into base64
		})
		_ = conn.WriteMessage(websocket.TextMessage, payload)

	case transport.MsgTerminalExit:
		var p transport.TerminalExitPayload
		if !decode(env.Payload, &p) {
			return
		}
		t.endSession(p.SessionID, "agent reported exit")
	}
}

func (t *TerminalProxy) endSession(sessionID, reason string) {
	t.mu.Lock()
	sess, ok := t.sessions[sessionID]
	if ok {
		delete(t.sessions, sessionID)
	}
	t.mu.Unlock()
	if !ok {
		return
	}
	sess.mu.Lock()
	if sess.closed {
		sess.mu.Unlock()
		return
	}
	sess.closed = true
	browser := sess.browser
	bytesIn, bytesOut := sess.bytesIn, sess.bytesOut
	sess.mu.Unlock()

	if browser != nil {
		_ = browser.WriteMessage(websocket.TextMessage, []byte(`{"type":"closed"}`))
		_ = browser.Close()
	}
	// Tell the agent to release its PTY (idempotent on the agent side).
	t.conns.SendTo(sess.serverID, transport.Envelope{
		Type:     transport.MsgTerminalClose,
		ServerID: sess.serverID,
		Ts:       time.Now().UnixMilli(),
		Payload: transport.TerminalClosePayload{
			SessionID: sessionID,
			Reason:    reason,
		},
	})

	if t.db != nil {
		_, err := t.db.Exec(
			`UPDATE terminal_sessions SET ended_at = ?, input_bytes = ?, output_bytes = ? WHERE id = ?`,
			time.Now().UnixMilli(), bytesIn, bytesOut, sessionID,
		)
		if err != nil {
			log.Printf("terminal_proxy: write end row: %v", err)
		}
	}
}

// decode is the same shape as the agent-side helper — kept local to
// hub to avoid exposing it.
func decode(payload interface{}, into interface{}) bool {
	b, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, into) == nil
}

func generateSessionID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "ts_" + hex.EncodeToString(buf), nil
}

// extractToken pulls a JWT from the standard locations. Kept private to
// hub to avoid importing internal/auth's helper (which is package-private).
func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if len(h) > 7 && h[:7] == "Bearer " {
			return h[7:]
		}
	}
	if c, err := r.Cookie("auth_token"); err == nil {
		return c.Value
	}
	return r.URL.Query().Get("token")
}
