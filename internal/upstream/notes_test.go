package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owl/internal/catalog"
	"owl/internal/store/sqlite"
)

// Extracts in the real formats (trimmed).
const k8sChangelog = `# v1.33.1

## Downloads for v1.33.1

### Source Code

filename | sha512 hash
-------- | -----------

## Changelog since v1.33.0

## Changes by Kind

### Feature

- Kubernetes is now built using Go 1.24.3 ([#131934](https://github.com/kubernetes/kubernetes/pull/131934), [@cpanato](https://github.com/cpanato)) [SIG Release and Testing]

### Bug or Regression

- Fixed a panic in the endpoint controller. ([#1](https://github.com/kubernetes/kubernetes/pull/1), [@x](https://github.com/x)) [SIG Network]

## Dependencies

### Added
_Nothing has changed._

# v1.33.0

## Changelog since v1.32.0

## Urgent Upgrade Notes

### (No, really, you MUST read this before you upgrade)

- [Action Required] CSI drivers that call IsLikelyNotMountPoint must check the return value. ([#129370](https://github.com/kubernetes/kubernetes/pull/129370), [@a](https://github.com/a), [@b](https://github.com/b)) [SIG Storage]

## Changes by Kind

### Deprecation

- The v1 Endpoints API is now officially deprecated. ([#130098](https://github.com/kubernetes/kubernetes/pull/130098), [@danwinship](https://github.com/danwinship)) [SIG API Machinery and Network]

### Documentation

- Docs change.

### Other (Cleanup or Flake)

- Cleanup.

# v1.33.0-rc.1

### Feature

- Something in an rc.
`

func TestKubernetesChangelog(t *testing.T) {
	got := kubernetesSource.split(k8sChangelog)
	v133 := got["v1.33.0"]
	for _, want := range []string{"Urgent Upgrade Notes:", "[Action Required] CSI drivers", "Deprecation:", "v1 Endpoints API is now officially deprecated."} {
		if !strings.Contains(v133, want) {
			t.Errorf("v1.33.0 should contain %q:\n%s", want, v133)
		}
	}
	for _, unwanted := range []string{"Docs change", "Cleanup", "#129370", "@danwinship", "[SIG", "github.com"} {
		if strings.Contains(v133, unwanted) {
			t.Errorf("v1.33.0 must not contain %q:\n%s", unwanted, v133)
		}
	}
	v1331 := got["v1.33.1"]
	if !strings.Contains(v1331, "built using Go 1.24.3") || strings.Contains(v1331, "panic in the endpoint") || strings.Contains(v1331, "sha512") {
		t.Errorf("patch: Feature kept, bug fixes and downloads dropped:\n%s", v1331)
	}
	if url := kubernetesSource.webURL("1.33", "v1.33.1"); url != "https://github.com/kubernetes/kubernetes/blob/master/CHANGELOG/CHANGELOG-1.33.md#v1331" {
		t.Errorf("anchor: %s", url)
	}
}

const redisNotes = `Redis Open Source 8.2 release notes
===================================

================================================================================
Redis 8.2.2    Released Fri 3 Oct 2025 10:00:00 IST
================================================================================

Update urgency: ` + "`SECURITY`" + `: There are security fixes in the release.

### Security fixes

- (CVE-2025-49844) A Lua script may lead to remote code execution

================================================================================
Redis 8.2.1    Released Mon 18 Aug 2025 10:00:00 IST
================================================================================

Update urgency: ` + "`MODERATE`" + `

- Fix a crash in SCAN.

================================================================================
8.2 GA (v8.2.0)    Released Mon 4 Aug 2025 10:00:00 IST
================================================================================

New GA notes.

================================================================================
8.2-RC1 (v7.9.240)    Released Mon 7 Jul 2025 10:00:00 IST
================================================================================

RC notes.

================================================================================
Redis Community Edition 7.4.0 GA   Released Mon Jul 29 2024 9:00:00 IDT
================================================================================

Old GA notes.

================================================================================
Redis 8.8 GA  (v8.8.0)    Released Mon 25 May 2026 12:00:00 IST
================================================================================

Newer GA notes.

================================================================================
Redis 8.6 GA (8.6.0)    Released Tue 10 Feb 2026 16:00:00 IST
================================================================================

No-v GA notes.

================================================================================
8.2.9 (v8.2.9)    Committed  Mon 18 Aug 2025 12:00:00 IST
================================================================================

Committed-style notes.

================================================================================
Redis 8.8-M03 (v8.7.226)    Released Tue 28 Apr 2026 16:00:00 IST
================================================================================

Milestone notes.
`

func TestRedisReleaseNotes(t *testing.T) {
	got := redisSource.split(redisNotes)
	if strings.Contains(got["8.2.9"], "Milestone notes") || strings.Contains(got["8.2.9"], "RC notes") {
		t.Errorf("pre-release notes must not be credited to the release above them: %q", got)
	}
	if !strings.Contains(got["8.2.0"], "New GA notes.") || !strings.Contains(got["8.2.0"], "Pre-release 8.2-RC1:\nRC notes.") {
		t.Errorf("a line's pre-releases are part of its x.y.0: %q", got["8.2.0"])
	}
	if !strings.Contains(got["8.8.0"], "Pre-release 8.8-M03:\nMilestone notes.") {
		t.Errorf("milestones too: %q", got["8.8.0"])
	}
	if len(got) != 7 || !strings.Contains(got["8.2.9"], "Committed-style") || !strings.Contains(got["8.2.2"], "CVE-2025-49844") || strings.Contains(got["8.2.2"], "SCAN") {
		t.Errorf("sections: %q", got)
	}
	if !strings.Contains(got["8.2.0"], "New GA") || !strings.Contains(got["7.4.0"], "Old GA") ||
		!strings.Contains(got["8.8.0"], "Newer GA") || !strings.Contains(got["8.6.0"], "No-v GA") {
		t.Errorf("every GA header style: %q", got)
	}
	for _, pre := range []string{"7.9.240", "8.7.226"} {
		if _, ok := got[pre]; ok {
			t.Errorf("pre-release %s is not a release", pre)
		}
	}
	if strings.HasPrefix(got["8.2.1"], "=") {
		t.Error("the ==== rulers are trimmed")
	}
}

const haproxyChangelog = `ChangeLog :
===========

2026/10/14 : 3.2.26
    - BUG/MEDIUM: cli: fix the last command detection on a full command line
    - REGTESTS: cli: test a command line of exactly tune.bufsize bytes
    - BUG/MAJOR: mux-h2: properly handle short reads on PING frames
    - MINOR: quic: implement the AEAD integrity limit
    - DOC: config: typo

2026/09/28 : 3.2.25
    - BUG/MINOR: something small
    - CLEANUP: tidy

2026/05/02 : 3.2.0
    - BUG/MEDIUM: fix since dev12

2026/04/20 : 3.2-dev12
    - MEDIUM: a dev change
    - MINOR: not kept

2026/04/01 : 3.2-dev11
    - DOC: nothing major
`

func TestHAProxyChangelog(t *testing.T) {
	got := haproxySource.split(haproxyChangelog)
	if want := "- BUG/MEDIUM: cli: fix the last command detection on a full command line\n- BUG/MAJOR: mux-h2: properly handle short reads on PING frames"; got["3.2.26"] != want {
		t.Errorf("3.2.26:\n%s", got["3.2.26"])
	}
	if !strings.Contains(got["3.2.25"], "only minor fixes") {
		t.Errorf("a release without MAJOR/MEDIUM entries says so: %q", got["3.2.25"])
	}
	if want := "- BUG/MEDIUM: fix since dev12\n\nPre-release 3.2-dev12:\n- MEDIUM: a dev change"; got["3.2.0"] != want {
		t.Errorf("3.2.0 includes the dev releases' major changes, nothing else:\n%q", got["3.2.0"])
	}
	if _, ok := got["3.2-dev12"]; ok {
		t.Error("dev releases are not versions of their own")
	}
}

const postgresPage = `<html><body><div id="docContent">
<div class="sect1" id="RELEASE-17-2">
<div class="titlepage"><h2 class="title">E.11. Release 17.2 <a href="#RELEASE-17-2" class="id_link">#</a></h2></div>
<div class="toc"><dl><dt>E.11.1. Migration to Version 17.2</dt></dl></div>
<p><strong>Release date: </strong>2024-11-21</p>
<div class="sect2"><h3 class="title">E.11.1. Migration to Version 17.2</h3>
<p>A dump/restore is not required for those running 17.X.</p></div>
<ul><li><p>Repair ABI break for extensions (Tom Lane)</p></li></ul>
</div>
<div id="footer">Copyright</div>
</div></body></html>`

func TestPostgresText(t *testing.T) {
	got := cleanNotes(postgresText([]byte(postgresPage)))
	for _, want := range []string{"Release 17.2", "A dump/restore is not required", "- Repair ABI break"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Copyright") || strings.Contains(got, "#") || strings.Count(got, "Migration to Version 17.2") != 1 {
		t.Errorf("only the release section, no permalinks or table of contents:\n%s", got)
	}
}

func TestCleanNotes(t *testing.T) {
	got := cleanNotes("<!-- template -->\r\nLine  \r\n\r\n\r\n\r\nNext")
	if got != "Line\n\nNext" {
		t.Errorf("%q", got)
	}
}

// HAProxy-style branch files repeat the history they were forked from; each version must come from
// its own line's file only, once.
func TestPerLineTakesVersionsFromTheirOwnFile(t *testing.T) {
	files := map[string]string{
		"/3.4": "2026/10/01 : 3.4.1\n    - BUG/MEDIUM: own 3.4.1\n\n2026/09/01 : 3.4.0\n    - MEDIUM: own 3.4.0\n\n" +
			"2026/08/01 : 3.4-dev1\n    - MAJOR: 3.4 dev work\n\n2026/05/01 : 3.2.0\n    - MEDIUM: copy of 3.2.0 in the 3.4 file\n",
		"/3.2": "2026/05/01 : 3.2.0\n    - MEDIUM: own 3.2.0\n\n2026/04/01 : 3.2-dev3\n    - MAJOR: 3.2 dev work\n",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(files[r.URL.Path])) }))
	defer srv.Close()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := &Client{F: &Fetcher{HTTP: srv.Client(), Cache: db, TTL: time.Hour}}
	src := haproxySource
	src.fileURL = func(l string) string { return srv.URL + "/" + l }
	from, _ := catalog.ParseVersion("3.0.5", "")
	to, _ := catalog.ParseVersion("3.4.1", "")
	include := func(v catalog.Version) bool { return v.Compare(from) > 0 && v.Compare(to) <= 0 }
	notes, err := c.perLine(context.Background(), &catalog.Component{ID: "haproxy"}, []string{"3.4", "3.2"}, include, src)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, n := range notes {
		if _, dup := got[n.Version]; dup {
			t.Errorf("%s listed twice", n.Version)
		}
		got[n.Version] = n.Text
	}
	if len(got) != 3 || !strings.Contains(got["3.2.0"], "own 3.2.0") || strings.Contains(got["3.2.0"], "copy of") {
		t.Errorf("3.2.0 must come from the 3.2 file: %q", got)
	}
	if !strings.Contains(got["3.2.0"], "3.2 dev work") || !strings.Contains(got["3.4.0"], "3.4 dev work") {
		t.Errorf("each line's dev releases fold into its own x.y.0: %q", got)
	}
}
