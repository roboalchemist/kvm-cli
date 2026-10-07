package cmd

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/spf13/cobra"
)

// repeaterCmd groups the Wi-Fi repeater (station-mode) operations. Disconnecting
// from the current AP and forgetting a saved AP are destructive and require
// --yes.

const rpFeature = "Wi-Fi repeater"

var rpYes bool

var repeaterCmd = &cobra.Command{
	Use:   "repeater",
	Short: "Manage the Wi-Fi repeater (station mode)",
	Long: `Manage the device's Wi-Fi repeater/station mode.

Subcommands mirror /api/repeater/*: scan for access points, connect to one,
disconnect, enable/disable the repeater, show the current connection, and list
or remove saved networks. Disconnecting and removing a saved network require
--yes. Firmware without Wi-Fi (or with the radio down) returns a clear error
rather than crashing.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli repeater scan
  kvm-cli repeater connect MyNetwork --key secret
  kvm-cli repeater status
  kvm-cli repeater disconnect --yes`,
}

var repeaterScanCmd = &cobra.Command{
	Use:     "scan",
	Short:   "Scan for access points (POST /api/repeater/scan)",
	Long:    "Scan the local area for Wi-Fi access points and saved networks. The scan can take up to 30 seconds.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli repeater scan\n  kvm-cli repeater scan --json",
	RunE:    runRepeaterScan,
}

var rpConnectKey string
var rpConnectIdentity string
var rpConnectManual bool

var repeaterConnectCmd = &cobra.Command{
	Use:   "connect SSID",
	Short: "Connect to a Wi-Fi network (POST /api/repeater/connect)",
	Long: `Connect the repeater to the access point named <ssid>.

Supply the passphrase with --key. For enterprise networks, --identity selects
the EAP identity. Use --manual to provide parameters explicitly.`,
	Args: cobra.ExactArgs(1),
	Example: `  kvm-cli repeater connect MyNetwork --key secret
  kvm-cli repeater connect CorpWiFi --key secret --identity user@example.com
  kvm-cli repeater connect OpenNetwork`,
	RunE: runRepeaterConnect,
}

var repeaterDisconnectCmd = &cobra.Command{
	Use:   "disconnect",
	Short: "Disconnect from the current AP (POST /api/repeater/disconnect) — DESTRUCTIVE",
	Long:  "Disconnect the repeater from its current access point. Requires --yes.",
	Args:  cobra.NoArgs,
	Example: `  kvm-cli repeater disconnect --yes
  kvm-cli repeater disconnect          # dry run`,
	RunE: runRepeaterDisconnect,
}

var repeaterEnableCmd = &cobra.Command{
	Use:     "enable TRUE|FALSE",
	Short:   "Enable or disable the repeater (POST /api/repeater/enable)",
	Long:    "Turn the repeater/station mode on or off.",
	Args:    cobra.ExactArgs(1),
	Example: "  kvm-cli repeater enable true\n  kvm-cli repeater enable false",
	RunE:    runRepeaterEnable,
}

var repeaterStatusCmd = &cobra.Command{
	Use:     "status",
	Short:   "Show the current repeater connection (GET /api/repeater/get_status)",
	Long:    "Show the access point the repeater is currently connected to, if any.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli repeater status\n  kvm-cli repeater status --json",
	RunE:    runRepeaterStatus,
}

var repeaterSavedCmd = &cobra.Command{
	Use:     "saved",
	Short:   "List saved Wi-Fi networks (GET /api/repeater/get_saved_ap_list)",
	Long:    "List the access points the device has saved for reconnection.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli repeater saved\n  kvm-cli repeater saved --json",
	RunE:    runRepeaterSaved,
}

var repeaterRemoveSavedCmd = &cobra.Command{
	Use:   "remove-saved SSID",
	Short: "Forget a saved Wi-Fi network (POST /api/repeater/remove_saved_ap) — DESTRUCTIVE",
	Long:  "Remove a saved access point so the device no longer reconnects to it. Requires --yes.",
	Args:  cobra.ExactArgs(1),
	Example: `  kvm-cli repeater remove-saved MyNetwork --yes
  kvm-cli repeater remove-saved MyNetwork          # dry run`,
	RunE: runRepeaterRemoveSaved,
}

func init() {
	repeaterConnectCmd.Flags().StringVar(&rpConnectKey, "key", "", "Wi-Fi passphrase")
	repeaterConnectCmd.Flags().StringVar(&rpConnectIdentity, "identity", "", "EAP identity for enterprise networks")
	repeaterConnectCmd.Flags().BoolVar(&rpConnectManual, "manual", false, "Provide connection parameters manually")

	vmRegisterConfirm(repeaterDisconnectCmd, &rpYes, "Confirm disconnecting (required to proceed)")
	vmRegisterConfirm(repeaterRemoveSavedCmd, &rpYes, "Confirm removing the saved network (required to proceed)")

	for _, c := range []*cobra.Command{
		repeaterConnectCmd,
		repeaterDisconnectCmd,
		repeaterEnableCmd,
		repeaterRemoveSavedCmd,
	} {
		MarkWrite(c)
	}

	repeaterCmd.AddCommand(
		repeaterScanCmd,
		repeaterConnectCmd,
		repeaterDisconnectCmd,
		repeaterEnableCmd,
		repeaterStatusCmd,
		repeaterSavedCmd,
		repeaterRemoveSavedCmd,
	)
	rootCmd.AddCommand(repeaterCmd)
}

func runRepeaterScan(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/repeater/scan", nil, &result); err != nil {
		return vmHandleUnsupported(err, rpFeature, "/api/repeater/scan")
	}
	return syswRenderKV(result)
}

func runRepeaterConnect(cmd *cobra.Command, args []string) error {
	ssid := strings.TrimSpace(args[0])
	if ssid == "" {
		return output.NewCodedError("USAGE", "ssid must not be empty")
	}

	body := map[string]any{"ssid": ssid}
	if cmd.Flags().Changed("key") {
		body["key"] = rpConnectKey
	}
	if cmd.Flags().Changed("identity") {
		body["identity"] = rpConnectIdentity
	}
	if rpConnectManual {
		body["manual"] = true
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/repeater/connect", body, &result); err != nil {
		return vmHandleUnsupported(err, rpFeature, "/api/repeater/connect")
	}
	if status, _ := result["result"].(string); strings.EqualFold(status, "failed") {
		return output.NewCodedError("DEVICE_ERROR", fmt.Sprintf("failed to connect to %q", ssid))
	}
	return vmRenderAction("connecting to "+ssid, result)
}

func runRepeaterDisconnect(cmd *cobra.Command, args []string) error {
	if err := vmRequireYes(rpYes, "disconnect the Wi-Fi repeater"); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/repeater/disconnect", nil, &result); err != nil {
		return vmHandleUnsupported(err, rpFeature, "/api/repeater/disconnect")
	}
	return vmRenderAction("disconnected the repeater", result)
}

func runRepeaterEnable(cmd *cobra.Command, args []string) error {
	enabled, err := strconv.ParseBool(strings.TrimSpace(args[0]))
	if err != nil {
		return output.NewCodedError("USAGE", fmt.Sprintf("invalid value %q (expected true or false)", args[0]))
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	values := url.Values{"enable": {strconv.FormatBool(enabled)}}
	var result map[string]any
	if err := client.Post("/api/repeater/enable?"+values.Encode(), nil, &result); err != nil {
		return vmHandleUnsupported(err, rpFeature, "/api/repeater/enable")
	}
	return vmRenderAction(fmt.Sprintf("set repeater enable=%t", enabled), result)
}

func runRepeaterStatus(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/repeater/get_status", &result); err != nil {
		return vmHandleUnsupported(err, rpFeature, "/api/repeater/get_status")
	}
	return syswRenderKV(result)
}

func runRepeaterSaved(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/repeater/get_saved_ap_list", &result); err != nil {
		return vmHandleUnsupported(err, rpFeature, "/api/repeater/get_saved_ap_list")
	}
	return syswRenderKV(result)
}

func runRepeaterRemoveSaved(cmd *cobra.Command, args []string) error {
	ssid := strings.TrimSpace(args[0])
	if err := vmRequireYes(rpYes, fmt.Sprintf("remove saved network %q", ssid)); err != nil {
		return err
	}
	if ssid == "" {
		return output.NewCodedError("USAGE", "ssid must not be empty")
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	body := map[string]any{"ssid": ssid}
	var result map[string]any
	if err := client.Post("/api/repeater/remove_saved_ap", body, &result); err != nil {
		return vmHandleUnsupported(err, rpFeature, "/api/repeater/remove_saved_ap")
	}
	return vmRenderAction("removed saved network "+ssid, result)
}
