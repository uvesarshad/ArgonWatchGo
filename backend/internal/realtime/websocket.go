package realtime

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"argon-watch-go/internal/transport"

	"github.com/gorilla/websocket"
)

// LocalServerID is the implicit server ID for in-process monitors running
// inside a single-binary hub install. Mirrors storage.LocalServerID; kept
// here as a copy to avoid an import cycle.
const LocalServerID = "local"

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		// DEBUG: Allow all origins to rule out CORS issues behind proxy
		return true
	},
}

type Hub struct {
	clients        map[*Client]bool
	broadcast      chan transport.Envelope
	register       chan *Client
	unregister     chan *Client
	mu             sync.Mutex
	messageHandler func(*Client, Message)
}

type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
}

// Message is the legacy v1 wire shape kept for the inbound message handler
// (browsers still send {type, payload}). Outbound, the hub speaks the v2
// transport.Envelope which adds serverId + ts. Old clients ignore unknown
// fields so this is backward-compatible.
type Message struct {
	Type string      `json:"type"`
	Data interface{} `json:"payload"`
}

func NewHub() *Hub {
	return &Hub{
		broadcast:  make(chan transport.Envelope, 64),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		clients:    make(map[*Client]bool),
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()
		case message := <-h.broadcast:
			payload, err := json.Marshal(message)
			if err != nil {
				log.Printf("Error marshaling message: %v", err)
				continue
			}
			h.mu.Lock()
			for client := range h.clients {
				select {
				case client.send <- payload:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}
			h.mu.Unlock()
		}
	}
}

// Broadcast sends a message tagged as originating from the implicit local
// server. Kept for v1 callers that pre-date multi-server routing.
func (h *Hub) Broadcast(msgType string, data interface{}) {
	h.BroadcastFor(LocalServerID, msgType, data)
}

// BroadcastFor sends a message scoped to a specific server. The browser
// receives the v2 envelope shape ({type, serverId, ts, payload}); v1 clients
// that only read "type"/"payload" still work since they ignore the extras.
func (h *Hub) BroadcastFor(serverID, msgType string, data interface{}) {
	env := transport.Envelope{
		Type:     msgType,
		ServerID: serverID,
		Ts:       time.Now().UnixMilli(),
		Payload:  data,
	}
	// Non-blocking send: a slow consumer must never stall a monitor goroutine.
	// Channel is buffered (64); if it's full we drop and log rather than block.
	select {
	case h.broadcast <- env:
	default:
		log.Printf("realtime: broadcast buffer full, dropping %s for %s", msgType, serverID)
	}
}

func (h *Hub) SetMessageHandler(handler func(*Client, Message)) {
	h.messageHandler = handler
}

func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Upgrade error: %v", err)
		return
	}

	client := &Client{hub: h, conn: conn, send: make(chan []byte, 256)}
	client.hub.register <- client

	// Allow collection of memory by starting goroutines
	go client.writePump()
	go client.readPump()
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
		// Parse incoming message
		var msg Message
		if err := json.Unmarshal(message, &msg); err == nil {
			// Call message handler if set
			if c.hub.messageHandler != nil {
				c.hub.messageHandler(c, msg)
			}
		}
	}
}

func (c *Client) writePump() {
	defer func() {
		c.conn.Close()
	}()
	for message := range c.send {
		w, err := c.conn.NextWriter(websocket.TextMessage)
		if err != nil {
			return
		}
		w.Write(message)

		if err := w.Close(); err != nil {
			return
		}
	}
	c.conn.WriteMessage(websocket.CloseMessage, []byte{})
}

func (c *Client) SendMessage(msgType string, data interface{}) error {
	msg := Message{
		Type: msgType,
		Data: data,
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	select {
	case c.send <- payload:
		return nil
	default:
		return nil
	}
}
