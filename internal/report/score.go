// Package report scores every running component and builds the combined report (levels, reasons,
// targets, drift). Levels are rule-based and deterministic — see DESIGN.md "Priority". Claude may
// explain a level later, but never sets one.
package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"service-inventory/internal/catalog"
	"service-inventory/internal/inventory"
	"service-inventory/internal/policy"
	"service-inventory/internal/upstream"
)

type Level string

const (
	LevelCritical Level = "critical"
	LevelHigh     Level = "high"
	LevelMedium   Level = "medium"
	LevelLow      Level = "low"
	LevelUnknown  Level = "unknown" // no upstream data: never shown as OK
	LevelOK       Level = "ok"
)

// Rank orders levels by urgency.
func (l Level) Rank() int {
	switch l {
	case LevelCritical:
		return 5
	case LevelHigh:
		return 4
	case LevelMedium:
		return 3
	case LevelLow:
		return 2
	case LevelUnknown:
		return 1
	}
	return 0
}

// EOLSoon is how close an end-of-life date must be to raise the level to High.
const EOLSoon = 90 * 24 * time.Hour

type Reason struct {
	Level Level  `json:"level"`
	Text  string `json:"text"`
}

type Score struct {
	Level        Level    `json:"level"`
	Reasons      []Reason `json:"reasons"`         // what determined the level, worst first
	Notes        []string `json:"notes,omitempty"` // informational, don't change the level
	Line         string   `json:"line,omitempty"`  // release line the running version belongs to
	LinesBehind  int      `json:"linesBehind"`
	LatestInLine string   `json:"latestInLine,omitempty"`
	// Advisories that count (certain, with a fix) — the evidence behind security reasons.
	Advisories []upstream.Match `json:"advisories,omitempty"`
}

func (s *Score) add(l Level, format string, args ...any) {
	s.Reasons = append(s.Reasons, Reason{l, fmt.Sprintf(format, args...)})
	if l.Rank() > s.Level.Rank() {
		s.Level = l
	}
}

// ScoreInstance applies the priority rules to one running instance. matches are the advisories for
// its version (nil when not evaluated, e.g. unpinned). target is nil when no policy target could be
// resolved. Reasons come back worst first.
func ScoreInstance(in inventory.Instance, info *upstream.Info, target *policy.Target, matches []upstream.Match, now time.Time) Score {
	s := Score{Level: LevelOK, Reasons: []Reason{}}
	defer func() {
		sort.SliceStable(s.Reasons, func(i, j int) bool { return s.Reasons[i].Level.Rank() > s.Reasons[j].Level.Rank() })
	}()

	// Unpinned is Medium regardless of upstream data: the fix (pin a version) doesn't depend on it.
	if in.Status == string(catalog.StatusUnpinned) {
		s.add(LevelMedium, "unpinned image tag (%s): exact version unknown — pin a version", in.Image)
	}
	var lines []upstream.ReleaseLine
	if info != nil {
		lines = policy.UsableLines(info.Lines)
	}
	if len(lines) == 0 {
		s.add(LevelUnknown, "no upstream release data")
		return s
	}

	// Lifecycle of the whole project.
	if info.Retired {
		last := "never"
		if info.LastReleaseAt != nil {
			last = info.LastReleaseAt.Format("2006-01-02")
		}
		s.add(LevelCritical, "upstream project retired (repository archived, last release %s) — plan a replacement", last)
	} else if info.Archived {
		s.Notes = append(s.Notes, "upstream repository is archived but still releasing — check where development moved")
	}
	if in.Status == string(catalog.StatusUnpinned) {
		return s // version-based rules can't be applied to an unknown version
	}
	v, ok := catalog.ParseVersion(in.Version, "")
	if !ok {
		s.add(LevelUnknown, "version %q not understood", in.Version)
		return s
	}

	// Security.
	review, noFix := 0, 0
	for _, m := range matches {
		switch {
		case !m.Certain:
			review++
			continue
		case m.NoFix:
			noFix++
			continue
		}
		s.Advisories = append(s.Advisories, m)
		fixed := ""
		if f := relevantFix(v, m.FixedIn); f != "" {
			fixed = ", fixed in " + f
		}
		id := advisoryLabel(m.Advisory)
		switch m.Severity {
		case "critical", "high":
			s.add(LevelCritical, "%s (%s)%s", id, m.Severity, fixed)
		case "moderate", "medium":
			s.add(LevelMedium, "%s (%s)%s", id, m.Severity, fixed)
		case "low":
			s.add(LevelLow, "%s (low)%s", id, fixed)
		default:
			s.add(LevelMedium, "%s (severity unknown)%s", id, fixed)
		}
	}
	if noFix > 0 {
		s.Notes = append(s.Notes, fmt.Sprintf("%d advisories affect this version with no fixed version listed", noFix))
	}
	if review > 0 {
		s.Notes = append(s.Notes, fmt.Sprintf("%d advisories need review (upstream data doesn't say which versions are fixed)", review))
	}
	if info.AdvisorySource == "" {
		s.Notes = append(s.Notes, "no advisory data for this component — vulnerability status unknown")
	}

	// Release line: EOL. A version whose line isn't listed sits between known lines (or below all of
	// them); it's EOL if the next older listed line is EOL — a version older than an EOL line is EOL.
	pos, exact := policy.Position(v, lines)
	if exact {
		l := lines[pos]
		s.Line, s.LatestInLine = l.Name, l.Latest
		switch {
		case policy.IsEOL(l, now):
			when := ""
			if l.EOLDate != nil {
				when = " since " + l.EOLDate.Format("2006-01-02")
			}
			s.add(LevelCritical, "release line %s is end-of-life%s", l.Name, when)
		case l.EOLDate != nil && l.EOLDate.Sub(now) <= EOLSoon:
			s.add(LevelHigh, "release line %s reaches end-of-life on %s", l.Name, l.EOLDate.Format("2006-01-02"))
		}
	} else {
		olderEOL := false
		for _, l := range lines[pos:] {
			if policy.IsEOL(l, now) {
				olderEOL = true
				break
			}
		}
		switch {
		case olderEOL || pos == len(lines) && policy.IsEOL(lines[len(lines)-1], now):
			s.add(LevelCritical, "version %s is older than end-of-life release lines (not tracked upstream any more)", in.Version)
		case pos == len(lines):
			s.Notes = append(s.Notes, "version is older than every release line upstream data covers")
		}
	}

	// Position vs target.
	if target == nil {
		s.add(LevelUnknown, "no version target could be resolved for the policy")
		return s
	}
	tv, ok := catalog.ParseVersion(target.Version, "")
	if !ok {
		s.add(LevelUnknown, "target version %q not understood", target.Version)
		return s
	}
	if v.Compare(tv) >= 0 {
		if s.LatestInLine != "" {
			if lv, ok := catalog.ParseVersion(s.LatestInLine, ""); ok && v.Compare(lv) < 0 {
				s.add(LevelLow, "patch available: %s", s.LatestInLine)
			}
		}
		return s
	}
	targetPos, targetListed := policy.Position(tv, lines)
	s.LinesBehind = pos - targetPos
	if !targetListed {
		// The target's line isn't in upstream data (e.g. an exact pin beyond it): positions can't count
		// lines we don't know, so use the version distance itself.
		s.LinesBehind = max(s.LinesBehind, lineDistance(v, tv, lines))
	}
	switch {
	case s.LinesBehind >= 2:
		s.add(LevelHigh, "%d release lines behind target %s (%s)", s.LinesBehind, target.Version, target.Policy)
	case s.LinesBehind == 1:
		s.add(LevelMedium, "1 release line behind target %s (%s)", target.Version, target.Policy)
	default:
		s.add(LevelLow, "patch available: %s", target.Version)
	}
	return s
}

// relevantFix picks the fix that matters for v out of a per-line list ("6.2.20, 7.2.11, 7.4.6"):
// the fix in v's own release line, else the lowest fixed version above v.
func relevantFix(v catalog.Version, fixed []string) string {
	var best *catalog.Version
	bestStr := ""
	for _, f := range fixed {
		fv, ok := catalog.ParseVersion(f, "")
		if !ok {
			continue
		}
		if fv.Major == v.Major && fv.Minor == v.Minor {
			return f
		}
		if fv.Compare(v) > 0 && (best == nil || fv.Compare(*best) < 0) {
			cp := fv
			best, bestStr = &cp, f
		}
	}
	if bestStr == "" && len(fixed) > 0 {
		return strings.Join(fixed, " / ")
	}
	return bestStr
}

// lineDistance counts release lines between v and t from the versions: majors for products whose
// lines are majors (Postgres "17"), otherwise minors; a major jump counts as at least 2 lines.
func lineDistance(v, t catalog.Version, lines []upstream.ReleaseLine) int {
	majorLines := false
	if lv, ok := catalog.ParseVersion(lines[0].Name, ""); ok && lv.Parts == 1 {
		majorLines = true
	}
	switch {
	case majorLines:
		return t.Major - v.Major
	case t.Major != v.Major:
		return max(2, t.Major-v.Major)
	}
	return t.Minor - v.Minor
}

// advisoryLabel prefers a CVE ID (what people search for), falling back to the advisory ID.
func advisoryLabel(a upstream.Advisory) string {
	for _, id := range append([]string{a.ID}, a.Aliases...) {
		if strings.HasPrefix(id, "CVE-") {
			return id
		}
	}
	return a.ID
}
