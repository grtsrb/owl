//go:build !owlclient && !owlagent

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"owl/internal/catalog"
	"owl/internal/store/sqlite"
)

// runCatalogCheck verifies built-in catalog entries against the live upstreams (milestone 13): each GitHub
// repo exists, isn't archived and has releases that parse with the entry's tag prefix; each endoflife.date
// slug exists and agrees with GitHub about the newest release line; each OSV package has advisories.
// A manual check while adding entries and before a release — not CI (live network, rate limits).
func runCatalogCheck(args []string) error {
	fs := flag.NewFlagSet("catalog-check", flag.ExitOnError)
	dbPath := fs.String("db", "bin/catalog-check.db", "SQLite file used as the HTTP cache")
	only := fs.String("component", "", "check only these component ids (comma separated)")
	_ = fs.Parse(args)

	cat, err := catalog.Builtin()
	if err != nil {
		return err
	}
	if os.Getenv("GITHUB_TOKEN") == "" {
		return fmt.Errorf("GITHUB_TOKEN not set (set -a; . ./.env; set +a) — without it GitHub allows 60 requests/hour")
	}
	db, err := sqlite.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	cl := newUpstreamClient(db)
	want := map[string]bool{}
	for _, id := range strings.Split(*only, ",") {
		if id = strings.TrimSpace(id); id != "" {
			want[id] = true
		}
	}
	ctx := context.Background()
	failed, warned, checked := 0, 0, 0
	for i := range cat.Components {
		c := &cat.Components[i]
		if len(want) > 0 && !want[c.ID] {
			continue
		}
		checked++
		var warns []string
		info := cl.Refresh(ctx, c)
		fails := append([]string(nil), info.Errors...)
		if len(info.Lines) == 0 && len(info.Errors) == 0 {
			if c.GitHub != "" {
				fails = append(fails, "no releases found on "+c.GitHub+" (check releaseTagPrefix)")
			} else {
				fails = append(fails, "no release lines on endoflife.date "+c.EOL)
			}
		}
		if info.Archived {
			warns = append(warns, c.GitHub+" is archived on GitHub")
		}
		if c.EOL != "" && c.GitHub != "" {
			gh := cl.GitHubLatest(ctx, c)
			a, okA := catalog.ParseVersion(info.LatestStable, "")
			b, okB := catalog.ParseVersion(gh, "")
			switch {
			case !okB:
				warns = append(warns, "no GitHub release to compare endoflife.date with")
			case okA && (a.Major != b.Major || a.Minor != b.Minor):
				warns = append(warns, fmt.Sprintf("endoflife.date %q says newest %s, GitHub %s says %s — same project?", c.EOL, info.LatestStable, c.GitHub, gh))
			}
		}
		if c.OSV != nil {
			n, err := cl.OSVCount(ctx, c.OSV)
			switch {
			case err != nil:
				fails = append(fails, "osv: "+err.Error())
			case n == 0:
				warns = append(warns, fmt.Sprintf("OSV has no advisories for %s %s — check the package name", c.OSV.Ecosystem, c.OSV.Name))
			}
		}
		status := "ok  "
		switch {
		case len(fails) > 0:
			status, failed = "FAIL", failed+1
		case len(warns) > 0:
			status, warned = "warn", warned+1
		}
		fmt.Printf("%s %-28s latest=%-12s lines=%-3d advisories=%-4d %s\n", status, c.ID, info.LatestStable, len(info.Lines),
			len(info.Advisories), strings.Join(append(fails, warns...), "; "))
	}
	fmt.Printf("\n%d checked, %d failed, %d with warnings\n", checked, failed, warned)
	if failed > 0 {
		return fmt.Errorf("%d catalog entries failed", failed)
	}
	return nil
}
