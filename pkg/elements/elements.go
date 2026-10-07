// Package elements selects grounded UI elements (from pkg/models) with
// deterministic, agent-friendly criteria — OCR text, regex, screen region,
// interactivity, and nearest-point — so agents do not need ad-hoc jq/python
// filters to pick an element to act on.
package elements

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/roboalchemist/kvm-cli/pkg/models"
)

// Region is a source-pixel rectangle. Membership is tested against an
// element's center point.
type Region struct {
	X1, Y1, X2, Y2 float64
}

// Contains reports whether (x,y) lies inside the region (inclusive edges).
func (r Region) Contains(x, y float64) bool {
	return x >= r.X1 && x <= r.X2 && y >= r.Y1 && y <= r.Y2
}

// Width returns the region width.
func (r Region) Width() float64 { return r.X2 - r.X1 }

// Height returns the region height.
func (r Region) Height() float64 { return r.Y2 - r.Y1 }

// ParseRegion parses "x1,y1,x2,y2" (whitespace tolerated) into a Region. The
// rectangle must be non-degenerate (x2>x1 and y2>y1).
func ParseRegion(s string) (Region, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return Region{}, fmt.Errorf("region must be x1,y1,x2,y2 (got %q)", strings.TrimSpace(s))
	}
	var v [4]float64
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return Region{}, fmt.Errorf("region value %q is not a number", strings.TrimSpace(p))
		}
		v[i] = f
	}
	r := Region{X1: v[0], Y1: v[1], X2: v[2], Y2: v[3]}
	if r.X2 <= r.X1 || r.Y2 <= r.Y1 {
		return Region{}, fmt.Errorf("region must have x2>x1 and y2>y1 (got %q)", strings.TrimSpace(s))
	}
	return r, nil
}

// ParsePoint parses "x,y" into a coordinate pair.
func ParsePoint(s string) (*[2]float64, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return nil, fmt.Errorf("point must be x,y (got %q)", strings.TrimSpace(s))
	}
	var p [2]float64
	for i, v := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return nil, fmt.Errorf("point value %q is not a number", strings.TrimSpace(v))
		}
		p[i] = f
	}
	return &p, nil
}

// Query describes a deterministic element selection. Zero-valued fields impose
// no constraint.
type Query struct {
	Text        string      // case-insensitive substring of content
	Exact       bool        // require full case-insensitive equality with Text
	Regex       string      // case-insensitive RE2 match against content
	Region      *Region     // require element center inside
	Interactive bool        // require Interactivity
	Nearest     *[2]float64 // sort matches by ascending distance to this point
	Index       int         // -1 = unset; else pick the Index-th match
}

// HasSelector reports whether q expresses any selection criterion (including a
// bare positional pick via Index).
func (q Query) HasSelector() bool {
	return strings.TrimSpace(q.Text) != "" || strings.TrimSpace(q.Regex) != "" ||
		q.Region != nil || q.Interactive || q.Nearest != nil || q.Index >= 0
}

// HasContentSelector reports whether q constrains by content text or regex.
func (q Query) HasContentSelector() bool {
	return strings.TrimSpace(q.Text) != "" || strings.TrimSpace(q.Regex) != ""
}

// Match is a selected element and its grounding id (index in the grounded
// element list).
type Match struct {
	ID       int
	Element  models.Element
	Distance float64
}

// Select applies q to elems and returns the matches in document order, or — when
// q.Nearest is set — sorted by ascending distance to the point. An out-of-range
// Index or an invalid Regex is an error.
func Select(elems []models.Element, q Query) ([]Match, error) {
	re, err := compileQuery(q)
	if err != nil {
		return nil, err
	}
	matches := make([]Match, 0, len(elems))
	for i, e := range elems {
		if !matchesElement(e, q, re) {
			continue
		}
		m := Match{ID: i, Element: e}
		if q.Nearest != nil {
			m.Distance = math.Hypot(e.Center[0]-q.Nearest[0], e.Center[1]-q.Nearest[1])
		}
		matches = append(matches, m)
	}
	if q.Nearest != nil {
		sort.SliceStable(matches, func(i, j int) bool { return matches[i].Distance < matches[j].Distance })
	}
	if q.Index >= 0 {
		if q.Index >= len(matches) {
			return nil, fmt.Errorf("index %d out of range: %d match(es)", q.Index, len(matches))
		}
		return matches[q.Index : q.Index+1], nil
	}
	return matches, nil
}

func compileQuery(q Query) (*regexp.Regexp, error) {
	if strings.TrimSpace(q.Regex) != "" {
		re, err := regexp.Compile("(?i)" + q.Regex)
		if err != nil {
			return nil, fmt.Errorf("invalid regex %q: %w", q.Regex, err)
		}
		return re, nil
	}
	return nil, nil
}

func matchesElement(e models.Element, q Query, re *regexp.Regexp) bool {
	if q.Interactive && !e.Interactivity {
		return false
	}
	if q.Region != nil && !q.Region.Contains(e.Center[0], e.Center[1]) {
		return false
	}
	if re != nil {
		return re.MatchString(e.Content)
	}
	if t := strings.TrimSpace(q.Text); t != "" {
		if q.Exact {
			return strings.EqualFold(strings.TrimSpace(e.Content), t)
		}
		return strings.Contains(strings.ToLower(e.Content), strings.ToLower(t))
	}
	return true
}
