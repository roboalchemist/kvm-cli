package cmd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/roboalchemist/kvm-cli/pkg/redact"
	"github.com/roboalchemist/kvm-cli/pkg/scratch"
	"github.com/roboalchemist/kvm-cli/pkg/vnc"
	"github.com/spf13/cobra"
)

// vnc flags (persistent on the vnc group).
var (
	flagVNCHost     string
	flagVNCUser     string
	flagVNCPassword string
	flagVNCTimeout  time.Duration

	// flagVNCAddr is the --vnc HOST:PORT integration flag exposed on
	// screenshot and cua so they can run over VNC instead of the KVM.
	flagVNCAddr string
)

// vncEndpoint resolves the target address and credentials using the precedence
// flag > environment. The address defaults to port 5900 when no port is given.
func vncEndpoint() (string, vnc.Credentials) {
	host := cuaFirstNonEmpty(flagVNCAddr, flagVNCHost, os.Getenv("KVM_VNC_HOST"), os.Getenv("VNC_HOST"))
	user := cuaFirstNonEmpty(flagVNCUser, os.Getenv("KVM_VNC_USERNAME"), os.Getenv("VNC_USERNAME"))
	pass := cuaFirstNonEmpty(flagVNCPassword, os.Getenv("KVM_VNC_PASSWORD"), os.Getenv("VNC_PASSWORD"))
	if host != "" && !strings.Contains(host, ":") {
		host += ":5900"
	}
	return host, vnc.Credentials{Username: user, Password: pass}
}

// vncTargetConfigured reports whether a VNC target was selected via --vnc/-host
// or the environment.
func vncTargetConfigured() bool {
	host, _ := vncEndpoint()
	return strings.TrimSpace(host) != ""
}

// VNCScreenshot captures a framebuffer over VNC using the resolved endpoint.
func VNCScreenshot(ctx context.Context) ([]byte, int, int, error) {
	c, err := vncDial()
	if err != nil {
		return nil, 0, 0, err
	}
	defer func() { _ = c.Close() }()
	return c.Screenshot(ctx)
}

// registerVNCFlags adds the --vnc/--vnc-username/--vnc-password integration
// flags to a command (used by screenshot and cua).
func registerVNCFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&flagVNCAddr, "vnc", "", "Drive a VNC target HOST[:PORT] instead of the KVM (env: KVM_VNC_HOST, VNC_HOST)")
	cmd.Flags().StringVar(&flagVNCUser, "vnc-username", "", "VNC username for ARD auth (env: KVM_VNC_USERNAME, VNC_USERNAME)")
	cmd.Flags().StringVar(&flagVNCPassword, "vnc-password", "", "VNC password (env: KVM_VNC_PASSWORD, VNC_PASSWORD)")
}

// vncRequireHost returns a coded error when no VNC host was configured.
func vncRequireHost(host string) error {
	if strings.TrimSpace(host) == "" {
		return output.NewCodedError("USAGE",
			"no VNC host: pass --host HOST[:PORT] or set KVM_VNC_HOST/VNC_HOST")
	}
	return nil
}

// vncDial connects using the resolved endpoint.
func vncDial() (*vnc.Client, error) {
	host, creds := vncEndpoint()
	if err := vncRequireHost(host); err != nil {
		return nil, err
	}
	timeout := flagVNCTimeout
	if timeout <= 0 {
		timeout = flagTimeout
	}
	return vnc.Dial(context.Background(), host, vnc.Options{
		Credentials: creds, Timeout: timeout, Debug: flagDebug,
	})
}

// vncDo opens a VNC session, runs action, and closes it.
func vncDo(action func(*vnc.Client) error) error {
	c, err := vncDial()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return action(c)
}

// ---- command group ----------------------------------------------------------

var vncCmd = &cobra.Command{
	Use:   "vnc",
	Short: "Drive a machine over VNC (RFB): screenshot, mouse, and keyboard",
	Long: `Control a machine reachable over VNC as an alternative to the GL.iNet KVM.

This speaks the RFB protocol natively (no external tools): it captures the
framebuffer and injects pointer/keyboard events. Supported authentication:
None, VNC DES, and the Apple (ARD) Diffie-Hellman scheme used by macOS Screen
Sharing. Framebuffer encodings: RAW, CopyRect and Hextile.

Connection (flag > environment):
  --host / KVM_VNC_HOST / VNC_HOST         HOST or HOST:PORT (default port 5900)
  --username / KVM_VNC_USERNAME / VNC_USERNAME   (required for Apple ARD auth)
  --password / KVM_VNC_PASSWORD / VNC_PASSWORD

Examples:
  kvm-cli vnc --host 192.168.1.100:5901 --password "$PW" info
  kvm-cli vnc --host mac-remote.local --username user --password "$PW" screenshot
  kvm-cli vnc --host mini:5900 --password "$PW" mouse click left --at 490,358`,
	Example: `  kvm-cli vnc --host HOST:PORT --password "$PW" info
  kvm-cli vnc --host HOST:PORT --password "$PW" screenshot -o /tmp/v.png
  kvm-cli vnc --host HOST:PORT --password "$PW" mouse move 100 200
  kvm-cli vnc --host HOST:PORT --password "$PW" key enter`,
}

// ---- vnc info ---------------------------------------------------------------

var vncInfoCmd = &cobra.Command{
	Use:     "info",
	Short:   "Connect and print the desktop name and framebuffer size",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli vnc --host HOST:PORT --password \"$PW\" info",
	RunE: func(cmd *cobra.Command, args []string) error {
		host, _ := vncEndpoint()
		if err := vncRequireHost(host); err != nil {
			return err
		}
		var name string
		var w, h int
		err := vncDo(func(c *vnc.Client) error {
			name, w, h = c.DesktopName(), c.Width(), c.Height()
			return nil
		})
		if err != nil {
			return err
		}
		data := map[string]any{"host": redact.Params(host), "desktop_name": name, "width": w, "height": h}
		td := output.TableData{Headers: []string{"FIELD", "VALUE"}, Rows: [][]string{
			{"host", redact.Params(host)}, {"desktop_name", name},
			{"width", strconv.Itoa(w)}, {"height", strconv.Itoa(h)},
		}}
		return output.Render(td, data, GetOutputOptions())
	},
}

// ---- vnc screenshot ---------------------------------------------------------

var (
	flagVNCScreenshotOutput string
)

var vncScreenshotCmd = &cobra.Command{
	Use:   "screenshot",
	Short: "Capture the VNC framebuffer to a PNG file",
	Long: `Capture the remote framebuffer over VNC and write it to a PNG file.

By default the PNG is written to a unique, timestamped file under the scratch
directory (the OS temp dir unless --scratch-dir / KVM_SCRATCH_DIR / config
scratch_dir says otherwise) — never the current working directory. Use -o to
choose the path (use - for stdout).`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli vnc --host HOST:PORT --password "$PW" screenshot
  kvm-cli vnc --host HOST:PORT --password "$PW" screenshot -o /tmp/screen.png
  kvm-cli vnc --host HOST:PORT --password "$PW" screenshot -o - > /tmp/screen.png`,
	RunE: runVNCScreenshot,
}

type vncScreenshotInfo struct {
	Host   string `json:"host"`
	Path   string `json:"path"`
	Bytes  int    `json:"bytes"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

func runVNCScreenshot(cmd *cobra.Command, args []string) error {
	host, _ := vncEndpoint()
	if err := vncRequireHost(host); err != nil {
		return err
	}
	out := strings.TrimSpace(flagVNCScreenshotOutput)
	toStdout := out == "-"
	opts := GetOutputOptions()
	if toStdout && opts.Mode != output.ModeTable {
		return output.NewCodedError("USAGE",
			"-o - writes raw PNG to stdout and cannot be combined with a structured format")
	}

	var png []byte
	var w, h int
	err := vncDo(func(c *vnc.Client) error {
		var serr error
		png, w, h, serr = c.Screenshot(context.Background())
		return serr
	})
	if err != nil {
		return err
	}

	if toStdout {
		if _, werr := os.Stdout.Write(png); werr != nil {
			return fmt.Errorf("write png to stdout: %w", werr)
		}
		fmt.Fprintf(os.Stderr, "Wrote %d bytes of PNG to stdout\n", len(png))
		return nil
	}

	path := out
	if path == "" {
		dir, derr := ScratchDir()
		if derr != nil {
			return derr
		}
		path, derr = scratch.PathIn(dir, "vnc-screenshot", ".png")
		if derr != nil {
			return derr
		}
	}
	if err := os.WriteFile(path, png, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Fprintf(os.Stderr, "Saved %s (%d bytes, %dx%d)\n", path, len(png), w, h)
	info := vncScreenshotInfo{Host: redact.Params(host), Path: path, Bytes: len(png), Width: w, Height: h}
	if opts.Mode != output.ModeTable {
		return output.Render(output.TableData{}, info, opts)
	}
	return nil
}

// ---- vnc mouse --------------------------------------------------------------

var (
	flagVNCMouseAt  string
	flagVNCMousePct bool
)

var vncMouseCmd = &cobra.Command{
	Use:     "mouse",
	Short:   "Mouse control over VNC",
	Long:    "Move the pointer, click buttons, press/release, or scroll the wheel over VNC.",
	Example: "  kvm-cli vnc --host HOST:PORT --password \"$PW\" mouse click left --at 490,358",
}

var vncMouseMoveCmd = &cobra.Command{
	Use:     "move X Y",
	Short:   "Move the pointer to absolute pixel X,Y",
	Args:    cobra.ExactArgs(2),
	Example: "  kvm-cli vnc --host HOST:PORT --password \"$PW\" mouse move 100 200",
	RunE: func(cmd *cobra.Command, args []string) error {
		x, err := strconv.Atoi(args[0])
		if err != nil {
			return output.NewCodedError("USAGE", fmt.Sprintf("invalid x %q", args[0]))
		}
		y, err := strconv.Atoi(args[1])
		if err != nil {
			return output.NewCodedError("USAGE", fmt.Sprintf("invalid y %q", args[1]))
		}
		return vncDo(func(c *vnc.Client) error {
			if flagVNCMousePct {
				if x < 0 || x > 100 || y < 0 || y > 100 {
					return output.NewCodedError("USAGE", "percentage coordinates must be 0..100")
				}
				x = x * c.Width() / 100
				y = y * c.Height() / 100
			}
			return c.MoveMouse(x, y)
		})
	},
}

var vncMouseClickCmd = &cobra.Command{
	Use:   "click LEFT|MIDDLE|RIGHT|UP|DOWN",
	Short: "Click a mouse button (optionally moving first with --at)",
	Args:  cobra.ExactArgs(1),
	Example: `  kvm-cli vnc --host HOST:PORT --password "$PW" mouse click left
  kvm-cli vnc --host HOST:PORT --password "$PW" mouse click left --at 490,358`,
	RunE: func(cmd *cobra.Command, args []string) error {
		button := args[0]
		if _, ok := vncButtonName(button); !ok {
			return output.NewCodedError("USAGE", fmt.Sprintf("unknown mouse button %q", button))
		}
		if flagVNCMouseAt != "" && flagVNCMousePct {
			return output.NewCodedError("USAGE", "--at and --at-pct are mutually exclusive")
		}
		return vncDo(func(c *vnc.Client) error {
			if flagVNCMouseAt == "" {
				return c.Click(button)
			}
			x, y, err := parseHIDPoint(flagVNCMouseAt)
			if err != nil {
				return err
			}
			if flagVNCMousePct {
				x = x * c.Width() / 100
				y = y * c.Height() / 100
			}
			return c.ClickAt(x, y, button)
		})
	},
}

var vncMouseDownCmd = &cobra.Command{
	Use:     "down LEFT|MIDDLE|RIGHT",
	Short:   "Press a mouse button",
	Args:    cobra.ExactArgs(1),
	Example: "  kvm-cli vnc --host HOST:PORT --password \"$PW\" mouse down left",
	RunE: func(cmd *cobra.Command, args []string) error {
		return vncDo(func(c *vnc.Client) error { return c.MouseDown(args[0]) })
	},
}

var vncMouseUpCmd = &cobra.Command{
	Use:     "up LEFT|MIDDLE|RIGHT",
	Short:   "Release a mouse button",
	Args:    cobra.ExactArgs(1),
	Example: "  kvm-cli vnc --host HOST:PORT --password \"$PW\" mouse up left",
	RunE: func(cmd *cobra.Command, args []string) error {
		return vncDo(func(c *vnc.Client) error { return c.MouseUp(args[0]) })
	},
}

var vncMouseWheelCmd = &cobra.Command{
	Use:     "wheel DX DY",
	Short:   "Scroll the wheel (positive dy scrolls up)",
	Args:    cobra.ExactArgs(2),
	Example: "  kvm-cli vnc --host HOST:PORT --password \"$PW\" mouse wheel 0 -3",
	RunE: func(cmd *cobra.Command, args []string) error {
		dx, err := strconv.Atoi(args[0])
		if err != nil {
			return output.NewCodedError("USAGE", fmt.Sprintf("invalid dx %q", args[0]))
		}
		dy, err := strconv.Atoi(args[1])
		if err != nil {
			return output.NewCodedError("USAGE", fmt.Sprintf("invalid dy %q", args[1]))
		}
		return vncDo(func(c *vnc.Client) error { return c.Wheel(dx, dy) })
	},
}

// vncButtonName validates a button name.
func vncButtonName(name string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "left", "middle", "right", "up", "down":
		return strings.ToLower(name), true
	default:
		return "", false
	}
}

// ---- vnc key / type ---------------------------------------------------------

var (
	flagVNCTypeEnter bool
)

var vncKeyCmd = &cobra.Command{
	Use:   "key NAME",
	Short: "Press a key by name (e.g. enter, tab, escape, ctrl, f5)",
	Args:  cobra.ExactArgs(1),
	Example: `  kvm-cli vnc --host HOST:PORT --password "$PW" key enter
  kvm-cli vnc --host HOST:PORT --password "$PW" key ctrl+a`,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		// Support "ctrl+a" style combos.
		if strings.Contains(name, "+") {
			parts := strings.Split(name, "+")
			return vncDo(func(c *vnc.Client) error { return c.Combo(parts) })
		}
		if _, ok := vnc.KeySym(name); !ok {
			return output.NewCodedError("USAGE", fmt.Sprintf("unknown key %q", name))
		}
		return vncDo(func(c *vnc.Client) error { return c.KeyName(name) })
	},
}

var vncTypeCmd = &cobra.Command{
	Use:     "type TEXT",
	Short:   "Type a string",
	Args:    cobra.ExactArgs(1),
	Example: "  kvm-cli vnc --host HOST:PORT --password \"$PW\" type \"hello\" --enter",
	RunE: func(cmd *cobra.Command, args []string) error {
		text := args[0]
		if flagVNCTypeEnter {
			text += "\n"
		}
		return vncDo(func(c *vnc.Client) error { return c.Type(text) })
	},
}

func init() {
	vncCmd.PersistentFlags().StringVar(&flagVNCHost, "host", "", "VNC host or HOST:PORT (env: KVM_VNC_HOST, VNC_HOST)")
	vncCmd.PersistentFlags().StringVar(&flagVNCUser, "username", "", "VNC username (env: KVM_VNC_USERNAME, VNC_USERNAME)")
	vncCmd.PersistentFlags().StringVar(&flagVNCPassword, "password", "", "VNC password (env: KVM_VNC_PASSWORD, VNC_PASSWORD)")
	vncCmd.PersistentFlags().DurationVar(&flagVNCTimeout, "vnc-timeout", 15*time.Second, "VNC dial/read timeout")

	vncScreenshotCmd.Flags().StringVarP(&flagVNCScreenshotOutput, "output", "o", "", "Output PNG file, or - for stdout")

	vncMouseMoveCmd.Flags().BoolVar(&flagVNCMousePct, "pct", false, "Treat X,Y as percentages (0..100)")
	vncMouseClickCmd.Flags().StringVar(&flagVNCMouseAt, "at", "", "Move to absolute X,Y before clicking")
	vncMouseClickCmd.Flags().BoolVar(&flagVNCMousePct, "at-pct", false, "Treat --at as percentages (0..100)")

	vncTypeCmd.Flags().BoolVar(&flagVNCTypeEnter, "enter", false, "Append an Enter after typing")

	vncMouseCmd.AddCommand(vncMouseMoveCmd, vncMouseClickCmd, vncMouseDownCmd, vncMouseUpCmd, vncMouseWheelCmd)
	vncCmd.AddCommand(vncInfoCmd, vncScreenshotCmd, vncMouseCmd, vncKeyCmd, vncTypeCmd)
	rootCmd.AddCommand(vncCmd)
}
