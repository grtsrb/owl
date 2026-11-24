package report

import "testing"

func TestEffectivePolicy(t *testing.T) {
	cases := []struct {
		name string
		pols map[string]string
		want string
		note bool
	}{
		{"nothing set: default", map[string]string{}, "lts-or-n-1", false},
		{"global", map[string]string{"global": "n-2"}, "n-2", false},
		{"component beats global", map[string]string{"global": "n-2", "redis": "pin:7.2"}, "pin:7.2", false},
		{"global pin (older data) ignored", map[string]string{"global": "pin:1.30"}, "lts-or-n-1", true},
		{"global pin ignored, component kept", map[string]string{"global": "pin:1.30", "redis": "latest"}, "latest", false},
		{"invalid component falls back to global", map[string]string{"global": "n-1", "redis": "bogus"}, "n-1", true},
	}
	for _, c := range cases {
		sp, note := effectivePolicy(c.pols, "redis")
		if sp.Raw != c.want || (note != "") != c.note {
			t.Errorf("%s: %q note %q", c.name, sp.Raw, note)
		}
	}
}
