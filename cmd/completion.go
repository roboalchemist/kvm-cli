package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var completionCmd = &cobra.Command{
	Use:   "completion [bash|zsh|fish|powershell]",
	Short: "Generate shell completion scripts",
	Long: `Generate shell completion scripts for kvm-cli.

To load completions:

Bash:
  $ source <(kvm-cli completion bash)

  # To load completions for each session, execute once:
  # Linux:
  $ kvm-cli completion bash > /etc/bash_completion.d/kvm-cli
  # macOS:
  $ kvm-cli completion bash > $(brew --prefix)/etc/bash_completion.d/kvm-cli

Zsh:
  $ source <(kvm-cli completion zsh)

  # To load completions for each session, execute once:
  $ kvm-cli completion zsh > "${fpath[1]}/_kvm-cli"

Fish:
  $ kvm-cli completion fish | source

  # To load completions for each session, execute once:
  $ kvm-cli completion fish > ~/.config/fish/completions/kvm-cli.fish

PowerShell:
  PS> kvm-cli completion powershell | Out-String | Invoke-Expression
`,
	Example: `  kvm-cli completion bash
  kvm-cli completion zsh > "${fpath[1]}/_kvm-cli"
  kvm-cli completion fish | source
  kvm-cli completion powershell | Out-String | Invoke-Expression`,
	DisableFlagsInUseLine: true,
	ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
	Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
	RunE: func(cmd *cobra.Command, args []string) error {
		switch args[0] {
		case "bash":
			return cmd.Root().GenBashCompletionV2(os.Stdout, true)
		case "zsh":
			return cmd.Root().GenZshCompletion(os.Stdout)
		case "fish":
			return cmd.Root().GenFishCompletion(os.Stdout, true)
		case "powershell":
			return cmd.Root().GenPowerShellCompletionWithDesc(os.Stdout)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(completionCmd)
}
