package cmd

import (
	"net/url"

	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/roboalchemist/kvm-cli/pkg/redact"
	"github.com/spf13/cobra"
)

// tailscaleCmd groups Tailscale lifecycle and configuration operations exposed
// by the Comet. Stop and logout are destructive (they drop the overlay
// network) and therefore require --yes.

const tsFeature = "Tailscale"

var tsYes bool

var tailscaleCmd = &cobra.Command{
	Use:   "tailscale",
	Short: "Manage the Tailscale overlay network",
	Long: `Manage the device's Tailscale client.

Subcommands mirror /api/tailscale/*: inspect status, start/stop the daemon, run
the interactive login flow, and read or write the Tailscale configuration.
Stopping the daemon or logging out disconnects the device from your tailnet and
requires --yes.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli tailscale status
  kvm-cli tailscale login-url
  kvm-cli tailscale stop --yes`,
}

var tailscaleStatusCmd = &cobra.Command{
	Use:     "status",
	Short:   "Show whether the Tailscale daemon is running (GET /api/tailscale/status)",
	Long:    "Report the Tailscale daemon's running state. Rendering honours the global output flags.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli tailscale status\n  kvm-cli tailscale status --json",
	RunE:    runTailscaleStatus,
}

var tailscaleStartCmd = &cobra.Command{
	Use:     "start",
	Short:   "Start the Tailscale daemon (POST /api/tailscale/start)",
	Long:    "Start the Tailscale daemon and bring the device onto the tailnet.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli tailscale start",
	RunE:    runTailscaleStart,
}

var tailscaleStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the Tailscale daemon (POST /api/tailscale/stop) — DESTRUCTIVE",
	Long:  "Stop the Tailscale daemon, disconnecting the device from the tailnet. Requires --yes.",
	Args:  cobra.NoArgs,
	Example: `  kvm-cli tailscale stop --yes
  kvm-cli tailscale stop          # dry run`,
	RunE: runTailscaleStop,
}

var tailscaleLoginCmd = &cobra.Command{
	Use:     "login",
	Short:   "Begin the Tailscale login flow (GET /api/tailscale/login)",
	Long:    "Query the device's login endpoint. Reports a clear NOT_SUPPORTED error when the firmware does not implement it (use 'login-url' instead).",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli tailscale login",
	RunE:    runTailscaleLogin,
}

var tailscaleLoginURLCmd = &cobra.Command{
	Use:     "login-url",
	Short:   "Fetch the Tailscale login URL (POST /api/tailscale/login_url)",
	Long:    "Ask the device for the URL to open in a browser to authorize it on your tailnet.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli tailscale login-url\n  kvm-cli tailscale login-url --json",
	RunE:    runTailscaleLoginURL,
}

var tailscaleLoginStatusCmd = &cobra.Command{
	Use:     "login-status",
	Short:   "Show the Tailscale login/bind status (GET /api/tailscale/login_status)",
	Long:    "Show the device's tailnet identity (IPv4/IPv6 addresses, login name) and backend state.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli tailscale login-status\n  kvm-cli tailscale login-status --json",
	RunE:    runTailscaleLoginStatus,
}

var tailscaleLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Log the device out of Tailscale (POST /api/tailscale/logout) — DESTRUCTIVE",
	Long:  "Unbind the device from your tailnet. Requires --yes.",
	Args:  cobra.NoArgs,
	Example: `  kvm-cli tailscale logout --yes
  kvm-cli tailscale logout          # dry run`,
	RunE: runTailscaleLogout,
}

var tsConfigExitNode string
var tsConfigAdvertiseRoutes string
var tsConfigSet []string

var tailscaleConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Read or write Tailscale configuration (GET/POST /api/tailscale/config)",
	Long: `Read the device's Tailscale configuration (accept_dns, accept_routes,
advertise_routes, enable, exit_node). Supplying any of --exit-node,
--advertise-routes or --set writes the configuration via query parameters
instead.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli tailscale config
  kvm-cli tailscale config --exit-node true
  kvm-cli tailscale config --advertise-routes auto
  kvm-cli tailscale config --set accept_dns=true`,
	RunE: runTailscaleConfig,
}

func init() {
	vmRegisterConfirm(tailscaleStopCmd, &tsYes, "Confirm stopping Tailscale (required to proceed)")
	vmRegisterConfirm(tailscaleLogoutCmd, &tsYes, "Confirm logging out of Tailscale (required to proceed)")

	tailscaleConfigCmd.Flags().StringVar(&tsConfigExitNode, "exit-node", "", "Exit node setting (true/false/address)")
	tailscaleConfigCmd.Flags().StringVar(&tsConfigAdvertiseRoutes, "advertise-routes", "", "Routes to advertise (\"auto\" or a comma-separated CIDR list)")
	tailscaleConfigCmd.Flags().StringArrayVar(&tsConfigSet, "set", nil, "key=value configuration pair (repeatable)")

	// Lifecycle and configuration-changing subcommands are writes.
	for _, c := range []*cobra.Command{
		tailscaleStartCmd,
		tailscaleStopCmd,
		tailscaleLoginCmd,
		tailscaleLoginURLCmd,
		tailscaleLogoutCmd,
		tailscaleConfigCmd,
	} {
		MarkWrite(c)
	}

	tailscaleCmd.AddCommand(
		tailscaleStatusCmd,
		tailscaleStartCmd,
		tailscaleStopCmd,
		tailscaleLoginCmd,
		tailscaleLoginURLCmd,
		tailscaleLoginStatusCmd,
		tailscaleLogoutCmd,
		tailscaleConfigCmd,
	)
	rootCmd.AddCommand(tailscaleCmd)
}

func runTailscaleStatus(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/tailscale/status", &result); err != nil {
		return vmHandleUnsupported(err, tsFeature, "/api/tailscale/status")
	}
	return syswRenderKV(result)
}

func runTailscaleStart(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/tailscale/start", nil, &result); err != nil {
		return vmHandleUnsupported(err, tsFeature, "/api/tailscale/start")
	}
	return vmRenderAction("started Tailscale", result)
}

func runTailscaleStop(cmd *cobra.Command, args []string) error {
	if err := vmRequireYes(tsYes, "stop the Tailscale daemon"); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/tailscale/stop", nil, &result); err != nil {
		return vmHandleUnsupported(err, tsFeature, "/api/tailscale/stop")
	}
	return vmRenderAction("stopped Tailscale", result)
}

func runTailscaleLogin(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/tailscale/login", &result); err != nil {
		return vmHandleUnsupported(err, tsFeature, "/api/tailscale/login")
	}
	if url, _ := result["url"].(string); url != "" {
		return tsRenderURL(url)
	}
	return syswRenderKV(result)
}

func runTailscaleLoginURL(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/tailscale/login_url", nil, &result); err != nil {
		return vmHandleUnsupported(err, tsFeature, "/api/tailscale/login_url")
	}
	if url, _ := result["url"].(string); url != "" {
		return tsRenderURL(url)
	}
	return syswRenderKV(result)
}

func runTailscaleLoginStatus(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/tailscale/login_status", &result); err != nil {
		return vmHandleUnsupported(err, tsFeature, "/api/tailscale/login_status")
	}
	return syswRenderKV(result)
}

func runTailscaleLogout(cmd *cobra.Command, args []string) error {
	if err := vmRequireYes(tsYes, "log the device out of Tailscale"); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/tailscale/logout", nil, &result); err != nil {
		return vmHandleUnsupported(err, tsFeature, "/api/tailscale/logout")
	}
	return vmRenderAction("logged out of Tailscale", result)
}

func runTailscaleConfig(cmd *cobra.Command, args []string) error {
	values := url.Values{}
	syswSetIfChanged(cmd, values, "exit_node", &tsConfigExitNode)
	syswSetIfChanged(cmd, values, "advertise_routes", &tsConfigAdvertiseRoutes)
	if err := syswMergeSets(values, tsConfigSet); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	if len(values) == 0 {
		var result map[string]any
		if err := client.Get("/api/tailscale/config", &result); err != nil {
			return vmHandleUnsupported(err, tsFeature, "/api/tailscale/config")
		}
		return syswRenderKV(result)
	}

	var result map[string]any
	if err := client.Post("/api/tailscale/config?"+values.Encode(), nil, &result); err != nil {
		return vmHandleUnsupported(err, tsFeature, "/api/tailscale/config")
	}
	// --set may carry secrets (for example password=); mask them from the
	// rendered action and any echoed configuration.
	return vmRenderAction("set Tailscale config ("+redact.Params(values.Encode())+")", redact.Map(result))
}

// renderURL prints a bare URL in the shape the global output flags expect.
func tsRenderURL(raw string) error {
	td := output.TableData{Headers: []string{"URL"}, Rows: [][]string{{raw}}}
	data := map[string]any{"url": raw}
	return output.Render(td, data, GetOutputOptions())
}
