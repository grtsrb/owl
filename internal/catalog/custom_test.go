package catalog

import (
	"strings"
	"testing"
)

func TestValidateCustom(t *testing.T) {
	ok := Component{ID: "openssh", Name: "OpenSSH (SFTP)", Images: []string{"atmoz/sftp", "ghcr.io/Org/App"}, GitHub: "openssh/openssh-portable"}
	if err := ValidateCustom(&ok); err != nil {
		t.Fatal(err)
	}
	if ok.Images[0] != "docker.io/atmoz/sftp" || ok.Images[1] != "ghcr.io/org/app" || !ok.Custom || ok.VersionParts != 3 {
		t.Errorf("not normalised: %+v", ok)
	}
	for name, c := range map[string]Component{
		"bad id":        {ID: "Open SSH", Name: "x", Images: []string{"a/b"}, GitHub: "a/b"},
		"no images":     {ID: "x", Name: "x", GitHub: "a/b"},
		"tagged image":  {ID: "x", Name: "x", Images: []string{"a/b:1.0"}, GitHub: "a/b"},
		"digest":        {ID: "x", Name: "x", Images: []string{"a/b@sha256:00"}, GitHub: "a/b"},
		"duplicate":     {ID: "x", Name: "x", Images: []string{"a/b", "docker.io/a/b"}, GitHub: "a/b"},
		"no github":     {ID: "x", Name: "x", Images: []string{"a/b"}},
		"github url":    {ID: "x", Name: "x", Images: []string{"a/b"}, GitHub: "https://github.com/a/b"},
		"eol slug":      {ID: "x", Name: "x", Images: []string{"a/b"}, GitHub: "a/b", EOL: "../x"},
		"html name":     {ID: "x", Name: "<b>x</b>", Images: []string{"a/b"}, GitHub: "a/b"},
		"osv":           {ID: "x", Name: "x", Images: []string{"a/b"}, GitHub: "a/b", OSV: &OSVPackage{"Go", "x"}},
		"release notes": {ID: "x", Name: "x", Images: []string{"a/b"}, GitHub: "a/b", ReleaseNotes: "redis-releasenotes"},
		"no group":      {ID: "x", Name: "x", Images: []string{"a/b"}, GitHub: "a/b", VersionPattern: `^\d+`},
		"bad regexp":    {ID: "x", Name: "x", Images: []string{"a/b"}, GitHub: "a/b", VersionPattern: `^(\d+`},
		"long pattern":  {ID: "x", Name: "x", Images: []string{"a/b"}, GitHub: "a/b", VersionPattern: "(" + strings.Repeat("a", 200) + ")"},
	} {
		if err := ValidateCustom(&c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestVersionPattern(t *testing.T) {
	c := Component{ID: "openssh", Name: "OpenSSH", Images: []string{"linuxserver/openssh-server"}, GitHub: "openssh/openssh-portable",
		VersionPattern: `^(\d+\.\d+)_p(\d+)`}
	if err := ValidateCustom(&c); err != nil {
		t.Fatal(err)
	}
	cat, states := Merge(&Catalog{}, []Component{c})
	if !states[0].Active {
		t.Fatalf("%+v", states)
	}
	for img, want := range map[string]string{
		"linuxserver/openssh-server:9.7_p1-r4-ls163": "identified 9.7.1",
		"linuxserver/openssh-server:latest":          "unpinned",
	} {
		id := cat.Identify(img)
		got := string(id.Status)
		if id.Version != nil {
			got += " " + id.Version.String()
		}
		if want != "" && got != want {
			t.Errorf("%s: %s, want %s", img, got, want)
		}
	}
}

func TestMergeBuiltinWins(t *testing.T) {
	b, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	custom := []Component{
		{ID: "redis", Name: "My Redis", Images: []string{"example.org/x/redis"}, GitHub: "a/b"},           // id taken
		{ID: "my-redis", Name: "My Redis", Images: []string{"docker.io/library/redis"}, GitHub: "a/b"},    // image taken
		{ID: "sftp", Name: "SFTP", Images: []string{"docker.io/atmoz/sftp"}, GitHub: "atmoz/sftp"},        // fine
		{ID: "sftp2", Name: "SFTP again", Images: []string{"docker.io/atmoz/sftp"}, GitHub: "atmoz/sftp"}, // claimed by custom
	}
	cat, states := Merge(b, custom)
	got := map[string]CustomState{}
	for _, s := range states {
		got[s.ID] = s
	}
	if got["redis"].Replaced != "redis" || got["my-redis"].Replaced != "redis" || !got["sftp"].Active || got["sftp2"].Problem == "" {
		t.Errorf("states: %+v", states)
	}
	if c := cat.ByID("redis"); c == nil || c.Custom {
		t.Error("built-in redis replaced")
	}
	if id := cat.Identify("atmoz/sftp:alpine"); id.Component == nil || id.Component.ID != "sftp" {
		t.Errorf("sftp not identified: %+v", id)
	}
	if len(b.Components) == len(cat.Components) || b.ByID("sftp") != nil {
		t.Error("built-in catalog modified")
	}
	if cat.Claimant("atmoz/sftp") != "sftp" || cat.Claimant("redis") != "redis" || cat.Claimant("nobody/else") != "" {
		t.Error("Claimant")
	}
}

func TestReviewFixes(t *testing.T) {
	g := Component{ID: "global", Name: "x", Images: []string{"a/b"}, GitHub: "a/b"}
	if ValidateCustom(&g) == nil {
		t.Error(`id "global" accepted: it's the global policy's scope`)
	}
	b, _ := Builtin()
	// A built-in already recognizes this mirror of redis: the custom one must not take it over.
	m := Component{ID: "myredis", Name: "x", Images: []string{"harbor.example.com/mirror/library/redis"}, GitHub: "a/b"}
	if err := ValidateCustom(&m); err != nil {
		t.Fatal(err)
	}
	cat, st := Merge(b, []Component{m})
	if st[0].Active || st[0].Replaced != "redis" {
		t.Errorf("mirror takeover: %+v", st)
	}
	if id := cat.Identify("harbor.example.com/mirror/library/redis:7.2.4"); id.Component == nil || id.Component.ID != "redis" {
		t.Errorf("built-in lost its mirror: %+v", id)
	}
	if cat.Claimant("harbor.example.com/mirror/library/redis") != "redis" {
		t.Error("Claimant ignores mirrors")
	}
	// With a version pattern, a tag prefix is still required.
	p := Component{ID: "ctl", Name: "x", Images: []string{"example.org/x/ctl"}, GitHub: "a/b", TagPrefix: "controller-", VersionPattern: `(\d+)\.(\d+)\.(\d+)`}
	if err := ValidateCustom(&p); err != nil {
		t.Fatal(err)
	}
	pc, _ := Merge(&Catalog{}, []Component{p})
	if id := pc.Identify("example.org/x/ctl:webhook-1.2.3"); id.Status != StatusUnpinned {
		t.Errorf("tag without the prefix read as a version: %+v", id)
	}
	if id := pc.Identify("example.org/x/ctl:controller-1.2.3"); id.Version == nil || id.Version.String() != "1.2.3" {
		t.Errorf("prefixed tag: %+v", id)
	}
}
