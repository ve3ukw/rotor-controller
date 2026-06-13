// Package calib converts between raw ADC position fractions (0..1, as
// reported in telemetry) and real-world degrees, correcting for the fact
// that the pot's electrical range doesn't exactly span the rotor's full
// mechanical travel (gain/offset error), and — for AZ — for the unknown
// rotation between the pot's electrical zero and true north.
package calib

import "math"

// Axis holds the gain calibration for one axis: the raw fraction (0..1)
// measured at each mechanical end stop. RawMin corresponds to mechanical
// 0°, RawMax to mechanical Range°.
type Axis struct {
	RawMin, RawMax float64
	Range          float64 // mechanical travel in degrees (450 for AZ, 180 for EL)
}

// MechDeg converts a raw fraction to mechanical degrees (0..Range),
// correcting for gain/offset error in the raw reading.
func (a Axis) MechDeg(raw float64) float64 {
	span := a.RawMax - a.RawMin
	if span == 0 {
		return raw * a.Range
	}
	return (raw - a.RawMin) / span * a.Range
}

// Raw converts mechanical degrees back to a raw fraction.
func (a Axis) Raw(mechDeg float64) float64 {
	span := a.RawMax - a.RawMin
	if span == 0 {
		return mechDeg / a.Range
	}
	return a.RawMin + (mechDeg/a.Range)*span
}

// TrueBearing converts mechanical AZ degrees (0..450) to a compass bearing
// (0..360, 0=N) given the offset between the pot's mechanical 0° and true
// north (offsetDeg = the mechanical degree reading that corresponds to 0°
// true).
func TrueBearing(mechDeg, offsetDeg float64) float64 {
	b := math.Mod(mechDeg-offsetDeg, 360)
	if b < 0 {
		b += 360
	}
	return b
}

// MechDegForBearing is the inverse of TrueBearing: given a target compass
// bearing, returns the mechanical degree (within [0, azRange]) closest to
// curMechDeg that points at that bearing. The G-5500's 450° mechanical
// range overlaps itself by 90°, so a given bearing maps to up to two valid
// mechanical positions — pick whichever requires less travel from the
// current position.
func MechDegForBearing(bearingDeg, offsetDeg, azRange, curMechDeg float64) float64 {
	base := math.Mod(bearingDeg+offsetDeg, 360)
	if base < 0 {
		base += 360
	}
	best := base
	bestDist := math.Abs(base - curMechDeg)
	if alt := base + 360; alt <= azRange {
		if d := math.Abs(alt - curMechDeg); d < bestDist {
			best, bestDist = alt, d
		}
	}
	return best
}
