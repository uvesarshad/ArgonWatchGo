package realtime

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		// DEBUG: Allow all origins to rule out CORS issues behind proxy
		origin := r.Header.Get("Origin")
		log.Printf("Debug: WebSocket connection attempt from Origin: %s, Host: %s", origin, r.Host)
		return true
	},
}

type Hub struct {
	clients        map[*Client]bool
	broadcast      chan Message
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

type Message struct {
	Type string      `json:"type"`
	Data interface{} `json:"payload"`
}

func NewHub() *Hub {
	return &Hub{
		broadcast:  make(chan Message),
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

func (h *Hub) Broadcast(msgType string, data interface{}) {
	h.broadcast <- Message{
		Type: msgType,
		Data: data,
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
