package main

import (
	"testing"

	brainconfig "rotor-controller/brain/internal/config"
	"rotor-controller/brain/internal/tracker"
)

func newTestTracker() *tracker.Tracker {
	return tracker.New(tracker.Config{
		BrainURL:  "http://127.0.0.1:1", // closed port — fails fast (connection refused) instead of timing out
		AzRange:   450,
		ElRange:   180,
		Tolerance: 2.0,
	}, brainconfig.DefaultCalibration())
}

func TestGS232BQueryFormat(t *testing.T) {
	g := newGS232B()
	trk := newTestTracker()

	got := string(g.HandleLine(trk, "C2"))
	want := "AZ=000EL=000"
	if got != want {
		t.Errorf("C2 = %q, want %q", got, want)
	}

	got = string(g.HandleLine(trk, "C"))
	want = "AZ=000"
	if got != want {
		t.Errorf("C = %q, want %q", got, want)
	}

	got = string(g.HandleLine(trk, "B"))
	want = "EL=000"
	if got != want {
		t.Errorf("B = %q, want %q", got, want)
	}
}

func TestGS232BSetCommandsNoReply(t *testing.T) {
	g := newGS232B()
	trk := newTestTracker()

	for _, line := range []string{"W180 045", "M090", "S", "L", "R", "U", "D", "A", "E", "X2"} {
		if reply := g.HandleLine(trk, line); reply != nil {
			t.Errorf("%q reply = %q, want nil (no reply)", line, reply)
		}
	}
}

func TestGS232BUnknownCommandIgnored(t *testing.T) {
	g := newGS232B()
	trk := newTestTracker()

	if reply := g.HandleLine(trk, "ZZZ"); reply != nil {
		t.Errorf("unknown command reply = %q, want nil", reply)
	}
	if reply := g.HandleLine(trk, ""); reply != nil {
		t.Errorf("empty line reply = %q, want nil", reply)
	}
}

func TestGS232BIndependentAxisJog(t *testing.T) {
	// L/R only touch AZ; U/D only touch EL. Verify the remembered per-axis
	// state doesn't clobber the other axis (the whole reason this state
	// lives in the protocol instead of always defaulting to "stop").
	g := newGS232B()

	trk := newTestTracker()
	g.jogAz(trk, "cw")
	if g.azCmd != "cw" || g.elCmd != "stop" {
		t.Fatalf("after jogAz(cw): azCmd=%q elCmd=%q, want cw/stop", g.azCmd, g.elCmd)
	}
	g.jogEl(trk, "up")
	if g.azCmd != "cw" || g.elCmd != "up" {
		t.Fatalf("after jogEl(up): azCmd=%q elCmd=%q, want cw/up (AZ should be untouched)", g.azCmd, g.elCmd)
	}
}
