package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/roboalchemist/kvm-cli/pkg/rustdesk"
	"github.com/spf13/cobra"
)

var (
	flagRustDeskConfigDir string
	flagRustDeskConnectID string
	flagRustDeskPassword  string
	flagRustDeskYes       bool
)

// rustdeskConfigDir resolves the config directory (flag > default location).
func rustdeskConfigDir() string {
	if strings.TrimSpace(flagRustDeskConfigDir) != "" {
		return flagRustDeskConfigDir
	}
	return rustdesk.DefaultConfigDir()
}

var rustdeskCmd = &cobra.Command{
	Use:   "rustdesk",
	Short: "Inspect and launch RustDesk sessions (config, peers, connect)",
	Long: `Manage local RustDesk state and launch RustDesk connections.

RustDesk has no headless screenshot/input API (its wire protocol is a proprietary
rendezvous + NaCl-authenticated video stream with no client library), so kvm-cli
does not re-implement it. Instead it reads RustDesk config and the address book,
reports service state, and launches connections. To *drive* a RustDesk session,
run it on a display and point kvm-cli's transports at that display — e.g. a
Linux host running RustDesk on :1 is drivable with 'kvm-cli vnc --host HOST:5901'.

Config files live at:
  Linux: ~/.config/rustdesk
  macOS: ~/Library/Preferences/com.carriez.RustDesk
Override with --config-dir. Passwords are never printed.`,
	Example: `  kvm-cli rustdesk info
  kvm-cli rustdesk peers
  kvm-cli rustdesk status
  kvm-cli rustdesk connect 191877719 --yes`,
}

// ---- rustdesk info ----------------------------------------------------------

type rustdeskInfoOutput struct {
	ConfigDir            string `json:"config_dir"`
	ID                   string `json:"id,omitempty"`
	HasID                bool   `json:"has_id"`
	RendezvousServer     string `json:"rendezvous_server,omitempty"`
	HasPermanentPassword bool   `json:"has_permanent_password"`
}

var rustdeskInfoCmd = &cobra.Command{
	Use:     "info",
	Short:   "Show the local RustDesk id, relay server, and password status",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli rustdesk info\n  kvm-cli rustdesk info --config-dir /tmp/rd",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := rustdeskConfigDir()
		cfg, err := rustdesk.ParseConfigDir(dir)
		if err != nil {
			return err
		}
		out := rustdeskInfoOutput{
			ConfigDir:            cfg.ConfigDir,
			ID:                   cfg.ID,
			HasID:                cfg.HasID,
			RendezvousServer:     cfg.RendezvousServer,
			HasPermanentPassword: cfg.HasPermanentPassword,
		}
		td := output.TableData{Headers: []string{"FIELD", "VALUE"}, Rows: [][]string{
			{"config_dir", out.ConfigDir},
			{"id", emptyDash(out.ID)},
			{"has_id", strconv.FormatBool(out.HasID)},
			{"rendezvous_server", emptyDash(out.RendezvousServer)},
			{"has_permanent_password", strconv.FormatBool(out.HasPermanentPassword)},
		}}
		return output.Render(td, out, GetOutputOptions())
	},
}

// ---- rustdesk id ------------------------------------------------------------

var rustdeskIDCmd = &cobra.Command{
	Use:   "id",
	Short: "Print the local RustDesk id (via the client, falling back to config)",
	Long: `Print the local RustDesk device id.

Newer RustDesk stores the id encrypted in the config, so the authoritative value
comes from the running client: this runs 'rustdesk --get-id' when the binary is
available and otherwise falls back to the plaintext 'id' in the config.`,
	Args:    cobra.NoArgs,
	Example: "  kvm-cli rustdesk id",
	RunE: func(cmd *cobra.Command, args []string) error {
		id := ""
		source := "config"
		if bin := rustdeskBinary(); bin != "" {
			if out, err := exec.Command(bin, "--get-id").Output(); err == nil {
				if s := strings.TrimSpace(string(out)); s != "" {
					id, source = firstNonEmptyLine(s), "client"
				}
			}
		}
		if id == "" {
			cfg, err := rustdesk.ParseConfigDir(rustdeskConfigDir())
			if err != nil {
				return err
			}
			id = cfg.ID
		}
		out := map[string]any{"id": id, "source": source}
		td := output.TableData{Headers: []string{"FIELD", "VALUE"}, Rows: [][]string{
			{"id", emptyDash(id)},
			{"source", source},
		}}
		return output.Render(td, out, GetOutputOptions())
	},
}

// firstNonEmptyLine returns the first non-blank line of s (rustdesk --get-id can
// print a trailing blank line).
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// ---- rustdesk peers ---------------------------------------------------------

var rustdeskPeersCmd = &cobra.Command{
	Use:     "peers",
	Short:   "List RustDesk address-book peers",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli rustdesk peers\n  kvm-cli rustdesk peers --json",
	RunE: func(cmd *cobra.Command, args []string) error {
		peers, err := rustdesk.Peers(rustdeskConfigDir())
		if err != nil {
			return err
		}
		type peersOut struct {
			Count int             `json:"count"`
			Peers []rustdesk.Peer `json:"peers"`
		}
		out := peersOut{Count: len(peers), Peers: peers}
		td := output.TableData{Headers: []string{"ID", "ALIAS", "HOSTNAME", "USERNAME", "PLATFORM"}}
		for _, p := range peers {
			td.Rows = append(td.Rows, []string{
				p.ID, emptyDash(p.Alias), emptyDash(p.Hostname), emptyDash(p.Username), emptyDash(p.Platform),
			})
		}
		td.Footer = fmt.Sprintf("config_dir: %s\ncount: %d", rustdeskConfigDir(), len(peers))
		return output.Render(td, out, GetOutputOptions())
	},
}

// ---- rustdesk status --------------------------------------------------------

var rustdeskStatusCmd = &cobra.Command{
	Use:     "status",
	Short:   "Report whether a local rustdesk process/service is running",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli rustdesk status\n  kvm-cli rustdesk status --json",
	RunE: func(cmd *cobra.Command, args []string) error {
		running := rustdeskProcessRunning()
		out := map[string]any{"running": running}
		td := output.TableData{Headers: []string{"FIELD", "VALUE"}, Rows: [][]string{
			{"running", strconv.FormatBool(running)},
		}}
		return output.Render(td, out, GetOutputOptions())
	},
}

// rustdeskProcessRunning reports whether a rustdesk process is present, using
// pgrep when available and falling back to a /proc-style scan.
func rustdeskProcessRunning() bool {
	if _, err := exec.LookPath("pgrep"); err == nil {
		if err := exec.Command("pgrep", "-x", "rustdesk").Run(); err == nil {
			return true
		}
		// Some setups run the binary as RustDesk (macOS) or rustdesk --service.
		if err := exec.Command("pgrep", "-f", "[Rr]ustdesk").Run(); err == nil {
			return true
		}
	}
	return false
}

// ---- rustdesk connect -------------------------------------------------------

var rustdeskConnectCmd = &cobra.Command{
	Use:   "connect ID",
	Short: "Launch a RustDesk session to a remote id (requires --yes)",
	Long: `Launch the local RustDesk client and initiate a connection to ID.

This starts the RustDesk GUI on the current display, pointed at the peer. It is
a write action: pass --yes (or -f/--force) to run it; --dry-run previews it.

Because RustDesk presents a normal window, the resulting session can be driven
with kvm-cli's screenshot/cua transports (or VNC) once it is on a display.`,
	Args: cobra.ExactArgs(1),
	Example: `  kvm-cli rustdesk connect 191877719 --yes
  kvm-cli rustdesk connect 191877719 --password "$PW" --yes
  kvm-cli rustdesk connect 191877719 --dry-run`,
	RunE: runRustDeskConnect,
}

func runRustDeskConnect(cmd *cobra.Command, args []string) error {
	id := strings.TrimSpace(args[0])
	if id == "" {
		return output.NewCodedError("USAGE", "id must not be empty")
	}
	bin := rustdeskBinary()
	if bin == "" {
		return output.NewCodedError("DEVICE_ERROR",
			"rustdesk binary not found; install RustDesk or set it on PATH")
	}
	// Build the argument vector; the password (if any) is passed to rustdesk
	// but never echoed by kvm-cli.
	rdArgs := []string{"--connect", id}
	if strings.TrimSpace(flagRustDeskPassword) != "" {
		rdArgs = append(rdArgs, "--password", flagRustDeskPassword)
	}
	if flagDryRun {
		out := map[string]any{"dry_run": true, "binary": bin, "id": id, "message": "would launch rustdesk --connect " + id}
		td := output.TableData{Headers: []string{"ACTION"}, Rows: [][]string{{"dry run: " + bin + " --connect " + id}}}
		return output.Render(td, out, GetOutputOptions())
	}
	if err := vmRequireYes(flagRustDeskYes, "launch a RustDesk connection"); err != nil {
		return err
	}
	c := exec.Command(bin, rdArgs...)
	// Detach stdio so the GUI runs independently of kvm-cli.
	c.Stdout = os.Stderr
	c.Stderr = os.Stderr
	if err := c.Start(); err != nil {
		return fmt.Errorf("launch rustdesk: %w", err)
	}
	out := map[string]any{"launched": true, "pid": c.Process.Pid, "id": id}
	td := output.TableData{Headers: []string{"FIELD", "VALUE"}, Rows: [][]string{
		{"launched", "true"},
		{"pid", strconv.Itoa(c.Process.Pid)},
		{"id", id},
	}}
	return output.Render(td, out, GetOutputOptions())
}

// rustdeskBinary locates the RustDesk executable.
func rustdeskBinary() string {
	if p, err := exec.LookPath("rustdesk"); err == nil {
		return p
	}
	if p, err := exec.LookPath("RustDesk"); err == nil {
		return p
	}
	for _, cand := range []string{
		"/Applications/RustDesk.app/Contents/MacOS/RustDesk",
		"/usr/bin/rustdesk",
		"/usr/share/rustdesk/rustdesk",
	} {
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand
		}
	}
	return ""
}

func init() {
	rustdeskCmd.PersistentFlags().StringVar(&flagRustDeskConfigDir, "config-dir", "",
		"RustDesk config directory (default: platform location)")
	rustdeskConnectCmd.Flags().StringVar(&flagRustDeskPassword, "password", "",
		"Remote RustDesk password (passed to rustdesk; never printed)")
	vmRegisterConfirm(rustdeskConnectCmd, &flagRustDeskYes, "Confirm launching a RustDesk connection")

	rustdeskCmd.AddCommand(rustdeskInfoCmd, rustdeskIDCmd, rustdeskPeersCmd, rustdeskStatusCmd, rustdeskConnectCmd)
	rootCmd.AddCommand(rustdeskCmd)
}
