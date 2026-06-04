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

func main() {
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
	srv := api.NewServer(cfg.HTTPAddr, st, fuClient.Send)

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
