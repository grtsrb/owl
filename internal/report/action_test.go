package report

import (
	"strings"
	"testing"

	"service-inventory/internal/inventory"
	"service-inventory/internal/policy"
	"service-inventory/internal/store"
	"service-inventory/internal/upstream"
)

func actionFor(version, status string, info *upstream.Info, matches []upstream.Match) Action {
	sp, _ := policy.Parse("lts-or-n-1")
	var t *policy.Target
	if info != nil {
		tt, _ := policy.Resolve(sp, info.Lines, now)
		t = &tt
	}
	in := inventory.Instance{Version: version, Status: status, Image: "img:" + version}
	return ActionFor(in, ScoreInstance(in, info, t, matches, now), t, info)
}

func TestActionFor(t *testing.T) {
	high := upstream.Match{Advisory: upstream.Advisory{ID: "CVE-1", Severity: "high"}, Certain: true, RelevantFix: "1.36.6"}
	retired := k8sInfo()
	retired.Retired = true
	cases := []struct {
		name, version, status string
		info                  *upstream.Info
		matches               []upstream.Match
		kind                  ActionKind
		text, why             string // substrings
	}{
		{"at target, latest patch", "1.36.6", "identified", k8sInfo(), nil, ActionNone, "Up to date", "at target"},
		{"patch on the target line", "1.36.4", "identified", k8sInfo(), nil, ActionPatch, "Patch to 1.36.6", "patch release"},
		{"newer than target: patch own line", "1.37.0", "identified", k8sInfo(), nil, ActionPatch, "Patch to 1.37.2", ""},
		{"one line behind", "1.35.0", "identified", k8sInfo(), nil, ActionUpgrade, "Upgrade to 1.36.6", "1 release line behind"},
		{"EOL line", "1.33.6", "identified", k8sInfo(), nil, ActionUpgrade, "Upgrade to 1.36.6", "1.33 is EOL since 2026-06-28"},
		{"advisories come first", "1.36.4", "identified", k8sInfo(), []upstream.Match{high}, ActionPatch, "Patch to 1.36.6", "fixes 1 high advisory"},
		{"retired", "1.36.4", "identified", retired, []upstream.Match{high, high}, ActionReplace, "Replace", "retired upstream; 2 high advisories open"},
		{"unpinned", "", "unpinned", k8sInfo(), nil, ActionPin, "Pin an exact version", "doesn't say which version"},
		{"no data", "1.0.0", "identified", nil, nil, ActionCheck, "Check manually", "no upstream release data"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := actionFor(c.version, c.status, c.info, c.matches)
			if a.Kind != c.kind || !strings.Contains(a.Text, c.text) || !strings.Contains(a.Why, c.why) {
				t.Errorf("got %+v, want kind %s text ~%q why ~%q", a, c.kind, c.text, c.why)
			}
		})
	}
}

func inst(cluster, stage, tenant, version string) ScoredInstance {
	return ScoredInstance{Instance: inventory.Instance{Cluster: cluster, Stage: stage, Tenant: tenant, Version: version, Status: "identified"}}
}

func TestDriftPerTenant(t *testing.T) {
	order := map[string]int{"dev": 1, "staging": 2, "prelive": 3, "live": 4}
	seen := now
	clusters := []store.Cluster{
		{Name: "it-stg", Tenant: "IT", LastSeenAt: &seen}, {Name: "it-pre", Tenant: "IT", LastSeenAt: &seen},
		{Name: "it-prod", Tenant: "IT", LastSeenAt: &seen}, {Name: "ds-prod", Tenant: "DS", LastSeenAt: &seen},
		{Name: "it-new", Tenant: "IT"}, // never reported: must not count as "not running"
	}
	insts := []ScoredInstance{
		inst("it-stg", "staging", "IT", "1.2.0"),
		inst("it-prod", "live", "IT", "1.1.0"),
		inst("ds-prod", "live", "DS", "1.5.0"), // DS is newer than IT staging: not an IT anomaly
	}
	d := drift(insts, order, clusters)
	var kinds []string
	for _, x := range d {
		kinds = append(kinds, string(x.Kind)+":"+x.Tenant+":"+strings.Join(x.Clusters, ","))
	}
	got := strings.Join(kinds, " ")
	want := "pending_promotion:IT:it-stg,it-prod only_in:IT:it-pre"
	if got != want {
		t.Errorf("drift:\n got  %s\n want %s\n%+v", got, want, d)
	}

	// The user's stage order decides direction: with live before staging, staging is "later".
	rev := map[string]int{"live": 1, "staging": 2}
	d = drift(insts[:2], rev, clusters[:3])
	if len(d) == 0 || d[0].Kind != DriftAnomaly {
		t.Errorf("reordered stages must flip the comparison: %+v", d)
	}
}
