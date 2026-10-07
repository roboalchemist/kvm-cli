// Package rustdesk reads and manages local RustDesk state (identity, relay
// server, address-book peers, and the service) so kvm-cli can enumerate and
// launch RustDesk sessions.
//
// RustDesk has no headless screenshot or input API: its wire protocol is a
// proprietary rendezvous + NaCl-authenticated, codec-compressed video stream
// with no client library. kvm-cli therefore does not re-implement that protocol;
// instead it manages RustDesk config/peers and launches connections, which are
// then driven through the display transports kvm-cli already speaks (KVM HID or
// VNC) — see the 'rustdesk connect' and 'rustdesk bridge' commands.
package rustdesk

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Config holds the parsed RustDesk settings relevant to agents.
type Config struct {
	// ID is the local RustDesk device id, when stored in plaintext.
	ID string
	// HasID reports whether the config carries an id (plaintext or encrypted).
	HasID bool
	// RendezvousServer is the configured relay/rendezvous host:port.
	RendezvousServer string
	// HasPermanentPassword reports whether a permanent (unattended) password is
	// configured. The password value itself is never read or returned.
	HasPermanentPassword bool
	// ConfigDir is the directory the values were read from.
	ConfigDir string
}

// DefaultConfigDir returns the platform default RustDesk config directory.
func DefaultConfigDir() string {
	home, _ := os.UserHomeDir()
	// macOS uses the Preferences bundle path; Linux uses ~/.config/rustdesk.
	mac := filepath.Join(home, "Library", "Preferences", "com.carriez.RustDesk")
	if fi, err := os.Stat(mac); err == nil && fi.IsDir() {
		return mac
	}
	return filepath.Join(home, ".config", "rustdesk")
}

// ParseConfigDir reads ID, rendezvous server, and permanent-password presence
// from dir (RustDesk.toml + RustDesk2.toml). Missing files are tolerated.
func ParseConfigDir(dir string) (*Config, error) {
	cfg := &Config{ConfigDir: dir}
	main, _ := os.ReadFile(filepath.Join(dir, "RustDesk.toml"))
	opts, _ := os.ReadFile(filepath.Join(dir, "RustDesk2.toml"))
	if cfg.ID == "" {
		cfg.ID = tomlString(string(main), "id")
	}
	if cfg.ID != "" {
		cfg.HasID = true
	} else if _, ok := tomlKey(string(main), "enc_id"); ok {
		// Newer RustDesk stores an encrypted id ("enc_id"); the plaintext id is
		// only obtainable from the running client (rustdesk --get-id).
		cfg.HasID = true
	}
	cfg.RendezvousServer = tomlString(string(opts), "rendezvous_server")
	if cfg.RendezvousServer == "" {
		cfg.RendezvousServer = tomlString(string(main), "rendezvous_server")
	}
	if _, ok := tomlKey(string(opts), "set-permanent-password"); ok {
		cfg.HasPermanentPassword = true
	}
	if _, ok := tomlKey(string(main), "password"); ok {
		cfg.HasPermanentPassword = true
	}
	return cfg, nil
}

// Peer is one address-book entry.
type Peer struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname,omitempty"`
	Username string `json:"username,omitempty"`
	Platform string `json:"platform,omitempty"`
	Alias    string `json:"alias,omitempty"`
}

// Peers reads the address book under dir/peers. Missing dir yields no peers.
func Peers(dir string) ([]Peer, error) {
	pdir := filepath.Join(dir, "peers")
	entries, err := os.ReadDir(pdir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("rustdesk: read peers: %w", err)
	}
	var out []Peer
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(pdir, e.Name()))
		if err != nil {
			continue
		}
		s := string(data)
		p := Peer{
			ID:       tomlString(s, "id"),
			Hostname: tomlString(s, "hostname"),
			Username: tomlString(s, "username"),
			Platform: tomlString(s, "platform"),
			Alias:    tomlString(s, "alias"),
		}
		if p.ID == "" {
			p.ID = strings.TrimSuffix(e.Name(), ".toml")
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// tomlKey returns the raw value string for a top-level key in a minimal TOML
// document, tolerating single/double quotes and surrounding whitespace. It
// understands the flat "key = 'value'" form RustDesk writes (including keys
// indented under a section header, which it still matches by name).
func tomlKey(doc, key string) (string, bool) {
	for _, line := range strings.Split(doc, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "[") {
			continue
		}
		eq := strings.IndexByte(t, '=')
		if eq < 0 {
			continue
		}
		k := strings.TrimSpace(t[:eq])
		if k != key {
			continue
		}
		return trimQuotes(strings.TrimSpace(t[eq+1:])), true
	}
	return "", false
}

// tomlString returns the value for key, or "" when absent.
func tomlString(doc, key string) string {
	v, _ := tomlKey(doc, key)
	return v
}

// trimQuotes strips a single layer of matching single or double quotes.
func trimQuotes(s string) string {
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
