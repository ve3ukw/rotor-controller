package main

import "rotor-controller/brain/internal/tracker"

// Protocol decodes one rotor-control serial wire format on top of the
// shared Tracker. Add a new file + registry entry to support another
// rotator protocol — the framing (line splitting, CR-only terminators) and
// the brain-facing control loop are shared.
type Protocol interface {
	// Name is the -m identifier used to select this protocol.
	Name() string
	// HandleLine processes one input line (terminator already stripped) and
	// returns the bytes to write back, or nil for commands that get no reply.
	HandleLine(t *tracker.Tracker, line string) []byte
}

// protocols is the registry of available -m models.
var protocols = map[string]Protocol{}

func registerProtocol(p Protocol) {
	protocols[p.Name()] = p
}
