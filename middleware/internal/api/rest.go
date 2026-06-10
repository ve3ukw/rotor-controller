package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"rotor-controller/brain/internal/state"
	"rotor-controller/brain/internal/wire"
)

type statusResponse struct {
	Linked    bool            `json:"linked"`
	AgeMs     int64           `json:"age_ms,omitempty"` // milliseconds since last telemetry
	Telemetry *wire.Telemetry `json:"telemetry,omitempty"`
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

type netconfigRequest struct {
	IP      string `json:"ip"`
	Subnet  string `json:"subnet"`
	Gateway string `json:"gateway"`
	MAC     string `json:"mac,omitempty"` // optional
}

func handleNetconfig(send func(wire.Command) (*wire.Ack, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req netconfigRequest
		if !decodeBody(w, r, &req) {
			return
		}
		if req.IP == "" || req.Subnet == "" || req.Gateway == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ip, subnet and gateway are required"})
			return
		}
		cmd := wire.Command{
			Type:    "set_netconfig",
			IP:      wire.StrPtr(req.IP),
			Subnet:  wire.StrPtr(req.Subnet),
			Gateway: wire.StrPtr(req.Gateway),
		}
		if req.MAC != "" {
			cmd.MAC = wire.StrPtr(req.MAC)
		}
		// NOTE: the field unit will drop the TCP connection immediately after
		// acking this command (its IP changes).  The ack may arrive before the
		// connection closes; a 5xx here just means the timing was tight.
		forwardCmd(w, cmd, send)
	}
}

func handleResetNetconfig(send func(wire.Command) (*wire.Ack, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		forwardCmd(w, wire.Command{Type: "reset_netconfig"}, send)
	}
}

// handleRange reports the configured AZ/EL travel range in degrees, used by
// the web UI to validate and label the goto-coordinate inputs.
func handleRange(azRange, elRange float64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]float64{"az_range": azRange, "el_range": elRange})
	}
}

// ── goto endpoint ─────────────────────────────────────────────────────────────

type gotoRequest struct {
	AzDeg float64 `json:"az_deg"`
	ElDeg float64 `json:"el_deg"`
}

func handleGoto(gc *GotoController) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, gc.Status())
		case http.MethodPost:
			var req gotoRequest
			if !decodeBody(w, r, &req) {
				return
			}
			if err := gc.Start(req.AzDeg, req.ElDeg); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, gc.Status())
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func handleGotoCancel(gc *GotoController) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		gc.Cancel()
		writeJSON(w, http.StatusOK, gc.Status())
	}
}

// ── block endpoints ──────────────────────────────────────────────────────────

type blockSetRequest struct {
	AzDeg   float64 `json:"az_deg"`   // AZ of the 5° sector, degrees
	ElFloor float64 `json:"el_floor"` // minimum elevation, degrees (0 = unrestricted)
}

// handleBlockGet returns the full 90-entry block table as a JSON array.
func handleBlockGet(st *state.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tbl := st.Blocks()
		writeJSON(w, http.StatusOK, map[string]any{
			"chunks":    tbl[:],
			"chunk_deg": 5,
		})
	}
}

// handleBlockSet sets one 5° sector and saves to field unit + config file.
func handleBlockSet(st *state.Store, send func(wire.Command) (*wire.Ack, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req blockSetRequest
		if !decodeBody(w, r, &req) {
			return
		}
		if req.AzDeg < 0 || req.AzDeg > 450 || req.ElFloor < 0 || req.ElFloor > 180 {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("az_deg 0-450, el_floor 0-180 (got az=%.1f el=%.1f)", req.AzDeg, req.ElFloor),
			})
			return
		}
		cmd := wire.Command{
			Type:    "set_block",
			AzDeg:   wire.F64Ptr(req.AzDeg),
			ElFloor: wire.F64Ptr(req.ElFloor),
		}
		ack, err := send(cmd)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			return
		}
		if !ack.Ok {
			writeJSON(w, http.StatusBadRequest, ack)
			return
		}
		st.SetBlock(req.AzDeg, uint8(req.ElFloor+0.5))
		writeJSON(w, http.StatusOK, ack)
	}
}

// handleBlockReset clears all blocks.
func handleBlockReset(st *state.Store, send func(wire.Command) (*wire.Ack, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ack, err := send(wire.Command{Type: "reset_blocks"})
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			return
		}
		if !ack.Ok {
			writeJSON(w, http.StatusBadRequest, ack)
			return
		}
		st.SetBlocks([state.BlockCount]uint8{})
		writeJSON(w, http.StatusOK, ack)
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
