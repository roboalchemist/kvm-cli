package cmd

import (
	"net/url"
	"strings"

	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/roboalchemist/kvm-cli/pkg/redact"
	"github.com/spf13/cobra"
)

// modemCmd groups the cellular modem controls exposed under /api/modem/*. On
// devices without a modem, or with no SIM present, the endpoints return a clear
// error which the commands surface without panicking.

const mdFeature = "Cellular modem"

var mdSimSet []string

var modemCmd = &cobra.Command{
	Use:   "modem",
	Short: "Manage the cellular modem",
	Long: `Manage the device's cellular modem.

Subcommands mirror /api/modem/*: run a raw AT command, submit a SIM PIN, and
read or write the SIM settings. When no modem/SIM is present the device returns
a descriptive error which is reported as-is.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli modem at "AT+CGMI"
  kvm-cli modem sim-setting
  kvm-cli modem input-pin 1234`,
}

var modemATCmd = &cobra.Command{
	Use:   "at COMMAND",
	Short: "Run a raw AT command (POST /api/modem/at)",
	Long: `Send an AT command to the modem and print its response.

Quote the command so its arguments are kept together.`,
	Args: cobra.ExactArgs(1),
	Example: `  kvm-cli modem at "AT+CGMI"
  kvm-cli modem at "AT+CSQ" --json`,
	RunE: runModemAT,
}

var modemInputPINCmd = &cobra.Command{
	Use:     "input-pin PIN",
	Short:   "Submit a SIM PIN (POST /api/modem/input_pin_code)",
	Long:    "Submit the SIM PIN so the modem can unlock the SIM card.",
	Args:    cobra.ExactArgs(1),
	Example: "  kvm-cli modem input-pin 1234",
	RunE:    runModemInputPIN,
}

var modemSimSettingCmd = &cobra.Command{
	Use:   "sim-setting",
	Short: "Read or write SIM settings (GET/POST /api/modem/sim_setting)",
	Long: `Read the modem's SIM settings (GET). Supplying --set key=value writes
settings via query parameters instead (POST).`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli modem sim-setting
  kvm-cli modem sim-setting --set apn=internet
  cat sim.json | kvm-cli modem sim-setting --json`,
	RunE: runModemSimSetting,
}

func init() {
	modemSimSettingCmd.Flags().StringArrayVar(&mdSimSet, "set", nil, "key=value setting to write (repeatable)")

	// input-pin's positional is the SIM PIN; mask it in the --dry-run preview.
	MarkSecretArgs(modemInputPINCmd, 0)

	// at can mutate modem state; input-pin unlocks the SIM; sim-setting writes
	// when --set is supplied.
	MarkWrite(modemATCmd)
	MarkWrite(modemInputPINCmd)
	MarkWrite(modemSimSettingCmd)

	modemCmd.AddCommand(modemATCmd, modemInputPINCmd, modemSimSettingCmd)
	rootCmd.AddCommand(modemCmd)
}

func runModemAT(cmd *cobra.Command, args []string) error {
	command := strings.TrimSpace(args[0])
	if command == "" {
		return output.NewCodedError("USAGE", "AT command must not be empty")
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	values := url.Values{"AT": {command}}
	var result map[string]any
	if err := client.Post("/api/modem/at?"+values.Encode(), nil, &result); err != nil {
		return vmHandleUnsupported(err, mdFeature, "/api/modem/at")
	}
	return syswRenderKV(result)
}

func runModemInputPIN(cmd *cobra.Command, args []string) error {
	pin := strings.TrimSpace(args[0])
	if pin == "" {
		return output.NewCodedError("USAGE", "PIN must not be empty")
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	// The PIN is submitted as a form-encoded POST body rather than in the query
	// string, so it never appears in the request URL and therefore never in
	// --debug logs or transport-error messages.
	form := url.Values{"pin": {pin}}
	var result map[string]any
	if err := client.PostForm("/api/modem/input_pin_code", form, &result); err != nil {
		return vmHandleUnsupported(err, mdFeature, "/api/modem/input_pin_code")
	}
	return vmRenderAction("submitted SIM PIN", redact.Map(result))
}

func runModemSimSetting(cmd *cobra.Command, args []string) error {
	values := url.Values{}
	if err := syswMergeSets(values, mdSimSet); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	if len(values) == 0 {
		var result map[string]any
		if err := client.Get("/api/modem/sim_setting", &result); err != nil {
			return vmHandleUnsupported(err, mdFeature, "/api/modem/sim_setting")
		}
		return syswRenderKV(result)
	}

	var result map[string]any
	if err := client.Post("/api/modem/sim_setting?"+values.Encode(), nil, &result); err != nil {
		return vmHandleUnsupported(err, mdFeature, "/api/modem/sim_setting")
	}
	// --set may carry secrets (for example password=); mask them from the
	// rendered action and any echoed result.
	return vmRenderAction("set SIM settings ("+redact.Params(values.Encode())+")", redact.Map(result))
}
