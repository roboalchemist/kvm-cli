package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestManNestedSubcommands is the regression: `man` must accept a nested
// subcommand path (e.g. "hid key") and emit a roff page for the resolved
// command. Before the fix the command used cobra.MaximumNArgs(1), so a
// two-word path failed with "accepts at most 1 arg(s)".
func TestManNestedSubcommands(t *testing.T) {
	for _, path := range [][]string{
		{"hid", "key"},
		{"msd", "format"},
		{"config", "get"},
		{"hid", "mouse", "click"},
	} {
		found, _, err := rootCmd.Find(path)
		if err != nil {
			t.Fatalf("Find(%v): %v", path, err)
		}
		if got := found.Name(); got != path[len(path)-1] {
			t.Fatalf("Find(%v) resolved %q, want %q", path, got, path[len(path)-1])
		}

		var buf bytes.Buffer
		if err := renderManPage(&buf, found); err != nil {
			t.Fatalf("renderManPage(%v): %v", path, err)
		}
		if out := buf.String(); !strings.Contains(out, ".SH SYNOPSIS") {
			t.Errorf("man %v: output missing SYNOPSIS section", path)
		}
	}

	// The man command itself must accept a multi-word path. This is the exact
	// validation that rejected `man hid key` before the fix.
	if err := manCmd.Args(manCmd, []string{"hid", "key"}); err != nil {
		t.Fatalf("man no longer accepts a nested path: %v", err)
	}
}

// TestManPreservesPositionalMetavar verifies the SYNOPSIS keeps required
// positional arguments (). go-md2man strips "<key>" as if it were an HTML
// tag, so Use strings use bare uppercase metavars instead.
func TestManPreservesPositionalMetavar(t *testing.T) {
	found, _, err := rootCmd.Find([]string{"config", "get"})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := renderManPage(&buf, found); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	if !strings.Contains(out, "KEY") {
		t.Errorf("config get SYNOPSIS lost its positional metavar:\n%s", out)
	}
	if strings.Contains(out, "<key>") || strings.Contains(out, "get  [flags]") {
		t.Errorf("config get SYNOPSIS still has the broken form:\n%s", out)
	}

	// No command should ship a Use string with angle-bracket metavars, which
	// go-md2man silently drops from man SYNOPSIS output.
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if strings.ContainsAny(c.Use, "<>") {
			t.Errorf("%s: Use %q contains angle brackets (dropped by go-md2man)", c.CommandPath(), c.Use)
		}
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(rootCmd)
}
