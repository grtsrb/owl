package summary

import (
	"strings"
	"testing"
)

func TestCleanRemovesLinks(t *testing.T) {
	for _, in := range []string{"see https://evil.example/upgrade now", "visit www.evil.example", "HTTP://X.Y/z"} {
		if got := clean(in, 200); strings.Contains(strings.ToLower(got), "evil") || strings.Contains(strings.ToLower(got), "x.y") {
			t.Errorf("clean(%q) = %q", in, got)
		}
	}
	if got := clean("upgrade to 1.2.3 first", 200); got != "upgrade to 1.2.3 first" {
		t.Errorf("plain text changed: %q", got)
	}
}
