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
//	calib                Show/set AZ/EL pot calibration (gain + AZ north offset)
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
	"rotor-controller/brain/internal/calib"
	"rotor-controller/brain/internal/config"
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

// ── verbose logging ───────────────────────────────────────────────────────────

var verbose bool

func debugf(format string, args ...any) {
	if verbose {
		fmt.Fprintf(os.Stderr, "[debug] "+format+"\n", args...)
	}
}

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	args := os.Args[1:]
	cfg := loadCfg()

	// Strip global flags (--brain, -v/--verbose) before dispatching.
	filtered := args[:0]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--brain":
			if i+1 >= len(args) {
				fatal("--brain requires a URL argument")
			}
			cfg.brainURL = args[i+1]
			i++
		case "-v", "--verbose":
			verbose = true
		default:
			filtered = append(filtered, args[i])
		}
	}
	args = filtered

	if verbose {
		debugf("brain URL: %s", cfg.brainURL)
		debugf("AZ range: %.0f°  EL range: %.0f°", cfg.azRange, cfg.elRange)
	}

	if len(args) == 0 {
		usage()
		os.Exit(1)
	}

	switch args[0] {
	case "status":
		cmdStatus(cfg, args[1:])
	case "move":
		cmdMove(cfg, args[1:])
	case "pol":
		cmdPol(cfg, args[1:])
	case "limits":
		cmdLimits(cfg, args[1:])
	case "calib":
		cmdCalib(cfg, args[1:])
	case "reboot":
		cmdPostVerbose(cfg, "/api/v1/reboot", nil, "reboot")
		fmt.Println("controller rebooting — brain will reconnect automatically")
	case "park":
		if len(args) > 1 && args[1] == "config" {
			cmdParkConfig(cfg, args[2:])
		} else {
			cmdPostVerbose(cfg, "/api/v1/park", nil, "park")
		}
	case "estop":
		cmdPostVerbose(cfg, "/api/v1/emergency_stop", nil, "emergency_stop")
	case "fault":
		cmdPostVerbose(cfg, "/api/v1/clear_fault", nil, "clear_fault")
	case "goto":
		cmdGoto(cfg, args[1:])
	case "block":
		cmdBlock(cfg, args[1:])
	case "netconfig":
		cmdNetconfig(cfg, args[1:])
	case "monitor":
		cmdMonitor(cfg, args[1:])
	case "version":
		fmt.Printf("rotor-cli %s\n", version)
	default:
		fatalf("unknown command %q — run without arguments for help", args[0])
	}
}

// ── status ────────────────────────────────────────────────────────────────────

func cmdStatus(c cfg, args []string) {
	deg := false
	for _, a := range args {
		if a == "--deg" || a == "-deg" {
			deg = true
		}
	}

	debugf("fetching status from %s", c.brainURL)
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
	azAxis, elAxis, azOffset := fetchCalibAxes(c)
	azBearing := calib.TrueBearing(azAxis.MechDeg(t.AzRaw), azOffset)
	elDeg := elAxis.MechDeg(t.ElRaw)
	if deg {
		fmt.Printf("AZ         : %6.1f°\n", azBearing)
		fmt.Printf("EL         : %6.1f°\n", elDeg)
	} else {
		fmt.Printf("AZ         : %.4f  (%6.1f°)\n", t.AzRaw, azBearing)
		fmt.Printf("EL         : %.4f  (%6.1f°)\n", t.ElRaw, elDeg)
	}
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
	debugf("set_motion az=%s el=%s", az, el)
	cmdPost(c, "/api/v1/motion", map[string]string{"az": az, "el": el})
}

// ── goto ──────────────────────────────────────────────────────────────────────

func cmdGoto(c cfg, args []string) {
	if len(args) == 1 && args[0] == "cancel" {
		cmdPost(c, "/api/v1/goto/cancel", nil)
		fmt.Println("goto cancelled")
		return
	}

	usage := fmt.Sprintf("goto <az-deg> <el-deg>\n"+
		"  az : 0-359.9°  compass bearing, 0=N  (also: az=<deg> / azimuth=<deg>)\n"+
		"  el : 0-%.0f°    mechanical, matches 'rotor status --deg' (also: el=<deg> / elevation=<deg>)\n"+
		"  Either axis may be omitted (by name) to leave it at its current position.\n"+
		"  examples: rotor goto 180 45\n"+
		"            rotor goto az=155\n"+
		"            rotor goto elevation=90\n"+
		"            rotor goto el=10 az=270\n"+
		"  rotor goto cancel   — stop an in-progress goto", c.elRange)

	var azDeg, elDeg float64
	var haveAz, haveEl bool

	named := false
	for _, a := range args {
		if strings.Contains(a, "=") {
			named = true
			break
		}
	}

	if named {
		for _, a := range args {
			parts := strings.SplitN(a, "=", 2)
			if len(parts) != 2 {
				fatalf("invalid argument %q\n\n%s", a, usage)
			}
			key, val := strings.ToLower(parts[0]), parts[1]
			v, err := strconv.ParseFloat(val, 64)
			if err != nil {
				fatalf("invalid value %q for %s — must be a number in degrees\n\n%s", val, key, usage)
			}
			switch key {
			case "az", "azimuth":
				azDeg, haveAz = v, true
			case "el", "elevation":
				elDeg, haveEl = v, true
			default:
				fatalf("unknown axis %q (az | el)\n\n%s", key, usage)
			}
		}
	} else {
		if len(args) != 2 {
			fatalf("%s", usage)
		}
		var err1, err2 error
		azDeg, err1 = strconv.ParseFloat(args[0], 64)
		elDeg, err2 = strconv.ParseFloat(args[1], 64)
		if err1 != nil || err2 != nil {
			fatalf("az/el must be numbers in degrees\n\n%s", usage)
		}
		haveAz, haveEl = true, true
	}

	if !haveAz || !haveEl {
		var resp statusResp
		getJSON(c, "/api/v1/status", &resp)
		if resp.Telemetry == nil {
			fatalf("cannot read current position to fill in the omitted axis (no telemetry)")
		}
		azAxis, elAxis, azOffset := fetchCalibAxes(c)
		if !haveAz {
			azDeg = calib.TrueBearing(azAxis.MechDeg(resp.Telemetry.AzRaw), azOffset)
		}
		if !haveEl {
			elDeg = elAxis.MechDeg(resp.Telemetry.ElRaw)
		}
	}

	debugf("goto az=%.1f° el=%.1f°", azDeg, elDeg)
	cmdPost(c, "/api/v1/goto", map[string]float64{"az_deg": azDeg, "el_deg": elDeg})
	fmt.Printf("driving to AZ %.1f°  EL %.1f° — use 'rotor monitor' to watch progress\n", azDeg, elDeg)
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
	debugf("set_polarization vhf=%v uhf=%v lna=%v rxtx=%v", on["vhf"], on["uhf"], on["lna"], on["rxtx"])
	cmdPost(c, "/api/v1/polarization", body)
}

// ── limits ────────────────────────────────────────────────────────────────────

type limitsResp struct {
	AzMin      float64 `json:"az_min"`
	AzMax      float64 `json:"az_max"`
	ElMin      float64 `json:"el_min"`
	ElMax      float64 `json:"el_max"`
	Configured bool    `json:"configured"`
}

// calibResp mirrors the brain's /api/v1/calibration response.
type calibResp struct {
	AzRawMin    float64 `json:"az_raw_min"`
	AzRawMax    float64 `json:"az_raw_max"`
	AzOffsetDeg float64 `json:"az_offset_deg"`
	ElRawMin    float64 `json:"el_raw_min"`
	ElRawMax    float64 `json:"el_raw_max"`
}

// fetchCalibAxes fetches the current calibration and returns the gain-corrected
// AZ/EL axes plus the AZ true-north offset.
func fetchCalibAxes(c cfg) (azAxis, elAxis calib.Axis, azOffsetDeg float64) {
	var cal calibResp
	getJSON(c, "/api/v1/calibration", &cal)
	azAxis = calib.Axis{RawMin: cal.AzRawMin, RawMax: cal.AzRawMax, Range: c.azRange}
	elAxis = calib.Axis{RawMin: cal.ElRawMin, RawMax: cal.ElRawMax, Range: c.elRange}
	return azAxis, elAxis, cal.AzOffsetDeg
}

func cmdLimits(c cfg, args []string) {
	flags := map[string]*float64{
		"az-min": nil, "az-max": nil,
		"el-min": nil, "el-max": nil,
	}
	for i := 0; i < len(args)-1; i++ {
		key := strings.TrimPrefix(args[i], "--")
		if _, ok := flags[key]; ok {
			v, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil {
				fatalf("invalid value for --%s: %q (degrees)", key, args[i+1])
			}
			flags[key] = &v
			i++
		}
	}

	var cur limitsResp
	getJSON(c, "/api/v1/limits", &cur)
	azAxis, elAxis, _ := fetchCalibAxes(c)

	if flags["az-min"] == nil && flags["az-max"] == nil && flags["el-min"] == nil && flags["el-max"] == nil {
		src := "configured"
		if !cur.Configured {
			src = "firmware defaults — not yet customized"
		}
		fmt.Printf("AZ limits  : %6.1f° .. %6.1f°  (mechanical, %s)\n", azAxis.MechDeg(cur.AzMin), azAxis.MechDeg(cur.AzMax), src)
		fmt.Printf("EL limits  : %6.1f° .. %6.1f°  (mechanical, %s)\n", elAxis.MechDeg(cur.ElMin), elAxis.MechDeg(cur.ElMax), src)
		return
	}

	azMin, azMax := azAxis.MechDeg(cur.AzMin), azAxis.MechDeg(cur.AzMax)
	elMin, elMax := elAxis.MechDeg(cur.ElMin), elAxis.MechDeg(cur.ElMax)
	if v := flags["az-min"]; v != nil {
		azMin = *v
	}
	if v := flags["az-max"]; v != nil {
		azMax = *v
	}
	if v := flags["el-min"]; v != nil {
		elMin = *v
	}
	if v := flags["el-max"]; v != nil {
		elMax = *v
	}

	body := map[string]float64{
		"az_min": azAxis.Raw(azMin),
		"az_max": azAxis.Raw(azMax),
		"el_min": elAxis.Raw(elMin),
		"el_max": elAxis.Raw(elMax),
	}
	cmdPost(c, "/api/v1/limits", body)
	fmt.Printf("limits set: AZ %.1f° .. %.1f°   EL %.1f° .. %.1f°  (mechanical)\n", azMin, azMax, elMin, elMax)
}

// ── calib ─────────────────────────────────────────────────────────────────────

func cmdCalib(c cfg, args []string) {
	usage := `rotor calib                    Show current calibration
  rotor calib az-min           Set AZ raw-min to the current AZ reading
                                (do this with the rotor at its AZ mechanical minimum stop)
  rotor calib az-max           Set AZ raw-max to the current AZ reading (at AZ max stop)
  rotor calib az-north         Set the AZ offset so the current position reads as 0° (true N)
  rotor calib el-min           Set EL raw-min to the current EL reading (at EL min stop)
  rotor calib el-max           Set EL raw-max to the current EL reading (at EL max stop)
  rotor calib reset            Clear all calibration (back to uncalibrated 1:1 raw==degrees)

  rotor calib --az-raw-min F --az-raw-max F --az-offset-deg F --el-raw-min F --el-raw-max F
                                Set raw values directly (any subset)`

	var cur calibResp
	getJSON(c, "/api/v1/calibration", &cur)

	printCalib := func(cal calibResp) {
		fmt.Printf("AZ raw     : %.4f .. %.4f  (mechanical 0-%.0f°)\n", cal.AzRawMin, cal.AzRawMax, c.azRange)
		fmt.Printf("AZ offset  : %.1f°  (mechanical degree that reads as true N)\n", cal.AzOffsetDeg)
		fmt.Printf("EL raw     : %.4f .. %.4f  (mechanical 0-%.0f°)\n", cal.ElRawMin, cal.ElRawMax, c.elRange)
	}

	if len(args) == 0 {
		printCalib(cur)
		return
	}

	switch args[0] {
	case "reset":
		def := config.DefaultCalibration()
		body := calibResp{AzRawMin: def.AzRawMin, AzRawMax: def.AzRawMax, AzOffsetDeg: def.AzOffsetDeg, ElRawMin: def.ElRawMin, ElRawMax: def.ElRawMax}
		cmdPost(c, "/api/v1/calibration", body)
		fmt.Println("calibration reset to uncalibrated 1:1 raw==degrees")
		return

	case "az-min", "az-max", "az-north", "el-min", "el-max":
		var resp statusResp
		getJSON(c, "/api/v1/status", &resp)
		if resp.Telemetry == nil {
			fatalf("cannot read current position (no telemetry)")
		}
		body := cur
		switch args[0] {
		case "az-min":
			body.AzRawMin = resp.Telemetry.AzRaw
		case "az-max":
			body.AzRawMax = resp.Telemetry.AzRaw
		case "az-north":
			azAxis := calib.Axis{RawMin: cur.AzRawMin, RawMax: cur.AzRawMax, Range: c.azRange}
			body.AzOffsetDeg = azAxis.MechDeg(resp.Telemetry.AzRaw)
		case "el-min":
			body.ElRawMin = resp.Telemetry.ElRaw
		case "el-max":
			body.ElRawMax = resp.Telemetry.ElRaw
		}
		cmdPost(c, "/api/v1/calibration", body)
		printCalib(body)
		return
	}

	// Direct numeric flags.
	flags := map[string]*float64{
		"az-raw-min": nil, "az-raw-max": nil, "az-offset-deg": nil,
		"el-raw-min": nil, "el-raw-max": nil,
	}
	for i := 0; i < len(args)-1; i++ {
		key := strings.TrimPrefix(args[i], "--")
		if _, ok := flags[key]; ok {
			v, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil {
				fatalf("invalid value for --%s: %q\n\n%s", key, args[i+1], usage)
			}
			flags[key] = &v
			i++
		}
	}
	if flags["az-raw-min"] == nil && flags["az-raw-max"] == nil && flags["az-offset-deg"] == nil &&
		flags["el-raw-min"] == nil && flags["el-raw-max"] == nil {
		fatalf("unknown calib command %q\n\n%s", args[0], usage)
	}

	body := cur
	if v := flags["az-raw-min"]; v != nil {
		body.AzRawMin = *v
	}
	if v := flags["az-raw-max"]; v != nil {
		body.AzRawMax = *v
	}
	if v := flags["az-offset-deg"]; v != nil {
		body.AzOffsetDeg = *v
	}
	if v := flags["el-raw-min"]; v != nil {
		body.ElRawMin = *v
	}
	if v := flags["el-raw-max"]; v != nil {
		body.ElRawMax = *v
	}
	cmdPost(c, "/api/v1/calibration", body)
	printCalib(body)
}

// ── park config ───────────────────────────────────────────────────────────────

type parkConfigResp struct {
	AzDeg      float64 `json:"az_deg"`
	ElDeg      float64 `json:"el_deg"`
	Configured bool    `json:"configured"`
}

func cmdParkConfig(c cfg, args []string) {
	var azDeg, elDeg *float64
	for i := 0; i < len(args)-1; i++ {
		v, err := strconv.ParseFloat(args[i+1], 64)
		if err != nil {
			continue
		}
		switch args[i] {
		case "--az":
			azDeg = &v
			i++
		case "--el":
			elDeg = &v
			i++
		}
	}

	if azDeg == nil && elDeg == nil {
		var resp parkConfigResp
		getJSON(c, "/api/v1/park/config", &resp)
		src := "configured"
		if !resp.Configured {
			src = "firmware defaults — not yet customized"
		}
		fmt.Printf("Park AZ    : %6.1f°  (compass bearing, %s)\n", resp.AzDeg, src)
		fmt.Printf("Park EL    : %6.1f°  (mechanical, %s)\n", resp.ElDeg, src)
		return
	}

	if azDeg == nil || elDeg == nil {
		fatalf("required: --az <compass-bearing> --el <mechanical-degrees>\n" +
			"  example: rotor park config --az 10 --el 5")
	}

	cmdPost(c, "/api/v1/park/config", map[string]float64{"az_deg": *azDeg, "el_deg": *elDeg})
	fmt.Printf("park position set: AZ %.1f° (compass)  EL %.1f° (mechanical) — persisted across reflash\n", *azDeg, *elDeg)
}

// ── block ─────────────────────────────────────────────────────────────────────

const blockChunks = 90   // 90 × 5° = 450°
const blockChunkDeg = 5  // degrees per chunk

type blocksResp struct {
	Chunks   []uint8 `json:"chunks"`
	ChunkDeg int     `json:"chunk_deg"`
}

func cmdBlock(c cfg, args []string) {
	if len(args) == 0 {
		fmt.Print(`rotor block — manage AZ-segmented elevation floors (obstacle avoidance)

Commands:
  show [--map]              List blocked sectors, or print an ASCII horizon map
  set --az <deg> --el <deg> Set minimum elevation for the 5° sector at az_deg
  train [--margin <deg>]    Record current position as the floor for its sector
  clear --az <deg>          Remove the floor for the 5° sector at az_deg
  clear-all                 Remove all floors

Examples:
  rotor block set --az 45 --el 20    sector 45°-50° must stay above 20° EL
  rotor block train                  point antenna at the edge of obstacle, run this
  rotor block train --margin 5       same but add 5° safety margin
  rotor block show --map
`)
		return
	}
	switch args[0] {
	case "show":
		showMap := false
		for _, a := range args[1:] {
			if a == "--map" || a == "-m" {
				showMap = true
			}
		}
		if showMap {
			cmdBlockMap(c)
		} else {
			cmdBlockShow(c)
		}
	case "set":
		cmdBlockSet(c, args[1:])
	case "train":
		cmdBlockTrain(c, args[1:])
	case "clear":
		cmdBlockClear(c, args[1:])
	case "clear-all":
		cmdPost(c, "/api/v1/blocks/reset", nil)
	default:
		fatalf("unknown block subcommand %q — run 'rotor block' for help", args[0])
	}
}

func fetchBlocks(c cfg) []uint8 {
	var resp blocksResp
	getJSON(c, "/api/v1/blocks", &resp)
	if len(resp.Chunks) != blockChunks {
		fatalf("unexpected blocks response length %d (want %d)", len(resp.Chunks), blockChunks)
	}
	return resp.Chunks
}

func cmdBlockShow(c cfg) {
	chunks := fetchBlocks(c)
	any := false
	for i, v := range chunks {
		if v > 0 {
			az := float64(i * blockChunkDeg)
			fmt.Printf("  %5.1f°–%5.1f°  min EL %d°\n", az, az+blockChunkDeg, v)
			any = true
		}
	}
	if !any {
		fmt.Println("No blocks set — all sectors open.")
	}
}

func cmdBlockMap(c cfg) {
	chunks := fetchBlocks(c)
	// Map: 45 columns (10° each, 2 chunks per col) × 10 rows (9° each)
	const cols, rows = 45, 10
	const elStep = 90 / rows // 9° per row

	fmt.Println("EL")
	for row := rows - 1; row >= 0; row-- {
		elBottom := row * elStep
		fmt.Printf("%2d°│", elBottom+elStep)
		for col := 0; col < cols; col++ {
			// max el_floor of the two 5° sectors in this 10° column
			a, b := chunks[col*2], chunks[col*2+1]
			maxFloor := a
			if b > maxFloor {
				maxFloor = b
			}
			if int(maxFloor) > elBottom {
				fmt.Print("█")
			} else {
				fmt.Print("·")
			}
		}
		fmt.Println()
	}
	fmt.Printf(" 0°└%s\n", strings.Repeat("─", cols))
	// AZ axis labels every 90° (9 columns)
	fmt.Print("   ")
	for deg := 0; deg <= 450; deg += 90 {
		label := fmt.Sprintf("%-9d", deg)
		fmt.Print(label)
	}
	fmt.Println("° AZ")
	fmt.Println("   N         E         S         W         N    (extra 90°)")
}

func cmdBlockSet(c cfg, args []string) {
	var azDeg, elDeg *float64
	for i := 0; i < len(args)-1; i++ {
		v, err := strconv.ParseFloat(args[i+1], 64)
		if err != nil {
			continue
		}
		switch args[i] {
		case "--az":
			azDeg = &v
			i++
		case "--el":
			elDeg = &v
			i++
		}
	}
	if azDeg == nil || elDeg == nil {
		fatalf("required: --az <degrees> --el <degrees>")
	}
	debugf("set block az=%.1f° el_floor=%.1f°", *azDeg, *elDeg)
	cmdPost(c, "/api/v1/blocks/set", map[string]float64{
		"az_deg":   *azDeg,
		"el_floor": *elDeg,
	})
}

func cmdBlockTrain(c cfg, args []string) {
	margin := 0.0
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--margin" {
			if v, err := strconv.ParseFloat(args[i+1], 64); err == nil {
				margin = v
			}
		}
	}
	var resp statusResp
	getJSON(c, "/api/v1/status", &resp)
	if resp.Telemetry == nil {
		fatal("no telemetry — is the brain connected to the field unit?")
	}
	t := resp.Telemetry
	azDeg := t.AzRaw * c.azRange
	elDeg := t.ElRaw*c.elRange + margin
	chunk := int(azDeg/blockChunkDeg) * blockChunkDeg // round to chunk start
	fmt.Printf("Training: AZ %.1f° (sector %d°–%d°)  EL floor %.1f°",
		azDeg, chunk, chunk+blockChunkDeg, elDeg)
	if margin > 0 {
		fmt.Printf(" (+%.1f° margin)", margin)
	}
	fmt.Println()
	cmdPost(c, "/api/v1/blocks/set", map[string]float64{
		"az_deg":   azDeg,
		"el_floor": elDeg,
	})
}

func cmdBlockClear(c cfg, args []string) {
	var azDeg *float64
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--az" {
			if v, err := strconv.ParseFloat(args[i+1], 64); err == nil {
				azDeg = &v
			}
		}
	}
	if azDeg == nil {
		fatalf("required: --az <degrees>")
	}
	debugf("clear block az=%.1f°", *azDeg)
	cmdPost(c, "/api/v1/blocks/set", map[string]float64{
		"az_deg": *azDeg, "el_floor": 0,
	})
}

// ── netconfig ─────────────────────────────────────────────────────────────────

func cmdNetconfig(c cfg, args []string) {
	// rotor netconfig --ip X --subnet X --gateway X [--mac X]
	// rotor netconfig reset   → factory defaults on next boot
	if len(args) > 0 && args[0] == "reset" {
		cmdPost(c, "/api/v1/netconfig/reset", nil)
		fmt.Println("ok — factory network defaults restored on next reboot")
		fmt.Println("note: current session keeps running until the field unit is power-cycled or reset")
		return
	}

	params := map[string]*string{
		"ip": nil, "subnet": nil, "gateway": nil, "mac": nil,
	}
	for i := 0; i < len(args)-1; i++ {
		key := strings.TrimPrefix(args[i], "--")
		if _, ok := params[key]; ok {
			v := args[i+1]
			params[key] = &v
			i++
		}
	}
	if params["ip"] == nil || params["subnet"] == nil || params["gateway"] == nil {
		fatalf("required: --ip X.X.X.X --subnet X.X.X.X --gateway X.X.X.X\n" +
			"  optional: --mac 02:00:xx:xx:xx:xx\n" +
			"  or: rotor netconfig reset  (revert to factory defaults on next boot)")
	}

	body := map[string]string{
		"ip":      *params["ip"],
		"subnet":  *params["subnet"],
		"gateway": *params["gateway"],
	}
	if params["mac"] != nil {
		body["mac"] = *params["mac"]
	}

	cmdPost(c, "/api/v1/netconfig", body)

	// Persist the new IP so rotor-brain picks it up on next start.
	newIP := *params["ip"]
	if err := config.SaveFieldUnitHost(newIP); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not save config file: %v\n", err)
		fmt.Printf("Restart brain with: BRAIN_FIELD_UNIT_HOST=%s ./rotor-brain\n", newIP)
	} else {
		fmt.Printf("\nConfig file updated — just restart rotor-brain to connect to %s.\n", newIP)
	}
}

// ── monitor ───────────────────────────────────────────────────────────────────

func cmdMonitor(c cfg, args []string) {
	// Parse -rate flag (Hz). Default 1 Hz; use 20 for raw stream.
	var rateHz float64 = 1.0
	deg := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-rate", "--rate":
			if i+1 >= len(args) {
				fatalf("missing value for %s", args[i])
			}
			v, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil || v <= 0 {
				fatalf("invalid -rate %q (must be a positive number)", args[i+1])
			}
			rateHz = v
			i++
		case "--deg", "-deg":
			deg = true
		}
	}
	period := time.Duration(float64(time.Second) / rateHz)
	azAxis, elAxis, azOffset := fetchCalibAxes(c)

	wsURL := httpToWS(c.brainURL) + "/api/v1/telemetry/ws"
	fmt.Fprintf(os.Stderr, "connecting to %s …\n", wsURL)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		fatal("websocket:", err)
	}
	defer conn.Close()
	fmt.Fprintf(os.Stderr, "connected — %.4g Hz display (ctrl-c to stop)\n", rateHz)
	if deg {
		fmt.Println("time           state       AZ      EL      az-mot  el-mot  V U L R  az% el%")
		fmt.Println(strings.Repeat("-", 80))
	} else {
		fmt.Println("time           state       AZ          EL          az-mot  el-mot  V U L R  az% el%")
		fmt.Println(strings.Repeat("-", 92))
	}

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
			debugf("averaging %d frames over %.4gs window", count, period.Seconds())
			az := azSum / float64(count)
			el := elSum / float64(count)
			azDuty := int(azDutySum/float64(count) + 0.5)
			elDuty := int(elDutySum/float64(count) + 0.5)

			switches := fmt.Sprintf("%s %s %s %s",
				flag(last.PolVHF, "V"), flag(last.PolUHF, "U"),
				flag(last.LnaUHF, "L"), flag(last.RxTxUHF, "R"))
			azBearing := calib.TrueBearing(azAxis.MechDeg(az), azOffset)
			elDeg := elAxis.MechDeg(el)
			if deg {
				fmt.Printf("%s  %-10s  %6.1f°  %6.1f°  %-6s  %-6s  %s  %2d %2d\n",
					time.Now().Format("15:04:05.000"),
					last.State,
					azBearing, elDeg,
					last.AzMotion, last.ElMotion,
					switches, azDuty, elDuty)
			} else {
				fmt.Printf("%s  %-10s  %.4f(%5.1f°)  %.4f(%5.1f°)  %-6s  %-6s  %s  %2d %2d\n",
					time.Now().Format("15:04:05.000"),
					last.State,
					az, azBearing,
					el, elDeg,
					last.AzMotion, last.ElMotion,
					switches, azDuty, elDuty)
			}

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
	url := c.brainURL + path
	debugf("GET %s", url)
	t0 := time.Now()
	resp, err := http.Get(url)
	if err != nil {
		fatal("GET", path+":", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	debugf("response %d  %dms  %d bytes", resp.StatusCode, time.Since(t0).Milliseconds(), len(body))
	if verbose {
		debugf("body: %s", body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		fatalf("decode response: %v\nraw: %s", err, body)
	}
}

func cmdPost(c cfg, path string, body any) {
	var reqBody []byte
	var r io.Reader
	if body != nil {
		reqBody, _ = json.Marshal(body)
		r = bytes.NewReader(reqBody)
	}
	debugf("POST %s%s  body=%s", c.brainURL, path, reqBody)
	t0 := time.Now()
	req, _ := http.NewRequest("POST", c.brainURL+path, r)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal("POST", path+":", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	debugf("response %d  %dms  %s", resp.StatusCode, time.Since(t0).Milliseconds(), raw)

	if resp.StatusCode >= 400 {
		fatalf("server error %d: %s", resp.StatusCode, raw)
	}
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

// cmdPostVerbose wraps cmdPost with a human-readable description logged at debug level.
func cmdPostVerbose(c cfg, path string, body any, desc string) {
	debugf("command: %s", desc)
	cmdPost(c, path, body)
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
  rotor [-v] [--brain URL] <command> [args]

Motion control:
  status [--deg]                 Current position, state, and RF switches
  move <az> <el>                 Set motion  az: cw|ccw|stop  el: up|down|stop
  goto <az-deg> <el-deg>         Drive to an absolute AZ/EL position (degrees)
  goto az=<deg> [el=<deg>]       Drive one or both axes (other axis unchanged)
  goto cancel                    Stop an in-progress goto
  park                           Drive to park position (configured AZ/EL)
  park config                    Show configured park position (compass AZ, mech EL)
  park config --az D --el D      Set park position; persisted across reflash/reboot
  estop                          Emergency stop
  fault                          Clear fault / acknowledge hardware ESTOP
  reboot                         Software-reset the field unit controller

Antenna configuration:
  pol  [vhf] [uhf] [lna] [rxtx] Set RF switches (listed=on, omitted=off)
  pol  off                       All switches off
  limits                         Show current soft travel limits (mechanical degrees)
  limits --az-min D --az-max D --el-min D --el-max D
                                 Set soft travel limits (mechanical degrees, any
                                 subset; omitted axes keep their current value).
                                 Persisted — re-applied after a reflash/reboot.
  calib                          Show current AZ/EL pot calibration
  calib az-min|az-max|el-min|el-max
                                 Capture current reading as that axis's raw
                                 end-stop (rotor must be at the mechanical stop)
  calib az-north                 Set AZ offset so current heading reads 0° (true N)
  calib reset                    Clear calibration (back to uncalibrated 1:1)

Obstacle avoidance (AZ-segmented EL floors):
  block show                     List blocked sectors (non-zero floors only)
  block show --map               ASCII horizon map of all floors
  block set --az <°> --el <°>    Set minimum elevation for a 5° AZ sector
  block train [--margin <°>]     Record current position as the floor
  block clear --az <°>           Remove floor for one sector
  block clear-all                Remove all floors

Network:
  netconfig --ip X --subnet X --gateway X [--mac X]
                                 Change field unit IP (saved to EEPROM + config file)
  netconfig reset                Revert to factory defaults on next boot

Monitoring:
  monitor [-rate Hz] [--deg]     Stream live telemetry (default 1 Hz averaged; 20 for raw)
  version                        Print version

Global flags (before command):
  -v, --verbose                  Show HTTP requests and responses on stderr
  --brain URL                    Override brain URL for this invocation

Environment:
  ROTOR_BRAIN_URL    Brain API base URL  (default: http://localhost:8090)
  ROTOR_AZ_RANGE     Full AZ travel °    (default: 450 — G-5500)
  ROTOR_EL_RANGE     Full EL travel °    (default: 180 — G-5500)
  BRAIN_CONFIG_FILE  Config file path    (default: ~/.rotor-brain.json)

Examples:
  rotor status
  rotor move cw stop
  rotor pol vhf uhf
  rotor park
  rotor block train --margin 5
  rotor block show --map
  rotor monitor -rate 5
  rotor -v move stop stop
  rotor --brain http://192.168.1.10:8090 status
`)
}

var version = "dev"
