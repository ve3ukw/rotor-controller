// rotor-hamlib — rotctld-compatible TCP server for the rotor controller.
//
// Any Hamlib-aware application (gpredict, WSJT-X, SkyRoof/SkyCat, fldigi …)
// can connect on port 4533 and control the antenna as if speaking to rotctld.
//
// Supported commands (Hamlib text protocol):
//
//	p          Get position       → az\nel\nRPRT 0
//	P az el    Go to position     → RPRT 0
//	S          Stop               → RPRT 0
//	K          Park               → RPRT 0
//	C          Clear fault        → RPRT 0
//	M dir spd  Move direction     → RPRT 0  (dir: 2=UP 4=DOWN 8=CCW 16=CW)
//	_          Model info         → RPRT 0
//	1          Dump caps          → RPRT 0
//	q / Q      Quit connection
//
// Commands may be prefixed with '+' (Hamlib extended) or '\' (escaped);
// both are handled identically.
//
// Environment:
//
//	HAMLIB_LISTEN     TCP listen address   (default: :4533)
//	HAMLIB_BRAIN_URL  Brain API base URL   (default: http://localhost:8090)
//	HAMLIB_AZ_RANGE   Full AZ travel °     (default: 450  — Yaesu G-5500)
//	HAMLIB_EL_RANGE   Full EL travel °     (default: 180  — Yaesu G-5500)
//	HAMLIB_TOLERANCE  Stop tolerance °     (default: 2.0)
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ── config ────────────────────────────────────────────────────────────────────

type config struct {
	listen    string
	brainURL  string
	azRange   float64
	elRange   float64
	tolerance float64
}

func loadConfig() config {
	return config{
		listen:    envStr("HAMLIB_LISTEN", ":4533"),
		brainURL:  envStr("HAMLIB_BRAIN_URL", "http://localhost:8090"),
		azRange:   envFloat("HAMLIB_AZ_RANGE", 450),
		elRange:   envFloat("HAMLIB_EL_RANGE", 180),
		tolerance: envFloat("HAMLIB_TOLERANCE", 2.0),
	}
}

// ── tracker ───────────────────────────────────────────────────────────────────

// Tracker maintains the live position from brain telemetry and runs the
// bang-bang positioning loop when a target is set via P.
type Tracker struct {
	cfg config

	mu sync.RWMutex

	// Current state (from brain WebSocket telemetry)
	azDeg  float64
	elDeg  float64
	state  string
	linked bool

	// Active tracking target (NaN = not tracking)
	targetAz float64
	targetEl float64

	// Last commanded directions — only send when changed
	lastAzCmd string
	lastElCmd string
}

func newTracker(cfg config) *Tracker {
	return &Tracker{
		cfg:      cfg,
		targetAz: math.NaN(),
		targetEl: math.NaN(),
	}
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

// Position returns current az/el in degrees.
func (t *Tracker) Position() (az, el float64) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.azDeg, t.elDeg
}

// SetTarget commands the tracker to slew to az/el (degrees).
func (t *Tracker) SetTarget(az, el float64) {
	// Clamp to reachable range.
	az = clamp(az, 0, t.cfg.azRange)
	el = clamp(el, 0, t.cfg.elRange)
	t.mu.Lock()
	t.targetAz = az
	t.targetEl = el
	t.mu.Unlock()
	log.Printf("hamlib: track → AZ %.1f°  EL %.1f°", az, el)
}

// Stop cancels tracking and commands all motion to stop.
func (t *Tracker) Stop() {
	t.mu.Lock()
	t.targetAz = math.NaN()
	t.targetEl = math.NaN()
	t.mu.Unlock()
	t.sendMotion("stop", "stop")
}

// Move handles the Hamlib M command (manual direction movement).
// direction bits: 2=UP 4=DOWN 8=CCW 16=CW (may be OR'd together).
func (t *Tracker) Move(direction int) {
	t.mu.Lock()
	t.targetAz = math.NaN() // cancel any active tracking
	t.targetEl = math.NaN()
	t.mu.Unlock()

	var azCmd, elCmd string
	switch {
	case direction&16 != 0:
		azCmd = "cw"
	case direction&8 != 0:
		azCmd = "ccw"
	default:
		azCmd = "stop"
	}
	switch {
	case direction&2 != 0:
		elCmd = "up"
	case direction&4 != 0:
		elCmd = "down"
	default:
		elCmd = "stop"
	}
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
	case azErr > t.cfg.tolerance:
		azCmd = "cw"
	case azErr < -t.cfg.tolerance:
		azCmd = "ccw"
	default:
		azCmd = "stop"
	}
	switch {
	case elErr > t.cfg.tolerance:
		elCmd = "up"
	case elErr < -t.cfg.tolerance:
		elCmd = "down"
	default:
		elCmd = "stop"
	}

	t.mu.Lock()
	changed := azCmd != t.lastAzCmd || elCmd != t.lastElCmd
	t.lastAzCmd = azCmd
	t.lastElCmd = elCmd
	if azCmd == "stop" && elCmd == "stop" {
		// Target reached — clear so we stop sending commands.
		t.targetAz = math.NaN()
		t.targetEl = math.NaN()
		log.Printf("hamlib: target reached (AZ %.1f° EL %.1f°)", az, el)
	}
	t.mu.Unlock()

	if changed {
		t.sendMotion(azCmd, elCmd)
	}
}

func (t *Tracker) subscribeWS() {
	wsURL := httpToWS(t.cfg.brainURL) + "/api/v1/telemetry/ws"
	for {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			log.Printf("hamlib: brain ws: %v — retrying in 5s", err)
			time.Sleep(5 * time.Second)
			continue
		}
		log.Printf("hamlib: brain telemetry connected")
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
				t.azDeg = telem.AzRaw * t.cfg.azRange
				t.elDeg = telem.ElRaw * t.cfg.elRange
				t.state = telem.State
				t.mu.Unlock()
			}
		}
		conn.Close()
		t.mu.Lock()
		t.linked = false
		t.mu.Unlock()
		log.Printf("hamlib: brain telemetry lost — reconnecting in 5s")
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
	req, err := http.NewRequest("POST", t.cfg.brainURL+path, r)
	if err != nil {
		return
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("hamlib: brain %s: %v", path, err)
		return
	}
	resp.Body.Close()
}

// ── rotctld protocol handler ──────────────────────────────────────────────────

func (t *Tracker) handleConn(conn net.Conn) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()
	log.Printf("hamlib: client connected: %s", remote)
	defer log.Printf("hamlib: client disconnected: %s", remote)

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Detect extended-protocol prefix ('+' or '\') before stripping.
		extended := len(line) > 0 && (line[0] == '+' || line[0] == '\\')
		line = strings.TrimLeft(line, "+\\")
		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}

		// Normalise long-form command names (used by extended protocol) to
		// the single-letter equivalents used in our switch below.
		longToShort := map[string]string{
			"get_pos": "p", "set_pos": "P",
			"stop": "S", "park": "K", "reset": "C",
			"move": "M", "get_info": "_", "dump_caps": "1",
			"quit": "q",
		}
		if short, ok := longToShort[parts[0]]; ok {
			parts[0] = short
		}

		switch parts[0] {

		case "p": // get position
			az, el := t.Position()
			if extended {
				// Extended protocol: labelled fields + RPRT
				fmt.Fprintf(conn, "get_pos:\n\nAzimuth: %.6f\nElevation: %.6f\n\nRPRT 0\n", az, el)
			} else {
				// Simple protocol: two bare values, no RPRT (standard rotctld behaviour)
				fmt.Fprintf(conn, "%.6f\n%.6f\n", az, el)
			}

		case "P": // set position / go-to
			if len(parts) < 3 {
				fmt.Fprintf(conn, "RPRT -1\n")
				continue
			}
			az, err1 := strconv.ParseFloat(parts[1], 64)
			el, err2 := strconv.ParseFloat(parts[2], 64)
			if err1 != nil || err2 != nil || az < 0 || az > t.cfg.azRange || el < 0 || el > t.cfg.elRange {
				fmt.Fprintf(conn, "RPRT -1\n")
				continue
			}
			t.SetTarget(az, el)
			fmt.Fprintf(conn, "RPRT 0\n")

		case "S": // stop
			t.Stop()
			fmt.Fprintf(conn, "RPRT 0\n")

		case "K": // park
			t.Park()
			fmt.Fprintf(conn, "RPRT 0\n")

		case "C": // reset / clear fault
			t.ClearFault()
			fmt.Fprintf(conn, "RPRT 0\n")

		case "M": // manual move: M direction speed
			if len(parts) >= 2 {
				dir, _ := strconv.Atoi(parts[1])
				t.Move(dir)
			}
			fmt.Fprintf(conn, "RPRT 0\n")

		case "_": // get info
			t.mu.RLock()
			linked := t.linked
			state := t.state
			t.mu.RUnlock()
			fmt.Fprintf(conn, "Rot Model: Yaesu G-5500\nLinked: %v\nState: %s\nRPRT 0\n",
				linked, state)

		case "1": // dump_caps (minimal)
			t.mu.RLock()
			az, el := t.azDeg, t.elDeg
			t.mu.RUnlock()
			fmt.Fprintf(conn,
				"Rot Model: Yaesu G-5500\n"+
					"Min Az: 0.000000\nMax Az: %.6f\n"+
					"Min El: 0.000000\nMax El: %.6f\n"+
					"Current Az: %.6f\nCurrent El: %.6f\n"+
					"RPRT 0\n",
				t.cfg.azRange, t.cfg.elRange, az, el)

		case "q", "Q": // quit
			return

		default:
			fmt.Fprintf(conn, "RPRT -11\n") // RIG_ENAVAIL — unknown command
		}
	}
}

// ── help ──────────────────────────────────────────────────────────────────────

func usage() {
	fmt.Print(`rotor-hamlib — rotctld-compatible TCP server for the rotor controller.

Acts as a Hamlib rotctld daemon: any application that can connect to rotctld
(gpredict, WSJT-X, SkyRoof/SkyCat, fldigi …) can talk to this server instead.
It subscribes to rotor-brain for live telemetry and forwards position/movement
commands from rotctld clients back to the brain.

Usage:
  rotor-hamlib [--help]

Environment:
  HAMLIB_LISTEN      TCP listen address    (default: :4533)
  HAMLIB_BRAIN_URL   Brain API base URL    (default: http://localhost:8090)
  HAMLIB_AZ_RANGE    Full AZ travel °      (default: 450  — Yaesu G-5500)
  HAMLIB_EL_RANGE    Full EL travel °      (default: 180  — Yaesu G-5500)
  HAMLIB_TOLERANCE   Stop tolerance °      (default: 2.0)

Supported rotctld commands:
  p          Get position  → az\nel\nRPRT 0
  P az el    Go to position (bang-bang tracking, 200 ms control loop)
  S          Stop all motion
  K          Park
  C          Clear fault
  M dir spd  Move direction  (dir bits: 2=UP 4=DOWN 8=CCW 16=CW)
  _          Model info
  1          Dump caps
  q / Q      Close connection

Example (run alongside rotor-brain):
  HAMLIB_BRAIN_URL=http://localhost:8090 rotor-hamlib
  # then point gpredict / SkyRoof at localhost:4533
`)
}

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	for _, a := range os.Args[1:] {
		if a == "--help" || a == "-h" {
			usage()
			return
		}
	}

	log.SetFlags(log.Ltime | log.Lmicroseconds)

	cfg := loadConfig()
	log.Printf("rotor-hamlib starting — listen %s  brain %s  AZ %.0f°  EL %.0f°  tol %.1f°",
		cfg.listen, cfg.brainURL, cfg.azRange, cfg.elRange, cfg.tolerance)

	tracker := newTracker(cfg)
	go tracker.Run()

	ln, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		log.Fatalf("listen %s: %v", cfg.listen, err)
	}
	log.Printf("rotctld server ready on %s", cfg.listen)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go tracker.handleConn(conn)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
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
