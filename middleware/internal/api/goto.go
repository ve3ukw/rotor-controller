package api

import (
	"context"
	"fmt"
	"sync"
	"time"

	"rotor-controller/brain/internal/calib"
	"rotor-controller/brain/internal/config"
	"rotor-controller/brain/internal/state"
	"rotor-controller/brain/internal/wire"
)

// gotoToleranceDeg/gotoHysteresisDeg define a per-axis deadband (in true
// degrees, converted to normalized units via azRange/elRange below). A
// multi-element Yagi has a beamwidth far wider than this, so a couple of
// degrees of slop costs nothing — but the field unit's AZ position reading
// jitters on its own, which without a deadband causes the goto loop to hunt
// back and forth (cw/ccw/cw/...) forever, burning duty cycle.
// gotoToleranceDeg stops the axis once within range; gotoHysteresisDeg
// (wider) is required before a stopped axis resumes correcting, so jitter
// around the edge of the tolerance band doesn't restart motion. Keep
// gotoToleranceDeg comfortably below the UI's smallest "nudge" increment
// (5°), or a same-size nudge request can land inside the deadband and
// report done without moving the antenna at all.
const gotoToleranceDeg = 2.0
const gotoHysteresisDeg = 10.0

// gotoPollInterval controls how often the controller checks position and
// (re)issues motion commands.
const gotoPollInterval = 500 * time.Millisecond

// GotoController drives the rotor toward a target AZ/EL position by
// repeatedly issuing set_motion commands until both axes are within
// tolerance, then stops. Only one goto can be active at a time — starting
// a new one cancels any in-progress goto.
type GotoController struct {
	send             func(wire.Command) (*wire.Ack, error)
	st               *state.Store
	azRange, elRange float64

	mu     sync.Mutex
	cancel context.CancelFunc
	active bool
	azDeg  float64
	elDeg  float64
	err    string
}

func NewGotoController(st *state.Store, send func(wire.Command) (*wire.Ack, error), azRange, elRange float64) *GotoController {
	return &GotoController{send: send, st: st, azRange: azRange, elRange: elRange}
}

// GotoStatus reports the current goto state for the UI.
type GotoStatus struct {
	Active bool    `json:"active"`
	AzDeg  float64 `json:"az_deg,omitempty"`
	ElDeg  float64 `json:"el_deg,omitempty"`
	Error  string  `json:"error,omitempty"`
}

func (g *GotoController) Status() GotoStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	return GotoStatus{Active: g.active, AzDeg: g.azDeg, ElDeg: g.elDeg, Error: g.err}
}

// Start begins driving toward (azDeg, elDeg), cancelling any goto in
// progress. azDeg is a true compass bearing (0..360, 0=N); elDeg is the
// mechanical EL degree (0..elRange), matching `rotor status --deg`.
func (g *GotoController) Start(azDeg, elDeg float64) error {
	if azDeg < 0 || azDeg >= 360 || elDeg < 0 || elDeg > g.elRange {
		return fmt.Errorf("az_deg 0-359.9 (compass bearing), el_deg 0-%.0f (got az=%.1f el=%.1f)", g.elRange, azDeg, elDeg)
	}

	t, linked, _ := g.st.Snapshot()
	if !linked || t == nil {
		return fmt.Errorf("field unit not linked — cannot resolve target position")
	}

	c := g.st.Calibration()
	azAxis := calib.Axis{RawMin: c.AzRawMin, RawMax: c.AzRawMax, Range: g.azRange}
	elAxis := calib.Axis{RawMin: c.ElRawMin, RawMax: c.ElRawMax, Range: g.elRange}

	curMechDeg := azAxis.MechDeg(t.AzRaw)
	targetMechDeg := calib.MechDegForBearing(azDeg, c.AzOffsetDeg, g.azRange, curMechDeg)
	azTarget := azAxis.Raw(targetMechDeg)
	elTarget := elAxis.Raw(elDeg)

	g.mu.Lock()
	if g.cancel != nil {
		g.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	g.cancel = cancel
	g.active = true
	g.azDeg = azDeg
	g.elDeg = elDeg
	g.err = ""
	g.mu.Unlock()

	go g.run(ctx, azTarget, elTarget, c)
	return nil
}

// Cancel stops any in-progress goto and halts motion.
func (g *GotoController) Cancel() {
	g.mu.Lock()
	if g.cancel != nil {
		g.cancel()
		g.cancel = nil
	}
	g.active = false
	g.mu.Unlock()

	_, _ = g.send(wire.Command{Type: "set_motion", Az: wire.StrPtr("stop"), El: wire.StrPtr("stop")})
}

// axisCmd decides the motion command for one axis given its signed error
// (target - position, normalized 0..1). If the axis is currently stopped, it
// must clear the wider hysteresis band before motion resumes; if already
// moving, it stops as soon as it's within the tighter tolerance band.
func axisCmd(err, tol, hyst float64, last string, posCmd, negCmd string) string {
	threshold := tol
	if last == "stop" {
		threshold = hyst
	}
	switch {
	case err > threshold:
		return posCmd
	case -err > threshold:
		return negCmd
	default:
		return "stop"
	}
}

func (g *GotoController) run(ctx context.Context, azTarget, elTarget float64, c config.Calibration) {
	ticker := time.NewTicker(gotoPollInterval)
	defer ticker.Stop()

	azSpan := c.AzRawMax - c.AzRawMin
	elSpan := c.ElRawMax - c.ElRawMin
	azTol := gotoToleranceDeg / g.azRange * azSpan
	azHyst := gotoHysteresisDeg / g.azRange * azSpan
	elTol := gotoToleranceDeg / g.elRange * elSpan
	elHyst := gotoHysteresisDeg / g.elRange * elSpan

	lastAz, lastEl := "", ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		t, linked, _ := g.st.Snapshot()
		if !linked || t == nil {
			continue
		}

		azCmd := axisCmd(azTarget-t.AzRaw, azTol, azHyst, lastAz, "cw", "ccw")
		elCmd := axisCmd(elTarget-t.ElRaw, elTol, elHyst, lastEl, "up", "down")

		if azCmd != lastAz || elCmd != lastEl {
			if _, err := g.send(wire.Command{Type: "set_motion", Az: wire.StrPtr(azCmd), El: wire.StrPtr(elCmd)}); err != nil {
				g.mu.Lock()
				g.active = false
				g.err = err.Error()
				g.mu.Unlock()
				return
			}
			lastAz, lastEl = azCmd, elCmd
		}

		if azCmd == "stop" && elCmd == "stop" {
			g.mu.Lock()
			g.active = false
			g.mu.Unlock()
			return
		}
	}
}
