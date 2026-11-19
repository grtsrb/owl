package report

import (
	"strings"
	"testing"

	"owl/internal/inventory"
	"owl/internal/policy"
	"owl/internal/store"
	"owl/internal/upstream"
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
	// Pinned to an EOL line and already on its last patch: never "Patch to <the same version>".
	pinned, _ := policy.Parse("pin:1.33")
	pt, _ := policy.Resolve(pinned, k8sInfo().Lines, now)
	in := inventory.Instance{Version: "1.33.13", Status: "identified", Image: "img:1.33.13"}
	if a := ActionFor(in, ScoreInstance(in, k8sInfo(), &pt, nil, now), &pt, k8sInfo()); a.Kind != ActionUpgrade || strings.Contains(a.Text, "1.33.13") {
		t.Errorf("pinned EOL line at latest patch: %+v", a)
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

// The component headline is the same goal whichever instance is worst — staging needing a patch and
// prod needing an upgrade both read "Update to <target>".
func TestComponentAction(t *testing.T) {
	sp, _ := policy.Parse("lts-or-n-1")
	tg, _ := policy.Resolve(sp, k8sInfo().Lines, now)
	mk := func(cluster, v string) ScoredInstance {
		in := inventory.Instance{Cluster: cluster, Version: v, Status: "identified", Image: "img:" + v}
		sc := ScoreInstance(in, k8sInfo(), &tg, nil, now)
		return ScoredInstance{Instance: in, Score: sc, Action: ActionFor(in, sc, &tg, k8sInfo())}
	}
	stg, prod := mk("stg", "1.36.4"), mk("prod", "1.33.6")
	for _, set := range [][]ScoredInstance{{stg}, {prod}, {stg, prod}} {
		if a := ComponentAction(set, &tg); a.Text != "Update to 1.36.6" {
			t.Errorf("%d instances: %+v", len(set), a)
		}
	}
	if a := ComponentAction([]ScoredInstance{stg, prod}, &tg); !strings.Contains(a.Why, "1.33 is EOL") {
		t.Errorf("why must come from the worst instance: %q", a.Why)
	}
	if a := ComponentAction([]ScoredInstance{mk("x", "1.36.6")}, &tg); a.Kind != ActionNone {
		t.Errorf("all up to date: %+v", a)
	}
}

// Review regressions (2026-11-19): a line that ends soon at its newest patch says so (not "Review
// advisories"); instances ahead of the target that only need a patch don't make the headline
// "Update to <older target>".
func TestActionEOLSoonAndAheadOfTarget(t *testing.T) {
	pin, _ := policy.Parse("pin:1.35")
	pt, _ := policy.Resolve(pin, k8sInfo().Lines, now)
	in := inventory.Instance{Version: "1.35.10", Status: "identified", Image: "img:1.35.10"}
	sc := ScoreInstance(in, k8sInfo(), &pt, nil, now)
	if a := ActionFor(in, sc, &pt, k8sInfo()); sc.Level != LevelHigh || a.Text != "Plan the move off 1.35" || !strings.Contains(a.Why, "2027-02-01") {
		t.Errorf("EOL soon at latest patch: %s %+v", sc.Level, a)
	}

	sp, _ := policy.Parse("lts-or-n-1")
	tg, _ := policy.Resolve(sp, k8sInfo().Lines, now)
	mk := func(v string) ScoredInstance {
		in := inventory.Instance{Cluster: "c", Version: v, Status: "identified", Image: "img:" + v}
		sc := ScoreInstance(in, k8sInfo(), &tg, nil, now)
		return ScoredInstance{Instance: in, Score: sc, Action: ActionFor(in, sc, &tg, k8sInfo())}
	}
	if a := ComponentAction([]ScoredInstance{mk("1.37.0"), mk("1.36.6")}, &tg); a.Text != "Patch to 1.37.2" {
		t.Errorf("ahead of target: %+v", a)
	}
	if a := ComponentAction([]ScoredInstance{mk("1.37.0"), mk("1.36.2")}, &tg); a.Text != "Update to 1.36.6" {
		t.Errorf("one instance needs the target: %+v", a)
	}
}

// An image the distribution ships: the step is upgrading the distribution; the level doesn't change.
// A component with only some instances bundled keeps the plain version step.
func TestActionBundled(t *testing.T) {
	sp, _ := policy.Parse("lts-or-n-1")
	tg, _ := policy.Resolve(sp, k8sInfo().Lines, now)
	mk := func(cluster, v, bundled string) ScoredInstance {
		in := inventory.Instance{Cluster: cluster, Version: v, Status: "identified", Image: "img:" + v, BundledWith: bundled}
		sc := ScoreInstance(in, k8sInfo(), &tg, nil, now)
		return ScoredInstance{Instance: in, Score: sc, Action: ActionFor(in, sc, &tg, k8sInfo())}
	}
	plain, rke := mk("a", "1.35.0", ""), mk("b", "1.35.0", "rke2")
	if plain.Score.Level != rke.Score.Level {
		t.Errorf("bundled must not change the level: %s vs %s", plain.Score.Level, rke.Score.Level)
	}
	if want := "Comes with RKE2 — upgrade RKE2 to a release that ships 1.36.6 or newer"; rke.Action.Text != want || rke.Action.Kind != ActionUpgrade {
		t.Errorf("instance: %+v", rke.Action)
	}
	if a := mk("c", "1.36.4", "k3s").Action; a.Text != "Comes with k3s — upgrade k3s to a release that ships 1.36.6 or newer" || a.Kind != ActionPatch {
		t.Errorf("patch: %+v", a)
	}
	if a := ComponentAction([]ScoredInstance{rke, mk("d", "1.33.6", "rke2")}, &tg); a.Text != "Comes with RKE2 — upgrade RKE2 to a release that ships 1.36.6 or newer" {
		t.Errorf("component, all bundled: %+v", a)
	}
	if a := ComponentAction([]ScoredInstance{rke, plain}, &tg); a.Text != "Update to 1.36.6" {
		t.Errorf("component, mixed: %+v", a)
	}
}
