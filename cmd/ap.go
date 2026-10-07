package cmd

import (
	"net/url"
	"strconv"

	"github.com/roboalchemist/kvm-cli/pkg/redact"
	"github.com/spf13/cobra"
)

// apCmd groups the device's own Wi-Fi access point (hotspot) operations. These
// are configured via /api/ap/*. Firmware without Wi-Fi returns a clear error.

const apFeature = "Hotspot AP"

var apSet []string
var apEnableFlag bool

var apCmd = &cobra.Command{
	Use:   "ap",
	Short: "Manage the device's Wi-Fi hotspot (access point mode)",
	Long: `Manage the device's built-in Wi-Fi access point.

Subcommands mirror /api/ap/*: inspect the hotspot status, enable/configure it,
re-open the last-used configuration, or close every AP mode.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli ap status
  kvm-cli ap enable --enable=false
  kvm-cli ap close-all`,
}

var apStatusCmd = &cobra.Command{
	Use:     "status",
	Short:   "Show hotspot status (GET /api/ap/status)",
	Long:    "Show the device's Wi-Fi hotspot configuration and state.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli ap status\n  kvm-cli ap status --json",
	RunE:    runAPStatus,
}

var apEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Enable or configure the hotspot (POST /api/ap/enable)",
	Long: `Turn the hotspot on (or off) and optionally supply extra configuration
via --set key=value. Parameters are sent as query parameters.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli ap enable
  kvm-cli ap enable --enable=false
  kvm-cli ap enable --set ssid=my-hotspot --set key=secret`,
	RunE: runAPEnable,
}

var apOpenLastCmd = &cobra.Command{
	Use:     "open-last",
	Short:   "Re-open the last AP mode (POST /api/ap/open_last_mode)",
	Long:    "Re-open the hotspot using the last-used configuration.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli ap open-last",
	RunE:    runAPOpenLast,
}

var apCloseAllCmd = &cobra.Command{
	Use:     "close-all",
	Short:   "Close all AP modes (POST /api/ap/close_all_mode)",
	Long:    "Disable every active access-point mode on the device.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli ap close-all",
	RunE:    runAPCloseAll,
}

func init() {
	apEnableCmd.Flags().BoolVar(&apEnableFlag, "enable", true, "Enable (true) or disable (false) the hotspot")
	apEnableCmd.Flags().StringArrayVar(&apSet, "set", nil, "extra key=value parameter (repeatable)")

	MarkWrite(apEnableCmd)
	MarkWrite(apOpenLastCmd)
	MarkWrite(apCloseAllCmd)

	apCmd.AddCommand(apStatusCmd, apEnableCmd, apOpenLastCmd, apCloseAllCmd)
	rootCmd.AddCommand(apCmd)
}

func runAPStatus(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/ap/status", &result); err != nil {
		return vmHandleUnsupported(err, apFeature, "/api/ap/status")
	}
	return syswRenderKV(result)
}

func runAPEnable(cmd *cobra.Command, args []string) error {
	values := url.Values{"enable": {strconv.FormatBool(apEnableFlag)}}
	if err := syswMergeSets(values, apSet); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/ap/enable?"+values.Encode(), nil, &result); err != nil {
		return vmHandleUnsupported(err, apFeature, "/api/ap/enable")
	}
	// The query string may contain the Wi-Fi passphrase (key=), so mask secret
	// parameters before it reaches stdout/JSON and mask any echoed result.
	return vmRenderAction("set hotspot ("+redact.Params(values.Encode())+")", redact.Map(result))
}

func runAPOpenLast(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/ap/open_last_mode", nil, &result); err != nil {
		return vmHandleUnsupported(err, apFeature, "/api/ap/open_last_mode")
	}
	return vmRenderAction("opened last AP mode", result)
}

func runAPCloseAll(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/ap/close_all_mode", nil, &result); err != nil {
		return vmHandleUnsupported(err, apFeature, "/api/ap/close_all_mode")
	}
	return vmRenderAction("closed all AP modes", result)
}
