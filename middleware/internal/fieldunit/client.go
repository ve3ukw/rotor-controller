// Package fieldunit manages the TCP connection to the field unit.
// It sends commands and reads acks, reconnecting automatically on disconnect.
package fieldunit

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"rotor-controller/brain/internal/wire"
)

const (
	reconnectDelay  = 3 * time.Second
	heartbeatPeriod = 1 * time.Second
	ackTimeout      = 5 * time.Second
)

// Client manages the TCP command connection to the field unit.
type Client struct {
	addr string

	mu      sync.Mutex
	conn    net.Conn
	writer  *bufio.Writer
	seq     atomic.Uint32
	pending sync.Map // seq (uint32) → chan *wire.Ack

	onLink      func(bool)
	onTelemetry func(*wire.Telemetry) // called for each telemetry frame on TCP
}

// NewClient creates a Client targeting addr ("host:port").
// onLink is called true on connect, false on disconnect.
// onTelemetry is called for every telemetry frame received on the TCP stream.
func NewClient(addr string, onLink func(bool), onTelemetry func(*wire.Telemetry)) *Client {
	return &Client{addr: addr, onLink: onLink, onTelemetry: onTelemetry}
}

// Run connects and maintains the TCP session. Blocks forever.
// Received acks are forwarded to callers waiting in Send().
func (c *Client) Run() {
	for {
		if err := c.connect(); err != nil {
			log.Printf("fieldunit: connect %s: %v — retrying in %s", c.addr, err, reconnectDelay)
			time.Sleep(reconnectDelay)
			continue
		}

		log.Printf("fieldunit: connected to %s", c.addr)
		c.onLink(true)

		// Send HELLO
		if err := c.send(wire.Command{Type: "hello"}); err != nil {
			log.Printf("fieldunit: hello failed: %v", err)
			c.closeConn()
			c.onLink(false)
			time.Sleep(reconnectDelay)
			continue
		}

		// Heartbeat goroutine — runs until conn closed
		done := make(chan struct{})
		go c.heartbeat(done)

		// Read acks (blocks until disconnect)
		c.readLoop()
		close(done)

		c.closeConn()
		c.onLink(false)
		log.Printf("fieldunit: disconnected — retrying in %s", reconnectDelay)
		time.Sleep(reconnectDelay)
	}
}

// Send sends a command and waits for an ack. Returns the ack or error.
// seq is auto-assigned.
func (c *Client) Send(cmd wire.Command) (*wire.Ack, error) {
	cmd.Seq = c.seq.Add(1)
	ackCh := make(chan *wire.Ack, 1)
	c.pending.Store(cmd.Seq, ackCh)
	defer c.pending.Delete(cmd.Seq)

	if err := c.send(cmd); err != nil {
		return nil, err
	}

	select {
	case ack := <-ackCh:
		return ack, nil
	case <-time.After(ackTimeout):
		return nil, fmt.Errorf("ack timeout for seq %d", cmd.Seq)
	}
}

func (c *Client) connect() error {
	conn, err := net.DialTimeout("tcp", c.addr, 5*time.Second)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.conn = conn
	c.writer = bufio.NewWriter(conn)
	c.mu.Unlock()
	return nil
}

func (c *Client) send(cmd wire.Command) error {
	b, err := cmd.Marshal()
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writer == nil {
		return fmt.Errorf("not connected")
	}
	_, err = c.writer.Write(b)
	if err != nil {
		return err
	}
	return c.writer.Flush()
}

func (c *Client) heartbeat(done <-chan struct{}) {
	t := time.NewTicker(heartbeatPeriod)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if err := c.send(wire.Command{Type: "heartbeat", Seq: c.seq.Add(1)}); err != nil {
				log.Printf("fieldunit: heartbeat error: %v", err)
				return
			}
		case <-done:
			return
		}
	}
}

func (c *Client) readLoop() {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), 4096) // telemetry frames are ~300 bytes
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		// Telemetry frames come over TCP now — route before trying ack parse.
		if t, err := wire.ParseTelemetry(line); err == nil && t.Type == "telemetry" {
			if c.onTelemetry != nil {
				c.onTelemetry(t)
			}
			continue
		}
		ack, err := wire.ParseAck(line)
		if err != nil {
			if !errors.Is(err, wire.ErrNoAck) {
				log.Printf("fieldunit: parse ack: %v", err)
			}
			continue
		}
		if ch, ok := c.pending.Load(ack.Seq); ok {
			ch.(chan *wire.Ack) <- ack
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("fieldunit: read: %v", err)
	}
}

func (c *Client) closeConn() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
		c.writer = nil
	}
}
