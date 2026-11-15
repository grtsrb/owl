package policy

import (
	"strings"
	"testing"
	"time"

	"service-inventory/internal/upstream"
)

var k8sLines = []upstream.ReleaseLine{
	{Name: "1.37", Latest: "1.37.2"}, {Name: "1.36", Latest: "1.36.6"}, {Name: "1.35", Latest: "1.35.10"},
	{Name: "1.34", Latest: "1.34.13", EOL: true},
}
var haproxyLines = []upstream.ReleaseLine{
	{Name: "3.4", Latest: "3.4.7", LTS: true}, {Name: "3.3", Latest: "3.3.17"}, {Name: "3.2", Latest: "3.2.26", LTS: true},
}
var pgLines = []upstream.ReleaseLine{{Name: "18", Latest: "18.7"}, {Name: "17", Latest: "17.12"}, {Name: "16", Latest: "16.16"}}

var now = time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)

func TestResolveSkipsLTSPastEOLDate(t *testing.T) {
	past := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	lines := []upstream.ReleaseLine{
		{Name: "3.3", Latest: "3.3.17"},
		{Name: "3.2", Latest: "3.2.26", LTS: true, EOLDate: &past}, // flag not flipped yet in cached data
		{Name: "3.1", Latest: "3.1.17"},
	}
	sp, _ := Parse("lts-or-n-1")
	got, _ := Resolve(sp, lines, now)
	// 3.2 is no longer a *supported* LTS, so the policy falls back to n-1 — which happens to be 3.2,
	// and the target must then say it's end-of-life instead of presenting it as fine.
	if got.Line != "3.2" || !strings.Contains(got.Note, "no supported LTS") || !strings.Contains(got.Note, "end-of-life") {
		t.Errorf("got %+v", got)
	}
	sp, _ = Parse("lts")
	lines[0].LTS = true // a supported LTS exists -> chosen over the expired one
	if got, _ := Resolve(sp, lines, now); got.Line != "3.3" {
		t.Errorf("supported LTS not chosen: %+v", got)
	}
}

func TestResolve(t *testing.T) {
	cases := []struct {
		spec  string
		lines []upstream.ReleaseLine
		want  string
	}{
		{"lts-or-n-1", k8sLines, "1.36.6"},    // no LTS -> n-1
		{"lts-or-n-1", haproxyLines, "3.4.7"}, // newest LTS
		{"lts", k8sLines, "1.36.6"},           // lts falls back to n-1
		{"latest", k8sLines, "1.37.2"},
		{"n-2", k8sLines, "1.35.10"},
		{"n-9", k8sLines, "1.34.13"},  // clamps to oldest known
		{"pin:16", pgLines, "16.16"},  // pin a line -> its latest patch
		{"pin:17.2", pgLines, "17.2"}, // exact pin
	}
	for _, c := range cases {
		sp, err := Parse(c.spec)
		if err != nil {
			t.Fatalf("%s: %v", c.spec, err)
		}
		got, ok := Resolve(sp, c.lines, now)
		if !ok || got.Version != c.want {
			t.Errorf("%s: got %+v want %s", c.spec, got, c.want)
		}
	}
	for _, bad := range []string{"", "newest", "n-x", "pin:", "pin:abc", "n-99"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
}
