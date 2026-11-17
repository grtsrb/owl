package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"owl/internal/catalog"
)

// ReleaseLine is one supported/unsupported release series (endoflife.date "cycle", otherwise a minor).
type ReleaseLine struct {
	Name       string     `json:"name"`   // "17", "1.31", "1.11"
	Latest     string     `json:"latest"` // newest stable version in the line
	LatestDate *time.Time `json:"latestDate,omitempty"`
	LTS        bool       `json:"lts"`
	EOLDate    *time.Time `json:"eolDate,omitempty"`
	EOL        bool       `json:"eol"`
	Maintained *bool      `json:"maintained,omitempty"` // nil = unknown (GitHub-derived lines)
}

// Info is everything known upstream about a component.
type Info struct {
	ComponentID  string        `json:"componentId"`
	Lines        []ReleaseLine `json:"lines"` // newest first
	LatestStable string        `json:"latestStable,omitempty"`
	LinesSource  string        `json:"linesSource,omitempty"` // endoflife.date | github-releases
	Archived     bool          `json:"archived"`              // GitHub repo archived
	// Retired: archived and no release for RetiredAfter. An archived repo that still ships releases
	// (development moved elsewhere) is not retired — that's a "check upstream" note, not Critical.
	Retired       bool       `json:"retired"`
	LastReleaseAt *time.Time `json:"lastReleaseAt,omitempty"`
	Advisories    []Advisory `json:"advisories"` // repo-published advisories (range matched)
	// AdvisorySource says where per-version vulnerability data comes from: "osv", "github-repo", or
	// "" when there is none — including repos that publish no GitHub advisories at all (postgres,
	// nginx, haproxy). The UI must say "no advisory data", never "no vulnerabilities".
	AdvisorySource string    `json:"advisorySource"`
	FetchedAt      time.Time `json:"fetchedAt"`
	Stale          bool      `json:"stale,omitempty"`
	Errors         []string  `json:"errors,omitempty"`
}

type Client struct {
	F *Fetcher
}

// Refresh gathers upstream info for one component. Partial failures are recorded in Errors; whatever
// could be fetched is still returned.
func (c *Client) Refresh(ctx context.Context, comp *catalog.Component) *Info {
	info := &Info{ComponentID: comp.ID, FetchedAt: time.Now().UTC(), Advisories: []Advisory{}}
	fail := func(what string, err error) { info.Errors = append(info.Errors, what+": "+err.Error()) }
	releasesKnown := false // did we actually get release data? (an error must not read as "no releases")

	if comp.EOL != "" {
		if err := c.eolLines(ctx, comp, info); err != nil {
			fail("endoflife.date", err)
		} else {
			releasesKnown = true
		}
	}
	if comp.GitHub != "" {
		if err := c.repoStatus(ctx, comp, info); err != nil {
			fail("github repo", err)
		}
		if len(info.Lines) == 0 {
			if err := c.githubLines(ctx, comp, info); err != nil {
				fail("github releases", err)
			} else {
				releasesKnown = true
			}
		}
	}
	osvOK := false
	if comp.OSV != nil {
		advs, stale, err := c.osvAll(ctx, comp.OSV)
		if err != nil {
			fail("osv", err)
		} else {
			c.fillSeverities(ctx, advs)
			info.Advisories, info.AdvisorySource, osvOK = advs, "osv", true
			info.Stale = info.Stale || stale
		}
	}
	if comp.GitHub != "" && !osvOK { // no OSV configured, or OSV failed: use the repo's advisories
		if err := c.repoAdvisories(ctx, comp, info); err != nil {
			fail("github advisories", err)
		} else if len(info.Advisories) > 0 {
			info.AdvisorySource = "github-repo"
		}
	}
	for _, l := range info.Lines {
		if l.LatestDate != nil && (info.LastReleaseAt == nil || l.LatestDate.After(*info.LastReleaseAt)) {
			info.LastReleaseAt = l.LatestDate
		}
	}
	info.Retired = info.Archived && releasesKnown &&
		(info.LastReleaseAt == nil || time.Since(*info.LastReleaseAt) > RetiredAfter)
	return info
}

// RetiredAfter is how long an archived project must have gone without a release to count as retired.
const RetiredAfter = 90 * 24 * time.Hour

// --- endoflife.date --------------------------------------------------------------------------

type eolProduct struct {
	Result struct {
		Releases []struct {
			Name         string  `json:"name"`
			IsLts        bool    `json:"isLts"`
			IsEol        bool    `json:"isEol"`
			EolFrom      *string `json:"eolFrom"`
			IsMaintained *bool   `json:"isMaintained"`
			Latest       *struct {
				Name string  `json:"name"`
				Date *string `json:"date"`
			} `json:"latest"`
		} `json:"releases"`
	} `json:"result"`
}

func (c *Client) eolLines(ctx context.Context, comp *catalog.Component, info *Info) error {
	res, err := c.F.get(ctx, "eol:"+comp.EOL, "https://endoflife.date/api/v1/products/"+url.PathEscape(comp.EOL))
	if err != nil {
		return err
	}
	info.Stale = info.Stale || res.Stale
	var p eolProduct
	if err := json.Unmarshal(res.Body, &p); err != nil {
		return err
	}
	for _, r := range p.Result.Releases {
		line := ReleaseLine{Name: r.Name, LTS: r.IsLts, EOL: r.IsEol, Maintained: r.IsMaintained}
		if r.EolFrom != nil {
			line.EOLDate = parseDate(*r.EolFrom)
		}
		if r.Latest != nil {
			line.Latest = r.Latest.Name
			if r.Latest.Date != nil {
				line.LatestDate = parseDate(*r.Latest.Date)
			}
		}
		info.Lines = append(info.Lines, line)
	}
	sortLines(info.Lines)
	info.LinesSource = "endoflife.date"
	info.LatestStable = newestLatest(info.Lines)
	return nil
}

// --- GitHub ----------------------------------------------------------------------------------

func (c *Client) repoStatus(ctx context.Context, comp *catalog.Component, info *Info) error {
	res, err := c.F.get(ctx, "gh-repo:"+comp.GitHub, "https://api.github.com/repos/"+comp.GitHub)
	if err != nil {
		return err
	}
	info.Stale = info.Stale || res.Stale
	var r struct {
		Archived bool `json:"archived"`
	}
	if err := json.Unmarshal(res.Body, &r); err != nil {
		return err
	}
	info.Archived = r.Archived
	return nil
}

type ghRelease struct {
	Tag         string    `json:"tag_name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
}

// githubLines derives release lines (per minor) from GitHub releases, newest 300.
func (c *Client) githubLines(ctx context.Context, comp *catalog.Component, info *Info) error {
	type found struct {
		v    catalog.Version
		date time.Time
	}
	best := map[string]found{} // line -> newest stable release
	for page := 1; page <= 3; page++ {
		u := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=100&page=%d", comp.GitHub, page)
		res, err := c.F.get(ctx, fmt.Sprintf("gh-releases:%s:%d", comp.GitHub, page), u)
		if err != nil {
			return err
		}
		info.Stale = info.Stale || res.Stale
		var rels []ghRelease
		if err := json.Unmarshal(res.Body, &rels); err != nil {
			return err
		}
		for _, r := range rels {
			if r.Draft || r.Prerelease {
				continue
			}
			if comp.ReleaseTagPrefix != "" && !strings.HasPrefix(r.Tag, comp.ReleaseTagPrefix) {
				continue
			}
			v, ok := catalog.ParseVersion(r.Tag, comp.TagPrefix)
			if !ok || v.Prerelease != "" || v.Parts < 2 {
				continue
			}
			line := fmt.Sprintf("%d.%d", v.Major, v.Minor)
			if cur, ok := best[line]; !ok || v.Compare(cur.v) > 0 {
				best[line] = found{v, r.PublishedAt}
			}
		}
		if len(rels) < 100 {
			break
		}
	}
	for name, f := range best {
		d := f.date
		info.Lines = append(info.Lines, ReleaseLine{Name: name, Latest: f.v.String(), LatestDate: &d})
	}
	sortLines(info.Lines)
	info.LinesSource = "github-releases"
	info.LatestStable = newestLatest(info.Lines)
	return nil
}

func (c *Client) repoAdvisories(ctx context.Context, comp *catalog.Component, info *Info) error {
	u := "https://api.github.com/repos/" + comp.GitHub + "/security-advisories?per_page=100&state=published"
	res, err := c.F.get(ctx, "gh-advisories:"+comp.GitHub, u)
	if err != nil {
		return err
	}
	info.Stale = info.Stale || res.Stale
	var raw []struct {
		GHSA     string  `json:"ghsa_id"`
		CVE      *string `json:"cve_id"`
		Severity string  `json:"severity"`
		Summary  string  `json:"summary"`
		URL      string  `json:"html_url"`
		Vulns    []struct {
			Range   string `json:"vulnerable_version_range"`
			Patched string `json:"patched_versions"`
		} `json:"vulnerabilities"`
	}
	if err := json.Unmarshal(res.Body, &raw); err != nil {
		return err
	}
	for _, r := range raw {
		a := Advisory{ID: r.GHSA, Severity: strings.ToLower(r.Severity), Summary: r.Summary, URL: r.URL, Source: "github-repo"}
		if r.CVE != nil && *r.CVE != "" {
			a.Aliases = []string{*r.CVE}
		}
		for _, v := range r.Vulns {
			a.Ranges = append(a.Ranges, AffectedRange{Range: v.Range, Patched: v.Patched})
		}
		info.Advisories = append(info.Advisories, a)
	}
	return nil
}

// --- OSV.dev ---------------------------------------------------------------------------------

// osvAll returns every advisory OSV has for the package, with affected ranges converted to the same
// form as GitHub repo advisories so one matcher evaluates both. We deliberately don't use OSV's
// per-version query: for modules whose release tags aren't Go module versions (ingress-nginx's
// controller-v1.x, Grafana) it returns every advisory regardless of version.
func (c *Client) osvAll(ctx context.Context, pkg *catalog.OSVPackage) ([]Advisory, bool, error) {
	type osvVuln struct {
		ID       string   `json:"id"`
		Aliases  []string `json:"aliases"`
		Summary  string   `json:"summary"`
		Database struct {
			Severity string `json:"severity"`
		} `json:"database_specific"`
		Affected []struct {
			Package struct {
				Name string `json:"name"`
			} `json:"package"`
			Ranges []struct {
				Type   string              `json:"type"`
				Events []map[string]string `json:"events"`
			} `json:"ranges"`
		} `json:"affected"`
	}
	// All pages are fetched together and cached as ONE entry, so a page can never be mixed with pages
	// (or page tokens) from another point in time.
	res, err := c.F.cached(ctx, "osv-all:"+pkg.Ecosystem+":"+pkg.Name, c.F.TTL,
		func(ctx context.Context, _ string) ([]byte, string, bool, error) {
			var all []json.RawMessage
			token := ""
			for page := 0; page < 20; page++ {
				q := map[string]any{"package": map[string]string{"ecosystem": pkg.Ecosystem, "name": pkg.Name}}
				if token != "" {
					q["page_token"] = token
				}
				body, _ := json.Marshal(q)
				b, err := c.F.postJSON(ctx, "https://api.osv.dev/v1/query", body)
				if err != nil {
					return nil, "", false, err
				}
				var r struct {
					Vulns []json.RawMessage `json:"vulns"`
					Next  string            `json:"next_page_token"`
				}
				if err := json.Unmarshal(b, &r); err != nil {
					return nil, "", false, err
				}
				all = append(all, r.Vulns...)
				if token = r.Next; token == "" {
					combined, err := json.Marshal(all)
					return combined, "", false, err
				}
			}
			return nil, "", false, errors.New("osv: too many result pages")
		})
	if err != nil {
		return nil, false, err
	}
	stale := res.Stale
	var vulns []osvVuln
	if err := json.Unmarshal(res.Body, &vulns); err != nil {
		return nil, stale, err
	}

	var out []Advisory
	for _, v := range vulns {
		a := Advisory{ID: v.ID, Aliases: v.Aliases, Summary: v.Summary, Severity: strings.ToLower(v.Database.Severity),
			URL: "https://osv.dev/vulnerability/" + v.ID, Source: "osv"}
		for _, af := range v.Affected {
			if af.Package.Name != pkg.Name {
				continue
			}
			for _, rg := range af.Ranges {
				if rg.Type == "SEMVER" || rg.Type == "ECOSYSTEM" {
					a.Ranges = append(a.Ranges, eventsToRanges(rg.Events)...)
				}
			}
		}
		if len(a.Ranges) > 0 {
			out = append(out, a)
		}
	}
	return dedupeOSV(out), stale, nil
}

// eventsToRanges converts OSV events (introduced/fixed/last_affected, in order) into GitHub-style
// range expressions: introduced 0 + fixed 1.11.5 -> "< 1.11.5" patched 1.11.5.
func eventsToRanges(events []map[string]string) []AffectedRange {
	var out []AffectedRange
	lower := ""
	open := false
	for _, ev := range events {
		switch {
		case ev["introduced"] != "":
			lower, open = ev["introduced"], true
		case ev["fixed"] != "" && open:
			out = append(out, AffectedRange{Range: joinBounds(lower, "< "+ev["fixed"]), Patched: ev["fixed"]})
			open = false
		case ev["last_affected"] != "" && open:
			out = append(out, AffectedRange{Range: joinBounds(lower, "<= "+ev["last_affected"])})
			open = false
		}
	}
	if open { // no fix yet
		if lower == "0" {
			out = append(out, AffectedRange{Range: ">= 0.0.0"})
		} else {
			out = append(out, AffectedRange{Range: ">= " + lower})
		}
	}
	return out
}

func joinBounds(lower, upper string) string {
	if lower == "" || lower == "0" {
		return upper
	}
	return ">= " + lower + ", " + upper
}

// fillSeverities looks up missing severities via GHSA aliases (Go vulndb GO-… entries carry none).
func (c *Client) fillSeverities(ctx context.Context, advs []Advisory) {
	for i := range advs {
		if advs[i].Severity != "" {
			continue
		}
		for _, alias := range advs[i].Aliases {
			if strings.HasPrefix(alias, "GHSA-") {
				if sev := c.osvSeverity(ctx, alias); sev != "" {
					advs[i].Severity = sev
					break
				}
			}
		}
	}
}

// osvSeverity returns the GitHub-reviewed severity of an advisory ID, or "" if unknown.
func (c *Client) osvSeverity(ctx context.Context, id string) string {
	// Severity of a published advisory rarely changes: cache for a week.
	res, err := c.F.getTTL(ctx, "osv-vuln:"+id, "https://api.osv.dev/v1/vulns/"+url.PathEscape(id), 7*24*time.Hour)
	if err != nil {
		return ""
	}
	var v struct {
		Database struct {
			Severity string `json:"severity"`
		} `json:"database_specific"`
	}
	if json.Unmarshal(res.Body, &v) != nil {
		return ""
	}
	return strings.ToLower(v.Database.Severity)
}

// dedupeOSV collapses the same vulnerability published under several IDs (GHSA-…, GO-…, CVE-…),
// preferring the GHSA entry because it carries a severity.
func dedupeOSV(in []Advisory) []Advisory {
	sort.SliceStable(in, func(i, j int) bool {
		return strings.HasPrefix(in[i].ID, "GHSA-") && !strings.HasPrefix(in[j].ID, "GHSA-")
	})
	seen := map[string]bool{}
	var out []Advisory
	for _, a := range in {
		ids := append([]string{a.ID}, a.Aliases...)
		dup := false
		for _, id := range ids {
			if seen[id] {
				dup = true
			}
		}
		for _, id := range ids {
			seen[id] = true
		}
		if !dup {
			out = append(out, a)
		}
	}
	return out
}

// --- helpers ---------------------------------------------------------------------------------

func sortLines(ls []ReleaseLine) {
	sort.SliceStable(ls, func(i, j int) bool { return lineNewer(ls[i].Name, ls[j].Name) })
}

func lineNewer(a, b string) bool {
	va, okA := catalog.ParseVersion(a, "")
	vb, okB := catalog.ParseVersion(b, "")
	if okA && okB {
		return va.Compare(vb) > 0
	}
	return a > b
}

func newestLatest(ls []ReleaseLine) string {
	var best *catalog.Version
	s := ""
	for _, l := range ls {
		if v, ok := catalog.ParseVersion(l.Latest, ""); ok && v.Prerelease == "" && (best == nil || v.Compare(*best) > 0) {
			vv := v
			best, s = &vv, l.Latest
		}
	}
	return s
}

func parseDate(s string) *time.Time {
	for _, layout := range []string{"2006-01-02", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

// EOLProduct is one endoflife.date product (for suggestions when adding a component in the UI).
type EOLProduct struct {
	Slug    string   `json:"slug"`
	Label   string   `json:"label"`
	Aliases []string `json:"aliases,omitempty"`
}

// EOLProducts lists endoflife.date's products (cached like every upstream call).
func (c *Client) EOLProducts(ctx context.Context) ([]EOLProduct, error) {
	res, err := c.F.get(ctx, "eol:products", "https://endoflife.date/api/v1/products")
	if err != nil {
		return nil, err
	}
	var p struct {
		Result []struct {
			Name    string   `json:"name"`
			Label   string   `json:"label"`
			Aliases []string `json:"aliases"`
		} `json:"result"`
	}
	if err := json.Unmarshal(res.Body, &p); err != nil {
		return nil, err
	}
	out := make([]EOLProduct, 0, len(p.Result))
	for _, r := range p.Result {
		out = append(out, EOLProduct{Slug: r.Name, Label: r.Label, Aliases: r.Aliases})
	}
	return out, nil
}

// GitHubRepo returns the canonical owner/name of a public GitHub repository, or "" if there's none
// with that exact name (one cached lookup — not a search). Used for "New component" suggestions.
func (c *Client) GitHubRepo(ctx context.Context, repo string) string {
	res, err := c.F.get(ctx, "gh-repo:"+repo, "https://api.github.com/repos/"+repo)
	if err != nil {
		return ""
	}
	var r struct {
		FullName string `json:"full_name"`
		Private  bool   `json:"private"`
	}
	if json.Unmarshal(res.Body, &r) != nil || r.Private {
		return ""
	}
	return r.FullName
}

// GitHubLatest is the newest stable GitHub release of a component (release lines only: no repo status
// or advisories) — for comparing with endoflife.date in the "New component" preview.
func (c *Client) GitHubLatest(ctx context.Context, comp *catalog.Component) string {
	info := &Info{ComponentID: comp.ID}
	if comp.GitHub == "" || c.githubLines(ctx, comp, info) != nil {
		return ""
	}
	return info.LatestStable
}
