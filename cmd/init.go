package cmd

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/roboalchemist/kvm-cli/pkg/api"
	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// initCmd groups first-run initialization and password management. The device
// exposes /api/init/is_inited, /api/init/init, and /api/init/change_password.
//
// Initializing the device and changing the password are destructive: they
// require --yes, and the passwords involved are never printed.
var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Inspect initialization state and manage the admin password",
	Long: `Inspect the remote KVM's initialization state and manage its admin password.

'status' reports whether the device has been initialized. 'run' performs first
initialization by setting the admin password, and 'change-password' rotates the
existing password.

Both 'run' and 'change-password' are destructive and require --yes. Passwords
are never echoed: prefer the flags or the environment variables, or omit them
to be prompted without echo on a terminal.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli init status
  kvm-cli init run --password 'correct horse battery staple' --yes
  kvm-cli init change-password --yes`,
}

var (
	k10InitRunYes      bool
	k10InitRunPassword string
	k10InitChangeYes   bool
	k10InitOldPassword string
	k10InitNewPassword string
	k10InitUser        string
)

var initStatusCmd = &cobra.Command{
	Use:     "status",
	Aliases: []string{"is-inited"},
	Short:   "Show initialization state (GET /api/init/is_inited)",
	Long:    "Report whether the device has completed first-run initialization, along with its screen and country settings.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli init status\n  kvm-cli init is-inited --json",
	RunE:    runInitStatus,
}

var initRunCmd = &cobra.Command{
	Use:     "run",
	Aliases: []string{"init"},
	Short:   "Initialize the device with an admin password (POST /api/init/init) — DESTRUCTIVE",
	Long: `Perform first-run initialization, setting the device's admin password.

Destructive: requires --yes. The password is never printed. Provide it with
--password or KVM_NEW_PASSWORD, or omit it to be prompted without echo on a
terminal.`,
	Args:    cobra.NoArgs,
	Example: "  kvm-cli init run --password 'correct horse battery staple' --yes\n  kvm-cli init init --yes   # prompt for the password",
	RunE:    runInitRun,
}

var initChangePasswordCmd = &cobra.Command{
	Use:   "change-password",
	Short: "Change the admin password (POST /api/init/change_password) — DESTRUCTIVE",
	Long: `Change the device's admin password.

Destructive: requires --yes. Passwords are never printed. Provide the old and
new passwords with the flags or the KVM_OLD_PASSWORD/KVM_NEW_PASSWORD
environment variables, or omit them to be prompted without echo.`,
	Args:    cobra.NoArgs,
	Example: "  kvm-cli init change-password --yes\n  kvm-cli init change-password --old-password old --new-password new --yes",
	RunE:    runInitChangePassword,
}

func init() {
	vmRegisterConfirm(initRunCmd, &k10InitRunYes, "Confirm initializing the device (required to proceed)")
	initRunCmd.Flags().StringVar(&k10InitRunPassword, "password", "", "Admin password to set (env: KVM_NEW_PASSWORD; prompted if omitted)")

	vmRegisterConfirm(initChangePasswordCmd, &k10InitChangeYes, "Confirm changing the password (required to proceed)")
	initChangePasswordCmd.Flags().StringVar(&k10InitOldPassword, "old-password", "", "Current password (env: KVM_OLD_PASSWORD; prompted if omitted)")
	initChangePasswordCmd.Flags().StringVar(&k10InitNewPassword, "new-password", "", "New password (env: KVM_NEW_PASSWORD; prompted if omitted)")
	initChangePasswordCmd.Flags().StringVar(&k10InitUser, "user", "admin", "Account whose password is changed")

	MarkWrite(initRunCmd)
	MarkWrite(initChangePasswordCmd)

	initCmd.AddCommand(initStatusCmd, initRunCmd, initChangePasswordCmd)
	rootCmd.AddCommand(initCmd)
}

func runInitStatus(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/init/is_inited", &result); err != nil {
		return vmDeviceError(err)
	}

	td := output.TableData{
		Headers: []string{"KEY", "VALUE"},
		Rows:    k10FlattenRows("", result),
	}
	return output.Render(td, result, GetOutputOptions())
}

func runInitRun(cmd *cobra.Command, args []string) error {
	if err := vmRequireYes(k10InitRunYes, "initialize the device (set the admin password)"); err != nil {
		return err
	}

	password, err := k10SecretValue(k10InitRunPassword, "KVM_NEW_PASSWORD", "New admin password: ")
	if err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	values := url.Values{"password": {password}}
	var result map[string]any
	// vmRawPost is used (rather than the JSON client) because the device reads
	// this endpoint's parameters from the query string. It redacts the URL and
	// any transport error through pkg/redact, so the password in the query
	// string never reaches the debug log or an error message.
	if err := vmRawPost(client, "/api/init/init?"+values.Encode(), "", nil, 0, &result); err != nil {
		return vmDeviceError(err)
	}
	return vmRenderAction("initialized the device", k10MaskMap(result))
}

func runInitChangePassword(cmd *cobra.Command, args []string) error {
	user := strings.TrimSpace(k10InitUser)
	if user == "" {
		return output.NewCodedError("USAGE", "a --user is required")
	}
	if err := vmRequireYes(k10InitChangeYes, fmt.Sprintf("change the password for %q", user)); err != nil {
		return err
	}

	oldPassword, err := k10SecretValue(k10InitOldPassword, "KVM_OLD_PASSWORD", "Current password: ")
	if err != nil {
		return err
	}
	newPassword, err := k10SecretValue(k10InitNewPassword, "KVM_NEW_PASSWORD", "New password: ")
	if err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	values := url.Values{
		"user":         {user},
		"old_password": {oldPassword},
		"new_password": {newPassword},
	}
	var result map[string]any
	// See runInitRun: the device reads these parameters from the query string,
	// and vmRawPost redacts the URL in --debug output and transport errors.
	if err := vmRawPost(client, "/api/init/change_password?"+values.Encode(), "", nil, 0, &result); err != nil {
		return vmDeviceError(err)
	}
	return vmRenderAction("changed the password for "+user, k10MaskMap(result))
}

// ---- shared helpers (cmd package) --------------------------------
//
// These helpers are prefixed with "k10" so they do not collide with helpers
// introduced by sibling tickets that share this package.

// k10Unsupported converts an HTTP 404 or device "BadRequestError" from a
// feature endpoint into a clear NOT_SUPPORTED error, so callers never surface
// a raw "404: Not Found". Any other error is passed through vmDeviceError.
func k10Unsupported(err error, feature, endpoint string) error {
	if err == nil {
		return nil
	}
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && (apiErr.IsNotFound() || apiErr.Code == "BadRequestError") {
		return output.NewCodedError("NOT_SUPPORTED",
			fmt.Sprintf("%s is not available on this device (%s)", feature, endpoint))
	}
	return vmDeviceError(err)
}

// k10FlattenRows flattens a JSON object into sorted dotted-key/value rows and
// collapses embedded newlines so multi-line values render on a single line in
// table mode.
func k10FlattenRows(prefix string, m map[string]any) [][]string {
	rows := vmFlattenRows(prefix, m)
	for _, row := range rows {
		for i := range row {
			row[i] = strings.ReplaceAll(strings.ReplaceAll(row[i], "\r\n", " "), "\n", " ")
		}
	}
	return rows
}

// k10MaskMap returns a copy of m with secret-looking values redacted.
func k10MaskMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	masked, _ := k10MaskSecrets(m).(map[string]any)
	return masked
}

// k10MaskSecrets recursively redacts values whose keys look secret. It handles
// maps, api.RawMap and arrays, returning the same structural shape.
func k10MaskSecrets(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if k10IsSecretKey(k) {
				out[k] = "***"
				continue
			}
			out[k] = k10MaskSecrets(val)
		}
		return out
	case api.RawMap:
		return k10MaskSecrets(map[string]any(t))
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = k10MaskSecrets(item)
		}
		return out
	default:
		return v
	}
}

// k10SecretKeyParts are substrings that mark a JSON key as secret material.
var k10SecretKeyParts = []string{
	"secret", "password", "passwd", "token", "credential",
	"otpauth", "private", "seed", "otp", "qrcode", "qr_code",
}

// k10IsSecretKey reports whether a JSON key names secret material.
func k10IsSecretKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	if k == "key" || k == "uri" {
		return true
	}
	for _, part := range k10SecretKeyParts {
		if strings.Contains(k, part) {
			return true
		}
	}
	return false
}

// k10SecretValue resolves a secret from an explicit flag, then an environment
// variable, then an interactive no-echo prompt. It never echoes the value.
func k10SecretValue(flagVal, envKey, prompt string) (string, error) {
	if v := strings.TrimSpace(flagVal); v != "" {
		return v, nil
	}
	if envKey != "" {
		if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
			return v, nil
		}
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", output.WrapCodedError("ERROR", err, fmt.Sprintf("read secret: %v", err))
		}
		if v := strings.TrimSpace(string(b)); v != "" {
			return v, nil
		}
	}
	hint := "the flag"
	if envKey != "" {
		hint = "the flag or " + envKey
	}
	return "", output.NewCodedError("USAGE", fmt.Sprintf("no value provided; supply it with %s or an interactive terminal", hint))
}
