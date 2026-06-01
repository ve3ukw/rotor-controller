package api

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  256,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// Hub broadcasts messages to all connected WebSocket clients.
type Hub struct {
	mu      sync.RWMutex
	clients map[*wsClient]struct{}

	// onCommand is called when a client sends a command (raw JSON).
	onCommand func([]byte)
}

func newHub(onCommand func([]byte)) *Hub {
	return &Hub{
		clients:   make(map[*wsClient]struct{}),
		onCommand: onCommand,
	}
}

// Broadcast sends msg to all connected clients. Clients that are slow are dropped.
func (h *Hub) Broadcast(msg []byte) {
	h.mu.RLock()
	clients := make([]*wsClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	for _, c := range clients {
		select {
		case c.send <- msg:
		default:
			h.remove(c)
		}
	}
}

func (h *Hub) add(c *wsClient) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *Hub) remove(c *wsClient) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
	c.conn.Close()
}

// ServeWS upgrades an HTTP request to a WebSocket connection.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}
	c := &wsClient{conn: conn, send: make(chan []byte, 16)}
	h.add(c)
	go c.writeLoop(func() { h.remove(c) })
	go c.readLoop(h)
}

type wsClient struct {
	conn *websocket.Conn
	send chan []byte
}

func (c *wsClient) writeLoop(onDone func()) {
	defer onDone()
	for msg := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}
}

func (c *wsClient) readLoop(h *Hub) {
	defer func() {
		h.remove(c)
		close(c.send)
	}()
	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		// Verify it's valid JSON before forwarding
		if json.Valid(msg) && h.onCommand != nil {
			h.onCommand(msg)
		}
	}
}
