package cmd

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/roboalchemist/kvm-cli/pkg/api"
	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/spf13/cobra"
)

// switchCmd groups USB switch-port control. On firmware that does not expose
// the switch API (for example V1.10.1 on the GL-RM1PE) the endpoints return
// HTTP 404; the commands degrade to a clear "not supported" message and exit
// non-zero rather than surfacing a raw 404.
var switchCmd = &cobra.Command{
	Use:   "switch",
	Short: "Inspect and set the active USB switch port",
	Long: `Inspect and control the device's USB switch ports.

Some firmware builds do not implement this API; when the device returns HTTP
404 the commands report that the feature is not supported on this device
instead of printing a raw error.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli switch status
  kvm-cli switch set-active 1`,
}

var switchStatusCmd = &cobra.Command{
	Use:     "status",
	Short:   "Show USB switch port state (GET /api/switch)",
	Long:    "Show the available USB switch ports and which one is active. Reports a clear message when the device does not implement the endpoint.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli switch status\n  kvm-cli switch status --json",
	RunE:    runSwitchStatus,
}

var switchSetActiveCmd = &cobra.Command{
	Use:     "set-active PORT",
	Short:   "Select the active USB switch port (POST /api/switch/set_active)",
	Long:    "Switch the active USB port to <port> so the target is connected to it. Reports a clear message when the device does not implement the endpoint.",
	Args:    cobra.ExactArgs(1),
	Example: "  kvm-cli switch set-active 1",
	RunE:    runSwitchSetActive,
}

func init() {
	MarkWrite(switchSetActiveCmd)

	switchCmd.AddCommand(switchStatusCmd, switchSetActiveCmd)
	rootCmd.AddCommand(switchCmd)
}

func runSwitchStatus(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/switch", &result); err != nil {
		return vmHandleUnsupported(err, "USB switch control", "/api/switch")
	}

	td := output.TableData{
		Headers: []string{"KEY", "VALUE"},
		Rows:    vmFlattenRows("", result),
	}
	return output.Render(td, result, GetOutputOptions())
}

func runSwitchSetActive(cmd *cobra.Command, args []string) error {
	port := args[0]

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/switch/set_active?"+url.Values{"port": {port}}.Encode(), nil, &result); err != nil {
		return vmHandleUnsupported(err, "USB switch control", "/api/switch/set_active")
	}
	return vmRenderAction("activated switch port "+port, result)
}

// vmHandleUnsupported converts an HTTP 404 from a feature endpoint into a clear,
// typed NOT_SUPPORTED error so callers never surface a raw "404: Not Found".
// Any other error is passed through vmDeviceError.
func vmHandleUnsupported(err error, feature, endpoint string) error {
	if err == nil {
		return nil
	}
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && apiErr.IsNotFound() {
		return output.NewCodedError("NOT_SUPPORTED",
			fmt.Sprintf("%s is not supported on this device (%s returned HTTP 404)", feature, endpoint))
	}
	return vmDeviceError(err)
}
