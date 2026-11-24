package report

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"owl/internal/catalog"
	"owl/internal/inventory"
	"owl/internal/policy"
	"owl/internal/snapshot"
	"owl/internal/store"
	"owl/internal/store/sqlite"
	"owl/internal/upstream"
)

// What the pods run counts: an older version left over from a rollout raises the level when it is worse
// than the template's, and never lowers it.
func TestWithRunning(t *testing.T) {
	cases := []struct {
		name, template string
		running        []string
		want           Level
		stillRunning   string
	}{
		{"template fine, pods on an EOL line", "1.36.6", []string{"1.33.6"}, LevelCritical, "1.33.6"},
		{"worst of several old versions", "1.36.6", []string{"1.33.6", "1.35.10"}, LevelCritical, "1.33.6"},
		{"old version one line behind", "1.36.6", []string{"1.35.0"}, LevelHigh, "1.35.0"}, // 1.35 ends within 90 days
		{"same level: template decides", "1.36.2", []string{"1.36.4"}, LevelLow, ""},
		{"newer pods (rollback in progress) never lower it", "1.33.6", []string{"1.36.6"}, LevelCritical, ""},
	}
	for _, c := range cases {
		var rs []RunningScore
		for _, v := range c.running {
			rs = append(rs, RunningScore{v, score(v, "identified", k8sInfo(), nil)})
		}
		tmpl := score(c.template, "identified", k8sInfo(), nil)
		got := WithRunning(tmpl, rs)
		if got.Level != c.want || got.StillRunning != c.stillRunning {
			t.Errorf("%s: level %s stillRunning %q, want %s %q (%+v)", c.name, got.Level, got.StillRunning, c.want, c.stillRunning, got.Reasons)
		}
		if c.stillRunning == "" {
			continue
		}
		if got.TemplateLevel != tmpl.Level || got.Line != tmpl.Line || got.LinesBehind != tmpl.LinesBehind {
			t.Errorf("%s: template details must stay the template's: %+v", c.name, got)
		}
		if got.Reasons[0].Level != c.want || !strings.HasPrefix(got.Reasons[0].Text, "pods still run "+c.stillRunning+": ") {
			t.Errorf("%s: worst reason first, prefixed: %+v", c.name, got.Reasons)
		}
	}
	// Input reasons aren't modified (the template score may be shared).
	tmpl := score("1.36.6", "identified", k8sInfo(), nil)
	n := len(tmpl.Reasons)
	WithRunning(tmpl, []RunningScore{{"1.33.6", score("1.33.6", "identified", k8sInfo(), nil)}})
	if len(tmpl.Reasons) != n {
		t.Error("WithRunning changed the template score's reasons")
	}
}

func TestActionRollout(t *testing.T) {
	sp, _ := policy.Parse("lts-or-n-1")
	tg, _ := policy.Resolve(sp, k8sInfo().Lines, now)
	act := func(in inventory.Instance) (Action, Score) {
		sc := ScoreInstance(in, k8sInfo(), &tg, nil, now)
		var rs []RunningScore
		for _, v := range in.RunningVersions {
			old := in
			old.Version, old.RunningVersions = v, nil
			rs = append(rs, RunningScore{v, ScoreInstance(old, k8sInfo(), &tg, nil, now)})
		}
		sc = WithRunning(sc, rs)
		return ActionFor(in, sc, &tg, k8sInfo()), sc
	}
	base := inventory.Instance{Status: "identified", Version: "1.36.6", Image: "img:1.36.6"}

	// Template at target, nothing rolled out, the pods on an EOL line: finish the rollout, Critical.
	in := base
	in.RunningVersions, in.NotRolledOut = []string{"1.33.6"}, true
	a, sc := act(in)
	if a.Kind != ActionRollout || a.Text != "Finish the rollout to 1.36.6" || sc.Level != LevelCritical ||
		!strings.Contains(a.Why, "pods still run 1.33.6: release line 1.33 is end-of-life") || !strings.Contains(a.Why, "no pod runs 1.36.6 yet") {
		t.Errorf("not rolled out: %s %+v", sc.Level, a)
	}

	// Half-way: some pods on the new version.
	in.NotRolledOut = false
	if a, _ := act(in); a.Kind != ActionRollout || !strings.Contains(a.Why, "a rollout in progress, or stuck") {
		t.Errorf("half-way: %+v", a)
	}

	// Not rolled out, but the old image is the same version (another digest) or unknown: still a rollout,
	// level stays the template's.
	in = base
	in.NotRolledOut = true
	if a, sc := act(in); a.Kind != ActionRollout || sc.Level != LevelOK || !strings.Contains(a.Why, "no pod runs 1.36.6 yet") {
		t.Errorf("not rolled out, same version: %s %+v", sc.Level, a)
	}

	// The template itself needs a patch: that's still the step (it fixes both once rolled out), and the
	// reason says what the pods run.
	in = base
	in.Version, in.Image, in.RunningVersions = "1.36.4", "img:1.36.4", []string{"1.33.6"}
	if a, sc := act(in); a.Kind != ActionPatch || a.Text != "Patch to 1.36.6" || sc.Level != LevelCritical ||
		!strings.HasPrefix(a.Why, "patch release; pods still run 1.33.6") {
		t.Errorf("template behind too: %s %+v", sc.Level, a)
	}

	// Component headline when rollouts are all that's left.
	ok := ScoredInstance{Instance: base, Score: Score{Level: LevelOK}, Action: Action{Kind: ActionNone, Text: "Up to date"}}
	ro := ScoredInstance{Instance: base, Score: Score{Level: LevelOK}, Action: Action{Kind: ActionRollout, Text: "Finish the rollout to 1.36.6", Why: "x"}}
	if a := ComponentAction([]ScoredInstance{ok, ro}, &tg); a.Kind != ActionRollout || a.Text != "Finish the rollout to 1.36.6" {
		t.Errorf("component: %+v", a)
	}
}

// End to end: a snapshot whose pods don't run the template's image goes through the store, inventory
// and report.
func TestBuildRunningVersions(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cat, _ := catalog.Builtin()
	c, _ := db.CreateCluster(ctx, "prod", store.StageLive, "IT")
	wl := func(name, image string, running ...snapshot.RunningImage) snapshot.Workload {
		return snapshot.Workload{Kind: "StatefulSet", Namespace: "ns", Name: name, DesiredReplicas: 3,
			Containers: []snapshot.Container{{Name: "c", Image: image}}, Running: running}
	}
	run := func(image string, pods int) snapshot.RunningImage {
		return snapshot.RunningImage{Container: "c", Image: image, ImageID: "sha256:" + image, Pods: pods, Ready: pods}
	}
	snap := &snapshot.Snapshot{SchemaVersion: snapshot.SchemaVersion, Workloads: []snapshot.Workload{
		wl("ondelete", "nginx:1.36.6", run("nginx:1.33.6", 3)),                        // template changed, no pod replaced
		wl("halfway", "nginx:1.36.6", run("nginx:1.36.6", 1), run("nginx:1.33.6", 2)), // rolling
		wl("done", "nginx:1.36.6", run("nginx:1.36.6", 3)),
		wl("scaled0", "nginx:1.36.6"),
		wl("oldlatest", "nginx:1.36.6", run("nginx:latest", 3)), // old image has no version: flagged, not scored
	}}
	if _, err := db.Ingest(ctx, c.ID, snap); err != nil {
		t.Fatal(err)
	}
	rep, err := Build(ctx, Inputs{Catalog: cat, Store: db, Infos: func(string) *upstream.Info { return k8sInfo() }, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ScoredInstance{}
	for _, cr := range rep.Components {
		if cr.ID == "nginx" {
			for _, in := range cr.Instances {
				got[in.Workload] = in
			}
			if cr.Level != LevelCritical {
				t.Errorf("component level must follow what runs: %s", cr.Level)
			}
		}
	}
	want := map[string]struct {
		notRolledOut bool
		versions     []string
		level        Level
		kind         ActionKind
	}{
		"ondelete":  {true, []string{"1.33.6"}, LevelCritical, ActionRollout},
		"halfway":   {false, []string{"1.33.6"}, LevelCritical, ActionRollout},
		"done":      {false, nil, LevelOK, ActionNone},
		"scaled0":   {false, nil, LevelOK, ActionNone},
		"oldlatest": {true, nil, LevelOK, ActionRollout},
	}
	for name, w := range want {
		in, ok := got[name]
		if !ok {
			t.Errorf("%s: missing", name)
			continue
		}
		if in.NotRolledOut != w.notRolledOut || !slices.Equal(in.RunningVersions, w.versions) || in.Score.Level != w.level || in.Action.Kind != w.kind {
			t.Errorf("%s: notRolledOut %v versions %v level %s action %+v", name, in.NotRolledOut, in.RunningVersions, in.Score.Level, in.Action)
		}
		if in.Version != "1.36.6" {
			t.Errorf("%s: version stays the template's: %s", name, in.Version)
		}
	}
}
