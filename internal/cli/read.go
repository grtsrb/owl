package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// JSON shapes of the API answers, only the fields the CLI shows (internal/report, internal/store,
// internal/upstream; web/src/api.ts mirrors the same types). Kept local so the client build doesn't
// pull in the server packages.

var levelRank = map[string]int{"critical": 5, "high": 4, "medium": 3, "low": 2, "unknown": 1, "ok": 0}
var levels = []string{"critical", "high", "medium", "low", "unknown", "ok"}

type reason struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

type action struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type instanceJSON struct {
	Cluster   string `json:"cluster"`
	Stage     string `json:"stage"`
	Tenant    string `json:"tenant"`
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Workload  string `json:"workload"`
	Container string `json:"container"`
	Image     string `json:"image"`
	Version   string `json:"version"`
	Status    string `json:"status"`
	Replicas  int    `json:"desiredReplicas"`
	Score     struct {
		Level   string   `json:"level"`
		Reasons []reason `json:"reasons"`
		Line    string   `json:"line"`
	} `json:"score"`
	Action action `json:"action"`
}

type componentReport struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Level  string `json:"level"`
	Target *struct {
		Policy  string `json:"policy"`
		Line    string `json:"line"`
		Version string `json:"version"`
	} `json:"target"`
	Upstream struct {
		LatestStable string `json:"latestStable"`
		Retired      bool   `json:"retired"`
		Archived     bool   `json:"archived"`
	} `json:"upstream"`
	Instances []instanceJSON `json:"instances"`
	Drift     []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"drift"`
	Action action `json:"action"`
}

type reportJSON struct {
	GeneratedAt  time.Time         `json:"generatedAt"`
	Summary      map[string]int    `json:"summary"`
	Components   []componentReport `json:"components"`
	Unpinned     int               `json:"unpinned"`
	InHouse      int               `json:"inHouse"`
	Unrecognized int               `json:"unrecognized"`
}

type clusterJSON struct {
	Name       string     `json:"name"`
	Stage      string     `json:"stage"`
	Tenant     string     `json:"tenant"`
	Kubernetes string     `json:"kubernetesVersion"`
	Agent      string     `json:"agentVersion"`
	LastSeenAt *time.Time `json:"lastSeenAt"`
}

func ago(t *time.Time) string {
	if t == nil {
		return "never"
	}
	d := time.Since(*t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// getRaw fetches path; with -o json the answer is printed as is and done=true.
func (a *App) getRaw(ctx context.Context, c *client, f *flags, path string, out any) (done bool, err error) {
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return false, err
	}
	if f.json() {
		_, err := fmt.Fprintln(a.Stdout, string(raw))
		return true, err
	}
	return false, json.Unmarshal(raw, out)
}

func runStatus(a *App, ctx context.Context, args []string) error {
	f := a.newFlags("status", "status [-o json]", true)
	if _, err := f.parse(args); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	var rep reportJSON
	var cls []clusterJSON
	if f.json() {
		var r, cl json.RawMessage
		if err := c.do(ctx, http.MethodGet, "/api/v1/report", nil, &r); err != nil {
			return err
		}
		if err := c.do(ctx, http.MethodGet, "/api/v1/clusters", nil, &cl); err != nil {
			return err
		}
		if err := json.Unmarshal(r, &rep); err != nil {
			return err
		}
		return a.printJSON(map[string]any{"summary": rep.Summary, "unpinned": rep.Unpinned, "inHouse": rep.InHouse,
			"unrecognized": rep.Unrecognized, "generatedAt": rep.GeneratedAt, "clusters": cl})
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/report", nil, &rep); err != nil {
		return err
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/clusters", nil, &cls); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "Components by priority (%s):\n", c.base.Host)
	tw := a.table()
	for _, l := range levels {
		fmt.Fprintf(tw, "  %s\t%d\n", l, rep.Summary[l])
	}
	tw.Flush()
	fmt.Fprintf(a.Stdout, "Unrecognized images: %d   unpinned: %d   in-house: %d\n\n", rep.Unrecognized, rep.Unpinned, rep.InHouse)
	return a.printClusters(cls)
}

func (a *App) printClusters(cls []clusterJSON) error {
	tw := a.table()
	fmt.Fprintln(tw, "CLUSTER\tSTAGE\tTENANT\tKUBERNETES\tAGENT\tLAST REPORT")
	for _, c := range cls {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", c.Name, c.Stage, c.Tenant, dash(c.Kubernetes), dash(c.Agent), ago(c.LastSeenAt))
	}
	return tw.Flush()
}

func runClusters(a *App, ctx context.Context, args []string) error {
	f := a.newFlags("clusters", "clusters [-o json]", true)
	if _, err := f.parse(args); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	var cls []clusterJSON
	if done, err := a.getRaw(ctx, c, f, "/api/v1/clusters", &cls); done || err != nil {
		return err
	}
	return a.printClusters(cls)
}

// splitList: "a,b" -> {a, b}; empty -> nil.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func validLevel(l string) bool { _, ok := levelRank[l]; return ok }

type outdatedRow struct {
	Level     string   `json:"level"`
	Component string   `json:"component"`
	Cluster   string   `json:"cluster"`
	Tenant    string   `json:"tenant"`
	Namespace string   `json:"namespace,omitempty"`
	Workload  string   `json:"workload,omitempty"`
	Container string   `json:"container,omitempty"`
	Image     string   `json:"image"`
	Version   string   `json:"version,omitempty"`
	Target    string   `json:"target,omitempty"`
	Action    string   `json:"action"`
	Reasons   []string `json:"reasons,omitempty"`
}

func runOutdated(a *App, ctx context.Context, args []string) error {
	f := a.newFlags("outdated", "outdated [--cluster a,b] [--tenant t] [--level low] [--all] [--fail-on critical|high|…] [-o json]", true)
	cluster := f.String("cluster", "", "only these clusters (comma-separated)")
	tenant := f.String("tenant", "", "only clusters of these tenants (comma-separated)")
	minLevel := f.String("level", "low", "show this level and above (critical, high, medium, low, unknown, ok)")
	all := f.Bool("all", false, "show everything, including ok (same as --level ok)")
	failOn := f.String("fail-on", "", "exit 1 if anything shown is at this level or above (for CI)")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return a.usageErr("unexpected arguments: %s (filters are flags: --cluster, --tenant, --level)", strings.Join(pos, " "))
	}
	if *all {
		*minLevel = "ok"
	}
	if !validLevel(*minLevel) || (*failOn != "" && !validLevel(*failOn)) {
		return a.usageErr("levels: critical, high, medium, low, unknown, ok")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	var rep reportJSON
	if err := c.do(ctx, http.MethodGet, "/api/v1/report", nil, &rep); err != nil {
		return err
	}
	clusters, tenants := splitList(*cluster), splitList(*tenant)
	var rows []outdatedRow
	failed := false
	for _, comp := range rep.Components {
		target := ""
		if comp.Target != nil {
			target = comp.Target.Version
		}
		for _, in := range comp.Instances {
			if len(clusters) > 0 && !slices.Contains(clusters, in.Cluster) || len(tenants) > 0 && !slices.Contains(tenants, in.Tenant) {
				continue
			}
			lvl := in.Score.Level
			if levelRank[lvl] < levelRank[*minLevel] {
				continue
			}
			if *failOn != "" && levelRank[lvl] >= levelRank[*failOn] {
				failed = true
			}
			r := outdatedRow{Level: lvl, Component: comp.ID, Cluster: in.Cluster, Tenant: in.Tenant, Namespace: in.Namespace,
				Workload: in.Workload, Container: in.Container, Image: in.Image, Version: in.Version, Target: target, Action: in.Action.Text}
			for _, rs := range in.Score.Reasons {
				r.Reasons = append(r.Reasons, rs.Text)
			}
			rows = append(rows, r)
		}
	}
	slices.SortStableFunc(rows, func(x, y outdatedRow) int {
		if d := levelRank[y.Level] - levelRank[x.Level]; d != 0 {
			return d
		}
		if d := strings.Compare(x.Component, y.Component); d != 0 {
			return d
		}
		return strings.Compare(x.Cluster, y.Cluster)
	})
	if f.json() {
		if rows == nil {
			rows = []outdatedRow{}
		}
		if err := a.printJSON(rows); err != nil {
			return err
		}
	} else if len(rows) == 0 {
		fmt.Fprintf(a.Stdout, "Nothing at %s or above.\n", *minLevel)
	} else {
		tw := a.table()
		fmt.Fprintln(tw, "LEVEL\tCOMPONENT\tCLUSTER\tWORKLOAD\tVERSION\tTARGET\tACTION")
		for _, r := range rows {
			wl := "-"
			if r.Workload != "" {
				wl = r.Namespace + "/" + r.Workload
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", strings.ToUpper(r.Level), r.Component, r.Cluster, wl, dash(r.Version), dash(r.Target), r.Action)
		}
		tw.Flush()
	}
	if failed {
		fmt.Fprintf(a.Stderr, "owl: found %s or above (--fail-on %s)\n", *failOn, *failOn)
		return exitError{1}
	}
	return nil
}

type componentDetail struct {
	Component struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		GitHub string `json:"github"`
		EOL    string `json:"eol"`
	} `json:"component"`
	Upstream *struct {
		Lines []struct {
			Name    string     `json:"name"`
			Latest  string     `json:"latest"`
			LTS     bool       `json:"lts"`
			EOLDate *time.Time `json:"eolDate"`
			EOL     bool       `json:"eol"`
		} `json:"lines"`
		LatestStable   string    `json:"latestStable"`
		Archived       bool      `json:"archived"`
		Retired        bool      `json:"retired"`
		AdvisorySource string    `json:"advisorySource"`
		FetchedAt      time.Time `json:"fetchedAt"`
		Stale          bool      `json:"stale"`
	} `json:"upstream"`
	AdvisoriesByVersion []struct {
		Version    string `json:"version"`
		Error      string `json:"error"`
		Advisories []struct {
			ID          string `json:"id"`
			Severity    string `json:"severity"`
			Summary     string `json:"summary"`
			URL         string `json:"url"`
			RelevantFix string `json:"relevantFix"`
			NoFix       bool   `json:"noFix"`
			Certain     bool   `json:"certain"`
		} `json:"advisories"`
	} `json:"advisoriesByVersion"`
}

func runComponent(a *App, ctx context.Context, args []string) error {
	f := a.newFlags("component", "component <id> [-o json]", true)
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return a.usageErr("usage: owl component <id>   (ids: `owl outdated --all`)")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	var d componentDetail
	if done, err := a.getRaw(ctx, c, f, "/api/v1/components/"+url.PathEscape(pos[0]), &d); done || err != nil {
		return err
	}
	var rep reportJSON
	if err := c.do(ctx, http.MethodGet, "/api/v1/report", nil, &rep); err != nil {
		return err
	}
	var comp *componentReport
	for i := range rep.Components {
		if rep.Components[i].ID == d.Component.ID {
			comp = &rep.Components[i]
		}
	}
	w := a.Stdout
	fmt.Fprintf(w, "%s (%s)\n", d.Component.Name, d.Component.ID)
	if d.Component.GitHub != "" {
		fmt.Fprintf(w, "  GitHub:      https://github.com/%s\n", d.Component.GitHub)
	}
	if comp != nil {
		fmt.Fprintf(w, "  Priority:    %s\n  Action:      %s\n", strings.ToUpper(comp.Level), comp.Action.Text)
		if comp.Target != nil {
			fmt.Fprintf(w, "  Target:      %s (policy %s)\n", dash(comp.Target.Version), comp.Target.Policy)
		}
	}
	if u := d.Upstream; u != nil {
		state := ""
		if u.Archived {
			state = " — repository archived"
		} else if u.Retired {
			state = " — retired upstream"
		}
		fmt.Fprintf(w, "  Latest:      %s%s\n", dash(u.LatestStable), state)
		if u.Stale {
			fmt.Fprintf(w, "  (upstream data is stale: last fetched %s)\n", ago(&u.FetchedAt))
		}
		if len(u.Lines) > 0 {
			fmt.Fprintln(w, "\nRelease lines:")
			tw := a.table()
			fmt.Fprintln(tw, "  LINE\tLATEST\tEOL")
			// The newest lines and every line something runs on; the rest is summarised (-o json has all).
			inUse := map[string]bool{}
			if comp != nil {
				for _, in := range comp.Instances {
					inUse[in.Score.Line] = true
				}
			}
			hidden := 0
			for i, l := range u.Lines {
				if i >= 5 && !inUse[l.Name] {
					hidden++
					continue
				}
				eol := "-"
				if l.EOLDate != nil {
					eol = l.EOLDate.UTC().Format("2006-01-02")
				}
				if l.EOL {
					eol += " (ended)"
				}
				lts := ""
				if l.LTS {
					lts = " LTS"
				}
				run := ""
				if inUse[l.Name] {
					run = "  ← running"
				}
				fmt.Fprintf(tw, "  %s%s\t%s\t%s%s\n", l.Name, lts, dash(l.Latest), eol, run)
			}
			tw.Flush()
			if hidden > 0 {
				fmt.Fprintf(w, "  … %d older lines nothing runs on (-o json shows all)\n", hidden)
			}
		}
	}
	if comp != nil && len(comp.Instances) > 0 {
		fmt.Fprintln(w, "\nRunning:")
		tw := a.table()
		fmt.Fprintln(tw, "  LEVEL\tCLUSTER\tWORKLOAD\tVERSION\tACTION")
		for _, in := range comp.Instances {
			wl := "-"
			if in.Workload != "" {
				wl = in.Namespace + "/" + in.Workload
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", strings.ToUpper(in.Score.Level), in.Cluster, wl, dash(in.Version), in.Action.Text)
		}
		tw.Flush()
		for _, dr := range comp.Drift {
			fmt.Fprintf(w, "  drift: %s\n", dr.Text)
		}
	}
	// No advisory source means "no data", never "no vulnerabilities" (DESIGN.md).
	if d.Upstream == nil || d.Upstream.AdvisorySource == "" {
		fmt.Fprintln(w, "\nAdvisories: no advisory data for this component (not the same as none).")
	}
	for _, v := range d.AdvisoriesByVersion {
		if v.Error != "" {
			fmt.Fprintf(w, "\nAdvisories for %s: not checked (%s)\n", v.Version, v.Error)
			continue
		}
		if len(v.Advisories) == 0 {
			continue
		}
		fmt.Fprintf(w, "\nAdvisories for %s:\n", v.Version)
		tw := a.table()
		review := false
		for _, adv := range v.Advisories {
			fix := "fixed in " + dash(adv.RelevantFix)
			if adv.NoFix {
				fix = "no fix yet"
			}
			if !adv.Certain {
				fix += " (review)"
				review = true
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", strings.ToUpper(dash(adv.Severity)), adv.ID, fix, adv.URL)
		}
		tw.Flush()
		if review {
			fmt.Fprintln(w, "  (review) = the advisory's affected versions can't be matched for sure: check it")
		}
	}
	return nil
}

type changeJSON struct {
	Cluster   string    `json:"cluster"`
	At        time.Time `json:"at"`
	Type      string    `json:"type"`
	Namespace string    `json:"namespace"`
	Kind      string    `json:"kind"`
	Workload  string    `json:"workload"`
	Container string    `json:"container"`
	OldValue  string    `json:"oldValue"`
	NewValue  string    `json:"newValue"`
}

func runChanges(a *App, ctx context.Context, args []string) error {
	f := a.newFlags("changes", "changes [--cluster name] [--limit 50] [-o json]", true)
	cluster := f.String("cluster", "", "only this cluster")
	limit := f.Int("limit", 50, "at most this many (newest first, ≤ 1000)")
	if _, err := f.parse(args); err != nil {
		return err
	}
	if *limit < 1 || *limit > 1000 {
		return a.usageErr("--limit: 1 to 1000")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	q := url.Values{"limit": {strconv.Itoa(*limit)}}
	if *cluster != "" {
		q.Set("cluster", *cluster)
	}
	var cs []changeJSON
	if done, err := a.getRaw(ctx, c, f, "/api/v1/changes?"+q.Encode(), &cs); done || err != nil {
		return err
	}
	if len(cs) == 0 {
		fmt.Fprintln(a.Stdout, "No changes recorded.")
		return nil
	}
	tw := a.table()
	fmt.Fprintln(tw, "WHEN\tCLUSTER\tCHANGE\tWORKLOAD\tFROM\tTO")
	for _, ch := range cs {
		wl := "-"
		if ch.Workload != "" {
			wl = ch.Namespace + "/" + ch.Workload
			if ch.Container != "" {
				wl += " (" + ch.Container + ")"
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", ch.At.Local().Format("2006-01-02 15:04"), ch.Cluster, ch.Type, wl, dash(ch.OldValue), dash(ch.NewValue))
	}
	return tw.Flush()
}

func runUnrecognized(a *App, ctx context.Context, args []string) error {
	f := a.newFlags("unrecognized", "unrecognized [-o json]", true)
	if _, err := f.parse(args); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodGet, "/api/v1/inventory", nil, &raw); err != nil {
		return err
	}
	if f.json() { // the server's own objects, untouched
		var part struct {
			Unrecognized json.RawMessage `json:"unrecognized"`
		}
		if err := json.Unmarshal(raw, &part); err != nil {
			return err
		}
		_, err := fmt.Fprintln(a.Stdout, string(part.Unrecognized))
		return err
	}
	var inv struct {
		Unrecognized []struct {
			Repository string         `json:"repository"`
			Instances  []instanceJSON `json:"instances"`
		} `json:"unrecognized"`
	}
	if err := json.Unmarshal(raw, &inv); err != nil {
		return err
	}
	if len(inv.Unrecognized) == 0 {
		fmt.Fprintln(a.Stdout, "Every image is recognized.")
		return nil
	}
	tw := a.table()
	fmt.Fprintln(tw, "REPOSITORY\tCLUSTERS\tTAGS")
	for _, u := range inv.Unrecognized {
		var cl, tags []string
		for _, in := range u.Instances {
			if !slices.Contains(cl, in.Cluster) {
				cl = append(cl, in.Cluster)
			}
			tag := imageTag(in.Image)
			if !slices.Contains(tags, tag) {
				tags = append(tags, tag)
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", u.Repository, strings.Join(cl, ","), strings.Join(tags, ","))
	}
	tw.Flush()
	fmt.Fprintln(a.Stdout, "\nAdd them in Owl: Clusters → Unrecognized images, or Settings → Catalog.")
	return nil
}

// imageTag is the tag of an image reference ("latest" when there's none), or a short digest.
func imageTag(image string) string {
	if i := strings.Index(image, "@"); i >= 0 {
		d := image[i+1:]
		if len(d) > 19 {
			d = d[:19] + "…"
		}
		return "@" + d
	}
	name := image[strings.LastIndex(image, "/")+1:] // a registry port (host:5000/…) isn't a tag
	if i := strings.LastIndex(name, ":"); i >= 0 {
		return name[i+1:]
	}
	return "latest"
}
