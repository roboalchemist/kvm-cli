package cmd

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	embeddedSkillMD     string
	embeddedCommandsRef string
	// embeddedSkillFS is an fs.FS (an embed.FS at runtime, injected by main.go)
	// so tests can substitute a fstest.MapFS.
	embeddedSkillFS fs.FS
	// skillForce suppresses the 'skill add' overwrite warning.
	skillForce bool
)

// SetSkillData receives the embedded skill content and filesystem from main.go
// so the skill subcommands can print or install the embedded skill.
func SetSkillData(skillMD, commandsRef string, skillFS fs.FS) {
	embeddedSkillMD = skillMD
	embeddedCommandsRef = commandsRef
	embeddedSkillFS = skillFS
}

// skillInstallDir returns the directory 'skill add' installs into
// (~/.claude/skills/kvm-cli).
func skillInstallDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".claude", "skills", "kvm-cli")
	}
	return filepath.Join(home, ".claude", "skills", "kvm-cli")
}

var skillCmd = &cobra.Command{
	Use:   "skill",
	Short: "Manage the embedded Claude Code skill",
	Long: `Manage the Claude Code skill bundled with kvm-cli.

The skill lets Claude Code (and other agents) discover and use kvm-cli
without external documentation.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli skill print   # print SKILL.md to stdout
  kvm-cli skill path    # show the install directory
  kvm-cli skill add     # install to ~/.claude/skills/kvm-cli/`,
}

var skillPrintCmd = &cobra.Command{
	Use:     "print",
	Short:   "Print the embedded SKILL.md to stdout",
	Long:    "Print the embedded SKILL.md to stdout.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli skill print",
	RunE: func(cmd *cobra.Command, args []string) error {
		_, err := fmt.Print(embeddedSkillMD)
		return err
	},
}

var skillPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print the skill install directory",
	Long: `Print the directory where 'kvm-cli skill add' installs the embedded skill
(by default ~/.claude/skills/kvm-cli).`,
	Args:    cobra.NoArgs,
	Example: "  kvm-cli skill path\n  ls \"$(kvm-cli skill path)\"",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println(skillInstallDir())
		return nil
	},
}

// skillDirExists reports whether destDir already exists as a directory.
func skillDirExists(destDir string) bool {
	info, err := os.Stat(destDir)
	return err == nil && info.IsDir()
}

var skillAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Install the embedded skill to ~/.claude/skills/kvm-cli/",
	Long: `Install the embedded skill (SKILL.md and reference files) to
~/.claude/skills/kvm-cli/ so Claude Code can discover it.

When the destination directory already exists a warning is printed before it
is overwritten; pass --force to silence that warning.`,
	Args:    cobra.NoArgs,
	Example: "  kvm-cli skill add\n  kvm-cli skill add --force",
	RunE:    runSkillAdd,
}

func runSkillAdd(cmd *cobra.Command, args []string) error {
	destDir := skillInstallDir()
	if !skillForce && skillDirExists(destDir) {
		fmt.Fprintf(os.Stderr, "warning: %s already exists; overwriting (pass --force to silence this warning)\n", destDir)
	}

	err := fs.WalkDir(embeddedSkillFS, "skill", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("skill", path)
		dest := filepath.Join(destDir, rel)

		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}

		data, err := fs.ReadFile(embeddedSkillFS, path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0o644)
	})
	if err != nil {
		return fmt.Errorf("install skill: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Installed skill to %s\n", destDir)
	return nil
}

func init() {
	// 'skill add' writes files to the local filesystem.
	MarkWrite(skillAddCmd)
	skillAddCmd.Flags().BoolVarP(&skillForce, "force", "f", false,
		"Overwrite an existing skill directory without warning")

	skillCmd.AddCommand(skillPrintCmd, skillPathCmd, skillAddCmd)
	rootCmd.AddCommand(skillCmd)
}
