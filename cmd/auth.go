package cmd

import (
	"os"

	"github.com/roboalchemist/kvm-cli/pkg/auth"
	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/roboalchemist/kvm-cli/pkg/redact"
	"github.com/spf13/cobra"
)

// authCmd groups session lifecycle commands. None of these ever prints the
// password or the raw session token.
var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Authenticate with the KVM device",
	Long: `Manage the authenticated session with the KVM device.

These commands never print the password or the raw token; they report only
whether credentials are configured and whether the session works.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli auth login
  kvm-cli auth check
  kvm-cli auth status
  kvm-cli auth logout`,
}

var authLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Force a fresh login and report token status",
	Long: `Authenticate against the device with the resolved credentials and report a
safe token summary. The raw token is never printed.`,
	Args:    cobra.NoArgs,
	Example: "  kvm-cli auth login\n  kvm-cli auth login --json",
	RunE:    runAuthLogin,
}

var authLogoutCmd = &cobra.Command{
	Use:     "logout",
	Short:   "Terminate the device session",
	Long:    "Terminate the device session (POST /api/auth/logout) and clear any locally persisted session.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli auth logout\n  kvm-cli auth logout --json",
	RunE:    runAuthLogout,
}

var authCheckCmd = &cobra.Command{
	Use:     "check",
	Short:   "Verify the current session token (GET /api/auth/check)",
	Long:    "Log in and verify the resulting token against /api/auth/check. Exits non-zero if the session is rejected.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli auth check\n  kvm-cli auth check --json",
	RunE:    runAuthCheck,
}

var authStatusCmd = &cobra.Command{
	Use:     "status",
	Short:   "Show resolved connection settings and whether a session works",
	Long:    "Show the resolved URL and username, whether a password is configured, and whether a session can be established. The password is never printed.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli auth status\n  kvm-cli auth status --json",
	RunE:    runAuthStatus,
}

func init() {
	// login establishes a device session; logout terminates it and clears any
	// locally persisted session.
	MarkWrite(authLoginCmd)
	MarkWrite(authLogoutCmd)

	authCmd.AddCommand(authLoginCmd, authLogoutCmd, authCheckCmd, authStatusCmd)
	rootCmd.AddCommand(authCmd)
}

func runAuthLogin(cmd *cobra.Command, args []string) error {
	creds, err := resolveAuthCreds(cmd)
	if err != nil {
		return err
	}
	client, err := getAuthClientFromCreds(creds)
	if err != nil {
		return cliDeviceError(err)
	}
	if flagDebug {
		client.SetDebug(DebugLog)
	}

	tokenPresent := client.Token() != ""
	tokenWord := "absent"
	if tokenPresent {
		tokenWord = "present (hidden)"
	}

	// creds.URL may carry userinfo (https://user:pass@host); mask it before it
	// reaches either output mode.
	safeURL := redact.URL(creds.URL)
	data := map[string]any{
		"status":        "logged_in",
		"url":           safeURL,
		"username":      creds.Username,
		"authenticated": true,
		"token_present": tokenPresent,
	}
	td := output.TableData{
		Headers: []string{"STATUS", "URL", "USERNAME", "TOKEN"},
		Rows:    [][]string{{"logged_in", safeURL, creds.Username, tokenWord}},
	}
	return output.Render(td, data, GetOutputOptions())
}

func runAuthLogout(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return cliDeviceError(err)
	}
	if err := client.Logout(); err != nil {
		return cliDeviceError(err)
	}
	clearSession()

	baseURL := redact.URL(client.BaseURL())
	data := map[string]any{"status": "logged_out", "url": baseURL}
	td := output.TableData{
		Headers: []string{"STATUS", "URL"},
		Rows:    [][]string{{"logged_out", baseURL}},
	}
	return output.Render(td, data, GetOutputOptions())
}

func runAuthCheck(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return cliDeviceError(err)
	}
	if err := client.Check(); err != nil {
		return cliDeviceError(err)
	}

	baseURL := redact.URL(client.BaseURL())
	data := map[string]any{"authenticated": true, "url": baseURL}
	td := output.TableData{
		Headers: []string{"STATUS", "URL", "AUTHENTICATED"},
		Rows:    [][]string{{"ok", baseURL, "yes"}},
	}
	return output.Render(td, data, GetOutputOptions())
}

func runAuthStatus(cmd *cobra.Command, args []string) error {
	creds, err := resolveAuthCredsNoPrompt(cmd)
	if err != nil {
		return err
	}

	// creds.URL may carry userinfo (https://user:pass@host); mask it once and
	// reuse it for every rendered field below.
	safeURL := redact.URL(creds.URL)

	password := "not set"
	if creds.Password != "" {
		password = "set"
	}

	authenticated := false
	detail := "not attempted"
	switch {
	case creds.URL == "":
		detail = "no URL configured"
	case creds.Username == "":
		detail = "no username configured"
	case creds.Password == "":
		detail = "no password configured"
	default:
		client, err := getAuthClientFromCreds(creds)
		switch {
		case err != nil:
			detail = redact.URL(err.Error())
		default:
			if cerr := client.Check(); cerr != nil {
				detail = redact.URL(cerr.Error())
			} else {
				authenticated = true
				detail = "session ok"
			}
		}
	}

	data := map[string]any{
		"url":           safeURL,
		"username":      creds.Username,
		"password":      password,
		"authenticated": authenticated,
		"detail":        detail,
	}
	authStatus := "fail"
	if authenticated {
		authStatus = "ok"
	}
	td := output.TableData{
		Headers: []string{"STATUS", "URL", "USERNAME", "PASSWORD", "AUTHENTICATED", "DETAIL"},
		Rows:    [][]string{{authStatus, safeURL, creds.Username, password, boolWord(authenticated), detail}},
	}
	return output.Render(td, data, GetOutputOptions())
}

// resolveAuthCreds resolves the credential chain without building a client, so
// commands can report the URL/username even when authentication is impossible.
// Missing fields are filled from an interactive prompt when stdin is a TTY.
func resolveAuthCreds(cmd *cobra.Command) (auth.Credentials, error) {
	url, username, password := AuthFlagValues(cmd)
	return auth.Resolve(url, username, password)
}

// resolveAuthCredsNoPrompt resolves the credential chain without ever
// prompting. It is used by read-only commands (auth status) that should report
// missing credentials rather than block on terminal input.
//
// pkg/auth enables prompting based on whether os.Stdin is a TTY, so pointing
// stdin at a pipe for the duration of the call suppresses the prompt without
// duplicating the flag/env/gopass/config priority chain. (A pipe, unlike
// /dev/null, is not a character device, so it is not mistaken for a TTY.)
func resolveAuthCredsNoPrompt(cmd *cobra.Command) (auth.Credentials, error) {
	origStdin := os.Stdin
	if r, w, err := os.Pipe(); err == nil {
		os.Stdin = r
		defer func() {
			os.Stdin = origStdin
			_ = r.Close()
			_ = w.Close()
		}()
	}

	url, username, password := AuthFlagValues(cmd)
	return auth.Resolve(url, username, password)
}

// clearSession best-effort removes any locally persisted session token.
func clearSession() {
	if path, err := auth.SessionPath(); err == nil {
		_ = os.Remove(path)
	}
}

func boolWord(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
