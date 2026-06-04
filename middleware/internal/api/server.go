package api

import (
	"encoding/json"
	"log"
	"net/http"

	"rotor-controller/brain/internal/state"
	"rotor-controller/brain/internal/wire"
)

// Server wires the REST + WebSocket HTTP server.
type Server struct {
	addr string
	hub  *Hub
	mux  *http.ServeMux
}

// NewServer creates the HTTP server.
// send is called to forward commands to the field unit.
// onWsCommand is invoked when a WebSocket client sends a command.
func NewServer(addr string, st *state.Store, send func(wire.Command) (*wire.Ack, error)) *Server {
	// onWsCommand parses the raw JSON and calls send.
	onWsCmd := func(raw []byte) {
		var cmd wire.Command
		if err := json.Unmarshal(raw, &cmd); err != nil {
			log.Printf("ws: bad command: %v", err)
			return
		}
		if _, err := send(cmd); err != nil {
			log.Printf("ws: send failed: %v", err)
		}
	}

	hub := newHub(onWsCmd)
	mux := http.NewServeMux()

	mux.Handle("GET /api/v1/status", methodOnly("GET", handleStatus(st)))
	mux.Handle("POST /api/v1/motion", methodOnly("POST", handleMotion(send)))
	mux.Handle("POST /api/v1/polarization", methodOnly("POST", handlePolarization(send)))
	mux.Handle("POST /api/v1/limits", methodOnly("POST", handleLimits(send)))
	mux.Handle("POST /api/v1/park", methodOnly("POST", handleSimple("park", send)))
	mux.Handle("POST /api/v1/netconfig", methodOnly("POST", handleNetconfig(send)))
	mux.Handle("POST /api/v1/netconfig/reset", methodOnly("POST", handleResetNetconfig(send)))
	mux.Handle("GET /api/v1/blocks", methodOnly("GET", handleBlockGet(st)))
	mux.Handle("POST /api/v1/blocks/set", methodOnly("POST", handleBlockSet(st, send)))
	mux.Handle("POST /api/v1/blocks/reset", methodOnly("POST", handleBlockReset(st, send)))
	mux.Handle("POST /api/v1/emergency_stop", methodOnly("POST", handleSimple("emergency_stop", send)))
	mux.Handle("POST /api/v1/clear_fault", methodOnly("POST", handleSimple("clear_fault", send)))
	mux.Handle("GET /api/v1/telemetry/ws", http.HandlerFunc(hub.ServeWS))

	return &Server{addr: addr, hub: hub, mux: mux}
}

// BroadcastTelemetry pushes a telemetry frame to all WebSocket clients.
func (s *Server) BroadcastTelemetry(t *wire.Telemetry) {
	b, err := json.Marshal(t)
	if err != nil {
		return
	}
	s.hub.Broadcast(b)
}

// Run starts the HTTP server. Blocks until the server fails.
func (s *Server) Run() error {
	log.Printf("api: listening on %s", s.addr)
	return http.ListenAndServe(s.addr, s.mux)
}
