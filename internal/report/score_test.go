package report

import (
	"strings"
	"testing"
	"time"

	"owl/internal/inventory"
	"owl/internal/policy"
	"owl/internal/upstream"
)

func day(s string) *time.Time { t, _ := time.Parse("2006-01-02", s); return &t }

var now = *day("2026-11-15")

func k8sInfo() *upstream.Info {
	return &upstream.Info{AdvisorySource: "osv", Lines: []upstream.ReleaseLine{
		{Name: "1.37", Latest: "1.37.2", EOLDate: day("2027-10-28")},
		{Name: "1.36", Latest: "1.36.6", EOLDate: day("2027-06-28")},
		{Name: "1.35", Latest: "1.35.10", EOLDate: day("2027-02-01")}, // within 90 days of now
		{Name: "1.34", Latest: "1.34.13", EOL: true, EOLDate: day("2026-10-27")},
		{Name: "1.33", Latest: "1.33.13", EOL: true, EOLDate: day("2026-06-28")},
	}}
}

func score(version, status string, info *upstream.Info, matches []upstream.Match) Score {
	sp, _ := policy.Parse("lts-or-n-1")
	var t *policy.Target
	if info != nil {
		tt, _ := policy.Resolve(sp, info.Lines, now)
		t = &tt
	}
	return ScoreInstance(inventory.Instance{Version: version, Status: status, Image: "img:" + version}, info, t, matches, now)
}

func has(s Score, substr string) bool {
	for _, r := range s.Reasons {
		if strings.Contains(r.Text, substr) {
			return true
		}
	}
	return false
}

func TestScoreLevels(t *testing.T) {
	cases := []struct {
		name, version string
		want          Level
		reason        string
	}{
		{"at target", "1.36.6", LevelOK, ""},
		{"newer than target, latest patch", "1.37.2", LevelOK, ""},
		{"newer than target, patch missing", "1.37.1", LevelLow, "patch available: 1.37.2"},
		{"same line, behind", "1.36.2", LevelLow, "patch available: 1.36.6"},
		{"one line behind + EOL soon", "1.35.10", LevelHigh, "reaches end-of-life on 2027-02-01"},
		{"EOL", "1.33.6", LevelCritical, "end-of-life since 2026-06-28"},
		{"older than all lines (older than EOL lines)", "1.20.0", LevelCritical, "older than end-of-life release lines"},
	}
	for _, c := range cases {
		s := score(c.version, "identified", k8sInfo(), nil)
		if s.Level != c.want || (c.reason != "" && !has(s, c.reason)) {
			t.Errorf("%s (%s): got %s %+v", c.name, c.version, s.Level, s.Reasons)
		}
	}
}

func TestScoreSecurityAndLifecycle(t *testing.T) {
	m := func(sev string, certain, noFix bool) upstream.Match {
		return upstream.Match{Advisory: upstream.Advisory{ID: "GHSA-x", Aliases: []string{"CVE-2025-1974"}, Severity: sev,
			FixedIn: []string{"1.36.7"}}, Certain: certain, NoFix: noFix}
	}
	if s := score("1.36.6", "identified", k8sInfo(), []upstream.Match{m("high", true, false)}); s.Level != LevelCritical || !has(s, "CVE-2025-1974 (high), fixed in 1.36.7") {
		t.Errorf("high advisory: %+v", s)
	}
	if s := score("1.36.6", "identified", k8sInfo(), []upstream.Match{m("moderate", true, false)}); s.Level != LevelMedium {
		t.Errorf("moderate advisory: %s", s.Level)
	}
	s := score("1.36.6", "identified", k8sInfo(), []upstream.Match{m("critical", false, false), m("high", true, true)})
	if s.Level != LevelOK || len(s.Notes) != 2 {
		t.Errorf("needs-review and no-fix must not raise the level: %s %v", s.Level, s.Notes)
	}

	retired := k8sInfo()
	retired.Retired, retired.LastReleaseAt = true, day("2026-03-19")
	if s := score("1.37.2", "identified", retired, nil); s.Level != LevelCritical || !has(s, "retired") {
		t.Errorf("retired: %+v", s)
	}
	if s := score("", "unpinned", k8sInfo(), nil); s.Level != LevelMedium || !has(s, "unpinned") {
		t.Errorf("unpinned: %+v", s)
	}
	if s := score("1.36.6", "identified", nil, nil); s.Level != LevelUnknown {
		t.Errorf("no upstream data must be unknown, never ok: %s", s.Level)
	}
	noData := k8sInfo()
	noData.AdvisorySource = ""
	if s := score("1.36.6", "identified", noData, nil); !strings.Contains(strings.Join(s.Notes, " "), "no advisory data") {
		t.Errorf("missing advisory source must be noted: %v", s.Notes)
	}
}

func TestDrift(t *testing.T) {
	in := func(stage, cluster, v string) ScoredInstance {
		return ScoredInstance{Instance: inventory.Instance{Stage: stage, Cluster: cluster, Version: v, Status: "identified"}}
	}
	d := driftDefault([]ScoredInstance{in("staging", "it-hetzner", "1.11.3"), in("prelive", "it-aws-test", "1.10.4"), in("live", "it-aws-prod", "1.11.5")})
	if len(d) != 2 || d[0].Kind != DriftPending || d[1].Kind != DriftAnomaly {
		t.Fatalf("drift: %+v", d)
	}
	if d := driftDefault([]ScoredInstance{in("staging", "a", "1.0.0"), in("live", "b", "1.0.0")}); len(d) != 0 {
		t.Errorf("equal versions: %+v", d)
	}
}

func TestScoreReviewRegressions(t *testing.T) {
	// Unpinned with no upstream data is still Medium (not Unknown).
	s := ScoreInstance(inventory.Instance{Status: "unpinned", Image: "postgres:16"}, nil, nil, nil, now)
	if s.Level != LevelMedium {
		t.Errorf("unpinned without upstream data: %s %+v", s.Level, s.Reasons)
	}
	// No resolvable target must never be OK.
	s = ScoreInstance(inventory.Instance{Status: "identified", Version: "1.36.6"}, k8sInfo(), nil, nil, now)
	if s.Level != LevelUnknown {
		t.Errorf("nil target: %s", s.Level)
	}
	// A line missing from the middle doesn't inflate "lines behind": 1.35.x vs target 1.36 is 1 behind.
	gap := &upstream.Info{AdvisorySource: "osv", Lines: []upstream.ReleaseLine{
		{Name: "1.37", Latest: "1.37.2"}, {Name: "1.36", Latest: "1.36.6"}, {Name: "1.34", Latest: "1.34.13"}}}
	tgt := &policy.Target{Policy: "n-1", Line: "1.36", Version: "1.36.6"}
	s = ScoreInstance(inventory.Instance{Status: "identified", Version: "1.35.4"}, gap, tgt, nil, now)
	if s.LinesBehind != 1 || s.Level != LevelMedium {
		t.Errorf("gap in lines: behind=%d level=%s", s.LinesBehind, s.Level)
	}
	// An exact pin on a line we don't list still counts the distance by position.
	pin := &policy.Target{Policy: "pin:1.39.0", Version: "1.39.0"}
	s = ScoreInstance(inventory.Instance{Status: "identified", Version: "1.36.6"}, gap, pin, nil, now)
	if s.Level != LevelHigh {
		t.Errorf("pin beyond known lines: %s behind=%d", s.Level, s.LinesBehind)
	}
	// Reasons are sorted worst first by ScoreInstance itself.
	s = score("1.36.2", "identified", k8sInfo(), []upstream.Match{{Advisory: upstream.Advisory{ID: "X", Severity: "high"}, Certain: true}})
	if s.Reasons[0].Level != LevelCritical {
		t.Errorf("reasons not worst-first: %+v", s.Reasons)
	}
}

func TestDriftPartialPromotion(t *testing.T) {
	in := func(stage, cluster, v string) ScoredInstance {
		return ScoredInstance{Instance: inventory.Instance{Stage: stage, Cluster: cluster, Version: v, Status: "identified"}}
	}
	d := driftDefault([]ScoredInstance{in("staging", "stg", "1.11.5"), in("live", "prod-a", "1.11.5"), in("live", "prod-b", "1.10.4")})
	if len(d) != 1 || d[0].Kind != DriftPending || !strings.Contains(d[0].Text, "prod-b") {
		t.Errorf("partial promotion must name the lagging cluster: %+v", d)
	}
}

// driftDefault: the default stage order, one tenant, no "only in" info (no cluster list).
func driftDefault(insts []ScoredInstance) []Drift {
	return drift(insts, map[string]int{"staging": 1, "prelive": 2, "live": 3}, nil)
}

// A line upstream data doesn't list yet (newer than all, or a gap) is judged by the next older line
// only — old EOL lines further down don't make it EOL.
func TestUnlistedLineEOL(t *testing.T) {
	if s := score("1.38.0", "identified", k8sInfo(), nil); has(s, "older than end-of-life") || s.Level == LevelCritical {
		t.Errorf("newer than all lines: %s %+v", s.Level, s.Reasons)
	}
	gap := &upstream.Info{AdvisorySource: "osv", Lines: []upstream.ReleaseLine{
		{Name: "8.0", Latest: "8.0.2"}, {Name: "7.4", Latest: "7.4.3"},
		{Name: "7.2", Latest: "7.2.9", EOL: true, EOLDate: day("2026-01-01")},
	}}
	if s := score("7.5.1", "identified", gap, nil); has(s, "older than end-of-life") {
		t.Errorf("gap above a supported line: %+v", s.Reasons)
	}
	if s := score("7.1.0", "identified", gap, nil); !has(s, "older than end-of-life") {
		t.Errorf("below an EOL line: %+v", s.Reasons)
	}
}
