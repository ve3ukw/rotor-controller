package config

import (
	"os"
	"strconv"
)

type Config struct {
	FieldUnitHost    string
	FieldUnitTCPPort int
	FieldUnitUDPPort int
	UDPListenAddr    string // local bind address for telemetry reception
	HTTPAddr         string
	MQTTBroker       string // empty = disabled
	MQTTTopicPrefix  string
}

func Load() *Config {
	return &Config{
		FieldUnitHost:    envStr("BRAIN_FIELD_UNIT_HOST", "192.168.1.5"),
		FieldUnitTCPPort: envInt("BRAIN_FIELD_UNIT_TCP_PORT", 7700),
		FieldUnitUDPPort: envInt("BRAIN_FIELD_UNIT_UDP_PORT", 7701),
		UDPListenAddr:    envStr("BRAIN_UDP_LISTEN_ADDR", ":7701"),
		HTTPAddr:         envStr("BRAIN_HTTP_ADDR", ":8090"),
		MQTTBroker:       envStr("BRAIN_MQTT_BROKER", ""),
		MQTTTopicPrefix:  envStr("BRAIN_MQTT_TOPIC_PREFIX", "rotor"),
	}
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
