package cmd

import (
	"github.com/spf13/cobra"
)

// netbirdCmd groups NetBird overlay-network operations. Logging out or stopping
// the daemon disconnects the device and requires --yes.

const nbFeature = "NetBird"

var nbYes bool

var netbirdCmd = &cobra.Command{
	Use:   "netbird",
	Short: "Manage the NetBird overlay network",
	Long: `Manage the device's NetBird client.

Subcommands mirror /api/netbird/*: inspect connection info, start/stop the
daemon, and log in or out. Logging out or stopping disconnects the device and
requires --yes.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli netbird info
  kvm-cli netbird start
  kvm-cli netbird stop --yes`,
}

var netbirdInfoCmd = &cobra.Command{
	Use:     "info",
	Short:   "Show NetBird connection info (GET /api/netbird/get_info)",
	Long:    "Show whether NetBird is running/connected, the assigned IP, and any error message.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli netbird info\n  kvm-cli netbird info --json",
	RunE:    runNetbirdInfo,
}

var netbirdLoginCmd = &cobra.Command{
	Use:     "login",
	Short:   "Log in to NetBird (POST /api/netbird/login)",
	Long:    "Start the NetBird login flow so the device joins your NetBird network.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli netbird login",
	RunE:    runNetbirdLogin,
}

var netbirdLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Log out of NetBird (POST /api/netbird/logout) — DESTRUCTIVE",
	Long:  "Log the device out of NetBird, disconnecting it from the network. Requires --yes.",
	Args:  cobra.NoArgs,
	Example: `  kvm-cli netbird logout --yes
  kvm-cli netbird logout          # dry run`,
	RunE: runNetbirdLogout,
}

var netbirdStartCmd = &cobra.Command{
	Use:     "start",
	Short:   "Start the NetBird daemon (POST /api/netbird/start)",
	Long:    "Start the NetBird daemon.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli netbird start",
	RunE:    runNetbirdStart,
}

var netbirdStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the NetBird daemon (POST /api/netbird/stop) — DESTRUCTIVE",
	Long:  "Stop the NetBird daemon, disconnecting the device from the network. Requires --yes.",
	Args:  cobra.NoArgs,
	Example: `  kvm-cli netbird stop --yes
  kvm-cli netbird stop          # dry run`,
	RunE: runNetbirdStop,
}

func init() {
	vmRegisterConfirm(netbirdLogoutCmd, &nbYes, "Confirm logging out of NetBird (required to proceed)")
	vmRegisterConfirm(netbirdStopCmd, &nbYes, "Confirm stopping NetBird (required to proceed)")

	for _, c := range []*cobra.Command{
		netbirdLoginCmd,
		netbirdLogoutCmd,
		netbirdStartCmd,
		netbirdStopCmd,
	} {
		MarkWrite(c)
	}

	netbirdCmd.AddCommand(
		netbirdInfoCmd,
		netbirdLoginCmd,
		netbirdLogoutCmd,
		netbirdStartCmd,
		netbirdStopCmd,
	)
	rootCmd.AddCommand(netbirdCmd)
}

func runNetbirdInfo(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/netbird/get_info", &result); err != nil {
		return vmHandleUnsupported(err, nbFeature, "/api/netbird/get_info")
	}
	return syswRenderKV(result)
}

func runNetbirdLogin(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/netbird/login", nil, &result); err != nil {
		return vmHandleUnsupported(err, nbFeature, "/api/netbird/login")
	}
	return vmRenderAction("logged NetBird in", result)
}

func runNetbirdLogout(cmd *cobra.Command, args []string) error {
	if err := vmRequireYes(nbYes, "log the device out of NetBird"); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/netbird/logout", nil, &result); err != nil {
		return vmHandleUnsupported(err, nbFeature, "/api/netbird/logout")
	}
	return vmRenderAction("logged NetBird out", result)
}

func runNetbirdStart(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/netbird/start", nil, &result); err != nil {
		return vmHandleUnsupported(err, nbFeature, "/api/netbird/start")
	}
	return vmRenderAction("started NetBird", result)
}

func runNetbirdStop(cmd *cobra.Command, args []string) error {
	if err := vmRequireYes(nbYes, "stop the NetBird daemon"); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/netbird/stop", nil, &result); err != nil {
		return vmHandleUnsupported(err, nbFeature, "/api/netbird/stop")
	}
	return vmRenderAction("stopped NetBird", result)
}
