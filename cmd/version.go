package cmd

import (
	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/spf13/cobra"
)

// versionInfo is the machine-readable payload for 'kvm-cli version'.
type versionInfo struct {
	Name    string `json:"name" yaml:"name"`
	Version string `json:"version" yaml:"version"`
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version",
	Long: `Print the kvm-cli version.

The output honours the global output flags, so it can be consumed by scripts
and agents using 'kvm-cli version --json' or 'kvm-cli version --format yaml'.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli version
  kvm-cli version --json
  kvm-cli version --format yaml
  kvm-cli version --json --fields version`,
	RunE: func(cmd *cobra.Command, args []string) error {
		data := versionInfo{Name: "kvm-cli", Version: appVersion}
		td := output.TableData{
			Headers: []string{"NAME", "VERSION"},
			Rows:    [][]string{{data.Name, data.Version}},
		}
		return output.Render(td, data, GetOutputOptions())
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
