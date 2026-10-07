package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var readmeContents string

// SetReadmeContents sets the README content embedded from main. It is called
// from main.go so that 'kvm-cli docs' can print the full documentation offline.
func SetReadmeContents(content string) {
	readmeContents = content
}

var docsCmd = &cobra.Command{
	Use:   "docs",
	Short: "Display the full documentation",
	Long: `Display the complete documentation from README.md.

The documentation is embedded in the binary at build time, so it is always
available offline and matches the installed version.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli docs                    # Display documentation
  kvm-cli docs | less             # View with pager
  kvm-cli docs > docs.md          # Save to a file`,
	RunE: func(cmd *cobra.Command, args []string) error {
		_, err := fmt.Print(readmeContents)
		return err
	},
}

func init() {
	rootCmd.AddCommand(docsCmd)
}
