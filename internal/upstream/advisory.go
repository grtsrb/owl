package upstream

import (
	"context"
	"strings"

	"service-inventory/internal/catalog"
)

type AffectedRange struct {
	Range   string `json:"range"`   // GitHub syntax: ">= 7.0.0, < 7.2.5", "<= 1.14.6", "All"
	Patched string `json:"patched"` // "1.14.7", "7.2.5, 7.4.1", "TBD", ""
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
}

// AdvisoriesFor returns the advisories affecting a component version. Both sources (OSV, GitHub
// repo advisories) are evaluated here against their ranges; no network calls.
func (c *Client) AdvisoriesFor(ctx context.Context, comp *catalog.Component, info *Info, version string) ([]Match, error) {
	out := []Match{}
	v, ok := catalog.ParseVersion(version, "")
	if !ok || info == nil {
		return out, nil
	}
	v.Build = "" // advisories are about the upstream version (rke2r2 vs r1 is handled as a newer release)
	for _, a := range info.Advisories {
		affected, certain := false, true
		var fixed []string
		for _, r := range a.Ranges {
			patched := parsePatched(r.Patched)
			in, known := InRange(v, r.Range)
			if strings.EqualFold(strings.TrimSpace(r.Range), "all") && len(patched) > 0 {
				in, known = true, true // "All" + per-line patches: decided by the patch list below
			}
			if in && !strings.Contains(r.Range, "<") {
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
					if !containsStr(fixed, p.Original) {
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
			m.Ranges = nil // the matched version is what matters to callers
			out = append(out, m)
		}
	}
	return out, nil
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
		c := v.Compare(withoutBuild(bound))
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
			return v.Compare(withoutBuild(p)) >= 0
		}
		if p.Major > v.Major || (p.Major == v.Major && p.Minor > v.Minor) {
			newestLine = true // a newer line got a fix, so v's line is affected and unpatched
		}
	}
	return !newestLine
}

func withoutBuild(v catalog.Version) catalog.Version {
	v.Build = ""
	return v
}
