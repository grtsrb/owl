// Package report scores every running component and builds the combined report (levels, reasons,
// targets, drift). Levels are rule-based and deterministic — see DESIGN.md "Priority". Claude may
// explain a level later, but never sets one.
package report

import (
	"fmt"
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
// its version (nil when not evaluated, e.g. unpinned).
func ScoreInstance(in inventory.Instance, info *upstream.Info, target *policy.Target, matches []upstream.Match, now time.Time) Score {
	s := Score{Level: LevelOK, Reasons: []Reason{}}
	if info == nil || len(info.Lines) == 0 {
		s.Level = LevelUnknown
		s.Reasons = append(s.Reasons, Reason{LevelUnknown, "no upstream release data"})
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
		s.add(LevelMedium, "unpinned image tag (%s): exact version unknown — pin a version", in.Image)
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
		if len(m.FixedIn) > 0 {
			fixed = ", fixed in " + strings.Join(m.FixedIn, " / ")
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

	// Release line: EOL and position vs target.
	lineIdx := -1
	for i, l := range info.Lines {
		if lv, ok := catalog.ParseVersion(l.Name, ""); ok && lv.Major == v.Major && (lv.Parts == 1 || lv.Minor == v.Minor) {
			lineIdx = i
			s.Line, s.LatestInLine = l.Name, l.Latest
			break
		}
	}
	if lineIdx >= 0 {
		l := info.Lines[lineIdx]
		switch {
		case l.EOL || (l.EOLDate != nil && !l.EOLDate.After(now)):
			when := ""
			if l.EOLDate != nil {
				when = " since " + l.EOLDate.Format("2006-01-02")
			}
			s.add(LevelCritical, "release line %s is end-of-life%s", l.Name, when)
		case l.EOLDate != nil && l.EOLDate.Sub(now) <= EOLSoon:
			s.add(LevelHigh, "release line %s reaches end-of-life on %s", l.Name, l.EOLDate.Format("2006-01-02"))
		}
	}

	if target == nil {
		return s
	}
	tv, ok := catalog.ParseVersion(target.Version, "")
	if !ok || v.Compare(tv) >= 0 {
		if ok && s.LatestInLine != "" {
			if lv, ok := catalog.ParseVersion(s.LatestInLine, ""); ok && v.Compare(lv) < 0 {
				s.add(LevelLow, "patch available: %s", s.LatestInLine)
			}
		}
		return s
	}
	targetIdx := -1
	for i, l := range info.Lines {
		if l.Name == target.Line {
			targetIdx = i
		}
	}
	switch {
	case lineIdx >= 0 && targetIdx >= 0:
		s.LinesBehind = lineIdx - targetIdx
	case lineIdx < 0: // older than every known line
		s.LinesBehind = len(info.Lines) - max(targetIdx, 0)
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

// advisoryLabel prefers a CVE ID (what people search for), falling back to the advisory ID.
func advisoryLabel(a upstream.Advisory) string {
	for _, id := range append([]string{a.ID}, a.Aliases...) {
		if strings.HasPrefix(id, "CVE-") {
			return id
		}
	}
	return a.ID
}
