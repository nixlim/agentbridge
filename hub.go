package main

import (
	"encoding/json"
	"sync"

	"github.com/gorilla/websocket"
)

type HubEvent struct {
	Event string      `json:"event"`
	Data  interface{} `json:"data"`
}

type WebSocketHub struct {
	clients    map[*websocket.Conn]bool
	broadcast  chan []byte
	register   chan *websocket.Conn
	unregister chan *websocket.Conn
	mu         sync.Mutex
	closed     chan struct{}
}

func NewWebSocketHub() *WebSocketHub {
	return &WebSocketHub{
		clients:    map[*websocket.Conn]bool{},
		broadcast:  make(chan []byte, 256),
		register:   make(chan *websocket.Conn),
		unregister: make(chan *websocket.Conn),
		closed:     make(chan struct{}),
	}
}

func (h *WebSocketHub) Run() {
	for {
		select {
		case conn := <-h.register:
			h.mu.Lock()
			h.clients[conn] = true
			h.mu.Unlock()
		case conn := <-h.unregister:
			h.removeClient(conn)
		case payload := <-h.broadcast:
			h.mu.Lock()
			for conn := range h.clients {
				if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
					go h.removeClient(conn)
				}
			}
			h.mu.Unlock()
		case <-h.closed:
			h.mu.Lock()
			for conn := range h.clients {
				_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "server shutdown"))
				_ = conn.Close()
				delete(h.clients, conn)
			}
			h.mu.Unlock()
			return
		}
	}
}

func (h *WebSocketHub) removeClient(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[conn]; ok {
		delete(h.clients, conn)
		_ = conn.Close()
	}
}

func (h *WebSocketHub) Broadcast(event string, data interface{}) {
	payload, err := json.Marshal(HubEvent{Event: event, Data: data})
	if err != nil {
		return
	}
	select {
	case h.broadcast <- payload:
	default:
	}
}

func (h *WebSocketHub) Shutdown() {
	close(h.closed)
}
