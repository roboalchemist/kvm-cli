package cmd

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/roboalchemist/kvm-cli/pkg/api"
	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/spf13/cobra"
)

// wolCmd groups Wake-on-LAN operations: managing the device's stored MAC
// addresses and dispatching magic packets.
var wolCmd = &cobra.Command{
	Use:   "wol",
	Short: "Manage Wake-on-LAN devices and wake them",
	Long: `Manage the remote KVM's Wake-on-LAN support.

The KVM keeps a list of MAC addresses it can wake on the local network, can scan
the network for candidate devices, and can send a magic packet to a given MAC.
Removing an entry is destructive and requires --yes.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli wol list
  kvm-cli wol scan
  kvm-cli wol add AA:BB:CC:DD:EE:FF workstation
  kvm-cli wol wake AA:BB:CC:DD:EE:FF
  kvm-cli wol remove AA:BB:CC:DD:EE:FF --yes`,
}

var wolListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List stored Wake-on-LAN devices (GET /api/wol/list)",
	Long:    "List the MAC addresses the device can wake, with any associated names and broadcast/port details.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli wol list\n  kvm-cli wol list --json",
	RunE:    runWOLList,
}

var wolScanCmd = &cobra.Command{
	Use:     "scan",
	Short:   "Scan the network for devices (POST /api/wol/scan)",
	Long:    "Ask the device to scan the local network for devices that could be woken, returning their names and MAC addresses.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli wol scan\n  kvm-cli wol scan --json",
	RunE:    runWOLScan,
}

var wolAddCmd = &cobra.Command{
	Use:     "add MAC [NAME]",
	Short:   "Add a Wake-on-LAN device (POST /api/wol/add)",
	Long:    "Store a MAC address (and optional friendly name) for later wake-ups.",
	Args:    cobra.RangeArgs(1, 2),
	Example: "  kvm-cli wol add AA:BB:CC:DD:EE:FF\n  kvm-cli wol add AA:BB:CC:DD:EE:FF workstation",
	RunE:    runWOLAdd,
}

var wolRemoveCmd = &cobra.Command{
	Use:   "remove MAC",
	Short: "Remove a Wake-on-LAN device (POST /api/wol/remove) — DESTRUCTIVE",
	Long:  "Delete a stored MAC address from the Wake-on-LAN list. Destructive: requires --yes.",
	Args:  cobra.ExactArgs(1),
	Example: `  kvm-cli wol remove AA:BB:CC:DD:EE:FF --yes
  kvm-cli wol remove AA:BB:CC:DD:EE:FF          # dry run`,
	RunE: runWOLRemove,
}

var wolWakeCmd = &cobra.Command{
	Use:     "wake MAC",
	Short:   "Send a magic packet (POST /api/wol/wake)",
	Long:    "Send a Wake-on-LAN magic packet to the given MAC address.",
	Args:    cobra.ExactArgs(1),
	Example: "  kvm-cli wol wake AA:BB:CC:DD:EE:FF",
	RunE:    runWOLWake,
}

var wolRemoveYes bool

func init() {
	vmRegisterConfirm(wolRemoveCmd, &wolRemoveYes, "Confirm this destructive removal (required to proceed)")

	// add/remove change stored state and wake transmits a magic packet.
	MarkWrite(wolAddCmd)
	MarkWrite(wolRemoveCmd)
	MarkWrite(wolWakeCmd)

	wolCmd.AddCommand(wolListCmd, wolScanCmd, wolAddCmd, wolRemoveCmd, wolWakeCmd)
	rootCmd.AddCommand(wolCmd)
}

func runWOLList(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var list api.WOLList
	if err := client.Get("/api/wol/list", &list); err != nil {
		return vmDeviceError(err)
	}
	return renderWOLDevices(list.Devices)
}

func runWOLScan(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var list api.WOLList
	if err := client.Post("/api/wol/scan", nil, &list); err != nil {
		return vmDeviceError(err)
	}
	return renderWOLDevices(list.Devices)
}

func runWOLAdd(cmd *cobra.Command, args []string) error {
	mac := strings.TrimSpace(args[0])
	name := ""
	if len(args) == 2 {
		name = strings.TrimSpace(args[1])
	}
	if mac == "" {
		return output.NewCodedError("USAGE", "a MAC address is required")
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	values := url.Values{"mac": {mac}}
	if name != "" {
		values.Set("name", name)
	}

	var result map[string]any
	if err := client.Post("/api/wol/add?"+values.Encode(), nil, &result); err != nil {
		return vmDeviceError(err)
	}
	label := mac
	if name != "" {
		label = fmt.Sprintf("%s (%s)", mac, name)
	}
	return vmRenderAction("added "+label, result)
}

func runWOLRemove(cmd *cobra.Command, args []string) error {
	mac := strings.TrimSpace(args[0])
	if err := vmRequireYes(wolRemoveYes, fmt.Sprintf("remove Wake-on-LAN device %q", mac)); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/wol/remove?"+url.Values{"mac": {mac}}.Encode(), nil, &result); err != nil {
		return vmDeviceError(err)
	}
	return vmRenderAction("removed "+mac, result)
}

func runWOLWake(cmd *cobra.Command, args []string) error {
	mac := strings.TrimSpace(args[0])

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/wol/wake?"+url.Values{"mac": {mac}}.Encode(), nil, &result); err != nil {
		return vmDeviceError(err)
	}
	return vmRenderAction("woke "+mac, result)
}

// renderWOLDevices renders a Wake-on-LAN device list for every output mode.
func renderWOLDevices(devices []api.WOLDevice) error {
	td := output.TableData{Headers: []string{"NAME", "MAC", "IP", "BROADCAST", "PORT", "ID"}}
	for _, d := range devices {
		td.Rows = append(td.Rows, []string{
			d.Name,
			d.MACAddress(),
			d.IP,
			d.Broadcast,
			vmValueString(d.Port),
			vmValueString(d.ID),
		})
	}
	if len(td.Rows) == 0 {
		td.Footer = "no Wake-on-LAN devices"
	}

	data := map[string]any{"devices": devices}
	return output.Render(td, data, GetOutputOptions())
}
