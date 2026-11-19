package summary

import (
	"strings"
	"testing"
)

func TestCleanRemovesLinks(t *testing.T) {
	for _, in := range []string{"see https://evil.example/upgrade now", "visit www.evil.example", "HTTP://X.Y/z", "go to evil.example/upgrade", "mailto:a@evil.example", "see //evil.example/x", "ping admin@evil.example", "EVIL.EXAMPLE"} {
		if got := clean(in, 200); strings.Contains(strings.ToLower(got), "evil") || strings.Contains(strings.ToLower(got), "x.y") {
			t.Errorf("clean(%q) = %q", in, got)
		}
	}
	for _, keep := range []string{"upgrade to 1.2.3 first", "set replicas in values.yaml (e.g. 3)", "v1.2.x and 2.0.0-rc.1", "Node.js 20: ratio 1:2"} {
		if got := clean(keep, 200); got != keep {
			t.Errorf("plain text changed: %q -> %q", keep, got)
		}
	}
}
