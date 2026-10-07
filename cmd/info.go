package cmd

import (
	"sort"

	"github.com/roboalchemist/kvm-cli/pkg/api"
	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/spf13/cobra"
)

// infoCmd implements 'kvm-cli info': a read-only dump of GET /api/info.
var infoCmd = &cobra.Command{
	Use:   "info",
	Short: "Show device information (GET /api/info)",
	Long: `Show the remote KVM's information document: authentication state, system
(kvmd, kernel, platform, streamer), live health (CPU, memory, temperature,
network rates), and the list of enabled extras/daemons.

The default table is a human-readable summary. Use --json to emit the complete
decoded document exactly as the device returns it.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli info
  kvm-cli info --json
  kvm-cli info --json --fields system
  kvm-cli info --json --jq .system.kvmd.version`,
	RunE: runInfo,
}

func init() {
	rootCmd.AddCommand(infoCmd)
}

func runInfo(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return cliDeviceError(err)
	}

	var info api.Info
	if err := client.Get("/api/info", &info); err != nil {
		return cliDeviceError(err)
	}

	// JSON/YAML consumers get the full document; the modelled Info struct drops
	// unmodelled keys, so render the retained raw object.
	data := any(info.Raw)
	if info.Raw == nil {
		data = info
	}
	td := output.TableData{
		Headers: []string{"PROPERTY", "VALUE"},
		Rows:    infoRows(info.Raw),
	}
	return output.Render(td, data, GetOutputOptions())
}

// infoPropertyPaths are the curated scalar paths surfaced in table mode, in
// display order.
var infoPropertyPaths = [][]string{
	{"auth", "enabled"},
	{"system", "kvmd", "version"},
	{"system", "platform", "model"},
	{"system", "platform", "board"},
	{"system", "platform", "base"},
	{"system", "kernel", "system"},
	{"system", "kernel", "release"},
	{"system", "kernel", "machine"},
	{"system", "streamer", "app"},
	{"system", "streamer", "version"},
	{"health", "cpu", "percent"},
	{"health", "mem", "percent"},
	{"health", "mem", "total"},
	{"health", "mem", "available"},
	{"health", "temp", "cpu"},
	{"health", "throttling"},
	{"meta", "server", "host"},
}

// infoRows builds the human-readable key/value summary from the raw /api/info
// document.
func infoRows(raw api.RawMap) [][]string {
	m := map[string]any(raw)
	rows := make([][]string, 0, len(infoPropertyPaths)+8)
	for _, path := range infoPropertyPaths {
		if v, ok := cliGetPath(m, path...); ok {
			rows = append(rows, []string{joinPath(path), cliValueString(v)})
		}
	}

	// Summarise each extra/daemon as a single enabled/started line.
	if extras, ok := cliAsMap(m["extras"]); ok && len(extras) > 0 {
		names := make([]string, 0, len(extras))
		for name := range extras {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			d, ok := cliAsMap(extras[name])
			if !ok {
				rows = append(rows, []string{"extras." + name, cliValueString(extras[name])})
				continue
			}
			detail := "enabled=" + cliValueString(d["enabled"]) + " started=" + cliValueString(d["started"])
			rows = append(rows, []string{"extras." + name, detail})
		}
	}
	return rows
}

func joinPath(path []string) string {
	out := ""
	for i, p := range path {
		if i > 0 {
			out += "."
		}
		out += p
	}
	return out
}
