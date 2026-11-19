package upstream

import (
	"slices"
	"strings"

	"owl/internal/catalog"
)

type AffectedRange struct {
	Range   string `json:"range"`   // GitHub syntax: ">= 7.0.0, < 7.2.5", "<= 1.14.6", "All"
	Patched string `json:"patched"` // "1.14.7", "7.2.5, 7.4.1", "TBD", ""
	// Exact: an open-ended range that really means "affected, no fix yet" (OSV: introduced without a
	// fixed event). GitHub-repo ranges are often never closed after a fix, so theirs need review.
	Exact bool `json:"exact,omitempty"`
}

type Advisory struct {
	ID       string          `json:"id"`
	Aliases  []string        `json:"aliases,omitempty"`
	Severity string          `json:"severity"` // critical | high | medium/moderate | low | "" (unknown)
	Summary  string          `json:"summary"`
	URL      string          `json:"url"`
	Source   string          `json:"source"`
	Ranges   []AffectedRange `json:"ranges,omitempty"`  // github-repo advisories
	FixedIn  []string        `json:"fixedIn,omitempty"` // osv advisories
}

// Match is an advisory that applies (or may apply) to a specific running version.
type Match struct {
	Advisory
	// Certain is false when the advisory gives no usable range ("All", unparseable): shown as
	// "needs review", never counted as affected for priority.
	Certain bool `json:"certain"`
	// NoFix: affected, and upstream lists no fixed version (yet). Shown, but not something an
	// upgrade resolves.
	NoFix bool `json:"noFix,omitempty"`
	// RelevantFix is the fixed version that matters for this running version (see RelevantFix).
	RelevantFix string `json:"relevantFix,omitempty"`
}

// RelevantFix picks the fix that matters for v from a per-line list ("6.2.20, 7.2.11, 7.4.6"): the
// lowest fix above v in v's own major.minor line, else in its major, else the lowest fix above v.
// Fixes at or below v are never "relevant" (they can appear with multi-range data).
func RelevantFix(v catalog.Version, fixed []string) string {
	type cand struct {
		v catalog.Version
		s string
	}
	var sameMinor, sameMajor, any *cand
	pick := func(best **cand, c cand) {
		if *best == nil || c.v.Compare((*best).v) < 0 {
			cc := c
			*best = &cc
		}
	}
	for _, f := range fixed {
		fv, ok := catalog.ParseVersion(f, "")
		if !ok || compareToBound(v, fv) >= 0 {
			continue
		}
		c := cand{fv, f}
		if fv.Major == v.Major && fv.Minor == v.Minor {
			pick(&sameMinor, c)
		}
		if fv.Major == v.Major {
			pick(&sameMajor, c)
		}
		pick(&any, c)
	}
	for _, c := range []*cand{sameMinor, sameMajor, any} {
		if c != nil {
			return c.s
		}
	}
	return ""
}

// AdvisoriesFor returns the advisories affecting a component version. Both sources (OSV, GitHub
// repo advisories) are evaluated here against their ranges; no network calls.
func AdvisoriesFor(info *Info, version string) []Match {
	out := []Match{}
	v, ok := catalog.ParseVersion(version, "")
	if !ok || info == nil {
		return out
	}
	for _, a := range info.Advisories {
		affected, certain := false, true
		var fixed []string
		for _, r := range a.Ranges {
			patched := parsePatched(r.Patched)
			in, known := InRange(v, r.Range)
			if strings.EqualFold(strings.TrimSpace(r.Range), "all") && len(patched) > 0 {
				in, known = true, true // "All" + per-line patches: decided by the patch list below
			}
			if in && openEnded(r.Range) && !r.Exact {
				// Open-ended range (">= 7.0.0", "All"): many repos never close it after fixing, so the
				// patched list decides. Without one we can't tell -> needs review, not affected.
				if len(patched) == 0 {
					in, known = false, false
				} else if fixedByPatches(v, patched) {
					in = false
				}
			}
			if !known {
				certain = false
			}
			if in || !known {
				for _, p := range patched {
					if !slices.Contains(fixed, p.Original) {
						fixed = append(fixed, p.Original)
					}
				}
			}
			if in {
				affected = true
			}
		}
		if affected || (!certain && len(a.Ranges) > 0) {
			m := Match{Advisory: a, Certain: affected, NoFix: affected && len(fixed) == 0}
			m.FixedIn = fixed
			m.RelevantFix = RelevantFix(v, fixed)
			m.Ranges = nil // the matched version is what matters to callers
			out = append(out, m)
		}
	}
	return out
}

// InRange evaluates a GitHub advisory version range for v. known=false means the range doesn't
// describe versions in a usable way ("All", empty, unparseable).
func InRange(v catalog.Version, expr string) (in bool, known bool) {
	expr = strings.TrimSpace(expr)
	if expr == "" || strings.EqualFold(expr, "all") || expr == "*" {
		return false, false
	}
	for _, part := range strings.Split(expr, ",") {
		part = strings.TrimSpace(part)
		op := "="
		for _, o := range []string{">=", "<=", ">", "<", "="} {
			if strings.HasPrefix(part, o) {
				op, part = o, strings.TrimSpace(part[len(o):])
				break
			}
		}
		bound, ok := catalog.ParseVersion(part, "")
		if !ok {
			return false, false
		}
		c := compareToBound(v, bound)
		var holds bool
		switch op {
		case ">=":
			holds = c >= 0
		case "<=":
			holds = c <= 0
		case ">":
			holds = c > 0
		case "<":
			holds = c < 0
		case "=":
			holds = c == 0
		}
		if !holds {
			return false, true
		}
	}
	return true, true
}

func parsePatched(s string) []catalog.Version {
	var out []catalog.Version
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" || strings.EqualFold(p, "tbd") {
			continue
		}
		if v, ok := catalog.ParseVersion(p, ""); ok {
			out = append(out, v)
		}
	}
	return out
}

// fixedByPatches: patched lists name one fixed version per release line ("7.2.11, 7.4.6, 8.0.4").
// v is fixed if its own line has a patched version <= v, or if v's line is newer than every
// patched line (the fix predates that line).
func fixedByPatches(v catalog.Version, patched []catalog.Version) bool {
	newestLine := false
	for _, p := range patched {
		if p.Major == v.Major && p.Minor == v.Minor {
			return compareToBound(v, p) >= 0
		}
		if p.Major > v.Major || (p.Major == v.Major && p.Minor > v.Minor) {
			newestLine = true // a newer line got a fix, so v's line is affected and unpatched
		}
	}
	return !newestLine
}

// compareToBound compares v with an advisory bound. A bound without build metadata ("1.30.4") refers
// to the upstream release, so v's distro build is ignored (1.30.4+rke2r1 == 1.30.4). A bound with a
// build ("1.30.4+rke2r2") is compared including it, so a fix shipped only in a newer distro build is
// detected.
func compareToBound(v, bound catalog.Version) int {
	if bound.Build == "" {
		v.Build = ""
	}
	return v.Compare(bound)
}

// openEnded reports whether a range has no upper bound: "All", "", or only ">"/">=" parts. Exact
// ranges ("= 1.2.3", "1.2.3") are bounded.
func openEnded(expr string) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" || strings.EqualFold(expr, "all") || expr == "*" {
		return true
	}
	for _, part := range strings.Split(expr, ",") {
		if !strings.HasPrefix(strings.TrimSpace(part), ">") {
			return false
		}
	}
	return true
}
