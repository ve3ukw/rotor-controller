// Package wire mirrors the field unit's newline-delimited JSON protocol.
// Command types match protocol.h on the C side.
package wire

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// --- Inbound (field unit → brain) ---

// Telemetry is a UDP frame sent by the field unit at ~20 Hz.
type Telemetry struct {
	Type       string  `json:"type"`
	Seq        uint32  `json:"seq"`
	TsMs       uint32  `json:"ts_ms"`
	AzRaw      float64 `json:"az_raw"`
	ElRaw      float64 `json:"el_raw"`
	AzMotion   string  `json:"az_motion"`
	ElMotion   string  `json:"el_motion"`
	PolVHF     bool    `json:"pol_vhf"`
	PolUHF     bool    `json:"pol_uhf"`
	LnaUHF     bool    `json:"lna_uhf"`
	RxTxUHF    bool    `json:"rxtx_uhf"`
	State      string  `json:"state"`
	FaultDetail string `json:"fault_detail"`
	DutyAzPct  uint8   `json:"duty_az_pct"`
	DutyElPct  uint8   `json:"duty_el_pct"`
}

// Ack is a TCP response from the field unit to a command.
type Ack struct {
	Type  string `json:"type"`
	Seq   uint32 `json:"seq"`
	Ok    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// --- Outbound (brain → field unit) ---

// Command is the common envelope for all outbound TCP messages.
type Command struct {
	Type string `json:"type"`
	Seq  uint32 `json:"seq"`
	// set_motion fields
	Az *string `json:"az,omitempty"`
	El *string `json:"el,omitempty"`
	// set_polarization fields
	PolVHF  *bool `json:"pol_vhf,omitempty"`
	PolUHF  *bool `json:"pol_uhf,omitempty"`
	LnaUHF  *bool `json:"lna_uhf,omitempty"`
	RxTxUHF *bool `json:"rxtx_uhf,omitempty"`
	// set_limits fields
	AzMin *float64 `json:"az_min,omitempty"`
	AzMax *float64 `json:"az_max,omitempty"`
	ElMin *float64 `json:"el_min,omitempty"`
	ElMax *float64 `json:"el_max,omitempty"`
	// set_netconfig fields (dotted-decimal strings)
	IP      *string `json:"ip,omitempty"`
	Subnet  *string `json:"subnet,omitempty"`
	Gateway *string `json:"gateway,omitempty"`
	MAC     *string `json:"mac,omitempty"`
	// set_block fields ("az_deg" avoids collision with motion "az" string field)
	AzDeg   *float64 `json:"az_deg,omitempty"`   // AZ in degrees
	ElFloor *float64 `json:"el_floor,omitempty"` // min EL in degrees
	// set_blocks fields
	Blocks []uint8 `json:"blocks,omitempty"` // 90-entry array, degrees
}

func (c Command) Marshal() ([]byte, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ParseTelemetry decodes a raw UDP payload.
func ParseTelemetry(data []byte) (*Telemetry, error) {
	var t Telemetry
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// ackPrefix is the known start of every ack frame from the field unit.
// Searching for the full prefix (not just '{') avoids false starts on garbage
// bytes that happen to contain '{' before the real object.
var ackPrefix = []byte(`{"type":"ack"`)

// ParseAck decodes a TCP ack line.
//
// The W5500 on this chip variant sends a stale-TX-buffer flush at connect
// time, so the scanner delivers lines that are garbage, garbage+ack,
// or even two concatenated acks (when a '\n' lands inside the garbage).
// Strategy: scan for ackPrefix, attempt decode, retry on the next occurrence
// if the decode fails (handles garbage-{-ack or truncated+full ack pairs).
// Returns ErrNoAck if no recognisable ack is present — callers can
// silently discard those lines without polluting the log.
var ErrNoAck = fmt.Errorf("no ack in data")

func ParseAck(data []byte) (*Ack, error) {
	search := data
	for {
		start := bytes.Index(search, ackPrefix)
		if start < 0 {
			return nil, ErrNoAck
		}
		var a Ack
		if err := json.NewDecoder(bytes.NewReader(search[start:])).Decode(&a); err != nil {
			// This occurrence was truncated or corrupt — try the next one.
			search = search[start+len(ackPrefix):]
			continue
		}
		return &a, nil
	}
}

// Str helpers — match field unit string representations.
func StrPtr(s string) *string  { return &s }
func BoolPtr(b bool) *bool     { return &b }
func F64Ptr(f float64) *float64 { return &f }
