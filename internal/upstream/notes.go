package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/html"

	"owl/internal/catalog"
)

// Note is one release's notes, as collected for an upgrade summary. URL is where a human reads them;
// it is always built by Owl, never taken from the notes or from Claude.
type Note struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	Text    string `json:"-"`
}

// notesTTL: release notes of a published version practically never change.
const notesTTL = 7 * 24 * time.Hour

// ErrNoNotes means the component's source publishes nothing usable for the requested versions.
var ErrNoNotes = errors.New("no release notes available")

// ReleaseNotes collects the notes of every stable version that include accepts, oldest first. lines
// are the release-line names ("1.33", "8.2", "17") whose files may hold those versions — sources that
// publish per line (Kubernetes, Redis, HAProxy, Postgres) only fetch these; GitHub releases ignore
// them. to bounds the versions Postgres probes (its versions are enumerated, not listed anywhere).
// Which versions belong to a jump is decided by the caller (internal/summary, with internal/policy).
func (c *Client) ReleaseNotes(ctx context.Context, comp *catalog.Component, to catalog.Version, lines []string,
	include func(catalog.Version) bool) ([]Note, error) {
	var notes []Note
	var err error
	switch comp.ReleaseNotes {
	case "":
		notes, err = c.githubNotes(ctx, comp, include)
	case "kubernetes-changelog":
		notes, err = c.perLine(ctx, comp, lines, include, kubernetesSource)
	case "redis-releasenotes":
		notes, err = c.perLine(ctx, comp, lines, include, redisSource)
	case "haproxy-changelog":
		notes, err = c.perLine(ctx, comp, lines, include, haproxySource)
	case "postgres-release-notes":
		notes, err = c.postgresNotes(ctx, to, lines, include)
	default:
		return nil, fmt.Errorf("unknown release notes source %q", comp.ReleaseNotes)
	}
	if err != nil {
		return nil, err
	}
	var kept []Note
	for _, n := range notes {
		if n.Text = cleanNotes(n.Text); n.Text != "" {
			kept = append(kept, n)
		}
	}
	if len(kept) == 0 {
		return nil, ErrNoNotes
	}
	sort.Slice(kept, func(i, j int) bool {
		a, _ := catalog.ParseVersion(kept[i].Version, "")
		b, _ := catalog.ParseVersion(kept[j].Version, "")
		return a.Compare(b) < 0
	})
	return kept, nil
}

// stable parses a version and reports whether it's a stable release include accepts.
func stable(raw, prefix string, include func(catalog.Version) bool) (catalog.Version, bool) {
	v, ok := catalog.ParseVersion(raw, prefix)
	if !ok || v.Prerelease != "" || v.Parts < 2 {
		return v, false
	}
	return v, include(v)
}

func (c *Client) githubNotes(ctx context.Context, comp *catalog.Component, include func(catalog.Version) bool) ([]Note, error) {
	if comp.GitHub == "" {
		return nil, ErrNoNotes
	}
	var out []Note
	for page := 1; page <= 3; page++ { // same URLs and cache keys as githubLines: usually no extra request
		u := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=100&page=%d", comp.GitHub, page)
		res, err := c.F.get(ctx, fmt.Sprintf("gh-releases:%s:%d", comp.GitHub, page), u)
		if err != nil {
			return nil, err
		}
		var rels []struct {
			Tag        string `json:"tag_name"`
			Draft      bool   `json:"draft"`
			Prerelease bool   `json:"prerelease"`
			Body       string `json:"body"`
			URL        string `json:"html_url"`
		}
		if err := json.Unmarshal(res.Body, &rels); err != nil {
			return nil, err
		}
		for _, r := range rels {
			if r.Draft || r.Prerelease || (comp.ReleaseTagPrefix != "" && !strings.HasPrefix(r.Tag, comp.ReleaseTagPrefix)) {
				continue
			}
			// The release prefix ("release-" for nginx, "controller-" for ingress-nginx) isn't part of the version.
			v, ok := stable(strings.TrimPrefix(r.Tag, comp.ReleaseTagPrefix), comp.TagPrefix, include)
			if !ok {
				continue
			}
			// The link is rebuilt from the repo and tag rather than trusted from the response.
			out = append(out, Note{Version: v.String(), Text: r.Body,
				URL: "https://github.com/" + comp.GitHub + "/releases/tag/" + urlPathEscape(r.Tag)})
		}
		if len(rels) < 100 {
			break
		}
	}
	return out, nil
}

// lineSource describes a project that publishes one notes file per release line.
type lineSource struct {
	name    string
	fileURL func(line string) string            // fetched
	webURL  func(line, version string) string   // shown to people
	split   func(text string) map[string]string // version -> raw section
}

func (c *Client) perLine(ctx context.Context, comp *catalog.Component, lines []string,
	include func(catalog.Version) bool, src lineSource) ([]Note, error) {
	var out []Note
	var firstErr error
	seen := map[string]bool{}
	for _, line := range lines {
		lv, ok := catalog.ParseVersion(line, "")
		if !reLine.MatchString(line) || !ok {
			continue // line names come from endoflife.date; never put anything else in a URL
		}
		res, err := c.F.getTTL(ctx, "notes:"+src.name+":"+line, src.fileURL(line), notesTTL)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for raw, text := range src.split(string(res.Body)) {
			v, ok := stable(raw, "", include)
			// A branch's file also carries the history it was forked from (HAProxy 3.4's CHANGELOG lists
			// 3.2.0 and the 3.3-dev releases): take each version only from its own line's file.
			if !ok || v.Major != lv.Major || (lv.Parts > 1 && v.Minor != lv.Minor) || seen[v.String()] {
				continue
			}
			seen[v.String()] = true
			out = append(out, Note{Version: v.String(), URL: src.webURL(line, raw), Text: text})
		}
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

var reLine = regexp.MustCompile(`^\d{1,3}(\.\d{1,3})?$`)

// --- Kubernetes: CHANGELOG/CHANGELOG-1.xx.md ---

var (
	reK8sVersion = regexp.MustCompile(`(?m)^# (v\d+\.\d+\.\d+\S*)\s*$`)
	// PR/author references and SIG tags: lots of tokens, nothing a summary needs.
	reMdLink  = regexp.MustCompile(`\[([^\]]*)\]\(https?://[^)\s]*\)`)
	reK8sRefs = regexp.MustCompile(`\s*\((?:#\d+|@[\w-]+)(?:,\s*(?:#\d+|@[\w-]+))*\)`)
	reK8sSIG  = regexp.MustCompile(`\s*\[SIG [^\]]*\]`)
)

// kubernetesKeep are the parts of a version's notes that matter for an upgrade; dependency bumps,
// bug-fix lists, docs and cleanups are left out (a .0 changelog is hundreds of KB otherwise).
var kubernetesKeep = []string{"Urgent Upgrade Notes", "Deprecation", "API Change", "Feature"}

var kubernetesSource = lineSource{
	name: "kubernetes",
	fileURL: func(l string) string {
		return "https://raw.githubusercontent.com/kubernetes/kubernetes/master/CHANGELOG/CHANGELOG-" + l + ".md"
	},
	webURL: func(l, v string) string {
		return "https://github.com/kubernetes/kubernetes/blob/master/CHANGELOG/CHANGELOG-" + l + ".md#" +
			strings.ReplaceAll(strings.ToLower(v), ".", "")
	},
	split: func(text string) map[string]string {
		// A .0 section already covers everything since the previous minor ("Changelog since v1.32.0"),
		// so pre-releases are simply dropped (the caller only takes stable versions).
		out := map[string]string{}
		for _, s := range splitByHeader(text, reK8sVersion) {
			if kept := markdownSections(s.text, kubernetesKeep); kept != "" {
				kept = reMdLink.ReplaceAllString(kept, "$1")
				kept = reK8sRefs.ReplaceAllString(kept, "")
				out[s.version] = reK8sSIG.ReplaceAllString(kept, "")
			}
		}
		return out
	},
}

// markdownSections keeps the headed sections whose title starts with one of titles, each up to the
// next heading of the same or a higher level.
func markdownSections(md string, titles []string) string {
	var b strings.Builder
	keepLevel := 0 // >0 while inside a kept section of that level
	for _, line := range strings.Split(md, "\n") {
		if lvl := headingLevel(line); lvl > 0 {
			title := strings.TrimSpace(strings.TrimLeft(line, "#"))
			if keepLevel > 0 && lvl <= keepLevel {
				keepLevel = 0
			}
			if keepLevel == 0 {
				for _, t := range titles {
					if strings.HasPrefix(title, t) {
						keepLevel = lvl
						b.WriteString(title + ":\n")
						break
					}
				}
				continue
			}
		}
		if keepLevel > 0 {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

func headingLevel(line string) int {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n == 0 || n >= len(line) || line[n] != ' ' {
		return 0
	}
	return n
}

type section struct{ version, text string }

// splitByHeader cuts text at each match of re into sections, in file order; the version is the first
// capture group that matched (a pattern may have alternatives).
func splitByHeader(text string, re *regexp.Regexp) []section {
	var out []section
	idx := re.FindAllStringSubmatchIndex(text, -1)
	for i, m := range idx {
		end := len(text)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		for g := 2; g+1 < len(m); g += 2 {
			if m[g] >= 0 {
				out = append(out, section{text[m[g]:m[g+1]], text[m[1]:end]})
				break
			}
		}
	}
	return out
}

// foldPrereleases maps sections to version -> text, appending each pre-release of a line ("8.2-RC1",
// "3.2-dev12") to that line's first release (8.2.0, 3.2.0). Projects whose GA entry only lists what
// changed since the last pre-release (Redis, HAProxy) keep most of a new line's changes there; going
// from the previous line to x.y.0 means all of them. Pre-releases without a GA in the file are dropped.
func foldPrereleases(secs []section, text func(section) string) map[string]string {
	out := map[string]string{}
	pre := map[string][]string{} // "8.2.0" -> pre-release texts, newest first (file order)
	for _, s := range secs {
		if line, _, isPre := strings.Cut(s.version, "-"); isPre {
			if t := text(s); t != "" {
				pre[line+".0"] = append(pre[line+".0"], "Pre-release "+s.version+":\n"+t)
			}
			continue
		}
		out[s.version] = text(s)
	}
	for ga, texts := range pre {
		if base, ok := out[ga]; ok {
			out[ga] = strings.TrimSpace(base + "\n\n" + strings.Join(texts, "\n\n"))
		}
	}
	return out
}

// --- Redis: 00-RELEASENOTES on each release branch ---

// Header styles seen across branches: "Redis 8.0.6    Released …", "Redis Community Edition 7.4.0 GA   Released …",
// "8.0 GA (v8.0.0)    Released …", "Redis 8.8 GA  (v8.8.0)    Released …", "Redis 8.6 GA (8.6.0)    Released …".
// and once "8.2.1 (v8.2.1)    Committed …". An unmatched header would fold that release's notes into
// the section above it (credited to the wrong version), so every variant matters.
// Pre-release headers ("8.0-RC1 (v7.9.240)", "Redis 8.8-M03 (v8.7.226)") are matched too: a GA entry
// only lists changes "compared to RC1", so milestone/RC notes are folded into the line's x.y.0.
var reRedisVersion = regexp.MustCompile(`(?m)^(?:Redis(?: Open Source| Community Edition)?\s+(\d+\.\d+\.\d+)(?: GA)?|(?:Redis\s+)?\d+\.\d+ GA\s+\(v?(\d+\.\d+\.\d+)\)|\d+\.\d+\.\d+\s+\(v(\d+\.\d+\.\d+)\)|(?:Redis\s+)?(\d+\.\d+-[A-Za-z]+\d*)\s+\(v?[\d.]+\))\s+(?:Released|Committed).*$`)

var redisSource = lineSource{
	name: "redis",
	fileURL: func(l string) string {
		return "https://raw.githubusercontent.com/redis/redis/" + l + "/00-RELEASENOTES"
	},
	webURL: func(l, _ string) string { return "https://github.com/redis/redis/blob/" + l + "/00-RELEASENOTES" },
	split: func(text string) map[string]string {
		return foldPrereleases(splitByHeader(text, reRedisVersion), func(s section) string { return strings.Trim(s.text, "=\n ") })
	},
}

// --- HAProxy: haproxy.org/download/<line>/src/CHANGELOG (the GitHub mirror is the dev branch) ---

var (
	reHAProxyVersion = regexp.MustCompile(`(?m)^\d{4}/\d{2}/\d{2} : (\S+)\s*$`)
	// Only entries tagged MAJOR/MEDIUM/CRITICAL (including BUG/…): a stable release lists dozens of
	// minor fixes, cleanups, docs and test changes.
	reHAProxyImportant = regexp.MustCompile(`^\s*- (?:BUG/)?(?:MAJOR|MEDIUM|CRITICAL)\b`)
)

var haproxySource = lineSource{
	name:    "haproxy",
	fileURL: func(l string) string { return "https://www.haproxy.org/download/" + l + "/src/CHANGELOG" },
	webURL:  func(l, _ string) string { return "https://www.haproxy.org/download/" + l + "/src/CHANGELOG" },
	// x.y.0 only lists what changed since the last -dev release; the dev releases hold the line's
	// actual new features and breaking changes, so they're folded into x.y.0.
	split: func(text string) map[string]string {
		return foldPrereleases(splitByHeader(text, reHAProxyVersion), func(s section) string {
			var kept []string
			for _, line := range strings.Split(s.text, "\n") {
				if reHAProxyImportant.MatchString(line) {
					kept = append(kept, strings.TrimSpace(line))
				}
			}
			switch {
			case len(kept) > 0:
				return strings.Join(kept, "\n")
			case strings.Contains(s.version, "-"):
				return "" // a dev release with nothing major: nothing to fold in
			}
			return "(only minor fixes, documentation and cleanups in this release)"
		})
	},
}

// --- Postgres: postgresql.org/docs/release/<version>/ ---

// postgresNotes probes each line's minor versions (they are numbered without gaps from .0) up to to;
// include decides which of them belong to the jump. A missing page is skipped.
func (c *Client) postgresNotes(ctx context.Context, to catalog.Version, lines []string, include func(catalog.Version) bool) ([]Note, error) {
	var out []Note
	var firstErr error
	for _, line := range lines {
		lv, ok := catalog.ParseVersion(line, "")
		if !reLine.MatchString(line) || !ok {
			continue
		}
		for minor := 0; minor < 100; minor++ {
			v := catalog.Version{Major: lv.Major, Minor: minor, Parts: 2}
			if v.Compare(to) > 0 {
				break
			}
			if !include(v) {
				continue
			}
			url := fmt.Sprintf("https://www.postgresql.org/docs/release/%s/", v)
			res, err := c.F.getTTL(ctx, "notes:postgres:"+v.String(), url, notesTTL)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if text := postgresText(res.Body); text != "" {
				out = append(out, Note{Version: v.String(), URL: url, Text: text})
			}
		}
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// postgresText extracts the release notes (div.sect1) from a postgresql.org release page as text.
func postgresText(page []byte) string {
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return ""
	}
	var root *html.Node
	var find func(*html.Node)
	find = func(n *html.Node) {
		if root != nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "div" && attr(n, "class") == "sect1" {
			root = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(doc)
	if root == nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch {
		case n.Type == html.TextNode:
			b.WriteString(strings.Join(strings.Fields(n.Data), " ") + " ")
		case n.Type == html.ElementNode && n.Data == "a" && attr(n, "class") == "id_link":
			return // the "#" permalink next to each heading
		case n.Type == html.ElementNode && attr(n, "class") == "toc":
			return // table of contents: the same headings again
		case n.Type == html.ElementNode:
			block := map[string]bool{"p": true, "li": true, "h2": true, "h3": true, "h4": true, "div": true, "dt": true, "dd": true}[n.Data]
			if block {
				b.WriteString("\n")
				if n.Data == "li" {
					b.WriteString("- ")
				}
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
			if block {
				b.WriteString("\n")
			}
		}
	}
	walk(root)
	return reListItem.ReplaceAllString(b.String(), "- ")
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// --- shared ---

var reListItem = regexp.MustCompile(`-\s*\n\s*`)

var (
	reHTMLComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	reSpaces      = regexp.MustCompile(`[ \t]+\n`)
	reBlankLines  = regexp.MustCompile(`\n{3,}`)
)

// cleanNotes normalizes whitespace and drops HTML comments (release templates are full of them).
func cleanNotes(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = reHTMLComment.ReplaceAllString(s, "")
	s = reSpaces.ReplaceAllString(s, "\n")
	s = reBlankLines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func urlPathEscape(s string) string {
	return strings.NewReplacer("%", "%25", "#", "%23", "?", "%3F", " ", "%20").Replace(s)
}
