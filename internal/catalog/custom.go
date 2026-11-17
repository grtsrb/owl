package catalog

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Components added in the UI (milestone 11) live in the database; Merge combines them with the
// built-in catalog. Built-in always wins: a custom component with a built-in's id, or claiming one of
// its images, is left out (its state says why).

var (
	reID      = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	reGitHub  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$`)
	reEOLSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	reImage   = regexp.MustCompile(`^[a-z0-9]([a-z0-9._:/-]{0,253}[a-z0-9])?$`)
	rePrefix  = regexp.MustCompile(`^[A-Za-z0-9._/-]{0,40}$`)
)

// reservedIDs can't be component ids: "global" is the global version policy's scope.
var reservedIDs = map[string]bool{"global": true}

const (
	MaxPatternLen = 200
	maxImages     = 20
	maxNameLen    = 60
)

// NormalizeImage turns an image repository as typed ("atmoz/sftp", "docker.io/atmoz/sftp",
// "ghcr.io/org/app") into the canonical registry/repository form the inventory shows. A tag or digest
// is an error: a component claims every tag of a repository.
func NormalizeImage(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || !reImage.MatchString(s) || strings.Contains(s, "@") || strings.Contains(s, "//") {
		return "", fmt.Errorf("%q is not an image repository (e.g. docker.io/atmoz/sftp)", s)
	}
	if strings.Contains(s[strings.LastIndex(s, "/")+1:], ":") { // a ':' before the last '/' is a registry port
		return "", fmt.Errorf("%q: give the repository without a tag", s)
	}
	return ParseRef(s + ":x").Name(), nil
}

// ValidateCustom checks a component entered in the UI and normalises it (image names, defaults). The
// fields that need code (OSV packages, release-note sources, cluster-sourced versions, notes) can't be
// set from the UI.
func ValidateCustom(c *Component) error {
	c.ID = strings.TrimSpace(c.ID)
	c.Name = strings.TrimSpace(c.Name)
	c.GitHub = strings.TrimSpace(c.GitHub)
	c.EOL = strings.TrimSpace(c.EOL)
	switch {
	case !reID.MatchString(c.ID):
		return errors.New("id: lowercase letters, digits and '-', up to 63 characters")
	case reservedIDs[c.ID]:
		return fmt.Errorf("id: %q is reserved", c.ID)
	case c.Name == "" || len([]rune(c.Name)) > maxNameLen || strings.ContainsAny(c.Name, "\r\n\t<>`"):
		return fmt.Errorf("name: 1–%d characters, no line breaks or <>`", maxNameLen)
	case len(c.Images) == 0 || len(c.Images) > maxImages:
		return fmt.Errorf("images: 1–%d image repositories", maxImages)
	case c.GitHub == "":
		return errors.New("github: the project's repository, e.g. atmoz/sftp — Owl reads releases and advisories there")
	case !reGitHub.MatchString(c.GitHub):
		return errors.New("github: owner/repository, e.g. openssh/openssh-portable")
	case c.EOL != "" && !reEOLSlug.MatchString(c.EOL):
		return errors.New("eol: an endoflife.date product, e.g. postgresql (or empty)")
	case c.VersionParts < 0 || c.VersionParts > 4:
		return errors.New("versionParts: 1–4 (0 = default 3)")
	case !rePrefix.MatchString(c.TagPrefix) || !rePrefix.MatchString(c.ReleaseTagPrefix):
		return errors.New("tag prefixes: up to 40 letters, digits, '.', '_', '-', '/'")
	case c.Source != "" || c.OSV != nil || c.ReleaseNotes != "" || c.Notes != "":
		return errors.New("source, osv, releaseNotes and notes can only be set in the built-in catalog")
	}
	seen := map[string]bool{}
	for i, img := range c.Images {
		n, err := NormalizeImage(img)
		if err != nil {
			return fmt.Errorf("images: %w", err)
		}
		if seen[n] {
			return fmt.Errorf("images: %s is listed twice", n)
		}
		seen[n] = true
		c.Images[i] = n
	}
	if c.VersionParts == 0 {
		c.VersionParts = 3
	}
	if _, err := compilePattern(c.VersionPattern); err != nil {
		return err
	}
	c.Custom = true
	return nil
}

func compilePattern(p string) (*regexp.Regexp, error) {
	if p == "" {
		return nil, nil
	}
	if len(p) > MaxPatternLen {
		return nil, fmt.Errorf("versionPattern: at most %d characters", MaxPatternLen)
	}
	re, err := regexp.Compile(p) // RE2: linear time, no catastrophic backtracking
	if err != nil {
		return nil, fmt.Errorf("versionPattern: %v", err)
	}
	if re.NumSubexp() == 0 {
		return nil, errors.New("versionPattern: needs at least one group, e.g. ^(\\d+\\.\\d+)_p(\\d+)")
	}
	return re, nil
}

// prepare parses image patterns and the version pattern (once, when a catalog is built).
func (c *Component) prepare() error {
	if c.VersionParts == 0 {
		c.VersionParts = 3
	}
	re, err := compilePattern(c.VersionPattern)
	if err != nil {
		return err
	}
	c.versionRe = re
	c.refs = nil
	for _, img := range c.Images {
		c.refs = append(c.refs, ParseRef(img+":x")) // dummy tag; only the name is used
	}
	return nil
}

// versionFromTag applies the version pattern: the groups that matched, joined with ".".
func (c *Component) versionFromTag(tag string) (string, bool) {
	m := c.versionRe.FindStringSubmatch(tag)
	if m == nil {
		return "", false
	}
	var parts []string
	for _, g := range m[1:] {
		if g != "" {
			parts = append(parts, g)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "."), true
}

// CustomState says whether a custom component is in effect.
type CustomState struct {
	ID       string `json:"id"`
	Active   bool   `json:"active"`
	Replaced string `json:"replacedBy,omitempty"` // built-in component that took over (same id or image)
	Problem  string `json:"problem,omitempty"`
}

// Merge returns a new catalog: the built-in components, then the custom ones that don't collide with
// them (or with an earlier custom one). The built-in catalog isn't modified.
func Merge(builtin *Catalog, custom []Component) (*Catalog, []CustomState) {
	out := &Catalog{InternalRegistries: builtin.InternalRegistries}
	out.Components = append(out.Components, builtin.Components...)
	claimed := map[string]string{} // image name -> component id
	ids := map[string]bool{}
	for _, c := range builtin.Components {
		ids[c.ID] = true
		for _, r := range c.refs {
			claimed[r.Name()] = c.ID
		}
	}
	var states []CustomState
	for _, c := range custom {
		st := CustomState{ID: c.ID}
		if ids[c.ID] {
			if out.ByID(c.ID).Custom {
				st.Problem = "duplicate id"
			} else {
				st.Replaced = c.ID
			}
			states = append(states, st)
			continue
		}
		c.Custom = true
		if err := c.prepare(); err != nil {
			st.Problem = err.Error()
			states = append(states, st)
			continue
		}
		for _, r := range c.refs {
			// Built-in first, the way identification works: exact name or a mirror of a built-in image.
			if b := builtin.match(r); b != nil {
				st.Replaced = b.ID
				break
			}
			if owner, ok := claimed[r.Name()]; ok {
				st.Problem = r.Name() + " is already claimed by " + owner
				break
			}
		}
		if st.Replaced == "" && st.Problem == "" {
			st.Active = true
			for _, r := range c.refs {
				claimed[r.Name()] = c.ID
			}
			ids[c.ID] = true
			out.Components = append(out.Components, c)
		}
		states = append(states, st)
	}
	return out, states
}

// Claimant returns the id of the component (built-in or custom) that would identify this image
// repository — by exact name or as a mirror of one of its images — or "".
func (c *Catalog) Claimant(image string) string {
	if comp := c.match(ParseRef(image + ":x")); comp != nil {
		return comp.ID
	}
	return ""
}
