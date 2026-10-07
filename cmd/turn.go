package cmd

import (
	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/spf13/cobra"
)

// turnCmd exposes the device's TURN (WebRTC relay) configuration. The TURN
// credentials are used by WebRTC clients to reach the device through NAT.
var turnCmd = &cobra.Command{
	Use:   "turn",
	Short: "Show the TURN (WebRTC relay) configuration",
	Long: `Show the TURN server configuration the device hands to WebRTC clients.

The device returns the TURN username, password, TTL, and relay URIs. These
credentials are read-only and are used by WebRTC clients to establish a relayed
connection to the device from outside its network.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli turn get
  kvm-cli turn get --json`,
}

var turnGetCmd = &cobra.Command{
	Use:     "get",
	Short:   "Show the TURN configuration (GET /api/turn/get_turn)",
	Long:    "Show the TURN server username, credential, TTL, and relay URIs.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli turn get\n  kvm-cli turn get --json",
	RunE:    runTurnGet,
}

func init() {
	turnCmd.AddCommand(turnGetCmd)
	rootCmd.AddCommand(turnCmd)
}

func runTurnGet(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/turn/get_turn", &result); err != nil {
		return vmDeviceError(err)
	}

	td := output.TableData{
		Headers: []string{"KEY", "VALUE"},
		Rows:    k10FlattenRows("", result),
	}
	return output.Render(td, result, GetOutputOptions())
}
