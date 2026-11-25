package main

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// TestBuildDeps keeps the two small builds small: the downloadable client (owlclient) and the slim agent
// image (owlagent) must not pull in server-side code. Packages of this module are an allowlist (so a new
// server package is caught without updating the test); heavy third-party modules are a denylist (prefixes).
func TestBuildDeps(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not in PATH")
	}
	heavy := []string{"modernc.org/sqlite", "github.com/minio/", "github.com/anthropics/", "github.com/yuin/goldmark"}
	for _, tc := range []struct {
		tag       string
		owl       []string // the only packages of this module allowed
		forbidden []string // third-party prefixes
	}{
		{"owlclient", []string{"owl/cmd/owl", "owl/internal/cli"}, slices.Concat(heavy, []string{"k8s.io/"})},
		{"owlagent", []string{"owl/cmd/owl", "owl/internal/agent", "owl/internal/snapshot"}, heavy},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			out, err := exec.Command("go", "list", "-deps", "-tags", tc.tag, ".").CombinedOutput()
			if err != nil {
				t.Fatalf("go list: %v\n%s", err, out)
			}
			for pkg := range strings.FieldsSeq(string(out)) {
				if (pkg == "owl" || strings.HasPrefix(pkg, "owl/")) && !slices.Contains(tc.owl, pkg) {
					t.Errorf("-tags %s depends on %s (allowed from this module: %v)", tc.tag, pkg, tc.owl)
				}
				for _, f := range tc.forbidden {
					if p := strings.TrimSuffix(f, "/"); pkg == p || strings.HasPrefix(pkg, p+"/") {
						t.Errorf("-tags %s depends on %s (forbidden: %s)", tc.tag, pkg, f)
					}
				}
			}
		})
	}
}
