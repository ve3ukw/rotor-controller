package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	"rotor-controller/brain/internal/tracker"
)

// gs232b implements the Yaesu GS-232B serial command set — the de facto
// standard "Yaesu" rotator protocol spoken by most ham radio software,
// including Ham Radio Deluxe. Commands are single uppercase letters,
// optionally followed by digits (space-separated for two values), terminated
// by CR. Confirmed wire format (captured from a real GS-232B exchange):
//
//	C2\r          -> "AZ=322EL=000\r"   (no space between fields, no LF)
//	Wddd eee\r    -> no reply
//
// L/R/U/D/A/E mirror the controller's independent AZ/EL jog buttons: each
// touches only its own axis, leaving the other axis's last commanded motion
// alone (e.g. holding R then tapping U should jog both simultaneously, not
// stop AZ). That per-axis state lives here, not in the shared Tracker, since
// it's a quirk of this protocol's UI model rather than anything the brain or
// rotctld protocol need to know about.
type gs232b struct {
	mu    sync.Mutex
	azCmd string // "cw" | "ccw" | "stop"
	elCmd string // "up" | "down" | "stop"
}

func newGS232B() *gs232b {
	return &gs232b{azCmd: "stop", elCmd: "stop"}
}

func (g *gs232b) Name() string { return "gs232b" }

func (g *gs232b) HandleLine(t *tracker.Tracker, line string) []byte {
	line = strings.ToUpper(strings.TrimSpace(line))
	if line == "" {
		return nil
	}
	cmd := line[0]
	rest := strings.TrimSpace(line[1:])

	switch cmd {
	case 'C': // C: azimuth only. C2: azimuth + elevation.
		az, el := t.Position()
		_, elRange := t.Range()
		if rest == "2" {
			return []byte(fmt.Sprintf("AZ=%03dEL=%03d", clampInt(az, 0, 359), clampInt(el, 0, elRange)))
		}
		return []byte(fmt.Sprintf("AZ=%03d", clampInt(az, 0, 359)))

	case 'B': // elevation-only query (symmetric convenience; not all controllers have this)
		_, el := t.Position()
		_, elRange := t.Range()
		return []byte(fmt.Sprintf("EL=%03d", clampInt(el, 0, elRange)))

	case 'M': // move to azimuth only — elevation left at its current value
		az, err := strconv.ParseFloat(rest, 64)
		if err != nil {
			return nil
		}
		_, el := t.Position()
		t.SetTarget(az, el)
		return nil

	case 'W': // move to azimuth and elevation: "Wddd eee"
		fields := strings.Fields(rest)
		if len(fields) != 2 {
			return nil
		}
		az, err1 := strconv.ParseFloat(fields[0], 64)
		el, err2 := strconv.ParseFloat(fields[1], 64)
		if err1 != nil || err2 != nil {
			return nil
		}
		t.SetTarget(az, el)
		return nil

	case 'L': // CCW azimuth jog
		g.jogAz(t, "ccw")
		return nil
	case 'R': // CW azimuth jog
		g.jogAz(t, "cw")
		return nil
	case 'U': // elevation up jog
		g.jogEl(t, "up")
		return nil
	case 'D': // elevation down jog
		g.jogEl(t, "down")
		return nil
	case 'A': // stop azimuth only
		g.jogAz(t, "stop")
		return nil
	case 'E': // stop elevation only
		g.jogEl(t, "stop")
		return nil
	case 'S': // stop both axes
		g.mu.Lock()
		g.azCmd, g.elCmd = "stop", "stop"
		g.mu.Unlock()
		t.Stop()
		return nil
	case 'X': // X1-X4 speed select — no variable-speed motor control on this hardware; accept, no-op
		return nil
	}
	return nil
}

func (g *gs232b) jogAz(t *tracker.Tracker, cmd string) {
	g.mu.Lock()
	g.azCmd = cmd
	az, el := g.azCmd, g.elCmd
	g.mu.Unlock()
	t.Move(az, el)
}

func (g *gs232b) jogEl(t *tracker.Tracker, cmd string) {
	g.mu.Lock()
	g.elCmd = cmd
	az, el := g.azCmd, g.elCmd
	g.mu.Unlock()
	t.Move(az, el)
}

func clampInt(v, lo, hi float64) int {
	v = math.Mod(v, 360)
	if v < 0 {
		v += 360
	}
	if v < lo {
		v = lo
	}
	if v > hi {
		v = hi
	}
	return int(v + 0.5)
}

func init() {
	registerProtocol(newGS232B())
}
