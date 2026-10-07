// Package scratch resolves throwaway artifact paths for kvm-cli.
//
// Screenshots and other transient files produced by computer-use agents are
// kept deliberately OUT of the model context: an agent passes a path to a tool
// rather than embedding image bytes in a prompt. This package picks the
// directory those artifacts live in — an explicit flag, then $KVM_SCRATCH_DIR,
// then a configured value, then the OS temp directory ($TMPDIR on Unix) — and
// mints unique, timestamped names for them.
package scratch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// EnvVar names the environment variable consulted when no explicit
	// directory is supplied.
	EnvVar = "KVM_SCRATCH_DIR"
	// DefaultPrefix is used when a caller passes an empty prefix.
	DefaultPrefix = "kvm"
	// dirPerm is the permission mode for a freshly created scratch directory.
	dirPerm = 0o700
	// stampLayout is the timestamp format embedded in generated names. It
	// carries millisecond precision so successive captures sort chronologically.
	stampLayout = "20060102-150405.000"
)

// Resolve returns the scratch directory using the precedence
// explicit > $KVM_SCRATCH_DIR > configured > os.TempDir(). The directory is
// created with 0700 permissions when missing. Surrounding whitespace on
// explicit and configured values is ignored; blank values fall through.
func Resolve(explicit, configured string) (string, error) {
	dir := firstNonEmpty(explicit, os.Getenv(EnvVar), configured, os.TempDir())
	return ensureDir(dir)
}

// Dir resolves the ambient scratch directory from $KVM_SCRATCH_DIR, falling
// back to os.TempDir(). It is shorthand for Resolve("", "").
func Dir() (string, error) { return Resolve("", "") }

// Path returns a unique, timestamped path inside the ambient scratch directory
// (see Dir), for example "kvm-screenshot-20261007-043612.123.jpg". The prefix
// is sanitised into a safe filename component and ext is normalised to have a
// leading dot. No file is created; the caller writes to the returned path.
func Path(prefix, ext string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return PathIn(dir, prefix, ext)
}

// PathIn is Path against an already-resolved directory, so callers that honoured
// an explicit --scratch-dir (via Resolve) do not fall back to the ambient one.
func PathIn(dir, prefix, ext string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("scratch: empty directory")
	}
	prefix = sanitizePrefix(prefix)
	ext = normalizeExt(ext)
	for {
		full := filepath.Join(dir, nextName(prefix, ext))
		// Reuse a name only when the path is provably taken. A stat error that
		// is not "file exists" (for example a missing parent) means we cannot
		// prove a collision, so the caller gets the candidate.
		if _, err := os.Stat(full); err != nil {
			return full, nil
		}
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func ensureDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("scratch: could not resolve a scratch directory")
	}
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return "", fmt.Errorf("scratch: create %s: %w", dir, err)
	}
	return dir, nil
}

// nameMu guards the in-process uniqueness state. Because PathIn does not create
// files, an existence check alone cannot distinguish two rapid calls; the
// monotonic sequence guarantees distinct names within the same millisecond.
var (
	nameMu    sync.Mutex
	lastStamp string
	lastSeq   int
)

func nextName(prefix, ext string) string {
	stamp := time.Now().Format(stampLayout)
	nameMu.Lock()
	defer nameMu.Unlock()
	if stamp == lastStamp {
		lastSeq++
	} else {
		lastStamp = stamp
		lastSeq = 0
	}
	if lastSeq == 0 {
		return prefix + "-" + stamp + ext
	}
	return fmt.Sprintf("%s-%s-%d%s", prefix, stamp, lastSeq, ext)
}

// unsafeNameReplacer folds characters that would escape the scratch directory
// (path separators) or are awkward in a filename into underscores.
var unsafeNameReplacer = strings.NewReplacer(
	"/", "_",
	"\\", "_",
	"\x00", "",
)

func sanitizePrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return DefaultPrefix
	}
	prefix = unsafeNameReplacer.Replace(prefix)
	if prefix = strings.Trim(prefix, "._- "); prefix == "" {
		return DefaultPrefix
	}
	return prefix
}

func normalizeExt(ext string) string {
	ext = strings.TrimSpace(ext)
	if ext == "" {
		return ""
	}
	ext = unsafeNameReplacer.Replace(ext)
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return ext
}
