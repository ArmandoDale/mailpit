// Package websockets is used to broadcast messages to connected clients
package websockets

import (
	"encoding/json"
	"sync/atomic"

	"github.com/axllent/mailpit/internal/logger"
	"github.com/axllent/mailpit/internal/scope"
	"github.com/gorilla/websocket"
)

// Hub maintains the set of active clients and broadcasts messages to the
// clients.
type Hub struct {
	// Registered clients.
	Clients map[*Client]bool

	// Inbound messages from the clients.
	Broadcast chan notification

	// Register requests from the clients.
	register chan *Client

	// Unregister requests from clients.
	unregister chan *Client

	// clientCount is an atomic count of connected clients, safe for concurrent reads.
	clientCount atomic.Int64
}

// WebsocketNotification struct for responses
type WebsocketNotification struct {
	Type string
	Data any
}

// NewHub returns a new hub configuration
func NewHub() *Hub {
	return &Hub{
		Broadcast:  make(chan notification),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		Clients:    make(map[*Client]bool),
	}
}

// Run runs the listener
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			if _, ok := h.Clients[client]; !ok {
				logger.Log().Debugf("[websocket] client %s connected", client.conn.RemoteAddr().String())
				h.Clients[client] = true
				h.clientCount.Add(1)
			}
		case client := <-h.unregister:
			if _, ok := h.Clients[client]; ok {
				logger.Log().Debugf("[websocket] client %s disconnected", client.conn.RemoteAddr().String())
				delete(h.Clients, client)
				close(client.send)
				h.clientCount.Add(-1)
			}
		case message := <-h.Broadcast:
			prepared, err := websocket.NewPreparedMessage(websocket.TextMessage, message.data)
			if err != nil {
				logger.Log().Errorf("[websocket] error preparing message: %s", err.Error())
				continue
			}
			// Payloads computed once per distinct scope, then shared by every
			// client that sees the same thing.
			var byScope map[string]*websocket.PreparedMessage
			if message.perScope != nil {
				byScope = make(map[string]*websocket.PreparedMessage)
			}

			for client := range h.Clients {
				out := prepared

				if message.perScope != nil {
					key := client.Scope.Key()

					p, ok := byScope[key]
					if !ok {
						b, err := json.Marshal(WebsocketNotification{
							Type: message.eventType,
							Data: message.perScope(client.Scope),
						})
						if err != nil {
							logger.Log().Errorf("[websocket] %s", err.Error())
							continue
						}

						if p, err = websocket.NewPreparedMessage(websocket.TextMessage, b); err != nil {
							logger.Log().Errorf("[websocket] %s", err.Error())
							continue
						}

						byScope[key] = p
					}

					out = p
				}

				// A notification about a specific message only reaches clients
				// whose scope allows that message. Without this the "new" event
				// would hand every connected browser the sender, subject and
				// tags of mail belonging to other projects.
				if message.scoped && !client.Scope.AllowsTags(message.tags) {
					continue
				}
				select {
				case client.send <- out:
				default:
					close(client.send)
					delete(h.Clients, client)
					h.clientCount.Add(-1)
				}
			}
		}
	}
}

// notification is a marshalled event plus, when the event concerns a single
// message, the tags of that message. The hub uses the tags to decide which
// clients may receive it.
type notification struct {
	data []byte

	// scoped marks an event about one specific message. Unscoped events
	// (prune, truncate, errors) go to everyone.
	scoped bool

	// perScope, when set, builds the payload for each distinct client scope.
	// Counters must reflect what a client can actually see, otherwise the UI
	// shows a total that contradicts its own list.
	perScope func(scope.Scope) any

	// eventType is needed to marshal per-scope payloads lazily.
	eventType string

	// tags of the message the event concerns. Empty means untagged.
	tags []string
}

// Broadcast sends an event to every connected client. Use it only for events
// that do not disclose anything about an individual message.
func Broadcast(t string, msg any) {
	broadcast(notification{scoped: false}, t, msg)
}

// BroadcastMessage sends an event about a single message, delivered only to
// clients whose scope allows the message's tags.
func BroadcastMessage(t string, msg any, tags []string) {
	broadcast(notification{scoped: true, tags: tags}, t, msg)
}

// BroadcastPerScope sends an event whose payload depends on what the receiving
// client is allowed to see. build is called once per distinct scope.
func BroadcastPerScope(t string, build func(scope.Scope) any) {
	if MessageHub == nil || MessageHub.clientCount.Load() == 0 {
		return
	}

	n := notification{perScope: build, eventType: t}

	// data is unused for per-scope events but must be valid for the
	// NewPreparedMessage call that precedes the fan-out.
	n.data = []byte("{}")

	go func() { MessageHub.Broadcast <- n }()
}

func broadcast(n notification, t string, msg any) {
	if MessageHub == nil || MessageHub.clientCount.Load() == 0 {
		return
	}

	w := WebsocketNotification{}
	w.Type = t
	w.Data = msg
	b, err := json.Marshal(w)

	if err != nil {
		logger.Log().Errorf("[websocket] broadcast received invalid data: %s", err.Error())
		return
	}

	n.data = b

	go func() { MessageHub.Broadcast <- n }()
}

// BroadCastClientError is a wrapper to broadcast client errors to the web UI
func BroadCastClientError(severity, errorType, ip, message string) {
	msg := struct {
		Level   string
		Type    string
		IP      string
		Message string
	}{
		severity,
		errorType,
		ip,
		message,
	}

	Broadcast("error", msg)
}
