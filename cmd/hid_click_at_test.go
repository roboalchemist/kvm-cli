package cmd

import (
	"testing"

	"github.com/roboalchemist/kvm-cli/pkg/output"
)

func TestParseHIDPoint(t *testing.T) {
	tests := []struct {
		in      string
		x, y    int
		wantErr bool
	}{
		{"961,803", 961, 803, false},
		{" 10 , 20 ", 10, 20, false},
		{"0,0", 0, 0, false},
		{"-5,-10", -5, -10, false},
		{"1", 0, 0, true},
		{"1,2,3", 0, 0, true},
		{"a,2", 0, 0, true},
		{"1,b", 0, 0, true},
	}
	for _, tt := range tests {
		x, y, err := parseHIDPoint(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseHIDPoint(%q): want error, got %d,%d", tt.in, x, y)
			} else if output.ErrorCode(err) != "USAGE" {
				t.Errorf("parseHIDPoint(%q): code = %q, want USAGE", tt.in, output.ErrorCode(err))
			}
			continue
		}
		if err != nil {
			t.Errorf("parseHIDPoint(%q): unexpected error %v", tt.in, err)
			continue
		}
		if x != tt.x || y != tt.y {
			t.Errorf("parseHIDPoint(%q) = %d,%d want %d,%d", tt.in, x, y, tt.x, tt.y)
		}
	}
}

func TestHIDMouseClickAtFlagsRegistered(t *testing.T) {
	for _, name := range []string{"at", "at-pct"} {
		if hidMouseClickCmd.Flags().Lookup(name) == nil {
			t.Errorf("hid mouse click is missing --%s", name)
		}
	}
}
