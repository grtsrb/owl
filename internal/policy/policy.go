// Package policy turns a version policy ("lts-or-n-1", "n-2", "pin:16", ...) and a component's
// release lines into a concrete target version.
package policy

import (
	"fmt"
	"strconv"
	"strings"

	"service-inventory/internal/catalog"
	"service-inventory/internal/upstream"
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

// Resolve picks the target. lines are newest first (as upstream.Info.Lines). ok=false when there is
// nothing to resolve against (no release data).
func Resolve(sp Spec, lines []upstream.ReleaseLine) (Target, bool) {
	var ls []upstream.ReleaseLine
	for _, l := range lines {
		if l.Latest != "" {
			ls = append(ls, l)
		}
	}
	t := Target{Policy: sp.Raw}
	pick := func(l upstream.ReleaseLine) (Target, bool) {
		t.Line, t.Version = l.Name, l.Latest
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
			if l.LTS && !l.EOL {
				return pick(l)
			}
		}
		target, ok := nth(sp.N)
		if ok && target.Note == "" {
			target.Note = fmt.Sprintf("no supported LTS line; using n-%d", sp.N)
		}
		return target, ok
	case "pin":
		pv, _ := catalog.ParseVersion(sp.Pin, "")
		for _, l := range ls {
			if l.Name == sp.Pin { // "pin:16" / "pin:1.11" = that line, latest patch
				return pick(l)
			}
		}
		t.Version, t.Line = sp.Pin, LineOf(pv, ls)
		return t, true
	}
	return t, false
}

// LineOf returns the name of the release line containing v ("" if none matches). Lines are named
// by major ("17") or major.minor ("1.36").
func LineOf(v catalog.Version, lines []upstream.ReleaseLine) string {
	for _, l := range lines {
		lv, ok := catalog.ParseVersion(l.Name, "")
		if !ok {
			continue
		}
		if lv.Major == v.Major && (lv.Parts == 1 || lv.Minor == v.Minor) {
			return l.Name
		}
	}
	return ""
}
