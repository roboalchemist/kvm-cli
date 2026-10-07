package cmd

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/spf13/cobra"
)

// fingerbotCmd groups the physical "fingerbot" button-presser: reading its
// battery, pressing the controlled machine's power button, and updating its
// firmware. The device only exposes these endpoints when a fingerbot is
// attached, so commands report a clear "not available" message otherwise.
var fingerbotCmd = &cobra.Command{
	Use:   "fingerbot",
	Short: "Inspect and control the fingerbot button-presser",
	Long: `Inspect and control the attached fingerbot (an actuator that presses a physical
button on the controlled machine).

'battery' reads the fingerbot's battery level, 'click' presses the button for a
given duration and strength, and 'upgrade' flashes the fingerbot firmware.
Firmware upgrade is destructive and requires --yes; the other commands are
benign. When no fingerbot is attached the commands report a clear message
instead of a raw error.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli fingerbot battery
  kvm-cli fingerbot click --press-time 500 --strength high
  kvm-cli fingerbot upgrade --yes`,
}

var (
	k10FingerbotUpgradeYes bool
	k10FingerbotPressTime  int
	k10FingerbotStrength   string
)

var fingerbotBatteryCmd = &cobra.Command{
	Use:     "battery",
	Short:   "Read the fingerbot battery level (GET /api/fingerbot/battery)",
	Long:    "Read the attached fingerbot's battery level. Reports a clear message when no fingerbot is attached.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli fingerbot battery\n  kvm-cli fingerbot battery --json",
	RunE:    runFingerbotBattery,
}

var fingerbotClickCmd = &cobra.Command{
	Use:   "click",
	Short: "Press the fingerbot button (POST /api/fingerbot/click)",
	Long: `Press the fingerbot for a given duration and strength.

--press-time is the hold time in milliseconds: 500 (0.5s) or 1000-60000
(1-60s). --strength is low or high. This is a benign action; no --yes is
required. Reports a clear message when no fingerbot is attached.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli fingerbot click
  kvm-cli fingerbot click --press-time 3000 --strength high
  kvm-cli fingerbot click --press-time 500 --strength low`,
	RunE: runFingerbotClick,
}

var fingerbotUpgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Flash the fingerbot firmware (POST /api/fingerbot/upgrade) — DESTRUCTIVE",
	Long: `Flash the attached fingerbot's firmware.

Destructive: the fingerbot is unavailable during the update, which can take
several minutes, so --yes is required. When no fingerbot is attached the
command reports a clear message.`,
	Args:    cobra.NoArgs,
	Example: "  kvm-cli fingerbot upgrade --yes",
	RunE:    runFingerbotUpgrade,
}

func init() {
	fingerbotClickCmd.Flags().IntVar(&k10FingerbotPressTime, "press-time", 500, "Hold time in milliseconds: 500 or 1000-60000")
	fingerbotClickCmd.Flags().StringVar(&k10FingerbotStrength, "strength", "high", "Press strength: low|high")
	vmRegisterConfirm(fingerbotUpgradeCmd, &k10FingerbotUpgradeYes, "Confirm flashing the fingerbot firmware (required to proceed)")

	MarkWrite(fingerbotClickCmd)
	MarkWrite(fingerbotUpgradeCmd)

	fingerbotCmd.AddCommand(fingerbotBatteryCmd, fingerbotClickCmd, fingerbotUpgradeCmd)
	rootCmd.AddCommand(fingerbotCmd)
}

func runFingerbotBattery(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/fingerbot/battery", &result); err != nil {
		return k10Unsupported(err, "fingerbot", "/api/fingerbot/battery")
	}

	td := output.TableData{
		Headers: []string{"KEY", "VALUE"},
		Rows:    k10FlattenRows("", result),
	}
	return output.Render(td, result, GetOutputOptions())
}

func runFingerbotClick(cmd *cobra.Command, args []string) error {
	if err := k10ValidatePressTime(k10FingerbotPressTime); err != nil {
		return err
	}
	strength, err := k10StrengthEnum(k10FingerbotStrength)
	if err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	values := url.Values{
		"press_time": {fmt.Sprintf("%d", k10FingerbotPressTime)},
		"angle_enum": {fmt.Sprintf("%d", strength)},
	}
	var result map[string]any
	if err := client.Post("/api/fingerbot/click?"+values.Encode(), nil, &result); err != nil {
		return k10Unsupported(err, "fingerbot", "/api/fingerbot/click")
	}
	return vmRenderAction(fmt.Sprintf("pressed fingerbot for %dms (%s)", k10FingerbotPressTime, strings.ToLower(k10FingerbotStrength)), result)
}

func runFingerbotUpgrade(cmd *cobra.Command, args []string) error {
	if err := vmRequireYes(k10FingerbotUpgradeYes, "flash the fingerbot firmware"); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := vmRawPost(client, "/api/fingerbot/upgrade", "", nil, vmUploadTimeout(cmd), &result); err != nil {
		return k10Unsupported(err, "fingerbot", "/api/fingerbot/upgrade")
	}
	return vmRenderAction("started fingerbot firmware upgrade", result)
}

// k10ValidatePressTime enforces the device's accepted press durations: 500ms
// (0.5s) or 1000-60000ms (1-60s).
func k10ValidatePressTime(ms int) error {
	if ms == 500 || (ms >= 1000 && ms <= 60000) {
		return nil
	}
	return output.NewCodedError("USAGE", fmt.Sprintf("invalid --press-time %d: expected 500 or 1000-60000 milliseconds", ms))
}

// k10StrengthEnum maps a strength name to the device's angle_enum value.
func k10StrengthEnum(name string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "low", "1":
		return 1, nil
	case "high", "2":
		return 2, nil
	default:
		return 0, output.NewCodedError("USAGE", fmt.Sprintf("invalid --strength %q: expected low or high", name))
	}
}
