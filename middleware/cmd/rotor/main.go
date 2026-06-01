// rotor — CLI client for the rotor-brain API.
//
// Usage:
//
//	rotor [--brain URL] <command> [args]
//
// Commands:
//
//	status               Print current state and position
//	move  <az> <el>      Set motion  az: cw|ccw|stop  el: up|down|stop
//	pol   [vhf uhf lna rxtx]  Set RF switches (listed = on, omitted = off)
//	limits --az-min F --az-max F --el-min F --el-max F
//	park                 Drive to park position
//	estop                Emergency stop
//	fault                Clear fault
//	monitor              Stream live telemetry (WebSocket)
//
// Environment:
//
//	ROTOR_BRAIN_URL   Brain HTTP base URL  (default: http://localhost:8080)
//	ROTOR_AZ_RANGE    Full AZ travel in degrees (default: 450)
//	ROTOR_EL_RANGE    Full EL travel in degrees (default: 180)
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// ── config ────────────────────────────────────────────────────────────────────

type cfg struct {
	brainURL string
	azRange  float64 // full AZ travel in degrees (G-5500: 450)
	elRange  float64 // full EL travel in degrees (G-5500: 180)
}

func loadCfg() cfg {
	return cfg{
		brainURL: envStr("ROTOR_BRAIN_URL", "http://localhost:8090"),
		azRange:  envFloat("ROTOR_AZ_RANGE", 450),
		elRange:  envFloat("ROTOR_EL_RANGE", 180),
	}
}

// ── wire types (matching internal/wire) ───────────────────────────────────────

type telemetry struct {
	Type        string  `json:"type"`
	Seq         uint32  `json:"seq"`
	TsMs        uint32  `json:"ts_ms"`
	AzRaw       float64 `json:"az_raw"`
	ElRaw       float64 `json:"el_raw"`
	AzMotion    string  `json:"az_motion"`
	ElMotion    string  `json:"el_motion"`
	PolVHF      bool    `json:"pol_vhf"`
	PolUHF      bool    `json:"pol_uhf"`
	LnaUHF      bool    `json:"lna_uhf"`
	RxTxUHF     bool    `json:"rxtx_uhf"`
	State       string  `json:"state"`
	FaultDetail string  `json:"fault_detail"`
	DutyAzPct   uint8   `json:"duty_az_pct"`
	DutyElPct   uint8   `json:"duty_el_pct"`
}

type statusResp struct {
	Linked    bool       `json:"linked"`
	AgeMs     int64      `json:"age_ms"`
	Telemetry *telemetry `json:"telemetry"`
}

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	// Strip the global --brain flag before dispatching subcommands.
	args := os.Args[1:]
	cfg := loadCfg()

	for len(args) > 0 && args[0] == "--brain" {
		if len(args) < 2 {
			fatal("--brain requires a URL argument")
		}
		cfg.brainURL = args[1]
		args = args[2:]
	}

	if len(args) == 0 {
		usage()
		os.Exit(1)
	}

	switch args[0] {
	case "status":
		cmdStatus(cfg)
	case "move":
		cmdMove(cfg, args[1:])
	case "pol":
		cmdPol(cfg, args[1:])
	case "limits":
		cmdLimits(cfg, args[1:])
	case "park":
		cmdPost(cfg, "/api/v1/park", nil)
	case "estop":
		cmdPost(cfg, "/api/v1/emergency_stop", nil)
	case "fault":
		cmdPost(cfg, "/api/v1/clear_fault", nil)
	case "monitor":
		cmdMonitor(cfg, args[1:])
	case "version":
		fmt.Printf("rotor-cli %s\n", version)
	default:
		fatalf("unknown command %q — run without arguments for help", args[0])
	}
}

// ── status ────────────────────────────────────────────────────────────────────

func cmdStatus(c cfg) {
	var resp statusResp
	getJSON(c, "/api/v1/status", &resp)

	linkStr := "DOWN"
	if resp.Linked {
		linkStr = "LINKED"
	}
	fmt.Printf("Field unit : %s\n", linkStr)

	if resp.Telemetry == nil {
		fmt.Println("Telemetry  : (none)")
		return
	}
	t := resp.Telemetry

	fmt.Printf("Age        : %dms\n", resp.AgeMs)
	fmt.Printf("State      : %s", t.State)
	if t.FaultDetail != "" {
		fmt.Printf("  (%s)", t.FaultDetail)
	}
	fmt.Println()
	fmt.Printf("AZ         : %.4f  (%6.1f°)\n", t.AzRaw, t.AzRaw*c.azRange)
	fmt.Printf("EL         : %.4f  (%6.1f°)\n", t.ElRaw, t.ElRaw*c.elRange)
	fmt.Printf("Motion     : az=%-4s  el=%s\n", t.AzMotion, t.ElMotion)
	fmt.Printf("RF switches: VHF=%-3s  UHF=%-3s  LNA=%-3s  RXTX=%s\n",
		onOff(t.PolVHF), onOff(t.PolUHF), onOff(t.LnaUHF), onOff(t.RxTxUHF))
	fmt.Printf("Duty cycle : AZ %2d%%  EL %2d%%\n", t.DutyAzPct, t.DutyElPct)
}

// ── move ──────────────────────────────────────────────────────────────────────

func cmdMove(c cfg, args []string) {
	if len(args) != 2 {
		fatalf("move requires two arguments: <az> <el>\n" +
			"  az : cw | ccw | stop\n" +
			"  el : up | down | stop\n" +
			"  example: rotor move cw stop")
	}
	az, el := args[0], args[1]
	validAz := map[string]bool{"cw": true, "ccw": true, "stop": true}
	validEl := map[string]bool{"up": true, "down": true, "stop": true}
	if !validAz[az] {
		fatalf("invalid az direction %q (cw | ccw | stop)", az)
	}
	if !validEl[el] {
		fatalf("invalid el direction %q (up | down | stop)", el)
	}
	cmdPost(c, "/api/v1/motion", map[string]string{"az": az, "el": el})
}

// ── pol ───────────────────────────────────────────────────────────────────────

func cmdPol(c cfg, args []string) {
	// Each positional arg enables that switch; anything not listed is off.
	// "rotor pol vhf uhf" → pol_vhf=true, pol_uhf=true, lna_uhf=false, rxtx_uhf=false
	// "rotor pol off"     → all false
	on := map[string]bool{}
	for _, a := range args {
		switch strings.ToLower(a) {
		case "vhf":
			on["vhf"] = true
		case "uhf":
			on["uhf"] = true
		case "lna":
			on["lna"] = true
		case "rxtx":
			on["rxtx"] = true
		case "off":
			// explicit "all off" — leave on empty
		default:
			fatalf("unknown switch %q  (vhf | uhf | lna | rxtx | off)", a)
		}
	}
	body := map[string]bool{
		"pol_vhf":  on["vhf"],
		"pol_uhf":  on["uhf"],
		"lna_uhf":  on["lna"],
		"rxtx_uhf": on["rxtx"],
	}
	cmdPost(c, "/api/v1/polarization", body)
}

// ── limits ────────────────────────────────────────────────────────────────────

func cmdLimits(c cfg, args []string) {
	lim := map[string]*float64{
		"az-min": nil, "az-max": nil,
		"el-min": nil, "el-max": nil,
	}
	for i := 0; i < len(args)-1; i++ {
		key := strings.TrimPrefix(args[i], "--")
		if _, ok := lim[key]; ok {
			v, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil {
				fatalf("invalid value for --%s: %q", key, args[i+1])
			}
			lim[key] = &v
			i++
		}
	}
	for k, v := range lim {
		if v == nil {
			fatalf("--%s is required", k)
		}
	}
	body := map[string]float64{
		"az_min": *lim["az-min"],
		"az_max": *lim["az-max"],
		"el_min": *lim["el-min"],
		"el_max": *lim["el-max"],
	}
	cmdPost(c, "/api/v1/limits", body)
}

// ── monitor ───────────────────────────────────────────────────────────────────

func cmdMonitor(c cfg, args []string) {
	// Parse -rate flag (Hz). Default 1 Hz; use 20 for raw stream.
	var rateHz float64 = 1.0
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-rate" || args[i] == "--rate" {
			v, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil || v <= 0 {
				fatalf("invalid -rate %q (must be a positive number)", args[i+1])
			}
			rateHz = v
			break
		}
	}
	period := time.Duration(float64(time.Second) / rateHz)

	wsURL := httpToWS(c.brainURL) + "/api/v1/telemetry/ws"
	fmt.Fprintf(os.Stderr, "connecting to %s …\n", wsURL)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		fatal("websocket:", err)
	}
	defer conn.Close()
	fmt.Fprintf(os.Stderr, "connected — %.4g Hz display (ctrl-c to stop)\n", rateHz)
	fmt.Println("time           state       AZ          EL          az-mot  el-mot  V U L R  az% el%")
	fmt.Println(strings.Repeat("-", 92))

	// Accumulator for averaged fields within each display period.
	var (
		azSum, elSum     float64
		azDutySum, elDutySum float64
		count            int
		last             *telemetry
	)

	frameCh := make(chan telemetry, 64)
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				close(frameCh)
				return
			}
			var t telemetry
			if json.Unmarshal(msg, &t) == nil {
				frameCh <- t
			}
		}
	}()

	ticker := time.NewTicker(period)
	defer ticker.Stop()

	for {
		select {
		case t, ok := <-frameCh:
			if !ok {
				fatal("connection closed")
			}
			azSum += t.AzRaw
			elSum += t.ElRaw
			azDutySum += float64(t.DutyAzPct)
			elDutySum += float64(t.DutyElPct)
			count++
			last = &t

		case <-ticker.C:
			if count == 0 || last == nil {
				continue
			}
			az := azSum / float64(count)
			el := elSum / float64(count)
			azDuty := int(azDutySum/float64(count) + 0.5)
			elDuty := int(elDutySum/float64(count) + 0.5)

			switches := fmt.Sprintf("%s %s %s %s",
				flag(last.PolVHF, "V"), flag(last.PolUHF, "U"),
				flag(last.LnaUHF, "L"), flag(last.RxTxUHF, "R"))
			fmt.Printf("%s  %-10s  %.4f(%5.1f°)  %.4f(%5.1f°)  %-6s  %-6s  %s  %2d %2d\n",
				time.Now().Format("15:04:05.000"),
				last.State,
				az, az*c.azRange,
				el, el*c.elRange,
				last.AzMotion, last.ElMotion,
				switches, azDuty, elDuty)

			// Reset accumulators.
			azSum, elSum = 0, 0
			azDutySum, elDutySum = 0, 0
			count = 0
			last = nil
		}
	}
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

func getJSON(c cfg, path string, out any) {
	resp, err := http.Get(c.brainURL + path)
	if err != nil {
		fatal("GET", path+":", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, out); err != nil {
		fatalf("decode response: %v\nraw: %s", err, body)
	}
}

func cmdPost(c cfg, path string, body any) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest("POST", c.brainURL+path, r)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal("POST", path+":", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		fatalf("server error %d: %s", resp.StatusCode, raw)
	}
	// Pretty-print the ack
	var pretty map[string]any
	if err := json.Unmarshal(raw, &pretty); err == nil {
		if errMsg, ok := pretty["error"]; ok {
			fatalf("error: %v", errMsg)
		}
		if ok, _ := pretty["ok"].(bool); ok {
			fmt.Println("ok")
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func flag(v bool, label string) string {
	if v {
		return label
	}
	return "-"
}

func httpToWS(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	return u.String()
}

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

func fatal(args ...any) {
	fmt.Fprintln(os.Stderr, append([]any{"error:"}, args...)...)
	os.Exit(1)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}

func usage() {
	fmt.Print(`rotor — antenna rotator controller CLI

Usage:
  rotor [--brain URL] <command> [args]

Commands:
  status                    Current position, state, and RF switches
  move <az> <el>            Set motion  az: cw|ccw|stop  el: up|down|stop
  pol  [vhf] [uhf] [lna] [rxtx]  Set RF switches (listed=on, omitted=off)
  limits --az-min F --az-max F --el-min F --el-max F
  park                      Drive to park position
  estop                     Emergency stop
  fault                     Clear fault
  monitor [-rate Hz]        Stream live telemetry (default 1 Hz averaged; 20 for raw)

Environment:
  ROTOR_BRAIN_URL   Brain API base URL  (default: http://localhost:8080)
  ROTOR_AZ_RANGE    Full AZ travel °    (default: 450  — G-5500)
  ROTOR_EL_RANGE    Full EL travel °    (default: 180  — G-5500)

Examples:
  rotor status
  rotor move cw stop
  rotor pol vhf uhf
  rotor pol off
  rotor park
  rotor monitor
  rotor --brain http://192.168.3.10:8080 status
`)
}

var version = "dev"
