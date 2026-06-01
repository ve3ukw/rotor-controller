// Package mqtt publishes telemetry and fault events to an MQTT broker.
// If the broker URL is empty the publisher is a no-op.
package mqtt

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"
	"rotor-controller/brain/internal/wire"
)

// Publisher wraps an MQTT client.
type Publisher struct {
	client      pahomqtt.Client
	topicPrefix string
	enabled     bool
}

// New creates a Publisher. broker is e.g. "tcp://localhost:1883".
// If broker is empty, the publisher is disabled (all methods are no-ops).
func New(broker, topicPrefix string) *Publisher {
	if broker == "" {
		return &Publisher{}
	}
	opts := pahomqtt.NewClientOptions().
		AddBroker(broker).
		SetClientID(fmt.Sprintf("rotor-brain-%d", time.Now().UnixMilli())).
		SetAutoReconnect(true).
		SetConnectTimeout(10 * time.Second).
		SetKeepAlive(30 * time.Second).
		SetOnConnectHandler(func(_ pahomqtt.Client) {
			log.Printf("mqtt: connected to %s", broker)
		}).
		SetConnectionLostHandler(func(_ pahomqtt.Client, err error) {
			log.Printf("mqtt: connection lost: %v", err)
		})

	client := pahomqtt.NewClient(opts)
	if tok := client.Connect(); tok.WaitTimeout(10*time.Second) && tok.Error() != nil {
		log.Printf("mqtt: initial connect failed: %v (will retry)", tok.Error())
	}

	return &Publisher{
		client:      client,
		topicPrefix: topicPrefix,
		enabled:     true,
	}
}

// PublishTelemetry sends a telemetry frame to <prefix>/telemetry.
func (p *Publisher) PublishTelemetry(t *wire.Telemetry) {
	if !p.enabled {
		return
	}
	b, err := json.Marshal(t)
	if err != nil {
		return
	}
	p.publish(p.topicPrefix+"/telemetry", b, 0, false)
}

// PublishFault sends a fault event to <prefix>/fault.
func (p *Publisher) PublishFault(state string) {
	if !p.enabled {
		return
	}
	msg := fmt.Sprintf(`{"state":%q,"ts":%d}`, state, time.Now().UnixMilli())
	p.publish(p.topicPrefix+"/fault", []byte(msg), 1, true)
}

// PublishLink sends link-state changes to <prefix>/link.
func (p *Publisher) PublishLink(linked bool) {
	if !p.enabled {
		return
	}
	msg := fmt.Sprintf(`{"linked":%v,"ts":%d}`, linked, time.Now().UnixMilli())
	p.publish(p.topicPrefix+"/link", []byte(msg), 1, true)
}

func (p *Publisher) publish(topic string, payload []byte, qos byte, retain bool) {
	tok := p.client.Publish(topic, qos, retain, payload)
	tok.WaitTimeout(2 * time.Second)
	if err := tok.Error(); err != nil {
		log.Printf("mqtt: publish %s: %v", topic, err)
	}
}
