package auth

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/roboalchemist/kvm-cli/pkg/api"
	"github.com/roboalchemist/kvm-cli/pkg/redact"
)

const (
	// defaultTimeout matches the api package default.
	defaultTimeout = 30 * time.Second
	// gopassPrefix is prepended to bare key names when querying gopass.
	gopassPrefix = "env/"
	// gopassTimeout bounds a gopass lookup so a locked store cannot hang the CLI.
	gopassTimeout = 10 * time.Second
)

// Credentials are the resolved connection settings for a KVM device.
type Credentials struct {
	URL      string
	Username string
	Password string
	Timeout  time.Duration
	Insecure bool
}

// interactivePrompt reports whether interactive prompting is allowed. It is a
// variable so tests can disable prompting deterministically.
var interactivePrompt = func() bool { return isTerminal(os.Stdin) }

// Resolve applies the credential priority chain:
//
// 1. explicit CLI flag values (flagURL, flagUser, flagPass)
// 2. environment variables KVM_URL/GLKVM_URL (and _USERNAME/_PASSWORD)
// 3. gopass entries env/GLKVM_URL, env/GLKVM_USERNAME, env/GLKVM_PASSWORD
// 4. ~/.config/kvm-cli/config.json
// 5. an interactive prompt, but only when stdin is a TTY and a value is
// still missing
//
// Values are resolved per field. Missing values are not an error; GetClient
// reports which fields are required. A malformed config file is an error.
func Resolve(flagURL, flagUser, flagPass string) (Credentials, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return Credentials{}, err
	}

	creds := Credentials{
		URL:      resolveField(flagURL, "KVM_URL", "GLKVM_URL", "GLKVM_URL", cfg.URL),
		Username: resolveField(flagUser, "KVM_USERNAME", "GLKVM_USERNAME", "GLKVM_USERNAME", cfg.Username),
		Password: resolveField(flagPass, "KVM_PASSWORD", "GLKVM_PASSWORD", "GLKVM_PASSWORD", cfg.Password),
		Timeout:  resolveTimeout(cfg),
		Insecure: resolveInsecure(cfg),
	}

	if err := promptMissing(&creds); err != nil {
		return Credentials{}, err
	}
	return creds, nil
}

// GetClientFromFlags resolves credentials from flags/env/gopass/config and
// returns an authenticated client.
func GetClientFromFlags(flagURL, flagUser, flagPass string) (*api.Client, error) {
	creds, err := Resolve(flagURL, flagUser, flagPass)
	if err != nil {
		return nil, err
	}
	return GetClient(creds)
}

// GetClient builds an *api.Client from creds and logs in. If any required
// field is missing it returns an error naming the missing field and every way
// to supply it.
//
// TLS: when creds.Insecure is false the client first verifies the certificate.
// If (and only if) that fails with an x509/certificate error, GetClient prints
// a one-line warning and retries with insecure TLS. Set KVM_TLS_STRICT=1 (or
// GLKVM_TLS_STRICT) to make certificate verification mandatory instead.
func GetClient(creds Credentials) (*api.Client, error) {
	url := strings.TrimSpace(creds.URL)
	if url == "" {
		return nil, errors.New("no KVM URL configured: pass --url, set KVM_URL/GLKVM_URL, " +
			"add env/GLKVM_URL to gopass, or run 'kvm-cli config set url <url>'")
	}
	if strings.TrimSpace(creds.Username) == "" {
		return nil, errors.New("no username configured: pass --username, set KVM_USERNAME/GLKVM_USERNAME, " +
			"add env/GLKVM_USERNAME to gopass, or run 'kvm-cli config set username <name>'")
	}
	if creds.Password == "" {
		return nil, errors.New("no password configured: pass --password, set KVM_PASSWORD/GLKVM_PASSWORD, " +
			"add env/GLKVM_PASSWORD to gopass, or run 'kvm-cli config set password <password>'")
	}

	opts := api.Options{Timeout: creds.Timeout, Insecure: creds.Insecure}
	if creds.Insecure {
		return login(url, creds, opts)
	}

	client, err := login(url, creds, opts)
	if err == nil {
		return client, nil
	}
	if !isCertError(err) || tlsStrict() {
		return nil, err
	}

	fmt.Fprintf(os.Stderr, "warning: TLS certificate verification failed for %s (%s); "+
		"retrying with insecure TLS (set KVM_TLS_STRICT=1 to require a valid certificate)\n",
		redact.URL(url), certReason(err))
	opts.Insecure = true
	return login(url, creds, opts)
}

// login constructs a client with the given options and authenticates.
func login(url string, creds Credentials, opts api.Options) (*api.Client, error) {
	client := api.NewClient(url, creds.Username, creds.Password, opts)
	if err := client.Login(); err != nil {
		return nil, fmt.Errorf("login to %s failed: %w", redact.URL(url), err)
	}
	return client, nil
}

// resolveField resolves one credential field through the priority chain.
func resolveField(flag, envPrimary, envAlias, gopassKey, cfgVal string) string {
	if v := strings.TrimSpace(flag); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv(envPrimary)); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv(envAlias)); v != "" {
		return v
	}
	if v := gopassValue(gopassKey); v != "" {
		return v
	}
	return strings.TrimSpace(cfgVal)
}

// resolveTimeout resolves the request timeout: env > gopass > config > default.
func resolveTimeout(cfg *Config) time.Duration {
	if v := firstEnv("KVM_TIMEOUT", "GLKVM_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	if v := gopassValue("GLKVM_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	if cfg != nil && cfg.Timeout != "" {
		if d, err := time.ParseDuration(cfg.Timeout); err == nil {
			return d
		}
	}
	return defaultTimeout
}

// resolveInsecure resolves the insecure-TLS preference: env > gopass > config.
func resolveInsecure(cfg *Config) bool {
	if v := firstEnv("KVM_INSECURE", "GLKVM_INSECURE"); v != "" {
		if b, err := parseBool(v); err == nil {
			return b
		}
	}
	if v := gopassValue("GLKVM_INSECURE"); v != "" {
		if b, err := parseBool(v); err == nil {
			return b
		}
	}
	return cfg != nil && cfg.Insecure
}

// gopassValue returns the value of gopass entry env/<key>. It is silent (empty
// string, no error surface) when gopass is missing, the entry is absent, or the
// lookup times out.
func gopassValue(key string) string {
	if key == "" {
		return ""
	}
	if _, err := exec.LookPath("gopass"); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), gopassTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "gopass", "show", "-o", gopassPrefix+key)
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// firstEnv returns the first non-empty value among keys.
func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// tlsStrict reports whether certificate verification must not be downgraded.
// An unparseable non-empty value is treated as strict (fail safe).
func tlsStrict() bool {
	v := firstEnv("KVM_TLS_STRICT", "GLKVM_TLS_STRICT")
	if v == "" {
		return false
	}
	b, err := parseBool(v)
	if err != nil {
		return true
	}
	return b
}

// isCertError reports whether err is (or wraps) a TLS certificate verification
// failure that warrants the insecure fallback.
func isCertError(err error) bool {
	if err == nil {
		return false
	}
	var unknownAuth x509.UnknownAuthorityError
	if errors.As(err, &unknownAuth) {
		return true
	}
	var certInvalid x509.CertificateInvalidError
	if errors.As(err, &certInvalid) {
		return true
	}
	var hostname x509.HostnameError
	if errors.As(err, &hostname) {
		return true
	}
	var certVerify *tls.CertificateVerificationError
	if errors.As(err, &certVerify) {
		return true
	}
	msg := err.Error()
	for _, needle := range []string{
		"x509:",
		"tls: failed to verify certificate",
		"certificate has expired",
		"certificate is not valid",
		"certificate signed by unknown authority",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// certReason produces a short human-readable reason for a certificate failure.
func certReason(err error) string {
	var certInvalid x509.CertificateInvalidError
	if errors.As(err, &certInvalid) {
		switch certInvalid.Reason {
		case x509.Expired:
			return "certificate has expired or is not yet valid"
		case x509.NameMismatch:
			return "certificate is valid for a different host"
		}
	}
	var unknownAuth x509.UnknownAuthorityError
	if errors.As(err, &unknownAuth) {
		return "certificate signed by unknown authority"
	}
	msg := err.Error()
	if i := strings.Index(msg, "x509:"); i >= 0 {
		reason := msg[i:]
		if nl := strings.IndexByte(reason, '\n'); nl >= 0 {
			reason = reason[:nl]
		}
		return reason
	}
	return "certificate verification error"
}

// promptMissing fills empty fields by prompting, but only when stdin is a TTY.
func promptMissing(creds *Credentials) error {
	if creds.URL != "" && creds.Username != "" && creds.Password != "" {
		return nil
	}
	if !interactivePrompt() {
		return nil
	}

	reader := bufio.NewReader(os.Stdin)
	if creds.URL == "" {
		v, err := promptLine(reader, "KVM URL")
		if err != nil {
			return err
		}
		creds.URL = v
	}
	if creds.Username == "" {
		v, err := promptLine(reader, "Username")
		if err != nil {
			return err
		}
		creds.Username = v
	}
	if creds.Password == "" {
		v, err := promptPassword("Password")
		if err != nil {
			return err
		}
		creds.Password = v
	}
	return nil
}

// promptLine writes label to stderr and reads one line from r.
func promptLine(r *bufio.Reader, label string) (string, error) {
	fmt.Fprintf(os.Stderr, "%s: ", label)
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read %s: %w", strings.ToLower(label), err)
	}
	return strings.TrimSpace(line), nil
}

// promptPassword reads a password without echoing it.
func promptPassword(label string) (string, error) {
	fmt.Fprintf(os.Stderr, "%s: ", label)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", strings.ToLower(label), err)
	}
	return string(b), nil
}

// isTerminal reports whether f is a real TTY. It must not use
// os.ModeCharDevice: /dev/null is a character device too, so that check would
// wrongly classify `kvm-cli info </dev/null` as interactive and send it down
// the password-prompt path (failing with a read error instead of the
// actionable "no username configured"). term.IsTerminal performs the actual
// ioctl, which is false for /dev/null, pipes, and regular files.
func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
