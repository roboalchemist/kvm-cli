package elements

import (
	"testing"

	"github.com/roboalchemist/kvm-cli/pkg/models"
)

func el(typ, content string, cx, cy float64, interactive bool) models.Element {
	return models.Element{
		Type:          typ,
		Content:       content,
		Interactivity: interactive,
		BBox:          [4]float64{cx - 10, cy - 5, cx + 10, cy + 5},
		Center:        [2]float64{cx, cy},
	}
}

func ids(ms []Match) []int {
	out := make([]int, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

func eqInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParseRegion(t *testing.T) {
	tests := []struct {
		in      string
		want    Region
		wantErr bool
	}{
		{"1,2,3,4", Region{1, 2, 3, 4}, false},
		{" 10, 20, 30, 40 ", Region{10, 20, 30, 40}, false},
		{"0,0,10,10", Region{0, 0, 10, 10}, false},
		{"1,2,3", Region{}, true},
		{"1,2,3,4,5", Region{}, true},
		{"a,2,3,4", Region{}, true},
		{"5,5,5,10", Region{}, true},  // x degenerate
		{"1,5,10,5", Region{}, true},  // y degenerate
		{"10,10,1,1", Region{}, true}, // inverted
	}
	for _, tt := range tests {
		got, err := ParseRegion(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseRegion(%q): want error, got %v", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRegion(%q): unexpected error %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseRegion(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParsePoint(t *testing.T) {
	p, err := ParsePoint(" 12 , 34 ")
	if err != nil || p == nil || p[0] != 12 || p[1] != 34 {
		t.Fatalf("ParsePoint valid: got %v err %v", p, err)
	}
	for _, in := range []string{"1", "1,2,3", "a,2"} {
		if _, err := ParsePoint(in); err == nil {
			t.Errorf("ParsePoint(%q): want error", in)
		}
	}
}

func TestRegionContains(t *testing.T) {
	r := Region{10, 10, 20, 20}
	if !r.Contains(10, 10) || !r.Contains(20, 20) || !r.Contains(15, 15) {
		t.Error("expected points inside")
	}
	if r.Contains(9, 15) || r.Contains(21, 15) || r.Contains(15, 21) {
		t.Error("expected points outside")
	}
	if r.Width() != 10 || r.Height() != 10 {
		t.Errorf("Width/Height = %v/%v", r.Width(), r.Height())
	}
}

func TestQueryHasSelector(t *testing.T) {
	if (Query{Index: -1}).HasSelector() {
		t.Error("empty query with Index -1 should not have a selector")
	}
	for _, q := range []Query{
		{Text: "x"}, {Regex: "x"}, {Region: &Region{0, 0, 1, 1}},
		{Interactive: true}, {Nearest: &[2]float64{1, 1}}, {Index: 0},
	} {
		if !q.HasSelector() {
			t.Errorf("query %+v should have a selector", q)
		}
	}
	if !(Query{Text: "x"}).HasContentSelector() || (Query{Index: 0}).HasContentSelector() {
		t.Error("HasContentSelector mismatch")
	}
}

func TestSelect(t *testing.T) {
	elems := []models.Element{
		el("text", "Sign in", 100, 100, true),
		el("text", "Next", 200, 100, true),
		el("icon", "", 300, 100, true),
		el("text", "next step", 400, 500, false),
		el("text", "Cancel", 500, 500, true),
	}
	tests := []struct {
		name string
		q    Query
		want []int
	}{
		{"substring case-insensitive", Query{Text: "next", Index: -1}, []int{1, 3}},
		{"exact", Query{Text: "Next", Exact: true, Index: -1}, []int{1}},
		{"regex", Query{Regex: "^next$", Index: -1}, []int{1}},
		{"region filter", Query{Region: &Region{300, 400, 600, 600}, Index: -1}, []int{3, 4}},
		{"interactive filter", Query{Text: "next", Interactive: true, Index: -1}, []int{1}},
		{"index picks nth match", Query{Text: "next", Index: 1}, []int{3}},
		{"index alone is element id", Query{Index: 2}, []int{2}},
		{"no criteria matches all", Query{Index: -1}, []int{0, 1, 2, 3, 4}},
	}
	for _, tt := range tests {
		got, err := Select(elems, tt.q)
		if err != nil {
			t.Errorf("%s: unexpected error %v", tt.name, err)
			continue
		}
		if !eqInts(ids(got), tt.want) {
			t.Errorf("%s: ids = %v, want %v", tt.name, ids(got), tt.want)
		}
	}
}

func TestSelectNearest(t *testing.T) {
	elems := []models.Element{
		el("text", "Next", 100, 100, true),
		el("text", "Next", 900, 900, true),
		el("text", "Next", 500, 500, true),
	}
	got, err := Select(elems, Query{Text: "Next", Nearest: &[2]float64{950, 950}, Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	if !eqInts(ids(got), []int{1, 2, 0}) {
		t.Fatalf("nearest order = %v, want [1 2 0]", ids(got))
	}
	if got[0].Distance == 0 || got[0].Distance > got[1].Distance {
		t.Errorf("distance ordering wrong: %+v", got)
	}
	// Index after nearest picks the nearest.
	got, err = Select(elems, Query{Text: "Next", Nearest: &[2]float64{950, 950}, Index: 0})
	if err != nil || !eqInts(ids(got), []int{1}) {
		t.Fatalf("nearest+index = %v err %v", ids(got), err)
	}
}

func TestSelectErrors(t *testing.T) {
	elems := []models.Element{el("text", "A", 1, 1, true)}
	if _, err := Select(elems, Query{Regex: "("}); err == nil {
		t.Error("invalid regex should error")
	}
	if _, err := Select(elems, Query{Index: 5}); err == nil {
		t.Error("out-of-range index should error")
	}
}
