// rotor-serial — serial-port rotor control shim for software that only
// speaks RS-232 rotator protocols (no Hamlib/rotctld support), e.g. Ham
// Radio Deluxe. Pair it with a virtual serial port driver (VSPD or
// com0com) to bridge HRD's COM port to this process.
//
// Speaks one rotor-control protocol per run, selected with -m, same as
// hamlib's rotctl/rotctld -m/-r convention. Currently supported:
//
//	gs232b   Yaesu GS-232B (the de facto standard "Yaesu" rotator protocol)
//
// Usage:
//
//	rotor-serial -m gs232b -r COM5 [-s 9600]
//
// Environment:
//
//	SERIAL_BRAIN_URL   Brain API base URL   (default: http://localhost:8090)
//	SERIAL_AZ_RANGE    Full AZ travel °      (default: 450  — Yaesu G-5500)
//	SERIAL_EL_RANGE    Full EL travel °      (default: 180  — Yaesu G-5500)
//	SERIAL_TOLERANCE   Stop tolerance °      (default: 2.0)
//	SERIAL_AZ_OFFSET   AZ correction °       (default: 0)
//	SERIAL_EL_OFFSET   EL correction °       (default: 0)
package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"

	"go.bug.st/serial"

	"rotor-controller/brain/internal/tracker"
)

// ── config ────────────────────────────────────────────────────────────────────

type config struct {
	model       string
	device      string
	baud        int
	brainURL    string
	azRange     float64
	elRange     float64
	tolerance   float64
	azOffsetDeg float64
	elOffsetDeg float64
	verbose     bool
}

func loadConfig() config {
	return config{
		baud:        9600,
		brainURL:    envStr("SERIAL_BRAIN_URL", "http://localhost:8090"),
		azRange:     envFloat("SERIAL_AZ_RANGE", 450),
		elRange:     envFloat("SERIAL_EL_RANGE", 180),
		tolerance:   envFloat("SERIAL_TOLERANCE", 2.0),
		azOffsetDeg: envFloat("SERIAL_AZ_OFFSET", 0),
		elOffsetDeg: envFloat("SERIAL_EL_OFFSET", 0),
	}
}

// ── serial line framing ───────────────────────────────────────────────────────

// scanCRorLF splits on a bare CR, bare LF, or CRLF — GS-232B (and most RS-232
// rotator protocols) terminate commands with CR alone, but we stay lenient
// on input in case a client sends CRLF or LF.
func scanCRorLF(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i, b := range data {
		if b == '\r' || b == '\n' {
			advance = i + 1
			if b == '\r' && i+1 < len(data) && data[i+1] == '\n' {
				advance++
			}
			return advance, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// ── main ──────────────────────────────────────────────────────────────────────

var version = "dev"

func main() {
	args := os.Args[1:]
	for _, a := range args {
		if a == "--help" || a == "-h" {
			usage()
			return
		}
		if a == "--version" || a == "version" {
			fmt.Printf("rotor-serial %s\n", version)
			return
		}
	}

	cfg := loadConfig()
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-m", "--model":
			if i+1 < len(args) {
				cfg.model = args[i+1]
				i++
			}
		case "-r", "--rot-file":
			if i+1 < len(args) {
				cfg.device = args[i+1]
				i++
			}
		case "-s", "--serial-speed":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					cfg.baud = n
				}
				i++
			}
		case "--brain":
			if i+1 < len(args) {
				cfg.brainURL = args[i+1]
				i++
			}
		case "--az-offset":
			if i+1 < len(args) {
				if v, err := strconv.ParseFloat(args[i+1], 64); err == nil {
					cfg.azOffsetDeg = v
				}
				i++
			}
		case "--el-offset":
			if i+1 < len(args) {
				if v, err := strconv.ParseFloat(args[i+1], 64); err == nil {
					cfg.elOffsetDeg = v
				}
				i++
			}
		case "-v", "--verbose":
			cfg.verbose = true
		}
	}

	if cfg.model == "" {
		fatalf("missing -m <model> — supported: %s", modelList())
	}
	proto, ok := protocols[cfg.model]
	if !ok {
		fatalf("unknown model %q — supported: %s", cfg.model, modelList())
	}
	if cfg.device == "" {
		fatal("missing -r <device> — e.g. -r COM5 (Windows) or -r /dev/ttyUSB0 (Linux)")
	}

	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.Printf("rotor-serial %s starting — model %s  port %s @ %d baud  brain %s",
		version, proto.Name(), cfg.device, cfg.baud, cfg.brainURL)
	if cfg.azOffsetDeg != 0 || cfg.elOffsetDeg != 0 {
		log.Printf("serial: pointing offsets: AZ %+.1f°  EL %+.1f°", cfg.azOffsetDeg, cfg.elOffsetDeg)
	}

	port, err := serial.Open(cfg.device, &serial.Mode{
		BaudRate: cfg.baud,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	})
	if err != nil {
		log.Fatalf("open %s: %v", cfg.device, err)
	}
	defer port.Close()

	cal := tracker.FetchCalibration(cfg.brainURL)
	log.Printf("serial: calibration: AZ raw %.4f..%.4f offset %.1f°  EL raw %.4f..%.4f",
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

	log.Printf("serial: ready — waiting for %s commands on %s", proto.Name(), cfg.device)
	serve(port, proto, trk, cfg.verbose)
}

// serve reads CR/LF-terminated commands from the serial port, dispatches
// them to proto, and writes back any reply (CR-terminated, no LF — matches
// the GS-232B wire format confirmed against real controller traffic).
func serve(port io.ReadWriter, proto Protocol, trk *tracker.Tracker, verbose bool) {
	scanner := bufio.NewScanner(port)
	scanner.Split(scanCRorLF)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if verbose {
			log.Printf("serial: <- %q", line)
		}
		reply := proto.HandleLine(trk, line)
		if reply == nil {
			continue
		}
		reply = append(reply, '\r')
		if verbose {
			log.Printf("serial: -> %q", string(reply))
		}
		if _, err := port.Write(reply); err != nil {
			log.Printf("serial: write: %v", err)
		}
	}
	if err := scanner.Err(); err != nil {
		log.Fatalf("serial: read: %v", err)
	}
}

func modelList() string {
	names := make([]string, 0, len(protocols))
	for name := range protocols {
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

// ── help ──────────────────────────────────────────────────────────────────────

func usage() {
	fmt.Print(`rotor-serial — serial-port rotor control shim for the rotor controller.

For software that only speaks an RS-232 rotator protocol (no Hamlib/rotctld
support) — e.g. Ham Radio Deluxe. Pair with a virtual serial port driver
(VSPD, com0com) to bridge the controlling software's COM port to this process.

Usage:
  rotor-serial -m <model> -r <device> [-s <baud>] [options]

Required:
  -m, --model <model>       Rotor protocol to speak (see Supported models below)
  -r, --rot-file <device>   Serial device: COM5 (Windows) or /dev/ttyUSB0 (Linux)

Options:
  -s, --serial-speed <baud> Baud rate                (default: 9600)
  --brain <url>             Brain API base URL        (default: http://localhost:8090)
  --az-offset <deg>         AZ correction; positive = rotate antenna further CW
  --el-offset <deg>         EL correction; positive = tilt antenna further up
  -v, --verbose             Log every raw command/reply on the serial line
  --help                    Show this help
  --version                 Show version

Supported models:
  gs232b   Yaesu GS-232B — the de facto standard "Yaesu" rotator protocol

Environment (same effect as the flags above; flags win):
  SERIAL_BRAIN_URL   SERIAL_AZ_RANGE   SERIAL_EL_RANGE
  SERIAL_TOLERANCE   SERIAL_AZ_OFFSET  SERIAL_EL_OFFSET

Example (Windows, paired with VSPD):
  rotor-serial -m gs232b -r COM5 -s 9600
  # in VSPD, pair COM5 <-> COM6, then point Ham Radio Deluxe's rotor
  # control at COM6, model "Yaesu GS-232B"
`)
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

func fatal(args ...any) {
	fmt.Fprintln(os.Stderr, append([]any{"error:"}, args...)...)
	os.Exit(1)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
