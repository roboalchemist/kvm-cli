// Package auth resolves KVM credentials from a layered priority chain
// (flags > environment > gopass > config file > interactive prompt) and builds
// authenticated *api.Client values.
//
// The GL-RM1PE device presents a self-signed certificate with an absurd
// validity window (1970-1979), so TLS verification always fails. GetClient
// therefore attempts certificate verification first and, only when it fails
// with an x509/certificate error, transparently falls back to insecure TLS
// after printing a one-line warning. Set KVM_TLS_STRICT=1 (or GLKVM_TLS_STRICT)
// to disable the fallback and require a valid certificate; pass --insecure to
// skip the initial verification attempt entirely.
package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ConfigKeys lists the keys accepted by SetConfigValue.
//
// The first five are the credential/connection keys; the last four are the
// non-secret computer-use assistant (CUA) keys consumed by cmd/cua.go and the
// screenshot scratch-dir default.
var ConfigKeys = []string{
	"url", "username", "password", "timeout", "insecure",
	"models_url", "grounding_model", "planner_model", "scratch_dir",
}

// Config is the persisted CLI configuration stored at
// ~/.config/kvm-cli/config.json (mode 0600).
type Config struct {
	URL      string `json:"url,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// Timeout is a Go duration string such as "30s" or "1m". Empty means the
	// built-in default (30s).
	Timeout string `json:"timeout,omitempty"`
	// Insecure skips TLS certificate verification.
	Insecure bool `json:"insecure,omitempty"`
	// ModelsURL is the computer-use models platform root. Empty means the
	// built-in default (https://models.example.com). Non-secret.
	ModelsURL string `json:"models_url,omitempty"`
	// GroundingModel is the screen-parser model id (OmniParser). Empty means
	// the built-in default (omniparser). Non-secret.
	GroundingModel string `json:"grounding_model,omitempty"`
	// PlannerModel is the chat model used as the element chooser. Empty means
	// "auto": pick a running chat model from the catalog. Non-secret.
	PlannerModel string `json:"planner_model,omitempty"`
	// ScratchDir is where transient artifacts (screenshots, Set-of-Mark PNGs)
	// are written. Empty means the OS temp directory. Non-secret.
	ScratchDir string `json:"scratch_dir,omitempty"`
}

// UnmarshalJSON is lenient: it accepts `insecure` as either a JSON boolean or a
// string ("true"/"false"/"1"/"0"), and `timeout` as a string or number. This
// keeps compatibility with config files written by earlier versions of the CLI
// that stored every value as a string.
func (c *Config) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["url"]; ok {
		c.URL = rawString(v)
	}
	if v, ok := raw["username"]; ok {
		c.Username = rawString(v)
	}
	if v, ok := raw["password"]; ok {
		c.Password = rawString(v)
	}
	if v, ok := raw["timeout"]; ok {
		c.Timeout = rawString(v)
	}
	if v, ok := raw["insecure"]; ok {
		b, err := rawBool(v)
		if err != nil {
			return fmt.Errorf("config: invalid \"insecure\" value: %w", err)
		}
		c.Insecure = b
	}
	if v, ok := raw["models_url"]; ok {
		c.ModelsURL = rawString(v)
	}
	if v, ok := raw["grounding_model"]; ok {
		c.GroundingModel = rawString(v)
	}
	if v, ok := raw["planner_model"]; ok {
		c.PlannerModel = rawString(v)
	}
	if v, ok := raw["scratch_dir"]; ok {
		c.ScratchDir = rawString(v)
	}
	return nil
}

// ConfigDir returns ~/.config/kvm-cli (not created here).
func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "kvm-cli"), nil
}

// ConfigPath returns the config file path. The KVM_CONFIG (or GLKVM_CONFIG)
// environment variable overrides the default ~/.config/kvm-cli/config.json.
func ConfigPath() (string, error) {
	if p := firstEnv("KVM_CONFIG", "GLKVM_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// LoadConfig reads the config file. A missing or empty file yields an empty,
// non-nil Config and no error. A malformed file is an error.
func LoadConfig() (*Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	return loadConfigFrom(path)
}

func loadConfigFrom(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	cfg := &Config{}
	if len(bytes.TrimSpace(data)) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// SaveConfig writes cfg to the config file with mode 0600, creating the parent
// directory (mode 0700) when necessary.
func SaveConfig(cfg *Config) error {
	if cfg == nil {
		return errors.New("save config: nil config")
	}
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// WriteFile does not tighten permissions on a pre-existing file.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

// SetConfigValue validates and persists a single key/value pair.
func SetConfigValue(key, value string) error {
	if !IsConfigKey(key) {
		return fmt.Errorf("unknown config key %q (supported: %s)", key, strings.Join(ConfigKeys, ", "))
	}
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	switch key {
	case "url":
		cfg.URL = value
	case "username":
		cfg.Username = value
	case "password":
		cfg.Password = value
	case "timeout":
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("invalid timeout %q: %w", value, err)
		}
		cfg.Timeout = value
	case "insecure":
		b, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("invalid insecure %q (expected true or false)", value)
		}
		cfg.Insecure = b
	case "models_url":
		cfg.ModelsURL = value
	case "grounding_model":
		cfg.GroundingModel = value
	case "planner_model":
		cfg.PlannerModel = value
	case "scratch_dir":
		cfg.ScratchDir = value
	}
	return SaveConfig(cfg)
}

// ListConfig returns the set configuration values as a key -> string map.
// Unset keys (including insecure=false) are omitted.
func ListConfig() (map[string]string, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if cfg.URL != "" {
		out["url"] = cfg.URL
	}
	if cfg.Username != "" {
		out["username"] = cfg.Username
	}
	if cfg.Password != "" {
		out["password"] = cfg.Password
	}
	if cfg.Timeout != "" {
		out["timeout"] = cfg.Timeout
	}
	if cfg.Insecure {
		out["insecure"] = "true"
	}
	if cfg.ModelsURL != "" {
		out["models_url"] = cfg.ModelsURL
	}
	if cfg.GroundingModel != "" {
		out["grounding_model"] = cfg.GroundingModel
	}
	if cfg.PlannerModel != "" {
		out["planner_model"] = cfg.PlannerModel
	}
	if cfg.ScratchDir != "" {
		out["scratch_dir"] = cfg.ScratchDir
	}
	return out, nil
}

// IsConfigKey reports whether key is a supported configuration key.
func IsConfigKey(key string) bool {
	for _, k := range ConfigKeys {
		if k == key {
			return true
		}
	}
	return false
}

// rawString best-effort decodes a JSON raw value into a string, accepting
// strings, numbers and booleans.
func rawString(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	switch t := v.(type) {
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return ""
	}
}

// rawBool decodes a JSON raw value into a bool, accepting booleans and the
// common string spellings.
func rawBool(raw json.RawMessage) (bool, error) {
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return parseBool(s)
	}
	return false, fmt.Errorf("expected true/false, got %s", string(raw))
}

// parseBool accepts the usual true/false spellings, case-insensitively.
func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off", "":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean %q", s)
	}
}
