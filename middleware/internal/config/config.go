package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

const BlockCount = 90 // must match state.BlockCount

// File is the JSON schema persisted to the config file.
type File struct {
	FieldUnitHost string  `json:"field_unit_host,omitempty"`
	HTTPAddr      string  `json:"http_addr,omitempty"`
	MQTTBroker    string  `json:"mqtt_broker,omitempty"`
	Blocks        []uint8      `json:"blocks,omitempty"`      // 90-entry AZ el_floor table
	Limits        *Limits      `json:"limits,omitempty"`      // soft travel limits, normalized 0..1
	Calibration   *Calibration `json:"calibration,omitempty"` // pot gain/offset calibration
}

// Limits holds the soft travel limits, normalized 0..1 (matches the
// firmware's set_limits command fields).
type Limits struct {
	AzMin float64 `json:"az_min"`
	AzMax float64 `json:"az_max"`
	ElMin float64 `json:"el_min"`
	ElMax float64 `json:"el_max"`
}

// Calibration holds the pot gain/offset calibration for both axes.
// RawMin/RawMax are the raw telemetry fractions (0..1) measured at each
// axis's mechanical end stops; AzOffsetDeg is the AZ mechanical degree
// (0..450) that corresponds to true north (0° compass bearing).
// Zero-value (all unset) is equivalent to uncalibrated 1:1 raw==degrees.
type Calibration struct {
	AzRawMin    float64 `json:"az_raw_min"`
	AzRawMax    float64 `json:"az_raw_max"`
	AzOffsetDeg float64 `json:"az_offset_deg"`
	ElRawMin    float64 `json:"el_raw_min"`
	ElRawMax    float64 `json:"el_raw_max"`
}

// DefaultCalibration is the uncalibrated identity mapping (raw fraction ==
// mechanical degrees / range, AZ offset 0).
func DefaultCalibration() Calibration {
	return Calibration{AzRawMin: 0, AzRawMax: 1, AzOffsetDeg: 0, ElRawMin: 0, ElRawMax: 1}
}

type Config struct {
	FieldUnitHost    string
	FieldUnitTCPPort int
	FieldUnitUDPPort int
	UDPListenAddr    string
	HTTPAddr         string
	MQTTBroker       string
	MQTTTopicPrefix  string
	AzRange          float64 // full AZ travel in degrees (G-5500: 450)
	ElRange          float64 // full EL travel in degrees (G-5500: 180)

	// Path of the loaded config file (empty if none found).
	FilePath string
}

// DefaultFilePath returns the platform-appropriate config file path.
//
// Search order:
//  1. BRAIN_CONFIG_FILE env var (explicit override)
//  2. rotor-brain.json next to the running binary  (drop-in, Windows-friendly)
//  3. %APPDATA%\rotor\brain.json                   (Windows standard)
//  4. ~/.rotor-brain.json                          (Linux / macOS)
func DefaultFilePath() string {
	if p := os.Getenv("BRAIN_CONFIG_FILE"); p != "" {
		return p
	}
	// Check for a config file sitting next to the executable — the most
	// convenient location on Windows (user drops exe + json in one folder).
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "rotor-brain.json")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	// Platform-appropriate home location.
	if runtime.GOOS == "windows" {
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "rotor", "brain.json")
		}
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
		FieldUnitHost:    "192.168.1.5",
		FieldUnitTCPPort: 7700,
		FieldUnitUDPPort: 7701,
		UDPListenAddr:    ":7701",
		HTTPAddr:         ":8090",
		MQTTBroker:       "",
		MQTTTopicPrefix:  "rotor",
		AzRange:          450,
		ElRange:          180,
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
	if v := os.Getenv("ROTOR_AZ_RANGE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.AzRange = f
		}
	}
	if v := os.Getenv("ROTOR_EL_RANGE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.ElRange = f
		}
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

// SaveLimits updates the soft travel limits in the config file.
func SaveLimits(l Limits) error {
	return updateFile(func(f *File) { f.Limits = &l })
}

// LoadLimits returns the stored soft travel limits, or nil if never set.
func LoadLimits() *Limits {
	f, _ := loadFile(DefaultFilePath())
	return f.Limits
}

// SaveCalibration updates the pot gain/offset calibration in the config file.
func SaveCalibration(c Calibration) error {
	return updateFile(func(f *File) { f.Calibration = &c })
}

// LoadCalibration returns the stored calibration, or the uncalibrated
// identity mapping if never set.
func LoadCalibration() Calibration {
	f, _ := loadFile(DefaultFilePath())
	if f.Calibration != nil {
		return *f.Calibration
	}
	return DefaultCalibration()
}

func updateFile(fn func(*File)) error {
	path := DefaultFilePath()
	// Create parent directory if needed (e.g. %APPDATA%\rotor\ on first run).
	if dir := filepath.Dir(path); dir != "." {
		_ = os.MkdirAll(dir, 0755)
	}
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
