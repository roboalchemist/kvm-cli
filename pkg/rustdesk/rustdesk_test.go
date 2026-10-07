package rustdesk

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestParseConfigDir(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "RustDesk.toml", `
id = '191877719'
password = '00Abc=='
salt = 'xyz'
`)
	write(t, dir, "RustDesk2.toml", `
rendezvous_server = 'rustdesk.example.com:21116'

[options]
set-permanent-password = 'secret'
accept-all = 'Y'
`)
	cfg, err := ParseConfigDir(dir)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.ID != "191877719" {
		t.Errorf("id = %q", cfg.ID)
	}
	if cfg.RendezvousServer != "rustdesk.example.com:21116" {
		t.Errorf("server = %q", cfg.RendezvousServer)
	}
	if !cfg.HasPermanentPassword {
		t.Error("expected permanent password detected")
	}
	if cfg.ConfigDir != dir {
		t.Errorf("configdir = %q", cfg.ConfigDir)
	}
}

func TestParseConfigDirEmpty(t *testing.T) {
	dir := t.TempDir()
	cfg, err := ParseConfigDir(dir)
	if err != nil {
		t.Fatalf("parse empty: %v", err)
	}
	if cfg.ID != "" || cfg.HasPermanentPassword {
		t.Errorf("expected empty config, got %+v", cfg)
	}
}

func TestPeers(t *testing.T) {
	dir := t.TempDir()
	pdir := filepath.Join(dir, "peers")
	if err := os.MkdirAll(pdir, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, pdir, "111.toml", `
id = '111'
username = 'alice'
hostname = 'workstation'
platform = 'Windows'
`)
	write(t, pdir, "222.toml", `
id = '222'
alias = 'server'
`)
	write(t, pdir, "ignore.txt", "not a peer")
	peers, err := Peers(dir)
	if err != nil {
		t.Fatalf("peers: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("got %d peers: %+v", len(peers), peers)
	}
	if peers[0].ID != "111" || peers[0].Username != "alice" || peers[0].Hostname != "workstation" {
		t.Errorf("peer0 = %+v", peers[0])
	}
	if peers[1].ID != "222" || peers[1].Alias != "server" {
		t.Errorf("peer1 = %+v", peers[1])
	}
}

func TestPeersMissingDir(t *testing.T) {
	peers, err := Peers(t.TempDir())
	if err != nil || peers != nil {
		t.Fatalf("expected nil, nil; got %v, %v", peers, err)
	}
}

func TestTomlKeyEdgeCases(t *testing.T) {
	doc := "# comment\n\n[section]\n  key = \"value\"\nother = plain\n"
	if v, ok := tomlKey(doc, "key"); !ok || v != "value" {
		t.Errorf("key = %q,%v", v, ok)
	}
	if v, ok := tomlKey(doc, "other"); !ok || v != "plain" {
		t.Errorf("other = %q,%v", v, ok)
	}
	if _, ok := tomlKey(doc, "missing"); ok {
		t.Error("missing key should be absent")
	}
	if _, ok := tomlKey("nokeyhere", "x"); ok {
		t.Error("line without '=' ignored")
	}
	// No matching section header line is treated as a key attempt and skipped.
	if v, ok := tomlKey("[section]", "section"); ok {
		t.Errorf("section header should not be a key, got %q", v)
	}
}

func TestTrimQuotes(t *testing.T) {
	cases := map[string]string{
		"'a'": "\"a\"", // not matching quotes
	}
	_ = cases
	if trimQuotes("'a'") != "a" {
		t.Error("single quotes")
	}
	if trimQuotes(`"b"`) != "b" {
		t.Error("double quotes")
	}
	if trimQuotes("c") != "c" {
		t.Error("bare")
	}
	if trimQuotes("''") != "" {
		t.Error("empty quotes")
	}
}

func TestDefaultConfigDir(t *testing.T) {
	if d := DefaultConfigDir(); d == "" {
		t.Error("expected non-empty default dir")
	}
}
