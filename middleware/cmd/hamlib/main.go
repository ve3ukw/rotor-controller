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
// Pointing offsets (compensate for mechanical misalignment):
//
//	--az-offset <deg>   positive = rotate antenna further CW than tracker says
//	--el-offset <deg>   positive = tilt antenna further up than tracker says
//	HAMLIB_AZ_OFFSET / HAMLIB_EL_OFFSET  same via environment (CLI wins)
//
// Environment:
//
//	HAMLIB_LISTEN     TCP listen address   (default: :4533)
//	HAMLIB_BRAIN_URL  Brain API base URL   (default: http://localhost:8090)
//	HAMLIB_AZ_RANGE   Full AZ travel °     (default: 450  — Yaesu G-5500)
//	HAMLIB_EL_RANGE   Full EL travel °     (default: 180  — Yaesu G-5500)
//	HAMLIB_TOLERANCE  Stop tolerance °     (default: 2.0)
//	HAMLIB_AZ_OFFSET  AZ correction °      (default: 0)
//	HAMLIB_EL_OFFSET  EL correction °      (default: 0)
package main

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"

	"rotor-controller/brain/internal/tracker"
)

// ── config ────────────────────────────────────────────────────────────────────

type config struct {
	listen      string
	brainURL    string
	azRange     float64
	elRange     float64
	tolerance   float64
	azOffsetDeg float64
	elOffsetDeg float64
}

func loadConfig() config {
	return config{
		listen:      envStr("HAMLIB_LISTEN", ":4533"),
		brainURL:    envStr("HAMLIB_BRAIN_URL", "http://localhost:8090"),
		azRange:     envFloat("HAMLIB_AZ_RANGE", 450),
		elRange:     envFloat("HAMLIB_EL_RANGE", 180),
		tolerance:   envFloat("HAMLIB_TOLERANCE", 2.0),
		azOffsetDeg: envFloat("HAMLIB_AZ_OFFSET", 0),
		elOffsetDeg: envFloat("HAMLIB_EL_OFFSET", 0),
	}
}

// ── rotctld protocol handler ──────────────────────────────────────────────────

func handleConn(conn net.Conn, t *tracker.Tracker) {
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
			if err1 != nil || err2 != nil {
				fmt.Fprintf(conn, "RPRT -1\n")
				continue
			}
			// SetTarget clamps az/el to the reachable range, so a
			// below-horizon elevation (satellite not yet risen) parks the
			// antenna at the horizon (el=0) instead of being rejected and
			// leaving a stale target active.
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

		case "M": // manual move: M direction speed  (dir bits: 2=UP 4=DOWN 8=CCW 16=CW)
			if len(parts) >= 2 {
				dir, _ := strconv.Atoi(parts[1])
				azCmd := "stop"
				switch {
				case dir&16 != 0:
					azCmd = "cw"
				case dir&8 != 0:
					azCmd = "ccw"
				}
				elCmd := "stop"
				switch {
				case dir&2 != 0:
					elCmd = "up"
				case dir&4 != 0:
					elCmd = "down"
				}
				t.Move(azCmd, elCmd)
			}
			fmt.Fprintf(conn, "RPRT 0\n")

		case "_": // get info
			linked, state := t.Status()
			fmt.Fprintf(conn, "Rot Model: Yaesu G-5500\nLinked: %v\nState: %s\nRPRT 0\n",
				linked, state)

		case "1": // dump_caps (minimal)
			az, el := t.Position()
			_, elRange := t.Range()
			fmt.Fprintf(conn,
				"Rot Model: Yaesu G-5500\n"+
					"Min Az: 0.000000\nMax Az: 360.000000\n"+
					"Min El: 0.000000\nMax El: %.6f\n"+
					"Current Az: %.6f\nCurrent El: %.6f\n"+
					"RPRT 0\n",
				elRange, az, el)

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
  rotor-hamlib [--help|--version]

Pointing offsets (tune these to compensate for mechanical alignment error):
  --az-offset <deg>  AZ correction; positive = rotate antenna further CW than tracker says
  --el-offset <deg>  EL correction; positive = tilt antenna further up than tracker says
  (same via env: HAMLIB_AZ_OFFSET, HAMLIB_EL_OFFSET — CLI flags win)

  To find your offsets: track a known signal source, watch the S-meter,
  nudge until it peaks, note the delta from the tracker's commanded position.

Environment:
  HAMLIB_LISTEN      TCP listen address    (default: :4533)
  HAMLIB_BRAIN_URL   Brain API base URL    (default: http://localhost:8090)
  HAMLIB_AZ_RANGE    Full AZ travel °      (default: 450  — Yaesu G-5500)
  HAMLIB_EL_RANGE    Full EL travel °      (default: 180  — Yaesu G-5500)
  HAMLIB_TOLERANCE   Stop tolerance °      (default: 2.0)
  HAMLIB_AZ_OFFSET   AZ correction °       (default: 0)
  HAMLIB_EL_OFFSET   EL correction °       (default: 0)

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

Examples:
  rotor-hamlib                              # defaults, no offset
  rotor-hamlib --az-offset 3.5             # antenna was 3.5° short of CW
  rotor-hamlib --az-offset -2 --el-offset 1.5
  HAMLIB_AZ_OFFSET=3.5 HAMLIB_EL_OFFSET=-1 rotor-hamlib
  # then point gpredict / SkyRoof / WSJT-X at localhost:4533
`)
}

// ── main ──────────────────────────────────────────────────────────────────────

var version = "dev"

func main() {
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--help", "-h":
			usage()
			return
		case "--version", "version":
			fmt.Printf("rotor-hamlib %s\n", version)
			return
		}
	}

	log.SetFlags(log.Ltime | log.Lmicroseconds)

	cfg := loadConfig()
	// CLI flags override env vars.
	for i := 0; i < len(args)-1; i++ {
		v, err := strconv.ParseFloat(args[i+1], 64)
		switch args[i] {
		case "--az-offset":
			if err == nil {
				cfg.azOffsetDeg = v
				i++
			}
		case "--el-offset":
			if err == nil {
				cfg.elOffsetDeg = v
				i++
			}
		}
	}

	log.Printf("rotor-hamlib %s starting — listen %s  brain %s  AZ %.0f°  EL %.0f°  tol %.1f°",
		version, cfg.listen, cfg.brainURL, cfg.azRange, cfg.elRange, cfg.tolerance)
	if cfg.azOffsetDeg != 0 || cfg.elOffsetDeg != 0 {
		log.Printf("hamlib: pointing offsets: AZ %+.1f°  EL %+.1f°", cfg.azOffsetDeg, cfg.elOffsetDeg)
	}

	cal := tracker.FetchCalibration(cfg.brainURL)
	log.Printf("hamlib: calibration: AZ raw %.4f..%.4f offset %.1f°  EL raw %.4f..%.4f",
		cal.AzRawMin, cal.AzRawMax, cal.AzOffsetDeg, cal.ElRawMin, cal.ElRawMax)

	trk := tracker.New(tracker.Config{
		BrainURL:    cfg.brainURL,
		AzRange:     cfg.azRange,
		ElRange:     cfg.elRange,
		Tolerance:   cfg.tolerance,
		AzOffsetDeg: cfg.azOffsetDeg,
		ElOffsetDeg: cfg.elOffsetDeg,
	}, cal)
	go trk.Run()

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
		go handleConn(conn, trk)
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
