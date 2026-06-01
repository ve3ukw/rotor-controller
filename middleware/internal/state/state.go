// Package state holds the latest known field unit state shared between
// the telemetry receiver, API server, and MQTT publisher.
package state

import (
	"sync"
	"time"

	"rotor-controller/brain/internal/wire"
)

// Store is a concurrency-safe snapshot of the latest field unit status.
type Store struct {
	mu        sync.RWMutex
	telemetry *wire.Telemetry
	updatedAt time.Time
	linked    bool // TCP connected to field unit
}

func NewStore() *Store { return &Store{} }

func (s *Store) UpdateTelemetry(t *wire.Telemetry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.telemetry = t
	s.updatedAt = time.Now()
}

func (s *Store) SetLinked(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.linked = v
}

// Snapshot returns a copy of current telemetry plus link status.
// Returns nil telemetry if no frame received yet.
func (s *Store) Snapshot() (t *wire.Telemetry, linked bool, age time.Duration) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.telemetry != nil {
		cp := *s.telemetry
		return &cp, s.linked, time.Since(s.updatedAt)
	}
	return nil, s.linked, 0
}
