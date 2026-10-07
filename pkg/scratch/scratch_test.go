package scratch

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestResolvePrecedence(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "explicit")
	env := filepath.Join(t.TempDir(), "env")
	configured := filepath.Join(t.TempDir(), "configured")

	t.Run("explicit wins", func(t *testing.T) {
		t.Setenv(EnvVar, env)
		got, err := Resolve(explicit, configured)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got != explicit {
			t.Fatalf("got %q, want explicit %q", got, explicit)
		}
	})

	t.Run("env beats configured", func(t *testing.T) {
		t.Setenv(EnvVar, env)
		got, err := Resolve("", configured)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got != env {
			t.Fatalf("got %q, want env %q", got, env)
		}
	})

	t.Run("configured when env blank", func(t *testing.T) {
		t.Setenv(EnvVar, "   ")
		got, err := Resolve("", configured)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got != configured {
			t.Fatalf("got %q, want configured %q", got, configured)
		}
	})

	t.Run("temp dir fallback", func(t *testing.T) {
		t.Setenv(EnvVar, "")
		got, err := Resolve("", "")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if want := filepath.Clean(os.TempDir()); got != want {
			t.Fatalf("got %q, want os.TempDir %q", got, want)
		}
	})

	t.Run("whitespace trimmed", func(t *testing.T) {
		t.Setenv(EnvVar, "")
		got, err := Resolve("  "+explicit+"  ", "")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got != explicit {
			t.Fatalf("got %q, want %q", got, explicit)
		}
	})
}

func TestDirUsesEnv(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ambient")
	t.Setenv(EnvVar, dir)
	got, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if got != dir {
		t.Fatalf("got %q, want %q", got, dir)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("Dir did not create the directory: %v", err)
	}
}

func TestResolveCreatesDirWithMode(t *testing.T) {
	nested := filepath.Join(t.TempDir(), "a", "b", "c")
	got, err := Resolve(nested, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != nested {
		t.Fatalf("got %q, want %q", got, nested)
	}
	info, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", nested)
	}
	if perm := info.Mode().Perm(); perm != dirPerm {
		t.Fatalf("perm = %o, want %o", perm, dirPerm)
	}
}

func TestResolveErrorWhenPathIsFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Resolve(filepath.Join(file, "sub"), ""); err == nil {
		t.Fatal("expected an error creating a directory beneath a regular file")
	}
}

func TestResolveEmptyIsImpossible(t *testing.T) {
	// os.TempDir always yields something, so the empty-string guard is only
	// reachable through ensureDir directly.
	if _, err := ensureDir("   "); err == nil {
		t.Fatal("expected error for blank directory")
	}
}

var pathNameRe = regexp.MustCompile(`^kvm-screenshot-\d{8}-\d{6}\.\d{3}(-\d+)?\.jpg$`)

func TestPathInFormatAndUniqueness(t *testing.T) {
	dir := t.TempDir()
	seen := make(map[string]bool)
	for i := 0; i < 500; i++ {
		got, err := PathIn(dir, "kvm-screenshot", "jpg")
		if err != nil {
			t.Fatalf("PathIn: %v", err)
		}
		if filepath.Dir(got) != dir {
			t.Fatalf("path %q is not in %q", got, dir)
		}
		base := filepath.Base(got)
		if !pathNameRe.MatchString(base) {
			t.Fatalf("name %q does not match %s", base, pathNameRe)
		}
		if seen[got] {
			t.Fatalf("duplicate path %q", got)
		}
		seen[got] = true
	}
}

func TestPathUsesAmbientDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ambient")
	t.Setenv(EnvVar, dir)
	got, err := Path("shot", ".png")
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if filepath.Dir(got) != dir {
		t.Fatalf("path %q not in ambient dir %q", got, dir)
	}
	if !strings.HasSuffix(got, ".png") {
		t.Fatalf("path %q missing .png suffix", got)
	}
}

func TestPathSanitizesName(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		prefix string
		ext    string
		want   string // expected basename prefix (before the timestamp)
		suffix string
	}{
		{"kvm-screenshot", "jpg", "kvm-screenshot-", ".jpg"},
		{"a/b\\c", "png", "a_b_c-", ".png"},
		{"../evil", ".txt", "evil-", ".txt"},
		{"", "", DefaultPrefix + "-", ""},
		{"  spaced  ", ".jpg", "spaced-", ".jpg"},
		{"...", ".jpg", DefaultPrefix + "-", ".jpg"},
	}
	for _, tc := range cases {
		got, err := PathIn(dir, tc.prefix, tc.ext)
		if err != nil {
			t.Fatalf("PathIn(%q,%q): %v", tc.prefix, tc.ext, err)
		}
		base := filepath.Base(got)
		if !strings.HasPrefix(base, tc.want) {
			t.Errorf("prefix %q ext %q: base %q does not start with %q", tc.prefix, tc.ext, base, tc.want)
		}
		if !strings.HasSuffix(base, tc.suffix) {
			t.Errorf("prefix %q ext %q: base %q does not end with %q", tc.prefix, tc.ext, base, tc.suffix)
		}
	}
}

func TestPathInEmptyDir(t *testing.T) {
	if _, err := PathIn("  ", "x", "jpg"); err == nil {
		t.Fatal("expected error for empty directory")
	}
}

func TestPathWritesNothingToCwd(t *testing.T) {
	cwd := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(old) }()

	t.Setenv(EnvVar, "")
	if _, err := Path("shot", "jpg"); err != nil {
		t.Fatalf("Path: %v", err)
	}

	entries, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("scratch wrote into the working directory: %v", entries)
	}
}
