package fieldunit

import (
	"log"
	"net"

	"rotor-controller/brain/internal/wire"
)

// RunTelemetryReceiver listens on UDP addr and forwards parsed frames to ch.
// Blocks until ctx is cancelled (or error — it logs and exits).
func RunTelemetryReceiver(addr string, ch chan<- *wire.Telemetry) {
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		log.Fatalf("udp listen %s: %v", addr, err)
	}
	defer conn.Close()
	log.Printf("telemetry: listening on UDP %s", addr)

	buf := make([]byte, 1024)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			log.Printf("telemetry: read error: %v", err)
			return
		}
		t, err := wire.ParseTelemetry(buf[:n])
		if err != nil {
			log.Printf("telemetry: parse error: %v (raw: %s)", err, buf[:n])
			continue
		}
		select {
		case ch <- t:
		default:
			// drop if consumer is lagging
		}
	}
}
