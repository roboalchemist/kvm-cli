package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Session is a persisted auth token, optionally reused across CLI invocations
// to avoid a fresh login on every call.
type Session struct {
	Token   string    `json:"token"`
	SavedAt time.Time `json:"saved_at"`
}

// SessionPath returns the session file path, colocated with the config file.
func SessionPath() (string, error) {
	cfgPath, err := ConfigPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(cfgPath), "session.json"), nil
}

// SaveSession persists token to ~/.config/kvm-cli/session.json (mode 0600,
// directory mode 0700).
func SaveSession(token string) error {
	path, err := SessionPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}
	data, err := json.MarshalIndent(Session{Token: token, SavedAt: time.Now().UTC()}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

// LoadSession reads the persisted session. A missing file returns an error
// satisfying errors.Is(err, os.ErrNotExist).
func LoadSession() (*Session, error) {
	path, err := SessionPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read session %s: %w", path, err)
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse session %s: %w", path, err)
	}
	return &s, nil
}
