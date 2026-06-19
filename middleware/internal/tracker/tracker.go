// Package tracker holds the brain-facing position tracking and bang-bang
// control loop shared by every rotor-control frontend (rotctld over TCP,
// GS-232B over serial, …). Each frontend owns its own wire protocol parsing
// and framing; this package only deals in az/el degrees and brain HTTP/WS calls.
package tracker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"rotor-controller/brain/internal/calib"
	brainconfig "rotor-controller/brain/internal/config"
)

// Config holds the parameters needed to run a Tracker against a brain instance.
type Config struct {
	BrainURL    string
	AzRange     float64 // full AZ travel in degrees (G-5500: 450)
	ElRange     float64 // full EL travel in degrees (G-5500: 180)
	Tolerance   float64 // degrees of error tolerated before the bang-bang loop calls it "arrived"
	AzOffsetDeg float64 // added to commanded AZ before steering (positive = more CW)
	ElOffsetDeg float64 // added to commanded EL before steering (positive = more up)
}

// resendInterval is how often the current motion command is re-sent to the
// brain even if unchanged, so a dropped /api/v1/motion POST self-heals.
const resendInterval = 5 * time.Second

// httpClient bounds brain HTTP calls so a stalled/unreachable brain can't
// block a caller indefinitely — e.g. rotor-serial's single-threaded serial
// read loop, which would otherwise stop processing all incoming commands.
var httpClient = &http.Client{Timeout: 3 * time.Second}

// Tracker maintains the live position from brain telemetry and runs the
// bang-bang positioning loop when a target is set via SetTarget.
type Tracker struct {
	cfg Config

	// cal holds the AZ/EL pot gain calibration and AZ true-north offset,
	// fetched from the brain at startup. azAxis/elAxis convert raw ADC
	// fractions (0..1) to mechanical degrees.
	cal    brainconfig.Calibration
	azAxis calib.Axis
	elAxis calib.Axis

	mu sync.RWMutex

	// Current state (from brain WebSocket telemetry). azDeg/elDeg are
	// mechanical degrees (gain-corrected, NOT az-offset-corrected).
	azDeg  float64
	elDeg  float64
	state  string
	linked bool

	// Active tracking target (NaN = not tracking)
	targetAz float64
	targetEl float64

	// Last commanded directions, and when they were last sent.
	lastAzCmd string
	lastElCmd string
	lastSent  time.Time
}

func New(cfg Config, cal brainconfig.Calibration) *Tracker {
	return &Tracker{
		cfg:      cfg,
		cal:      cal,
		azAxis:   calib.Axis{RawMin: cal.AzRawMin, RawMax: cal.AzRawMax, Range: cfg.AzRange},
		elAxis:   calib.Axis{RawMin: cal.ElRawMin, RawMax: cal.ElRawMax, Range: cfg.ElRange},
		targetAz: math.NaN(),
		targetEl: math.NaN(),
	}
}

// FetchCalibration retrieves the AZ/EL pot gain calibration and AZ
// true-north offset from the brain. If the brain is unreachable or returns
// an error, falls back to uncalibrated 1:1 (raw == mechanical degrees,
// AZ offset 0) so the caller still starts and behaves sanely.
func FetchCalibration(brainURL string) brainconfig.Calibration {
	resp, err := httpClient.Get(brainURL + "/api/v1/calibration")
	if err != nil {
		log.Printf("tracker: fetch calibration: %v — using uncalibrated 1:1", err)
		return brainconfig.DefaultCalibration()
	}
	defer resp.Body.Close()
	var cal brainconfig.Calibration
	if err := json.NewDecoder(resp.Body).Decode(&cal); err != nil {
		log.Printf("tracker: decode calibration: %v — using uncalibrated 1:1", err)
		return brainconfig.DefaultCalibration()
	}
	return cal
}

// Run starts the telemetry subscriber and the control loop. Blocks forever.
func (t *Tracker) Run() {
	go t.subscribeWS()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		t.step()
	}
}

// Range returns the configured AZ/EL travel ranges in degrees.
func (t *Tracker) Range() (azRange, elRange float64) {
	return t.cfg.AzRange, t.cfg.ElRange
}

// Status returns the brain link state and the field unit's reported state string.
func (t *Tracker) Status() (linked bool, state string) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.linked, t.state
}

// Position returns current az/el in degrees, with pointing offsets removed
// so the controlling software sees the antenna at the bearing it commanded.
// az is a true compass bearing (0..360, 0=N); el is mechanical degrees.
func (t *Tracker) Position() (az, el float64) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	az = math.Mod(calib.TrueBearing(t.azDeg, t.cal.AzOffsetDeg)-t.cfg.AzOffsetDeg+360, 360)
	el = t.elDeg - t.cfg.ElOffsetDeg
	return
}

// SetTarget commands the tracker to slew to az/el (degrees).
//
// The G-5500's extended range (EL 0-180°) lets the antenna reach any sky
// position in two ways: a "normal" raw pose (el ≤ 90°) or a "past-zenith"
// raw pose (el > 90°, az offset by 180°) that points at the same true
// compass/elevation. Given a target, pick whichever raw representation is
// closer to the antenna's current raw position, so tracking never swings
// the antenna up over zenith and back down just to reach an equivalent pose.
func (t *Tracker) SetTarget(azBearing, elDeg float64) {
	// Apply pointing correction offsets before all other math.
	// Positive AzOffsetDeg rotates the antenna further CW than commanded;
	// positive ElOffsetDeg tilts it further up.
	azBearing = math.Mod(azBearing+t.cfg.AzOffsetDeg+360, 360)
	elDeg += t.cfg.ElOffsetDeg

	// Clamp EL to reachable range.
	el := clamp(elDeg, 0, t.cfg.ElRange)

	t.mu.Lock()
	curAz, curEl := t.azDeg, t.elDeg

	// Convert the client's compass bearing to a mechanical AZ degree,
	// picking whichever of the two mechanical representations (base or
	// base+360, due to the 450°/360° overlap) is closer to the antenna's
	// current mechanical position.
	az := calib.MechDegForBearing(azBearing, t.cal.AzOffsetDeg, t.cfg.AzRange, curAz)

	// True (compass, elevation ≤ 90°) pose of the requested target.
	trueAz, trueEl := az, el
	if el > 90 {
		trueAz -= 180
		trueEl = 180 - el
	}
	if trueAz < 0 {
		trueAz += 360
	}

	// Below ~5° elevation is rarely a usable link, and the firmware enforces
	// a matching soft limit (el_min/el_max = 5°/175° raw) — hold at 5° true
	// elevation rather than chasing the target to the horizon.
	if trueEl < 5 {
		trueEl = 5
	}

	// Two raw representations of that true pose.
	normAz, normEl := trueAz, trueEl
	zenAz := trueAz + 180
	if zenAz > t.cfg.AzRange {
		zenAz -= 360
	}
	zenEl := 180 - trueEl

	// Pick whichever is closer to the antenna's current raw position.
	normDist := math.Abs(normAz-curAz) + math.Abs(normEl-curEl)
	zenDist := math.Abs(zenAz-curAz) + math.Abs(zenEl-curEl)
	if zenDist < normDist {
		az, el = zenAz, zenEl
	} else {
		az, el = normAz, normEl
	}

	t.targetAz = az
	t.targetEl = el
	t.mu.Unlock()
	log.Printf("tracker: track → AZ %.1f° mech (bearing %.1f°)  EL %.1f°", az, azBearing, el)
}

// Stop cancels tracking and commands all motion to stop.
func (t *Tracker) Stop() {
	t.mu.Lock()
	t.targetAz = math.NaN()
	t.targetEl = math.NaN()
	t.mu.Unlock()
	t.sendMotion("stop", "stop")
}

// Move commands manual directional movement and cancels any active tracking.
// azCmd: "cw" | "ccw" | "stop"   elCmd: "up" | "down" | "stop"
func (t *Tracker) Move(azCmd, elCmd string) {
	t.mu.Lock()
	t.targetAz = math.NaN()
	t.targetEl = math.NaN()
	t.mu.Unlock()
	t.sendMotion(azCmd, elCmd)
}

// Park sends the firmware park command.
func (t *Tracker) Park() {
	t.mu.Lock()
	t.targetAz = math.NaN()
	t.targetEl = math.NaN()
	t.mu.Unlock()
	t.postBrain("/api/v1/park", nil)
}

// ClearFault clears a controller fault.
func (t *Tracker) ClearFault() {
	t.postBrain("/api/v1/clear_fault", nil)
}

// step runs one iteration of the bang-bang control loop.
func (t *Tracker) step() {
	t.mu.RLock()
	hasTarget := !math.IsNaN(t.targetAz)
	az, el := t.azDeg, t.elDeg
	targetAz, targetEl := t.targetAz, t.targetEl
	t.mu.RUnlock()

	if !hasTarget {
		return
	}

	azErr := targetAz - az
	elErr := targetEl - el

	var azCmd, elCmd string
	switch {
	case azErr > t.cfg.Tolerance:
		azCmd = "cw"
	case azErr < -t.cfg.Tolerance:
		azCmd = "ccw"
	default:
		azCmd = "stop"
	}
	switch {
	case elErr > t.cfg.Tolerance:
		elCmd = "up"
	case elErr < -t.cfg.Tolerance:
		elCmd = "down"
	default:
		elCmd = "stop"
	}

	t.mu.Lock()
	changed := azCmd != t.lastAzCmd || elCmd != t.lastElCmd
	due := time.Since(t.lastSent) >= resendInterval
	t.lastAzCmd = azCmd
	t.lastElCmd = elCmd
	if azCmd == "stop" && elCmd == "stop" {
		// Target reached — clear so we stop sending commands.
		t.targetAz = math.NaN()
		t.targetEl = math.NaN()
		log.Printf("tracker: target reached (AZ %.1f° EL %.1f°)", az, el)
	}
	if changed || due {
		t.lastSent = time.Now()
	}
	t.mu.Unlock()

	if changed || due {
		log.Printf("tracker: send az=%s el=%s (azErr=%.1f° elErr=%.1f°)", azCmd, elCmd, azErr, elErr)
		t.sendMotion(azCmd, elCmd)
	}
}

func (t *Tracker) subscribeWS() {
	wsURL := httpToWS(t.cfg.BrainURL) + "/api/v1/telemetry/ws"
	for {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			log.Printf("tracker: brain ws: %v — retrying in 5s", err)
			time.Sleep(5 * time.Second)
			continue
		}
		log.Printf("tracker: brain telemetry connected")
		t.mu.Lock()
		t.linked = true
		t.mu.Unlock()

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				break
			}
			var telem struct {
				AzRaw float64 `json:"az_raw"`
				ElRaw float64 `json:"el_raw"`
				State string  `json:"state"`
			}
			if json.Unmarshal(msg, &telem) == nil && telem.State != "" {
				t.mu.Lock()
				t.azDeg = t.azAxis.MechDeg(telem.AzRaw)
				t.elDeg = t.elAxis.MechDeg(telem.ElRaw)
				t.state = telem.State
				t.mu.Unlock()
			}
		}
		conn.Close()
		t.mu.Lock()
		t.linked = false
		t.mu.Unlock()
		log.Printf("tracker: brain telemetry lost — reconnecting in 5s")
		time.Sleep(5 * time.Second)
	}
}

func (t *Tracker) sendMotion(az, el string) {
	body := fmt.Sprintf(`{"az":%q,"el":%q}`, az, el)
	t.postBrain("/api/v1/motion", []byte(body))
}

func (t *Tracker) postBrain(path string, body []byte) {
	var r *bytes.Reader
	if body != nil {
		r = bytes.NewReader(body)
	} else {
		r = bytes.NewReader(nil)
	}
	req, err := http.NewRequest("POST", t.cfg.BrainURL+path, r)
	if err != nil {
		return
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("tracker: brain %s: %v", path, err)
		return
	}
	resp.Body.Close()
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func httpToWS(u string) string {
	if strings.HasPrefix(u, "https://") {
		return "wss://" + u[8:]
	}
	return "ws://" + strings.TrimPrefix(u, "http://")
}
