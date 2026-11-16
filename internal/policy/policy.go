// Package policy turns a version policy ("lts-or-n-1", "n-2", "pin:16", ...) and a component's
// release lines into a concrete target version.
package policy

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"owl/internal/catalog"
	"owl/internal/upstream"
)

// Default is the global default: the newest LTS line if the product has one, otherwise the
// previous release line (n-1).
const Default = "lts-or-n-1"

// Presets are offered in the UI; pin:<version> is free-form.
var Presets = []string{"lts-or-n-1", "lts", "latest", "n-1", "n-2", "pin:<version>"}

type Spec struct {
	Raw  string
	Kind string // latest | n | lts | lts-or-n | pin
	N    int    // for n / lts-or-n
	Pin  string // for pin
}

func Parse(s string) (Spec, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	sp := Spec{Raw: s}
	switch {
	case s == "latest":
		sp.Kind = "latest"
	case s == "lts":
		sp.Kind, sp.N = "lts", 1 // falls back to n-1 when a product has no LTS line
	case strings.HasPrefix(s, "lts-or-n-"):
		n, err := strconv.Atoi(strings.TrimPrefix(s, "lts-or-n-"))
		if err != nil || n < 0 || n > 10 {
			return sp, fmt.Errorf("invalid policy %q", s)
		}
		sp.Kind, sp.N = "lts-or-n", n
	case strings.HasPrefix(s, "n-"):
		n, err := strconv.Atoi(strings.TrimPrefix(s, "n-"))
		if err != nil || n < 0 || n > 10 {
			return sp, fmt.Errorf("invalid policy %q", s)
		}
		sp.Kind, sp.N = "n", n
	case strings.HasPrefix(s, "pin:"):
		p := strings.TrimSpace(strings.TrimPrefix(s, "pin:"))
		if _, ok := catalog.ParseVersion(p, ""); !ok {
			return sp, fmt.Errorf("invalid pinned version %q", p)
		}
		sp.Kind, sp.Pin = "pin", strings.TrimPrefix(p, "v")
	default:
		return sp, fmt.Errorf("unknown policy %q (use one of %s)", s, strings.Join(Presets, ", "))
	}
	return sp, nil
}

// Target is what a component should run under a policy.
type Target struct {
	Policy  string `json:"policy"`
	Line    string `json:"line"`    // release line name, e.g. "1.36" or "17"
	Version string `json:"version"` // latest patch of that line (or the exact pin)
	Note    string `json:"note,omitempty"`
}

// UsableLines are lines with a known latest version, newest first. Resolve and the scorer both index
// into this list, so "n-1" and "lines behind" always count the same lines.
func UsableLines(lines []upstream.ReleaseLine) []upstream.ReleaseLine {
	var ls []upstream.ReleaseLine
	for _, l := range lines {
		if l.Latest != "" {
			ls = append(ls, l)
		}
	}
	return ls
}

// IsEOL: flagged EOL, or its EOL date has passed (cached data may predate the flag flipping).
func IsEOL(l upstream.ReleaseLine, now time.Time) bool {
	return l.EOL || (l.EOLDate != nil && !l.EOLDate.After(now))
}

// Position returns where v sits in usable lines (newest first): exact=true with that index if v's
// line is listed; otherwise the index v's line would occupy (len(ls) = older than all known lines).
// Lines are named by major ("17") or major.minor ("1.36").
func Position(v catalog.Version, ls []upstream.ReleaseLine) (idx int, exact bool) {
	pos := 0
	for i, l := range ls {
		lv, ok := catalog.ParseVersion(l.Name, "")
		if !ok {
			continue
		}
		if lv.Major == v.Major && (lv.Parts == 1 || lv.Minor == v.Minor) {
			return i, true
		}
		if lv.Major > v.Major || (lv.Major == v.Major && lv.Parts > 1 && lv.Minor > v.Minor) {
			pos = i + 1 // this line is newer than v
		}
	}
	return pos, false
}

// Resolve picks the target. lines are newest first (as upstream.Info.Lines). ok=false when there is
// nothing to resolve against (no usable release data). EOL lines are never chosen as an LTS target;
// if n-k lands on an EOL line, the target says so in Note.
func Resolve(sp Spec, lines []upstream.ReleaseLine, now time.Time) (Target, bool) {
	ls := UsableLines(lines)
	t := Target{Policy: sp.Raw}
	pick := func(l upstream.ReleaseLine) (Target, bool) {
		t.Line, t.Version = l.Name, l.Latest
		if IsEOL(l, now) {
			t.Note = strings.TrimPrefix(t.Note+"; target line "+l.Name+" is end-of-life", "; ")
		}
		return t, true
	}
	nth := func(n int) (Target, bool) {
		if len(ls) == 0 {
			return t, false
		}
		if n >= len(ls) {
			n = len(ls) - 1
			t.Note = fmt.Sprintf("only %d release lines known; using the oldest", len(ls))
		}
		return pick(ls[n])
	}

	switch sp.Kind {
	case "latest":
		return nth(0)
	case "n":
		return nth(sp.N)
	case "lts", "lts-or-n":
		for _, l := range ls {
			if l.LTS && !IsEOL(l, now) {
				return pick(l)
			}
		}
		t.Note = fmt.Sprintf("no supported LTS line; using n-%d", sp.N)
		return nth(sp.N)
	case "pin":
		if len(ls) == 0 {
			return t, false
		}
		for _, l := range ls {
			if l.Name == sp.Pin { // "pin:16" / "pin:1.11" = that line, latest patch
				return pick(l)
			}
		}
		pv, _ := catalog.ParseVersion(sp.Pin, "")
		t.Version = sp.Pin
		if i, exact := Position(pv, ls); exact {
			t.Line = ls[i].Name
		}
		return t, true
	}
	return t, false
}
