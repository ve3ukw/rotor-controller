package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
)

const BlockCount = 90 // must match state.BlockCount

// File is the JSON schema persisted to the config file.
type File struct {
	FieldUnitHost string  `json:"field_unit_host,omitempty"`
	HTTPAddr      string  `json:"http_addr,omitempty"`
	MQTTBroker    string  `json:"mqtt_broker,omitempty"`
	Blocks        []uint8 `json:"blocks,omitempty"` // 90-entry AZ el_floor table
}

type Config struct {
	FieldUnitHost    string
	FieldUnitTCPPort int
	FieldUnitUDPPort int
	UDPListenAddr    string
	HTTPAddr         string
	MQTTBroker       string
	MQTTTopicPrefix  string

	// Path of the loaded config file (empty if none found).
	FilePath string
}

// DefaultFilePath returns ~/.rotor-brain.json (cross-platform).
func DefaultFilePath() string {
	if p := os.Getenv("BRAIN_CONFIG_FILE"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "rotor-brain.json"
	}
	return filepath.Join(home, ".rotor-brain.json")
}

// Load builds Config with precedence: defaults → config file → env vars.
func Load() *Config {
	// 1. Hard-coded defaults
	cfg := &Config{
		FieldUnitHost:   "192.168.1.5",
		FieldUnitTCPPort: 7700,
		FieldUnitUDPPort: 7701,
		UDPListenAddr:   ":7701",
		HTTPAddr:        ":8090",
		MQTTBroker:      "",
		MQTTTopicPrefix: "rotor",
	}

	// 2. Config file (overrides defaults for fields present in the file)
	path := DefaultFilePath()
	if f, err := loadFile(path); err == nil {
		cfg.FilePath = path
		if f.FieldUnitHost != "" {
			cfg.FieldUnitHost = f.FieldUnitHost
		}
		if f.HTTPAddr != "" {
			cfg.HTTPAddr = f.HTTPAddr
		}
		if f.MQTTBroker != "" {
			cfg.MQTTBroker = f.MQTTBroker
		}
	}

	// 3. Environment variables (always win)
	if v := os.Getenv("BRAIN_FIELD_UNIT_HOST"); v != "" {
		cfg.FieldUnitHost = v
	}
	if v := os.Getenv("BRAIN_FIELD_UNIT_TCP_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.FieldUnitTCPPort = n
		}
	}
	if v := os.Getenv("BRAIN_HTTP_ADDR"); v != "" {
		cfg.HTTPAddr = v
	}
	if v := os.Getenv("BRAIN_MQTT_BROKER"); v != "" {
		cfg.MQTTBroker = v
	}
	if v := os.Getenv("BRAIN_MQTT_TOPIC_PREFIX"); v != "" {
		cfg.MQTTTopicPrefix = v
	}

	return cfg
}

// SaveFieldUnitHost updates (or creates) the config file with a new field unit host.
func SaveFieldUnitHost(host string) error {
	return updateFile(func(f *File) { f.FieldUnitHost = host })
}

// SaveBlocks updates the block table in the config file.
func SaveBlocks(blocks [BlockCount]uint8) error {
	return updateFile(func(f *File) {
		f.Blocks = make([]uint8, BlockCount)
		copy(f.Blocks, blocks[:])
	})
}

// LoadBlocks returns the stored block table, or all-zeros if not set.
func LoadBlocks() [BlockCount]uint8 {
	f, _ := loadFile(DefaultFilePath())
	var out [BlockCount]uint8
	if len(f.Blocks) == BlockCount {
		copy(out[:], f.Blocks)
	}
	return out
}

func updateFile(fn func(*File)) error {
	path := DefaultFilePath()
	existing, _ := loadFile(path)
	fn(&existing)
	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

func loadFile(path string) (File, error) {
	var f File
	data, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	err = json.Unmarshal(data, &f)
	return f, err
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
