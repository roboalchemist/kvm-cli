package auth

import (
	"encoding/json"
	"testing"
)

// TestConfigKeysIncludeCUASettings verifies the four non-secret CUA keys are
// part of the supported key set and round-trip through set/list/unset.
func TestConfigKeysIncludeCUASettings(t *testing.T) {
	isolate(t)

	want := map[string]string{
		"models_url":      "https://models.example.com",
		"grounding_model": "omniparser",
		"planner_model":   "qwen-test",
		"scratch_dir":     "/tmp/kvmshot",
	}
	for k, v := range want {
		if !IsConfigKey(k) {
			t.Fatalf("IsConfigKey(%q) = false, want true", k)
		}
		if err := SetConfigValue(k, v); err != nil {
			t.Fatalf("SetConfigValue(%q): %v", k, err)
		}
	}

	list, err := ListConfig()
	if err != nil {
		t.Fatalf("ListConfig: %v", err)
	}
	for k, v := range want {
		if list[k] != v {
			t.Errorf("ListConfig[%q] = %q, want %q", k, list[k], v)
		}
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.ModelsURL != want["models_url"] ||
		cfg.GroundingModel != want["grounding_model"] ||
		cfg.PlannerModel != want["planner_model"] ||
		cfg.ScratchDir != want["scratch_dir"] {
		t.Fatalf("Config fields not persisted: %+v", cfg)
	}

	for k := range want {
		if err := SetConfigValue(k, ""); err != nil {
			t.Fatalf("clear %q: %v", k, err)
		}
	}
	list, err = ListConfig()
	if err != nil {
		t.Fatalf("ListConfig after clear: %v", err)
	}
	for k := range want {
		if _, ok := list[k]; ok {
			t.Errorf("key %q still set after clearing: %v", k, list)
		}
	}
}

// TestConfigUnmarshalNewKeys covers the lenient UnmarshalJSON path for the
// new keys, including non-string scalars.
func TestConfigUnmarshalNewKeys(t *testing.T) {
	var cfg Config
	raw := `{"models_url":"https://m","grounding_model":"g","planner_model":"p","scratch_dir":"/tmp/s"}`
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if cfg.ModelsURL != "https://m" || cfg.GroundingModel != "g" || cfg.PlannerModel != "p" || cfg.ScratchDir != "/tmp/s" {
		t.Fatalf("unexpected decode: %+v", cfg)
	}
}
