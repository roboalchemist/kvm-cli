package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

var manCmd = &cobra.Command{
	Use:   "man [COMMAND...]",
	Short: "Print the man page for kvm-cli or a subcommand",
	Long: `Print a roff(7) man page for kvm-cli or one of its subcommands to stdout.

With no argument the root man page is printed. A command path may span several
words (for example 'man hid key'). Pre-generated man pages for the entire
command tree are also available via 'make man'.`,
	Args: cobra.ArbitraryArgs,
	Example: `  kvm-cli man              # root man page
  kvm-cli man config       # man page for the config command
  kvm-cli man hid key      # man page for a nested subcommand
  kvm-cli man | man -l -   # view with man`,
	RunE: func(cmd *cobra.Command, args []string) error {
		target := rootCmd
		if len(args) > 0 {
			found, _, err := rootCmd.Find(args)
			if err != nil {
				return err
			}
			target = found
		}
		return renderManPage(cmd.OutOrStdout(), target)
	},
}

// renderManPage writes the cobra-generated man page for c to w.
func renderManPage(w io.Writer, c *cobra.Command) error {
	header := &doc.GenManHeader{
		Title:   "KVM-CLI",
		Section: "1",
		Source:  "kvm-cli " + appVersion,
		Manual:  "User Commands",
	}
	if err := doc.GenMan(c, header, w); err != nil {
		return fmt.Errorf("generate man page: %w", err)
	}
	return nil
}

func init() {
	rootCmd.AddCommand(manCmd)
}
