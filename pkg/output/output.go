// Package output provides agent-forward rendering utilities for CLI output.
//
// It supports four output modes — table (default, for humans), JSON (for
// agents), plaintext (tab-separated, for piping) and YAML — plus JSON field
// projection (--fields) and jq filtering (--jq).
//
// Data is always written to stdout and errors to stderr so that agents can
// safely parse stdout while discarding stderr:
//
//	result := $(kvm-cli info --json 2>/dev/null)
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/roboalchemist/kvm-cli/pkg/redact"
	"gopkg.in/yaml.v3"
)

// Mode represents an output format.
type Mode int

const (
	// ModeTable renders a colored, aligned table for humans (default).
	ModeTable Mode = iota
	// ModeJSON renders pretty-printed JSON for agents.
	ModeJSON
	// ModePlaintext renders tab-separated values (no headers) for piping.
	ModePlaintext
	// ModeYAML renders YAML.
	ModeYAML
)

// String returns the canonical name of the mode.
func (m Mode) String() string {
	switch m {
	case ModeTable:
		return "table"
	case ModeJSON:
		return "json"
	case ModePlaintext:
		return "plaintext"
	case ModeYAML:
		return "yaml"
	default:
		return "unknown"
	}
}

// ParseMode converts a format string (table|json|plaintext|yaml) into a Mode.
// Unknown or empty values default to ModeTable.
func ParseMode(s string) Mode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "json", "j":
		return ModeJSON
	case "plaintext", "plain", "p":
		return ModePlaintext
	case "yaml", "yml":
		return ModeYAML
	case "table", "":
		return ModeTable
	default:
		return ModeTable
	}
}

// Options configures output rendering.
type Options struct {
	// Mode selects the output format.
	Mode Mode
	// NoColor disables ANSI colors even when writing to a terminal.
	NoColor bool
	// Debug enables verbose debug logging (interpreted by callers).
	Debug bool
	// Fields, when non-empty, projects JSON output to only the named keys.
	Fields []string
	// JQ, when non-empty, is a jq expression applied to JSON output after
	// Fields projection.
	JQ string
	// NoRedact disables the automatic secret-masking pass. It is the escape
	// hatch for callers that have already redacted their payload (or that are
	// rendering non-device data); the zero value keeps redaction enabled.
	NoRedact bool
}

// TableData holds the tabular representation of data for table and plaintext
// modes.
type TableData struct {
	Headers []string
	Rows    [][]string
	Footer  string
}

// Render renders td and data according to opts.Mode. Table and plaintext modes
// use td; JSON and YAML modes use data.
//
// Every render passes through a secret-masking layer unless opts.NoRedact is
// set: values whose field name names a secret (see pkg/redact) are replaced by
// "***" in table, JSON, plaintext and YAML output alike. This closes the
// read-path leak where a device echoes a secret (ssl_key, setup_key, authkey,
// ...) in a GET response.
func Render(td TableData, data any, opts Options) error {
	if !opts.NoRedact {
		td = redactTable(td)
		data = redact.Any(data)
	}
	switch opts.Mode {
	case ModeJSON:
		return renderJSON(os.Stdout, data, opts)
	case ModeYAML:
		return renderYAML(os.Stdout, data)
	case ModePlaintext:
		return renderPlaintext(os.Stdout, td)
	default:
		return renderTable(os.Stdout, td, opts)
	}
}

// RenderTable is an alias for Render, retained for commands written against the
// bmc-cli-style API where the table is the primary data shape.
func RenderTable(td TableData, data any, opts Options) error {
	return Render(td, data, opts)
}

// RenderJSON renders v as pretty-printed JSON to stdout, applying the secret
// filter, then --fields projection and --jq filtering when configured.
func RenderJSON(v any, opts Options) error {
	if !opts.NoRedact {
		v = redact.Any(v)
	}
	return renderJSON(os.Stdout, v, opts)
}

// RenderYAML renders v as YAML to stdout, applying the secret filter first.
func RenderYAML(v any, opts Options) error {
	if !opts.NoRedact {
		v = redact.Any(v)
	}
	return renderYAML(os.Stdout, v)
}

// redactTable masks the value columns of any two-or-more-column table whose
// first column names a secret. This is the table counterpart of redact.Any: the
// table rows are pre-built strings, so the field name is recovered from the
// first cell (a flattened dotted key such as "config.ssl_key" is understood by
// redact.IsSecretKey). Single-column tables (id lists, key lists) are left
// alone because they have no key/value pairing.
func redactTable(td TableData) TableData {
	if len(td.Rows) == 0 {
		return td
	}
	rows := make([][]string, len(td.Rows))
	for i, row := range td.Rows {
		if len(row) < 2 || !redact.IsSecretKey(row[0]) {
			rows[i] = row
			continue
		}
		masked := append([]string(nil), row...)
		for j := 1; j < len(masked); j++ {
			if masked[j] != "" && masked[j] != redact.Mask {
				masked[j] = redact.Mask
			}
		}
		rows[i] = masked
	}
	td.Rows = rows
	return td
}

func renderJSON(w io.Writer, v any, opts Options) error {
	value := v
	if len(opts.Fields) > 0 {
		value = ProjectFields(v, opts.Fields)
	}
	if strings.TrimSpace(opts.JQ) != "" {
		plain, err := normalizeJSON(value)
		if err != nil {
			return err
		}
		return runJQ(w, plain, opts.JQ)
	}
	return writeJSON(w, value)
}

func writeJSON(w io.Writer, v any) error {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("json marshal: %w", err)
	}
	if _, err := fmt.Fprintln(w, string(out)); err != nil {
		return fmt.Errorf("write json: %w", err)
	}
	return nil
}

func renderYAML(w io.Writer, v any) error {
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		_ = enc.Close()
		return fmt.Errorf("yaml encode: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("yaml close: %w", err)
	}
	return nil
}

func renderPlaintext(w io.Writer, td TableData) error {
	// Headers are intentionally omitted: plaintext output is meant for piping,
	// where only the values (one tab-separated record per line) are useful.
	for _, row := range td.Rows {
		if _, err := fmt.Fprintln(w, strings.Join(row, "\t")); err != nil {
			return fmt.Errorf("write plaintext: %w", err)
		}
	}
	return nil
}

func renderTable(w io.Writer, td TableData, opts Options) error {
	table := tablewriter.NewWriter(w)

	table.Configure(func(cfg *tablewriter.Config) {
		cfg.Header.Alignment.Global = tw.AlignLeft
		cfg.Row.Alignment.Global = tw.AlignLeft
		cfg.Header.Formatting.AutoFormat = tw.Off
	})

	// Minimal, borderless layout: columns separated by spaces only.
	table.Options(
		tablewriter.WithRendition(tw.Rendition{
			Borders: tw.Border{
				Left:   tw.Off,
				Right:  tw.Off,
				Top:    tw.Off,
				Bottom: tw.Off,
			},
			Settings: tw.Settings{
				Separators: tw.Separators{
					ShowHeader:     tw.Off,
					BetweenRows:    tw.Off,
					BetweenColumns: tw.On,
				},
				Lines: tw.Lines{
					ShowTop:        tw.Off,
					ShowBottom:     tw.Off,
					ShowHeaderLine: tw.Off,
					ShowFooterLine: tw.Off,
				},
			},
		}),
	)

	headers := append([]string(nil), td.Headers...)
	if shouldColor(opts) {
		for i, h := range headers {
			headers[i] = color.New(color.FgCyan, color.Bold).Sprint(h)
		}
	}
	table.Header(headers)

	for _, row := range td.Rows {
		if err := table.Append(row); err != nil {
			return fmt.Errorf("append table row: %w", err)
		}
	}
	if err := table.Render(); err != nil {
		return fmt.Errorf("render table: %w", err)
	}

	if td.Footer != "" {
		footer := td.Footer
		if shouldColor(opts) {
			footer = color.New(color.FgHiBlack).Sprint(footer)
		}
		if _, err := fmt.Fprintf(w, "\n%s\n", footer); err != nil {
			return fmt.Errorf("write footer: %w", err)
		}
	}
	return nil
}

// shouldColor reports whether ANSI colors should be used. Color is disabled
// when NoColor is set, when the NO_COLOR environment variable is non-empty, or
// when stdout is not a terminal (e.g. piper).
func shouldColor(opts Options) bool {
	if opts.NoColor {
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
