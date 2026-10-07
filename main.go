package main

import (
	"embed"
	"os"

	"github.com/roboalchemist/kvm-cli/cmd"
)

// version is set via ldflags at build time: -X main.version=x.y.z
var version = "dev"

//go:embed README.md
var readmeContents string

//go:embed skill/SKILL.md
var skillMD string

//go:embed skill/reference/commands.md
var commandsRef string

//go:embed skill
var skillFS embed.FS

func main() {
	cmd.SetVersion(version)
	cmd.SetReadmeContents(readmeContents)
	cmd.SetSkillData(skillMD, commandsRef, skillFS)
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
