package cmd

import (
	"net/url"

	"github.com/roboalchemist/kvm-cli/pkg/redact"
	"github.com/spf13/cobra"
)

// zerotierCmd groups ZeroTier overlay-network operations. Stopping the daemon
// disconnects the device and requires --yes.

const ztFeature = "ZeroTier"

var ztYes bool

var zerotierCmd = &cobra.Command{
	Use:   "zerotier",
	Short: "Manage the ZeroTier overlay network",
	Long: `Manage the device's ZeroTier client.

Subcommands mirror /api/zerotier/*: inspect status, start/stop the daemon, and
store the network ID (token). Stopping the daemon disconnects the device and
requires --yes.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli zerotier status
  kvm-cli zerotier set-token 8056c2e21c000001
  kvm-cli zerotier stop --yes`,
}

var zerotierStatusCmd = &cobra.Command{
	Use:     "status",
	Short:   "Show ZeroTier status (GET /api/zerotier/status)",
	Long:    "Show whether ZeroTier is enabled and whether its process is running.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli zerotier status\n  kvm-cli zerotier status --json",
	RunE:    runZerotierStatus,
}

var zerotierStartCmd = &cobra.Command{
	Use:     "start",
	Short:   "Start the ZeroTier daemon (POST /api/zerotier/start)",
	Long:    "Start the ZeroTier daemon.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli zerotier start",
	RunE:    runZerotierStart,
}

var zerotierStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the ZeroTier daemon (POST /api/zerotier/stop) — DESTRUCTIVE",
	Long:  "Stop the ZeroTier daemon, disconnecting the device from the network. Requires --yes.",
	Args:  cobra.NoArgs,
	Example: `  kvm-cli zerotier stop --yes
  kvm-cli zerotier stop          # dry run`,
	RunE: runZerotierStop,
}

var zerotierSetTokenCmd = &cobra.Command{
	Use:   "set-token NETWORK-ID",
	Short: "Store the ZeroTier network ID (POST /api/zerotier/set_token)",
	Long: `Store the 16-character ZeroTier network ID the device should join.

Despite the endpoint name, the value is the network ID, not an API token.`,
	Args:    cobra.ExactArgs(1),
	Example: "  kvm-cli zerotier set-token 8056c2e21c000001",
	RunE:    runZerotierSetToken,
}

func init() {
	vmRegisterConfirm(zerotierStopCmd, &ztYes, "Confirm stopping ZeroTier (required to proceed)")

	// set-token's positional is the ZeroTier network ID. Mask it in the
	// --dry-run preview so it is never echoed back.
	MarkSecretArgs(zerotierSetTokenCmd, 0)

	for _, c := range []*cobra.Command{
		zerotierStartCmd,
		zerotierStopCmd,
		zerotierSetTokenCmd,
	} {
		MarkWrite(c)
	}

	zerotierCmd.AddCommand(
		zerotierStatusCmd,
		zerotierStartCmd,
		zerotierStopCmd,
		zerotierSetTokenCmd,
	)
	rootCmd.AddCommand(zerotierCmd)
}

func runZerotierStatus(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/zerotier/status", &result); err != nil {
		return vmHandleUnsupported(err, ztFeature, "/api/zerotier/status")
	}
	return syswRenderKV(result)
}

func runZerotierStart(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/zerotier/start", nil, &result); err != nil {
		return vmHandleUnsupported(err, ztFeature, "/api/zerotier/start")
	}
	return vmRenderAction("started ZeroTier", result)
}

func runZerotierStop(cmd *cobra.Command, args []string) error {
	if err := vmRequireYes(ztYes, "stop the ZeroTier daemon"); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/zerotier/stop", nil, &result); err != nil {
		return vmHandleUnsupported(err, ztFeature, "/api/zerotier/stop")
	}
	return vmRenderAction("stopped ZeroTier", result)
}

func runZerotierSetToken(cmd *cobra.Command, args []string) error {
	token := args[0]

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	// The token is submitted as a form-encoded POST body rather than in the
	// query string, and it is never echoed back, so it cannot leak through the
	// action message, --debug logs, or transport errors.
	form := url.Values{"token": {token}}
	var result map[string]any
	if err := client.PostForm("/api/zerotier/set_token", form, &result); err != nil {
		return vmHandleUnsupported(err, ztFeature, "/api/zerotier/set_token")
	}
	return vmRenderAction("stored ZeroTier network ID", redact.Map(result))
}
