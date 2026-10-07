package cmd

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"

	"github.com/roboalchemist/kvm-cli/pkg/api"
	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/spf13/cobra"
)

// twofaCmd groups two-factor authentication (TOTP) management. The API is
// exposed under /api/2fa; "2fa" is registered as an alias.
//
// Secret material (the shared TOTP secret and its otpauth URI) is always
// redacted from command output, including for 'show' and 'create'. Enabling,
// initializing, and disabling 2FA are destructive and require --yes.
var twofaCmd = &cobra.Command{
	Use:     "twofa",
	Aliases: []string{"2fa"},
	Short:   "Manage two-factor authentication (TOTP)",
	Long: `Manage the remote KVM's two-factor authentication.

'is-enabled' reports whether TOTP is active and 'show' reports the current 2FA
document. 'create' generates a new TOTP secret, 'init' confirms it with a
6-digit code from your authenticator, and 'delete' disables 2FA.

For safety, secret material (the shared secret and the otpauth URI) is always
redacted from output. create, init, and delete are destructive and require
--yes.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli 2fa is-enabled
  kvm-cli 2fa show --json
  kvm-cli 2fa create --yes
  kvm-cli 2fa init --secret JBSWY3DPEHPK3PXP --code 123456 --yes
  kvm-cli 2fa delete --yes`,
}

var (
	k10TwofaCreateYes  bool
	k10TwofaInitYes    bool
	k10TwofaDeleteYes  bool
	k10TwofaInitSecret string
	k10TwofaInitCode   string
)

var twofaIsEnabledCmd = &cobra.Command{
	Use:     "is-enabled",
	Short:   "Report whether 2FA is enabled (GET /api/2fa/is_enabled)",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli 2fa is-enabled\n  kvm-cli 2fa is-enabled --json",
	RunE:    runTwofaIsEnabled,
}

var twofaShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show the 2FA document (GET /api/2fa/show)",
	Long: `Show the device's current two-factor authentication document.

Secret material (the shared secret and otpauth URI) is redacted. When 2FA is
not enabled the device returns "not found"; this command reports that state
clearly instead of a raw error.`,
	Args:    cobra.NoArgs,
	Example: "  kvm-cli 2fa show\n  kvm-cli 2fa show --json",
	RunE:    runTwofaShow,
}

var twofaCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Generate a new TOTP secret (POST /api/2fa/create) — DESTRUCTIVE",
	Long: `Generate a new TOTP secret on the device.

Destructive: requires --yes. The returned secret and otpauth URI are redacted
from output for safety; read them directly on a trusted device if needed.`,
	Args:    cobra.NoArgs,
	Example: "  kvm-cli 2fa create --yes",
	RunE:    runTwofaCreate,
}

var twofaInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Activate 2FA with a code (POST /api/2fa/init) — DESTRUCTIVE",
	Long: `Activate two-factor authentication by confirming a generated secret with a
6-digit TOTP code from your authenticator.

Destructive: requires --yes. The secret is never printed. Provide it with
--secret (or KVM_2FA_SECRET); when omitted and stdin is a terminal you are
prompted without echo. The --code is the current 6-digit authenticator code.`,
	Args:    cobra.NoArgs,
	Example: "  kvm-cli 2fa init --secret JBSWY3DPEHPK3PXP --code 123456 --yes",
	RunE:    runTwofaInit,
}

var twofaDeleteCmd = &cobra.Command{
	Use:     "delete",
	Short:   "Disable 2FA (POST /api/2fa/delete) — DESTRUCTIVE",
	Long:    "Disable two-factor authentication and discard the stored secret. Destructive: requires --yes.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli 2fa delete --yes\n  kvm-cli 2fa delete          # dry run",
	RunE:    runTwofaDelete,
}

func init() {
	vmRegisterConfirm(twofaCreateCmd, &k10TwofaCreateYes, "Confirm generating a new TOTP secret (required to proceed)")
	vmRegisterConfirm(twofaInitCmd, &k10TwofaInitYes, "Confirm activating 2FA (required to proceed)")
	twofaInitCmd.Flags().StringVar(&k10TwofaInitSecret, "secret", "", "TOTP shared secret (env: KVM_2FA_SECRET; prompted if omitted)")
	twofaInitCmd.Flags().StringVar(&k10TwofaInitCode, "code", "", "Current 6-digit authenticator code")
	vmRegisterConfirm(twofaDeleteCmd, &k10TwofaDeleteYes, "Confirm disabling 2FA (required to proceed)")

	MarkWrite(twofaCreateCmd)
	MarkWrite(twofaInitCmd)
	MarkWrite(twofaDeleteCmd)

	twofaCmd.AddCommand(
		twofaIsEnabledCmd,
		twofaShowCmd,
		twofaCreateCmd,
		twofaInitCmd,
		twofaDeleteCmd,
	)
	rootCmd.AddCommand(twofaCmd)
}

func runTwofaIsEnabled(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/2fa/is_enabled", &result); err != nil {
		return vmDeviceError(err)
	}

	td := output.TableData{
		Headers: []string{"KEY", "VALUE"},
		Rows:    k10FlattenRows("", result),
	}
	return output.Render(td, result, GetOutputOptions())
}

func runTwofaShow(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/2fa/show", &result); err != nil {
		var apiErr *api.APIError
		if errors.As(err, &apiErr) && (apiErr.IsNotFound() || apiErr.Code == "NotFoundError") {
			return output.NewCodedError("NOT_ENABLED", "two-factor authentication is not enabled on this device")
		}
		return vmDeviceError(err)
	}

	// Never emit secret material, in any output mode.
	result = k10MaskMap(result)
	td := output.TableData{
		Headers: []string{"KEY", "VALUE"},
		Rows:    k10FlattenRows("", result),
	}
	return output.Render(td, result, GetOutputOptions())
}

func runTwofaCreate(cmd *cobra.Command, args []string) error {
	if err := vmRequireYes(k10TwofaCreateYes, "generate a new TOTP secret"); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/2fa/create", nil, &result); err != nil {
		return vmDeviceError(err)
	}

	result = k10MaskMap(result)
	td := output.TableData{
		Headers: []string{"KEY", "VALUE"},
		Rows:    k10FlattenRows("", result),
	}
	return output.Render(td, result, GetOutputOptions())
}

func runTwofaInit(cmd *cobra.Command, args []string) error {
	if err := vmRequireYes(k10TwofaInitYes, "activate two-factor authentication"); err != nil {
		return err
	}

	secret, err := k10SecretValue(k10TwofaInitSecret, "KVM_2FA_SECRET", "TOTP secret: ")
	if err != nil {
		return err
	}

	code := k10TwofaInitCode
	if code == "" {
		return output.NewCodedError("USAGE", "a 6-digit authenticator code is required (--code)")
	}
	if !k10SixDigits.MatchString(code) {
		return output.NewCodedError("USAGE", fmt.Sprintf("invalid --code %q: expected 6 digits", code))
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	values := url.Values{"secret": {secret}, "key": {code}}
	var result map[string]any
	// vmRawPost is used because the device reads this endpoint's parameters
	// from the query string; it redacts the URL and any transport error, so
	// the TOTP secret never reaches the debug log or an error message.
	if err := vmRawPost(client, "/api/2fa/init?"+values.Encode(), "", nil, 0, &result); err != nil {
		return vmDeviceError(err)
	}
	// The action label intentionally omits the secret.
	return vmRenderAction("activated two-factor authentication", k10MaskMap(result))
}

func runTwofaDelete(cmd *cobra.Command, args []string) error {
	if err := vmRequireYes(k10TwofaDeleteYes, "disable two-factor authentication"); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/2fa/delete", nil, &result); err != nil {
		return vmDeviceError(err)
	}
	return vmRenderAction("disabled two-factor authentication", k10MaskMap(result))
}

var k10SixDigits = regexp.MustCompile(`^\d{6}$`)
