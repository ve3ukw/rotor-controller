package main

import (
	"fmt"
	"log"
	"os"

	"rotor-controller/brain/internal/api"
	"rotor-controller/brain/internal/config"
	"rotor-controller/brain/internal/fieldunit"
	"rotor-controller/brain/internal/mqtt"
	"rotor-controller/brain/internal/state"
	"rotor-controller/brain/internal/wire"
)

var version = "dev"

func usage() {
	fmt.Printf(`rotor-brain %s — antenna rotator brain daemon

Usage:
  rotor-brain [version]

The brain connects to the field unit, exposes a REST + WebSocket API for the
rotor CLI and other clients, and optionally publishes telemetry to MQTT.

Configuration — lowest to highest precedence:
  1. Built-in defaults
  2. Config file   %s
  3. Environment variables (always win)

Config file (JSON, created automatically by 'rotor netconfig'):
  {
    "field_unit_host": "192.168.1.5"
  }

Environment variables:
  BRAIN_FIELD_UNIT_HOST   Field unit IP address    (default: 192.168.1.5)
  BRAIN_FIELD_UNIT_TCP_PORT  TCP port              (default: 7700)
  BRAIN_HTTP_ADDR         REST/WebSocket listen    (default: :8090)
  BRAIN_MQTT_BROKER       MQTT broker URL          (empty = disabled)
  BRAIN_MQTT_TOPIC_PREFIX MQTT topic prefix        (default: rotor)
  BRAIN_CONFIG_FILE       Config file path override

Quick start (Windows PowerShell):
  .\rotor-brain.exe                          # uses config file / defaults
  $env:BRAIN_FIELD_UNIT_HOST="192.168.1.5"; .\rotor-brain.exe
`, version, config.DefaultFilePath())
}

func main() {
	for _, a := range os.Args[1:] {
		if a == "--help" || a == "-h" || a == "help" {
			usage()
			return
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("rotor-brain %s\n", version)
		return
	}

	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.Printf("rotor-brain %s starting", version)

	cfg := config.Load()
	if cfg.FilePath != "" {
		log.Printf("config: loaded from %s", cfg.FilePath)
	} else {
		log.Printf("config: no config file found at %s — using defaults", config.DefaultFilePath())
	}
	log.Printf("config: field unit %s:%d  http %s  mqtt %q",
		cfg.FieldUnitHost, cfg.FieldUnitTCPPort, cfg.HTTPAddr, cfg.MQTTBroker)

	st := state.NewStore()
	pub := mqtt.New(cfg.MQTTBroker, cfg.MQTTTopicPrefix)

	// Telemetry channel — fan-out goroutine started after srv is created below.
	telemCh := make(chan *wire.Telemetry, 32)

	// TCP client — commands, acks, and telemetry on one connection.
	// Telemetry arrives over TCP because the W5500 on this module silently
	// drops UDP SEND commands (Sn_MR is cleared after OPEN).
	fuAddr := fmt.Sprintf("%s:%d", cfg.FieldUnitHost, cfg.FieldUnitTCPPort)
	// Load stored block table so we can push it to the field unit on connect.
	storedBlocks := config.LoadBlocks()
	st.SetBlocks(storedBlocks)

	// Load stored soft limits (if customized) so we can re-push them on
	// connect — this survives a firmware reflash, which resets sm_init()
	// defaults.
	if storedLimits := config.LoadLimits(); storedLimits != nil {
		st.SetLimits(state.Limits{
			AzMin: storedLimits.AzMin, AzMax: storedLimits.AzMax,
			ElMin: storedLimits.ElMin, ElMax: storedLimits.ElMax,
		})
	}

	// Load stored pot gain/offset calibration (purely brain-side; nothing
	// to push to the field unit).
	st.SetCalibration(config.LoadCalibration())

	// Load stored park position so we can re-push it on connect.
	if storedPark := config.LoadPark(); storedPark != nil {
		st.SetPark(storedPark)
	}

	var fuClient *fieldunit.Client
	fuClient = fieldunit.NewClient(fuAddr,
		func(linked bool) {
			st.SetLinked(linked)
			pub.PublishLink(linked)
			if linked {
				log.Printf("fieldunit: link UP")
				// Push block table so field unit enforces the latest config
				// even if it rebooted and only has older EEPROM data.
				blocks := st.Blocks()
				go func() {
					cmd := wire.Command{Type: "set_blocks", Blocks: blocks[:]}
					if _, err := fuClient.Send(cmd); err != nil {
						log.Printf("blocks: push failed: %v", err)
					} else {
						log.Printf("blocks: pushed 90 sectors to field unit")
					}
				}()
				// Push customized soft limits, if any, so a field unit that
				// rebooted (and reset to sm_init() defaults) picks them up.
				if l := st.Limits(); l != nil {
					go func() {
						cmd := wire.Command{
							Type:  "set_limits",
							AzMin: wire.F64Ptr(l.AzMin), AzMax: wire.F64Ptr(l.AzMax),
							ElMin: wire.F64Ptr(l.ElMin), ElMax: wire.F64Ptr(l.ElMax),
						}
						if _, err := fuClient.Send(cmd); err != nil {
							log.Printf("limits: push failed: %v", err)
						} else {
							log.Printf("limits: pushed custom soft limits to field unit")
						}
					}()
				}
				// Push customized park position, if any.
				if p := st.Park(); p != nil {
					go func() {
						cmd := wire.Command{
							Type:      "set_park",
							ParkAzRaw: wire.F64Ptr(p.AzRaw),
							ParkElRaw: wire.F64Ptr(p.ElRaw),
						}
						if _, err := fuClient.Send(cmd); err != nil {
							log.Printf("park: push failed: %v", err)
						} else {
							log.Printf("park: pushed custom park position to field unit")
						}
					}()
				}
			} else {
				log.Printf("fieldunit: link DOWN")
			}
		},
		func(t *wire.Telemetry) {
			select {
			case telemCh <- t:
			default:
			}
		},
	)

	// HTTP server (REST + WebSocket)
	srv := api.NewServer(cfg.HTTPAddr, st, fuClient.Send, cfg.AzRange, cfg.ElRange)

	// Telemetry fan-out: state store, WebSocket broadcast, MQTT
	lastState := ""
	go func() {
		for t := range telemCh {
			st.UpdateTelemetry(t)
			srv.BroadcastTelemetry(t)
			pub.PublishTelemetry(t)
			if t.State != lastState {
				if isFault(t.State) {
					pub.PublishFault(t.State)
				}
				lastState = t.State
			}
		}
	}()

	go fuClient.Run()
	log.Fatal(srv.Run())
}

func isFault(state string) bool {
	return len(state) >= 5 && state[:5] == "FAULT"
}
