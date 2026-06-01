package api

import (
	"encoding/json"
	"net/http"

	"rotor-controller/brain/internal/state"
	"rotor-controller/brain/internal/wire"
)

type statusResponse struct {
	Linked    bool             `json:"linked"`
	AgeMs     int64            `json:"age_ms,omitempty"` // milliseconds since last telemetry
	Telemetry *wire.Telemetry  `json:"telemetry,omitempty"`
}

func handleStatus(st *state.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, linked, age := st.Snapshot()
		resp := statusResponse{Linked: linked}
		if t != nil {
			resp.Telemetry = t
			resp.AgeMs = age.Milliseconds()
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// motionRequest mirrors the REST body for POST /api/v1/motion
type motionRequest struct {
	Az string `json:"az"` // "cw" | "ccw" | "stop"
	El string `json:"el"` // "up" | "down" | "stop"
}

type polRequest struct {
	PolVHF  bool `json:"pol_vhf"`
	PolUHF  bool `json:"pol_uhf"`
	LnaUHF  bool `json:"lna_uhf"`
	RxTxUHF bool `json:"rxtx_uhf"`
}

type limitsRequest struct {
	AzMin float64 `json:"az_min"`
	AzMax float64 `json:"az_max"`
	ElMin float64 `json:"el_min"`
	ElMax float64 `json:"el_max"`
}

func handleMotion(send func(wire.Command) (*wire.Ack, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req motionRequest
		if !decodeBody(w, r, &req) {
			return
		}
		cmd := wire.Command{
			Type: "set_motion",
			Az:   wire.StrPtr(req.Az),
			El:   wire.StrPtr(req.El),
		}
		forwardCmd(w, cmd, send)
	}
}

func handlePolarization(send func(wire.Command) (*wire.Ack, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req polRequest
		if !decodeBody(w, r, &req) {
			return
		}
		cmd := wire.Command{
			Type:    "set_polarization",
			PolVHF:  wire.BoolPtr(req.PolVHF),
			PolUHF:  wire.BoolPtr(req.PolUHF),
			LnaUHF:  wire.BoolPtr(req.LnaUHF),
			RxTxUHF: wire.BoolPtr(req.RxTxUHF),
		}
		forwardCmd(w, cmd, send)
	}
}

func handleLimits(send func(wire.Command) (*wire.Ack, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req limitsRequest
		if !decodeBody(w, r, &req) {
			return
		}
		cmd := wire.Command{
			Type:  "set_limits",
			AzMin: wire.F64Ptr(req.AzMin),
			AzMax: wire.F64Ptr(req.AzMax),
			ElMin: wire.F64Ptr(req.ElMin),
			ElMax: wire.F64Ptr(req.ElMax),
		}
		forwardCmd(w, cmd, send)
	}
}

func handleSimple(cmdType string, send func(wire.Command) (*wire.Ack, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		forwardCmd(w, wire.Command{Type: cmdType}, send)
	}
}

// forwardCmd sends cmd to the field unit and writes the ack as JSON.
func forwardCmd(w http.ResponseWriter, cmd wire.Command, send func(wire.Command) (*wire.Ack, error)) {
	ack, err := send(cmd)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	if !ack.Ok {
		writeJSON(w, http.StatusBadRequest, ack)
		return
	}
	writeJSON(w, http.StatusOK, ack)
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// methodOnly wraps a handler to reject methods other than the given one.
func methodOnly(method string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Basic timeout guard
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		h.ServeHTTP(w, r)
	})
}

