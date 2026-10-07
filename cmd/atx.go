package cmd

import (
	"fmt"
	"net/url"
	"strconv"

	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/spf13/cobra"
)

// atx flags.
var (
	atxLong bool
	atxYes  bool
)

// atxStatus mirrors GET /api/atx.
type atxStatus struct {
	Enabled bool   `json:"enabled" yaml:"enabled"`
	Busy    bool   `json:"busy" yaml:"busy"`
	Power   string `json:"power" yaml:"power"`
	Leds    struct {
		Power bool `json:"power" yaml:"power"`
		HDD   bool `json:"hdd" yaml:"hdd"`
	} `json:"leds" yaml:"leds"`
}

// atxResult is emitted after a click.
type atxResult struct {
	Button string `json:"button" yaml:"button"`
	Power  string `json:"power,omitempty" yaml:"power,omitempty"`
	Sent   bool   `json:"sent" yaml:"sent"`
}

var atxCmd = &cobra.Command{
	Use:   "atx",
	Short: "Control the target's ATX power (power/reset buttons)",
	Long: `Control the target machine's ATX power and reset buttons (powered through the
KVM's ATX header).

  atx status   show the current power state and LEDs
  atx power    press the power button (use --long for a 5-second hold)
  atx reset    press the reset button
  atx click    press an explicit button (power|power_long|reset)

The action commands press a physical button, so like every other write they
require confirmation: pass --yes (or its GNU synonym -f/--force). The same
confirmation also overrides the "ATX disabled" guard: some devices report
enabled:false when no ATX wiring is present, and --yes/-f/--force attempts the
click anyway. Use --dry-run to preview without touching the device.`,
	Example: `  kvm-cli atx status
  kvm-cli atx power --yes
  kvm-cli atx power --long --yes
  kvm-cli atx reset -f            # -f is a synonym for --yes
  kvm-cli atx click power_long --yes
  kvm-cli atx power --dry-run     # preview only, exits 0`,
}

var atxStatusCmd = &cobra.Command{
	Use:     "status",
	Short:   "Show the ATX power state",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli atx status\n  kvm-cli atx status --json",
	RunE:    runATXStatus,
}

var atxPowerCmd = &cobra.Command{
	Use:   "power",
	Short: "Press the target power button",
	Long: `Press the ATX power button. With --long, press and hold it (button
"power_long"), which many machines interpret as a forced power-off.

Requires confirmation: pass --yes (or -f/--force).`,
	Args:    cobra.NoArgs,
	Example: "  kvm-cli atx power --yes\n  kvm-cli atx power --long --yes\n  kvm-cli atx power --dry-run",
	RunE: func(cmd *cobra.Command, args []string) error {
		button := "power"
		if atxLong {
			button = "power_long"
		}
		return runATXClick(cmd, button, atxYes)
	},
}

var atxResetCmd = &cobra.Command{
	Use:     "reset",
	Short:   "Press the target reset button",
	Long:    "Press the ATX reset button. Requires confirmation: pass --yes (or -f/--force).",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli atx reset --yes\n  kvm-cli atx reset --dry-run",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runATXClick(cmd, "reset", atxYes)
	},
}

var atxClickCmd = &cobra.Command{
	Use:   "click POWER|POWER_LONG|RESET",
	Short: "Press an explicit ATX button",
	Long:  "Press the named ATX button (power, power_long or reset). Requires confirmation: pass --yes (or -f/--force).",
	Args:  cobra.ExactArgs(1),
	Example: `  kvm-cli atx click power --yes
  kvm-cli atx click power_long --yes
  kvm-cli atx click reset --yes`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runATXClick(cmd, args[0], atxYes)
	},
}

func runATXStatus(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return err
	}
	var st atxStatus
	if err := client.Get("/api/atx", &st); err != nil {
		return fmt.Errorf("fetch ATX state: %w", err)
	}

	td := output.TableData{
		Headers: []string{"FIELD", "VALUE"},
		Rows: [][]string{
			{"enabled", strconv.FormatBool(st.Enabled)},
			{"power", emptyDash(st.Power)},
			{"busy", strconv.FormatBool(st.Busy)},
			{"power led", strconv.FormatBool(st.Leds.Power)},
			{"hdd led", strconv.FormatBool(st.Leds.HDD)},
		},
		Footer: atxFooter(st),
	}
	return output.Render(td, st, GetOutputOptions())
}

func atxFooter(st atxStatus) string {
	if !st.Enabled {
		return "ATX is disabled on this device (no ATX wiring detected); power/reset actions need --yes (or -f/--force)."
	}
	return ""
}

func runATXClick(cmd *cobra.Command, button string, confirmed bool) error {
	switch button {
	case "power", "power_long", "reset":
	default:
		return output.NewCodedError("USAGE",
			fmt.Sprintf("invalid ATX button %q (expected power|power_long|reset)", button))
	}

	// Confirmation is required before any device contact, exactly like the
	// other write commands. The same gate doubles as the "ATX disabled"
	// override: a confirmed click proceeds even when the device reports
	// enabled=false.
	if err := vmRequireYes(confirmed, fmt.Sprintf("press the ATX %s button", button)); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return err
	}

	var st atxStatus
	if err := client.Get("/api/atx", &st); err != nil {
		return fmt.Errorf("fetch ATX state: %w", err)
	}

	path := "/api/atx/click?button=" + url.QueryEscape(button)
	if err := client.Post(path, nil, nil); err != nil {
		return fmt.Errorf("ATX %s failed: %w", button, err)
	}

	result := atxResult{Button: button, Power: st.Power, Sent: true}
	res := output.TableData{
		Headers: []string{"BUTTON", "POWER", "SENT"},
		Rows:    [][]string{{button, emptyDash(st.Power), "true"}},
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Sent ATX button %q\n", button)
	return output.Render(res, result, GetOutputOptions())
}

func init() {
	atxPowerCmd.Flags().BoolVar(&atxLong, "long", false, "Hold the power button (power_long)")

	// The action commands press a physical button: require the standard
	// --yes/-f/--force confirmation like every other write. -f is the GNU
	// synonym for --yes; a confirmed click also overrides enabled=false.
	vmRegisterConfirm(atxPowerCmd, &atxYes, "Confirm pressing the power button (required to proceed)")
	vmRegisterConfirm(atxResetCmd, &atxYes, "Confirm pressing the reset button (required to proceed)")
	vmRegisterConfirm(atxClickCmd, &atxYes, "Confirm pressing the ATX button (required to proceed)")

	// atx status is read-only; the action subcommands mutate the target.
	MarkWrite(atxPowerCmd)
	MarkWrite(atxResetCmd)
	MarkWrite(atxClickCmd)

	atxCmd.AddCommand(atxStatusCmd, atxPowerCmd, atxResetCmd, atxClickCmd)
	rootCmd.AddCommand(atxCmd)
}
