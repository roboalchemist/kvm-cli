package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/roboalchemist/kvm-cli/pkg/ws"
	"github.com/spf13/cobra"
)

// ---- flags -----------------------------------------------------------------

var (
	hidKeyTap      bool
	hidKeyDown     bool
	hidKeyUp       bool
	hidTypeFile    string
	hidTypeEnter   bool
	hidTypeKeymap  string
	hidPrintKeymap string

	hidMouseRelative bool
	hidMousePct      bool
	hidMouseAbsolute bool
	hidMouseClickAt  string
	hidMouseClickPct bool
)

// ---- shared WebSocket helpers ----------------------------------------------

// hidTLSInsecure resolves whether the WebSocket dial should skip TLS
// verification. It mirrors pkg/auth's policy: an explicit KVM_INSECURE (or the
// --insecure flag) wins; KVM_TLS_STRICT forces verification; otherwise the
// device's self-signed certificate means verification is skipped by default.
func hidTLSInsecure() bool {
	if flagInsecure {
		return true
	}
	if v, ok := os.LookupEnv("KVM_INSECURE"); ok {
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			return b
		}
	}
	if v, ok := os.LookupEnv("GLKVM_INSECURE"); ok {
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			return b
		}
	}
	// A strict setting forces verification (fail safe on an unparseable value).
	if v, ok := os.LookupEnv("KVM_TLS_STRICT"); ok {
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			return !b
		}
		return false
	}
	if v, ok := os.LookupEnv("GLKVM_TLS_STRICT"); ok {
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			return !b
		}
		return false
	}
	return true
}

// hidWSOptions builds the ws.Options from the global flags/env.
func hidWSOptions() ws.Options {
	timeout := flagTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return ws.Options{Insecure: hidTLSInsecure(), Timeout: timeout, Debug: flagDebug}
}

// hidDial authenticates over REST and then dials the HID WebSocket channel.
func hidDial(cmd *cobra.Command) (*ws.Client, error) {
	client, err := NewClient(cmd)
	if err != nil {
		return nil, err
	}
	return ws.Connect(client.BaseURL(), client.Token(), hidWSOptions())
}

// hidDo opens the HID WebSocket, waits for the device HID state to arrive,
// runs action, flushes briefly, then closes. It is the common shell for every
// one-shot HID action.
func hidDo(cmd *cobra.Command, action func(*ws.Client) error) error {
	c, err := hidDial(cmd)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	if _, err := c.WaitFor(func(s ws.State) bool { return s.HIDSeen }, 4*time.Second); err != nil {
		return fmt.Errorf("device HID state unavailable: %w", err)
	}
	if err := action(c); err != nil {
		return err
	}
	// Give the device a moment to deliver the event before the socket closes.
	time.Sleep(200 * time.Millisecond)
	return nil
}

// ---- hid status ------------------------------------------------------------

// hidDeviceState mirrors GET /api/hid.
type hidDeviceState struct {
	Enabled   bool `json:"enabled" yaml:"enabled"`
	Online    bool `json:"online" yaml:"online"`
	Busy      bool `json:"busy" yaml:"busy"`
	Connected bool `json:"connected" yaml:"connected"`
	Keyboard  struct {
		Online bool `json:"online" yaml:"online"`
		Leds   struct {
			Caps   bool `json:"caps" yaml:"caps"`
			Num    bool `json:"num" yaml:"num"`
			Scroll bool `json:"scroll" yaml:"scroll"`
		} `json:"leds" yaml:"leds"`
		Outputs struct {
			Active    string   `json:"active" yaml:"active"`
			Available []string `json:"available" yaml:"available"`
		} `json:"outputs" yaml:"outputs"`
	} `json:"keyboard" yaml:"keyboard"`
	Mouse struct {
		Online   bool `json:"online" yaml:"online"`
		Absolute bool `json:"absolute" yaml:"absolute"`
		Outputs  struct {
			Active    string   `json:"active" yaml:"active"`
			Available []string `json:"available" yaml:"available"`
		} `json:"outputs" yaml:"outputs"`
	} `json:"mouse" yaml:"mouse"`
}

var hidStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the HID (keyboard/mouse) state",
	Long: `Show the device's HID state: whether HID is enabled and online, the keyboard
LED state (caps/num/scroll), and the active mouse mode and output.

This is the read-only observability command agents use to verify that an
injected key was delivered (for example, CapsLock toggles the reported caps
LED).`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli hid status
  kvm-cli hid status --json
  kvm-cli hid status --json --fields enabled,online,connected`,
	RunE: runHIDStatus,
}

func runHIDStatus(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return err
	}
	var state hidDeviceState
	if err := client.Get("/api/hid", &state); err != nil {
		return fmt.Errorf("fetch HID state: %w", err)
	}

	td := output.TableData{
		Headers: []string{"FIELD", "VALUE"},
		Rows: [][]string{
			{"enabled", strconv.FormatBool(state.Enabled)},
			{"online", strconv.FormatBool(state.Online)},
			{"connected", strconv.FormatBool(state.Connected)},
			{"busy", strconv.FormatBool(state.Busy)},
			{"keyboard online", strconv.FormatBool(state.Keyboard.Online)},
			{"mouse online", strconv.FormatBool(state.Mouse.Online)},
			{"mouse absolute", strconv.FormatBool(state.Mouse.Absolute)},
			{"mouse output", emptyDash(state.Mouse.Outputs.Active)},
			{"caps lock", strconv.FormatBool(state.Keyboard.Leds.Caps)},
			{"num lock", strconv.FormatBool(state.Keyboard.Leds.Num)},
			{"scroll lock", strconv.FormatBool(state.Keyboard.Leds.Scroll)},
		},
	}
	return output.Render(td, state, GetOutputOptions())
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ---- hid key / combo -------------------------------------------------------

var hidKeyCmd = &cobra.Command{
	Use:   "key NAME",
	Short: "Inject a single key (tap by default)",
	Long: `Inject a keyboard event. By default the key is tapped (pressed and released
in one frame). Use --down to hold the key and --up to release it; holding a
modifier then pressing another key is how combinations are built, though the
'combo' subcommand does this for you.

Key names use the DOM KeyboardEvent.code set (KeyA, Enter, ArrowUp,
ControlLeft, CapsLock, F5, ...). Friendly aliases such as 'ctrl', 'enter',
'del' and 'caps' are also accepted. Run 'kvm-cli hid keys' for the full list.`,
	Args: cobra.ExactArgs(1),
	Example: `  kvm-cli hid key CapsLock
  kvm-cli hid key Enter
  kvm-cli hid key KeyA --down
  kvm-cli hid key KeyA --up
  kvm-cli hid key ctrl --down && kvm-cli hid key c --tap`,
	RunE: runHIDKey,
}

func runHIDKey(cmd *cobra.Command, args []string) error {
	switch {
	case hidKeyDown && hidKeyUp:
		return output.NewCodedError("USAGE", "--down and --up are mutually exclusive")
	case hidKeyTap && (hidKeyDown || hidKeyUp):
		return output.NewCodedError("USAGE", "--tap cannot be combined with --down or --up")
	}
	name := args[0]
	return hidDo(cmd, func(c *ws.Client) error {
		switch {
		case hidKeyDown:
			return c.PressKey(name)
		case hidKeyUp:
			return c.ReleaseKey(name)
		default:
			return c.TapKey(name)
		}
	})
}

var hidComboCmd = &cobra.Command{
	Use:   "combo KEY1+KEY2+...",
	Short: "Send a key combination (e.g. ctrl+alt+del)",
	Long: `Send a key combination as a sequence of HID events: the keys are pressed in
the order given, then released in reverse order. There is no atomic combo event
on this device.

Keys are joined with '+'. Each part accepts the same names as 'hid key'.`,
	Args: cobra.ExactArgs(1),
	Example: `  kvm-cli hid combo ctrl+alt+del
  kvm-cli hid combo ctrl+shift+t
  kvm-cli hid combo MetaLeft+KeyL
  kvm-cli hid combo alt+F4`,
	RunE: runHIDCombo,
}

func runHIDCombo(cmd *cobra.Command, args []string) error {
	keys, err := parseCombo(args[0])
	if err != nil {
		return output.NewCodedError("USAGE", err.Error())
	}
	return hidDo(cmd, func(c *ws.Client) error {
		return c.Combo(keys)
	})
}

// parseCombo splits "ctrl+alt+del" into its parts.
func parseCombo(spec string) ([]string, error) {
	var keys []string
	for _, part := range strings.Split(spec, "+") {
		part = strings.TrimSpace(part)
		if part != "" {
			keys = append(keys, part)
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("empty key combination")
	}
	return keys, nil
}

// ---- hid type / print ------------------------------------------------------

var hidTypeCmd = &cobra.Command{
	Use:   "type TEXT",
	Short: "Type a string on the target (layout-aware, handles newlines)",
	Long: `Type text on the target using the device's layout-aware print endpoint.

Newlines in the text are sent as Enter taps, so multi-line input is typed
correctly, and --enter appends a trailing Enter. Use --file to read the text
from a file and --keymap to select the keyboard layout (default en-us).

This is the higher-level counterpart of 'hid print': prefer 'hid type' when the
text may contain newlines or when you want --enter/--file handling. For a single
named key use 'hid key'.`,
	Args: cobra.MaximumNArgs(1),
	Example: `  kvm-cli hid type "hello world"
  kvm-cli hid type "password123" --enter
  kvm-cli hid type --file ./message.txt
  kvm-cli hid type "user@example.com" --keymap de`,
	RunE: runHIDType,
}

func runHIDType(cmd *cobra.Command, args []string) error {
	var text string
	switch {
	case hidTypeFile != "":
		if len(args) > 0 {
			return output.NewCodedError("USAGE", "cannot combine --file with inline text")
		}
		data, err := os.ReadFile(hidTypeFile)
		if err != nil {
			return fmt.Errorf("read %s: %w", hidTypeFile, err)
		}
		text = string(data)
	default:
		if len(args) == 0 {
			return output.NewCodedError("USAGE", "provide text to type or use --file")
		}
		text = args[0]
	}

	return hidDo(cmd, func(c *ws.Client) error {
		if hidTypeKeymap != "" {
			// Print the raw text with the requested keymap, mapping newlines to
			// Enter taps ourselves.
			if err := typeWithKeymap(c, text, hidTypeKeymap); err != nil {
				return err
			}
		} else if err := c.TypeText(text); err != nil {
			return err
		}
		if hidTypeEnter {
			return c.TapKey("Enter")
		}
		return nil
	})
}

// typeWithKeymap is TypeText with an explicit keymap.
func typeWithKeymap(c *ws.Client, text, keymap string) error {
	if !strings.Contains(text, "\n") {
		return c.PrintWithKeymap(text, keymap)
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if line != "" {
			if err := c.PrintWithKeymap(line, keymap); err != nil {
				return err
			}
		}
		if i < len(lines)-1 {
			if err := c.TapKey("Enter"); err != nil {
				return err
			}
		}
	}
	return nil
}

var hidPrintCmd = &cobra.Command{
	Use:   "print TEXT",
	Short: "Type raw text in one request (no newline/--enter handling)",
	Long: `Type raw text using the layout-aware POST /api/hid/print endpoint in a single
request.

Unlike 'hid type', embedded newlines are sent verbatim (not converted to Enter
taps) and there is no --enter or --file handling, so use it for a single line or
a paste-style payload. It takes exactly one argument; --keymap selects the
keyboard layout (default en-us).

For multi-line input, --enter, or --file use 'hid type'.`,
	Args: cobra.ExactArgs(1),
	Example: `  kvm-cli hid print "hello"
  kvm-cli hid print "guten tag" --keymap de`,
	RunE: runHIDPrint,
}

func runHIDPrint(cmd *cobra.Command, args []string) error {
	text := args[0]
	keymap := hidPrintKeymap
	return hidDo(cmd, func(c *ws.Client) error {
		return c.PrintWithKeymap(text, keymap)
	})
}

var hidKeysCmd = &cobra.Command{
	Use:   "keys",
	Short: "List the accepted key names",
	Long:  "List every exact DOM KeyboardEvent.code accepted by 'hid key' and 'hid combo'.",
	Args:  cobra.NoArgs,
	Example: `  kvm-cli hid keys
  kvm-cli hid keys --plaintext`,
	RunE: runHIDKeys,
}

func runHIDKeys(cmd *cobra.Command, args []string) error {
	keys := ws.ValidKeys()
	rows := make([][]string, 0, len(keys))
	for _, k := range keys {
		rows = append(rows, []string{k})
	}
	td := output.TableData{Headers: []string{"KEY"}, Rows: rows}
	return output.Render(td, keys, GetOutputOptions())
}

// ---- hid set-mouse-output --------------------------------------------------

var hidSetMouseOutputCmd = &cobra.Command{
	Use:   "set-mouse-output USB|USB_REL|USB_HYBRID|USB_TOUCH",
	Short: "Switch the mouse between absolute and relative modes",
	Long: `Set the mouse output mode via POST /api/hid/set_params?mouse_output=<mode>.

  usb         absolute positioning (default; requires a resolution)
  usb_rel     relative positioning (deltas)
  usb_hybrid  hybrid
  usb_touch   touch-style absolute

The aliases 'absolute' and 'relative' map to usb and usb_rel.`,
	Args: cobra.ExactArgs(1),
	Example: `  kvm-cli hid set-mouse-output usb_rel
  kvm-cli hid set-mouse-output absolute
  kvm-cli hid set-mouse-output usb`,
	RunE: runHIDSetMouseOutput,
}

func runHIDSetMouseOutput(cmd *cobra.Command, args []string) error {
	mode := strings.TrimSpace(args[0])
	switch strings.ToLower(mode) {
	case "absolute", "abs":
		mode = "usb"
	case "relative", "rel":
		mode = "usb_rel"
	}
	return hidDo(cmd, func(c *ws.Client) error {
		return c.SetMouseOutput(mode)
	})
}

// ---- hid mouse -------------------------------------------------------------

var hidMouseCmd = &cobra.Command{
	Use:   "mouse",
	Short: "Control the remote mouse",
	Long:  "Move, click, and scroll the remote mouse over the HID WebSocket channel.",
	Example: `  kvm-cli hid mouse move 1000 500
  kvm-cli hid mouse click left
  kvm-cli hid mouse wheel 0 -3`,
}

var hidMouseMoveCmd = &cobra.Command{
	Use:   "move X Y",
	Short: "Move the mouse to an absolute or relative position",
	Long: `Move the mouse.

By default x and y are absolute pixel coordinates in the streamed frame; the
position is converted to the device's signed int16 coordinate space using the
resolution reported by the streamer. With --pct, x and y are percentages
(0..100). With --relative, x and y are relative deltas.

Because negative coordinates would otherwise look like flags, flags may be
written before the coordinates: 'hid mouse move --relative 5 -5'. Global flags
such as --json may also be written after the coordinates
('hid mouse move 1000 500 --json'): the parser stops flag scanning at the first
negative number, not at the first argument.

Absolute positioning requires the mouse to be in absolute mode; if it is not,
run 'kvm-cli hid set-mouse-output usb' first.`,
	Args: validateHIDMouseArgs,
	Example: `  kvm-cli hid mouse move 1000 500
  kvm-cli hid mouse move --pct 50 50
  kvm-cli hid mouse move --relative 5 -5
  kvm-cli hid mouse move --absolute 960 540`,
	RunE: runHIDMouseMove,
}

func runHIDMouseMove(cmd *cobra.Command, args []string) error {
	pos, err := trailingFlagArgs(cmd, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return output.NewCodedError("USAGE", fmt.Sprintf("accepts 2 arg(s), received %d", len(pos)))
	}
	x, err := strconv.Atoi(strings.TrimSpace(pos[0]))
	if err != nil {
		return output.NewCodedError("USAGE", fmt.Sprintf("invalid x %q: %v", pos[0], err))
	}
	y, err := strconv.Atoi(strings.TrimSpace(pos[1]))
	if err != nil {
		return output.NewCodedError("USAGE", fmt.Sprintf("invalid y %q: %v", pos[1], err))
	}
	if hidMouseRelative && hidMousePct {
		return output.NewCodedError("USAGE", "--relative and --pct are mutually exclusive")
	}

	return hidDo(cmd, func(c *ws.Client) error {
		switch {
		case hidMouseRelative:
			return c.MouseRelative(x, y)
		case hidMousePct:
			if x < 0 || x > 100 || y < 0 || y > 100 {
				return output.NewCodedError("USAGE", "percentage coordinates must be between 0 and 100")
			}
			return c.MouseMovePct(float64(x), float64(y))
		default:
			return c.MouseMovePixels(x, y)
		}
	})
}

var hidMouseClickCmd = &cobra.Command{
	Use:   "click LEFT|MIDDLE|RIGHT|UP|DOWN",
	Short: "Click a mouse button (optionally moving to a coordinate first)",
	Long: `Click a mouse button.

With --at X,Y the pointer is first moved to that absolute pixel position, then the
button is clicked — a single round trip instead of 'hid mouse move X Y' followed
by 'hid mouse click'. Use --at-pct X,Y to move by percentage (0..100) instead.`,
	Args: cobra.ExactArgs(1),
	Example: `  kvm-cli hid mouse click left
  kvm-cli hid mouse click right
  kvm-cli hid mouse click left --at 961,803
  kvm-cli hid mouse click left --at-pct 50,50`,
	RunE: func(cmd *cobra.Command, args []string) error {
		button := args[0]
		if hidMouseClickAt != "" && hidMouseClickPct {
			return output.NewCodedError("USAGE", "--at and --at-pct are mutually exclusive")
		}
		if hidMouseClickAt == "" {
			return hidDo(cmd, func(c *ws.Client) error { return c.MouseClick(button) })
		}
		x, y, err := parseHIDPoint(hidMouseClickAt)
		if err != nil {
			return err
		}
		return hidDo(cmd, func(c *ws.Client) error {
			if hidMouseClickPct {
				if x < 0 || x > 100 || y < 0 || y > 100 {
					return output.NewCodedError("USAGE", "percentage coordinates must be between 0 and 100")
				}
				if err := c.MouseMovePct(float64(x), float64(y)); err != nil {
					return err
				}
			} else if err := c.MouseMovePixels(x, y); err != nil {
				return err
			}
			time.Sleep(80 * time.Millisecond)
			return c.MouseClick(button)
		})
	},
}

// parseHIDPoint parses an "x,y" coordinate pair (whitespace tolerated).
func parseHIDPoint(s string) (int, int, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return 0, 0, output.NewCodedError("USAGE", fmt.Sprintf("expected X,Y (got %q)", strings.TrimSpace(s)))
	}
	x, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, output.NewCodedError("USAGE", fmt.Sprintf("invalid x %q: %v", parts[0], err))
	}
	y, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, output.NewCodedError("USAGE", fmt.Sprintf("invalid y %q: %v", parts[1], err))
	}
	return x, y, nil
}

var hidMouseDownCmd = &cobra.Command{
	Use:     "down LEFT|MIDDLE|RIGHT|UP|DOWN",
	Short:   "Press a mouse button down",
	Args:    cobra.ExactArgs(1),
	Example: "  kvm-cli hid mouse down left",
	RunE: func(cmd *cobra.Command, args []string) error {
		button := args[0]
		return hidDo(cmd, func(c *ws.Client) error { return c.MouseButton(button, true) })
	},
}

var hidMouseUpCmd = &cobra.Command{
	Use:     "up LEFT|MIDDLE|RIGHT|UP|DOWN",
	Short:   "Release a mouse button",
	Args:    cobra.ExactArgs(1),
	Example: "  kvm-cli hid mouse up left",
	RunE: func(cmd *cobra.Command, args []string) error {
		button := args[0]
		return hidDo(cmd, func(c *ws.Client) error { return c.MouseButton(button, false) })
	},
}

var hidMouseWheelCmd = &cobra.Command{
	Use:   "wheel DX DY",
	Short: "Scroll the mouse wheel",
	Long:  "Scroll by a relative delta. Positive y scrolls up, negative y scrolls down. Flags (if any) may precede or follow the deltas; negative deltas are never mistaken for flags.",
	Args:  validateHIDMouseArgs,
	Example: `  kvm-cli hid mouse wheel 0 -3
  kvm-cli hid mouse wheel 1 0
  kvm-cli hid mouse wheel 0 -3 --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		pos, err := trailingFlagArgs(cmd, args)
		if err != nil {
			return err
		}
		if len(pos) != 2 {
			return output.NewCodedError("USAGE", fmt.Sprintf("accepts 2 arg(s), received %d", len(pos)))
		}
		dx, err := strconv.Atoi(pos[0])
		if err != nil {
			return output.NewCodedError("USAGE", fmt.Sprintf("invalid dx %q: %v", pos[0], err))
		}
		dy, err := strconv.Atoi(pos[1])
		if err != nil {
			return output.NewCodedError("USAGE", fmt.Sprintf("invalid dy %q: %v", pos[1], err))
		}
		return hidDo(cmd, func(c *ws.Client) error { return c.MouseWheel(dx, dy) })
	},
}

// ---- wiring ----------------------------------------------------------------

// trailingFlagArgs splits the argv of a command that disables interspersed
// parsing ('hid mouse move'/'wheel') into leading positional coordinates and
// any trailing global/local flags.
//
// Those commands disable interspersed parsing so negative deltas (e.g. -5) are
// not mistaken for flags. The cost is that a flag written after the coordinates
// (e.g. 'hid mouse move 1000 500 --json') would otherwise be passed through as a
// positional and rejected. This helper restores that form: a token starts the
// trailing-flag section only when it looks like a flag (leading '-' and not a
// numeric literal), so negative deltas stay positional. The trailing flags are
// parsed into cmd's flag set, making --json/--format/etc. effective after the
// coordinates.
func trailingFlagArgs(cmd *cobra.Command, args []string) ([]string, error) {
	split := len(args)
	for i, a := range args {
		if strings.HasPrefix(a, "-") && !isNegativeNumber(a) {
			split = i
			break
		}
	}
	positional := args[:split]
	if split < len(args) {
		if err := cmd.Flags().Parse(args[split:]); err != nil {
			return nil, output.WrapCodedError("USAGE", err, err.Error())
		}
	}
	return positional, nil
}

// validateHIDMouseArgs is the Args validator for the non-interspersed mouse
// commands. Parsing the trailing flags here (Args runs before
// PersistentPreRunE) means global flags written after the coordinates are seen
// by --dry-run/--format enforcement as well as by the command body.
func validateHIDMouseArgs(cmd *cobra.Command, args []string) error {
	pos, err := trailingFlagArgs(cmd, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return output.NewCodedError("USAGE", fmt.Sprintf("accepts 2 arg(s), received %d", len(pos)))
	}
	return nil
}

// isNegativeNumber reports whether s is a numeric literal with a leading minus
// (e.g. "-5", "-3.5"), which must be treated as a positional delta rather than
// a flag.
func isNegativeNumber(s string) bool {
	if len(s) < 2 || s[0] != '-' {
		return false
	}
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

func init() {
	hidKeyCmd.Flags().BoolVar(&hidKeyTap, "tap", false, "Tap the key (default)")
	hidKeyCmd.Flags().BoolVar(&hidKeyDown, "down", false, "Hold the key down")
	hidKeyCmd.Flags().BoolVar(&hidKeyUp, "up", false, "Release the key")

	hidTypeCmd.Flags().StringVar(&hidTypeFile, "file", "", "Read the text to type from a file")
	hidTypeCmd.Flags().BoolVar(&hidTypeEnter, "enter", false, "Append an Enter (newline) after typing")
	hidTypeCmd.Flags().StringVar(&hidTypeKeymap, "keymap", "", "Keyboard layout keymap (default: en-us)")

	hidPrintCmd.Flags().StringVar(&hidPrintKeymap, "keymap", "en-us", "Keyboard layout keymap")

	hidMouseMoveCmd.Flags().BoolVar(&hidMouseAbsolute, "absolute", false, "Treat x,y as absolute pixel coordinates (default)")
	hidMouseMoveCmd.Flags().BoolVar(&hidMouseRelative, "relative", false, "Treat x,y as relative deltas")
	hidMouseMoveCmd.Flags().BoolVar(&hidMousePct, "pct", false, "Treat x,y as percentages (0..100)")
	// Negative coordinates (common for relative moves) would otherwise be
	// mistaken for flags, so stop flag parsing at the first positional arg.
	hidMouseMoveCmd.Flags().SetInterspersed(false)
	hidMouseWheelCmd.Flags().SetInterspersed(false)
	hidMouseClickCmd.Flags().StringVar(&hidMouseClickAt, "at", "", "Move to absolute X,Y (pixels) before clicking")
	hidMouseClickCmd.Flags().BoolVar(&hidMouseClickPct, "at-pct", false, "Treat --at as percentages (0..100)")

	// Every HID injection command mutates the target's input state.
	for _, c := range []*cobra.Command{
		hidKeyCmd,
		hidComboCmd,
		hidTypeCmd,
		hidPrintCmd,
		hidSetMouseOutputCmd,
		hidMouseMoveCmd,
		hidMouseClickCmd,
		hidMouseDownCmd,
		hidMouseUpCmd,
		hidMouseWheelCmd,
	} {
		MarkWrite(c)
	}

	hidMouseCmd.AddCommand(hidMouseMoveCmd, hidMouseClickCmd, hidMouseDownCmd, hidMouseUpCmd, hidMouseWheelCmd)
	hidCmd.AddCommand(hidStatusCmd, hidKeyCmd, hidComboCmd, hidTypeCmd, hidPrintCmd, hidKeysCmd, hidSetMouseOutputCmd, hidMouseCmd)
	rootCmd.AddCommand(hidCmd)
}

var hidCmd = &cobra.Command{
	Use:   "hid",
	Short: "Keyboard and mouse injection over the HID WebSocket",
	Long: `Control the target's keyboard and mouse through the device's HID WebSocket
channel (/api/ws).

Keys use the DOM KeyboardEvent.code names: KeyA..KeyZ, Digit0..Digit9, Enter,
Escape, Tab, Space, Backspace, ArrowUp/Down/Left/Right, F1..F24,
ControlLeft/Right, ShiftLeft/Right, AltLeft/Right, MetaLeft/Right, and the
common punctuation/navigation keys. Friendly aliases (ctrl, alt, del, caps,
pgup, ...) are accepted too.

Most subcommands open a short-lived WebSocket, send the event, and close.
'hid status' reads GET /api/hid and is the way to verify that an injected key
was delivered (CapsLock toggles the reported caps LED).`,
	Example: `  kvm-cli hid status
  kvm-cli hid key Enter
  kvm-cli hid combo ctrl+alt+del
  kvm-cli hid type "hello world" --enter
  kvm-cli hid mouse move 1000 500
  kvm-cli hid mouse click left
  kvm-cli hid set-mouse-output usb_rel`,
}
