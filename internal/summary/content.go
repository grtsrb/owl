package summary

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"owl/internal/upstream"
)

// Item is one point of a summary, tied to the release whose notes it comes from.
type Item struct {
	Text    string `json:"text"`
	Version string `json:"version"`
}

// Path values for UpgradePath.Direct.
const (
	PathDirect       = "direct_supported" // the notes say a direct upgrade is supported
	PathIntermediate = "via_intermediate" // the notes require stepping through other versions
	PathNotStated    = "not_stated"
)

// Content is what Claude returns (structured output), after validation.
type Content struct {
	Headline        string `json:"headline"`
	BreakingChanges []Item `json:"breakingChanges"`
	Migrations      []Item `json:"migrations"`
	Deprecations    []Item `json:"deprecations"`
	SecurityFixes   []Item `json:"securityFixes"`
	NotableFeatures []Item `json:"notableFeatures"`
	UpgradePath     struct {
		Direct string   `json:"direct"`
		Via    []string `json:"via"`
		Reason string   `json:"reason"`
	} `json:"upgradePath"`
	Risk struct {
		Level  string `json:"level"` // low | medium | high — upgrade risk, never priority
		Reason string `json:"reason"`
	} `json:"risk"`
}

// Stored is what goes into the summaries table: the content plus the sources it was written from
// (links built by Owl, so the UI never shows a URL that came from the model or the notes).
type Stored struct {
	Content
	Sources []upstream.Note `json:"sources"`
}

const (
	maxItems      = 8
	maxItemChars  = 600
	maxLineChars  = 300
	maxVia        = 6
	maxVersionLen = 40
)

func itemSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"text", "version"},
		"properties": map[string]any{
			"text":    map[string]any{"type": "string", "description": "One or two plain sentences, specific (name the setting, API or flag)."},
			"version": map[string]any{"type": "string", "description": "The version attribute of the release notes this comes from, exactly as given."},
		},
	}
}

// Schema is the JSON schema for structured outputs.
func Schema() map[string]any {
	list := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": itemSchema(), "description": desc + " At most 8, most important first; empty if none."}
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"headline", "breakingChanges", "migrations", "deprecations", "securityFixes", "notableFeatures", "upgradePath", "risk"},
		"properties": map[string]any{
			"headline":        map[string]any{"type": "string", "description": "The single most important thing to know about this upgrade, under 120 characters."},
			"breakingChanges": list("Changes that can break an existing deployment when upgrading."),
			"migrations":      list("Manual steps the upgrade needs: config changes, data migrations, required order of operations."),
			"deprecations":    list("Features, flags, APIs or settings deprecated or removed."),
			"securityFixes":   list("Fixed vulnerabilities; include CVE IDs when the notes give them."),
			"notableFeatures": list("The few most useful new features."),
			"upgradePath": map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"direct", "via", "reason"},
				"properties": map[string]any{
					"direct": map[string]any{"type": "string", "enum": []string{PathDirect, PathIntermediate, PathNotStated}},
					"via":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Intermediate versions the notes require, oldest first; empty otherwise."},
					"reason": map[string]any{"type": "string", "description": "One sentence; say 'Not stated in the release notes.' when they don't address it."},
				},
			},
			"risk": map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"level", "reason"},
				"properties": map[string]any{
					"level":  map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}, "description": "How risky or laborious the upgrade itself is (not how urgent)."},
					"reason": map[string]any{"type": "string", "description": "One sentence grounded in the notes."},
				},
			},
		},
	}
}

// parseContent validates Claude's JSON. Structured outputs guarantee the shape; this enforces the
// limits the schema can't express and treats every string as untrusted text: control characters are
// removed, lengths capped, lists cut to 8, and an item's version kept only if Owl supplied it.
func parseContent(raw string, sources []upstream.Note) (Content, error) {
	var c Content
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("invalid summary JSON: %w", err)
	}
	known := map[string]bool{}
	for _, s := range sources {
		known[s.Version] = true
	}
	c.Headline = clean(c.Headline, maxLineChars)
	if c.Headline == "" {
		return c, fmt.Errorf("summary has no headline")
	}
	for _, l := range []*[]Item{&c.BreakingChanges, &c.Migrations, &c.Deprecations, &c.SecurityFixes, &c.NotableFeatures} {
		var out []Item
		for _, it := range *l {
			it.Text = clean(it.Text, maxItemChars)
			if it.Text == "" {
				continue
			}
			if it.Version = clean(it.Version, maxVersionLen); !known[it.Version] {
				it.Version = "" // not one of the releases we sent: show the text, but no source link
			}
			if out = append(out, it); len(out) == maxItems {
				break
			}
		}
		if out == nil {
			out = []Item{}
		}
		*l = out
	}
	switch c.UpgradePath.Direct {
	case PathDirect, PathIntermediate, PathNotStated:
	default:
		c.UpgradePath.Direct = PathNotStated
	}
	var via []string
	for _, v := range c.UpgradePath.Via {
		if v = clean(v, maxVersionLen); v != "" && len(via) < maxVia {
			via = append(via, v)
		}
	}
	if via == nil {
		via = []string{}
	}
	c.UpgradePath.Via = via
	c.UpgradePath.Reason = clean(c.UpgradePath.Reason, maxItemChars)
	switch c.Risk.Level {
	case "low", "medium", "high":
	default:
		return c, fmt.Errorf("summary has an invalid risk level %q", c.Risk.Level)
	}
	c.Risk.Reason = clean(c.Risk.Reason, maxItemChars)
	return c, nil
}

// clean removes control characters (newlines become spaces), collapses whitespace and caps the length.
// Links: a scheme ("https://", "mailto:"), "//host", "www.", or a bare host whose last label is
// letters ("evil.example/x") — Mattermost and mail clients auto-link all of these. File names in
// notes ("values.yaml") are kept; versions ("1.2.3") never match.
var (
	reURL      = regexp.MustCompile(`(?i)(?:\b[a-z][a-z0-9+.-]*:(?://)?|(?:^|[\s(\[<"'])//)\S+|\b[\w-]+(?:\.[\w-]+)*\.([a-z]{2,63})\b(?:[/:?#]\S*)?`)
	fileSuffix = map[string]bool{"yaml": true, "yml": true, "json": true, "toml": true, "conf": true, "cfg": true,
		"ini": true, "txt": true, "xml": true, "log": true, "lock": true, "proto": true, "tpl": true, "pem": true,
		"crt": true, "js": true}
)

func stripLinks(s string) string {
	return reURL.ReplaceAllStringFunc(s, func(m string) string {
		sm := reURL.FindStringSubmatch(m)
		if sm[1] != "" && !strings.ContainsAny(m, "/:?#@") && fileSuffix[strings.ToLower(sm[1])] {
			return m // a file name, not a host
		}
		lead := ""
		if m != "" && strings.ContainsRune(" ([<\"'", rune(m[0])) {
			lead = m[:1]
		}
		return lead + "[link removed]"
	})
}

func clean(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	// Links in a summary come only from Owl's own release-note sources. A URL in Claude's text (an
	// injection in upstream notes could plant one) would be auto-linked by Mattermost and mail clients.
	s = stripLinks(s)
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max-1])) + "…"
	}
	return s
}
