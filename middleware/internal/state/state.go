// Package state holds the latest known field unit state shared between
// the telemetry receiver, API server, and MQTT publisher.
package state

import (
	"sync"
	"time"

	"rotor-controller/brain/internal/config"
	"rotor-controller/brain/internal/wire"
)

const BlockCount = 90 // 90 × 5° = 450° AZ range

// Limits holds the soft travel limits, normalized 0..1.
type Limits struct {
	AzMin, AzMax, ElMin, ElMax float64
}

// Store is a concurrency-safe snapshot of the latest field unit status.
type Store struct {
	mu        sync.RWMutex
	telemetry *wire.Telemetry
	updatedAt time.Time
	linked    bool
	blocks    [BlockCount]uint8 // AZ el_floor table, degrees, index = floor(az_deg/5)
	limits    *Limits           // soft travel limits, nil = not yet customized
	calib     config.Calibration
}

func NewStore() *Store {
	return &Store{calib: config.DefaultCalibration()}
}

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

// SetBlocks replaces the entire AZ block table.
func (s *Store) SetBlocks(b [BlockCount]uint8) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blocks = b
}

// SetBlock updates one 5° AZ chunk (index = floor(az_deg/5)).
func (s *Store) SetBlock(azDeg float64, elFloorDeg uint8) {
	idx := int(azDeg / 5.0)
	if idx < 0 {
		idx = 0
	}
	if idx >= BlockCount {
		idx = BlockCount - 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blocks[idx] = elFloorDeg
}

// Blocks returns a copy of the current block table.
func (s *Store) Blocks() [BlockCount]uint8 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.blocks
}

// SetLimits stores the configured soft travel limits.
func (s *Store) SetLimits(l Limits) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limits = &l
}

// Limits returns the configured soft travel limits, or nil if never set.
func (s *Store) Limits() *Limits {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.limits == nil {
		return nil
	}
	v := *s.limits
	return &v
}

// SetCalibration replaces the pot gain/offset calibration.
func (s *Store) SetCalibration(c config.Calibration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calib = c
}

// Calibration returns the current pot gain/offset calibration.
func (s *Store) Calibration() config.Calibration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.calib
}
