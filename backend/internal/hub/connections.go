package hub

import (
	"log"
	"sync"
	"time"

	"argon-watch-go/internal/transport"

	"github.com/gorilla/websocket"
)

// AgentConn is one connected agent. The hub keeps at most one connection
// per serverID; a new connection for the same ID kicks the old one.
type AgentConn struct {
	ServerID string
	conn     *websocket.Conn
	send     chan transport.Envelope
	closed   chan struct{}
	closeOnce sync.Once
}

// Connections is the in-memory map of currently-attached agents.
type Connections struct {
	mu       sync.RWMutex
	byID     map[string]*AgentConn
	registry *Registry
}

func NewConnections(reg *Registry) *Connections {
	return &Connections{
		byID:     make(map[string]*AgentConn),
		registry: reg,
	}
}

// Attach takes over the WebSocket as the active connection for serverID.
// If another connection exists for the same ID, it is closed first. The
// caller's goroutine should call Read in a loop until it returns.
func (c *Connections) Attach(serverID string, conn *websocket.Conn) *AgentConn {
	c.mu.Lock()
	if old, ok := c.byID[serverID]; ok {
		old.Close()
	}
	a := &AgentConn{
		ServerID: serverID,
		conn:     conn,
		send:     make(chan transport.Envelope, 32),
		closed:   make(chan struct{}),
	}
	c.byID[serverID] = a
	c.mu.Unlock()

	c.registry.MarkOnline(serverID, "", "")
	go a.writePump()
	return a
}

// Detach removes the connection and marks the server offline.
func (c *Connections) Detach(serverID string) {
	c.mu.Lock()
	a, ok := c.byID[serverID]
	if ok {
		delete(c.byID, serverID)
	}
	c.mu.Unlock()
	if a != nil {
		a.Close()
	}
	c.registry.MarkOffline(serverID)
}

// SendTo dispatches a message to one specific agent. Returns false if no
// connection is currently attached.
func (c *Connections) SendTo(serverID string, env transport.Envelope) bool {
	c.mu.RLock()
	a, ok := c.byID[serverID]
	c.mu.RUnlock()
	if !ok {
		return false
	}
	select {
	case a.send <- env:
		return true
	case <-time.After(2 * time.Second):
		log.Printf("hub: send to %s timed out", serverID)
		return false
	}
}

// IsConnected reports whether an agent currently has an open stream.
func (c *Connections) IsConnected(serverID string) bool {
	c.mu.RLock()
	_, ok := c.byID[serverID]
	c.mu.RUnlock()
	return ok
}

// Close terminates the WebSocket and signals the read/write loops to exit.
// Safe to call multiple times.
func (a *AgentConn) Close() {
	a.closeOnce.Do(func() {
		close(a.closed)
		_ = a.conn.Close()
	})
}

func (a *AgentConn) Done() <-chan struct{} { return a.closed }

// Read blocks until the next inbound envelope arrives or the connection
// dies. Hub uses this in the /agent handler loop.
func (a *AgentConn) Read() (transport.Envelope, error) {
	var env transport.Envelope
	if err := a.conn.ReadJSON(&env); err != nil {
		return env, err
	}
	return env, nil
}

func (a *AgentConn) writePump() {
	pingTicker := time.NewTicker(20 * time.Second)
	defer pingTicker.Stop()
	for {
		select {
		case <-a.closed:
			return
		case env := <-a.send:
			if err := a.conn.WriteJSON(env); err != nil {
				log.Printf("hub: write %s: %v", a.ServerID, err)
				a.Close()
				return
			}
		case <-pingTicker.C:
			// Keep NAT/proxy state alive; a missed pong is detected by
			// the read pump's deadline (set on each Read in the handler).
			if err := a.conn.WriteControl(
				websocket.PingMessage, nil,
				time.Now().Add(5*time.Second),
			); err != nil {
				a.Close()
				return
			}
		}
	}
}
