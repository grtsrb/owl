// Package catalog identifies which upstream component (and version) a container image is.
package catalog

import (
	_ "embed"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"
)

//go:embed catalog.yaml
var builtin []byte

type Component struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Images       []string `json:"images"`
	GitHub       string   `json:"github,omitempty"`
	EOL          string   `json:"eol,omitempty"`
	VersionParts int      `json:"versionParts,omitempty"`
	TagPrefix    string   `json:"tagPrefix,omitempty"`
	Source       string   `json:"source,omitempty"` // "cluster" = version from the cluster, not an image
	// ReleaseTagPrefix filters GitHub release tags to this component's own releases.
	ReleaseTagPrefix string      `json:"releaseTagPrefix,omitempty"`
	OSV              *OSVPackage `json:"osv,omitempty"`
	Notes            string      `json:"notes,omitempty"`

	refs []Ref // parsed Images, built once in Builtin()
}

type OSVPackage struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
}

// ByID returns the component with the given ID, or nil.
func (c *Catalog) ByID(id string) *Component {
	for i := range c.Components {
		if c.Components[i].ID == id {
			return &c.Components[i]
		}
	}
	return nil
}

type Catalog struct {
	Components []Component `json:"components"`
	// InternalRegistries mark in-house images (inventory only, no upstream checks), e.g.
	// registry.gitlab.example.com.
	InternalRegistries []string `json:"-"`
}

// Builtin returns the embedded catalog.
func Builtin() (*Catalog, error) {
	var c Catalog
	if err := yaml.UnmarshalStrict(builtin, &c); err != nil {
		return nil, fmt.Errorf("builtin catalog: %w", err)
	}
	seen := map[string]bool{}
	for i := range c.Components {
		comp := &c.Components[i]
		if comp.ID == "" || seen[comp.ID] || (len(comp.Images) == 0) == (comp.Source != "cluster") {
			return nil, fmt.Errorf("builtin catalog: invalid or duplicate component %q", comp.ID)
		}
		seen[comp.ID] = true
		if comp.VersionParts == 0 {
			comp.VersionParts = 3
		}
		for _, img := range comp.Images {
			comp.refs = append(comp.refs, ParseRef(img+":x")) // dummy tag; only the name is used
		}
	}
	return &c, nil
}

type Status string

const (
	StatusIdentified   Status = "identified"   // known component with a pinned version
	StatusUnpinned     Status = "unpinned"     // known component, but tag doesn't pin a version
	StatusInHouse      Status = "in_house"     // from an internal registry: inventory only
	StatusUnrecognized Status = "unrecognized" // not in the catalog
	StatusIgnored      Status = "ignored"      // unrecognized, and the user chose to ignore it
)

// Identification is the result of matching one image against the catalog.
type Identification struct {
	Ref       Ref
	Component *Component // nil unless identified/unpinned
	Status    Status
	Version   *Version // parsed upstream version; nil if unpinned/unknown
	Reason    string   // why unpinned/unrecognized, human readable
}

func (c *Catalog) Identify(image string) Identification {
	ref := ParseRef(image)
	id := Identification{Ref: ref}
	for _, reg := range c.InternalRegistries {
		if reg != "" && (ref.Registry == reg || strings.HasSuffix(ref.Registry, "."+reg)) {
			id.Status = StatusInHouse
			if v, ok := ParseVersion(ref.Tag, ""); ok {
				id.Version = &v
			}
			return id
		}
	}
	comp := c.match(ref)
	if comp == nil {
		id.Status, id.Reason = StatusUnrecognized, "image not in catalog"
		return id
	}
	return identifyAs(id, comp)
}

// IdentifyAs identifies image as the given component regardless of the catalog's image patterns
// (a user mapped an unrecognized repository to it). The version rules are the component's own.
func (c *Catalog) IdentifyAs(image string, comp *Component) Identification {
	return identifyAs(Identification{Ref: ParseRef(image)}, comp)
}

func identifyAs(id Identification, comp *Component) Identification {
	ref := id.Ref
	id.Component = comp
	if ref.Tag == "" { // digest-only
		id.Status, id.Reason = StatusUnpinned, "pinned by digest only — version unknown"
		return id
	}
	v, ok := ParseVersion(ref.Tag, comp.TagPrefix)
	switch {
	case !ok:
		id.Status, id.Reason = StatusUnpinned, fmt.Sprintf("tag %q is not a version", ref.Tag)
	case v.Parts < comp.VersionParts:
		id.Status, id.Reason = StatusUnpinned, fmt.Sprintf("floating tag %q (moves with new releases)", ref.Tag)
		id.Version = &v
	default:
		id.Status, id.Version = StatusIdentified, &v
	}
	return id
}

func (c *Catalog) match(ref Ref) *Component {
	// Exact name match wins over a mirror-suffix match, so e.g. bitnami/redis is never claimed by a
	// shorter pattern of another component.
	for i := range c.Components {
		for _, p := range c.Components[i].refs {
			if p.Registry == ref.Registry && p.Repository == ref.Repository {
				return &c.Components[i]
			}
		}
	}
	if publicRegistries[ref.Registry] {
		// On public registries a longer path is someone else's image (ghcr.io/myorg/bitnami/redis is a
		// fork), not a mirror. Mirrors/pull-through caches live on private registries.
		return nil
	}
	for i := range c.Components {
		for _, p := range c.Components[i].refs {
			if ref.mirrorOf(p) {
				return &c.Components[i]
			}
		}
	}
	return nil
}

// ClusterVersions identifies the Kubernetes version and distribution from a version string like
// "v1.30.4+rke2r1" or "v1.33.6+k3s1". The distribution's own version is the full string (RKE2/k3s
// release names are "v1.30.4+rke2r1").
func (c *Catalog) ClusterVersions(gitVersion string) []Identification {
	var out []Identification
	v, ok := ParseVersion(gitVersion, "")
	byID := c.ByID
	if k := byID("kubernetes"); k != nil {
		id := Identification{Component: k, Ref: Ref{Raw: gitVersion}}
		if ok {
			kv := v
			kv.Build = "" // the Kubernetes version itself; the distro revision belongs to rke2/k3s
			id.Status, id.Version = StatusIdentified, &kv
		} else {
			id.Status, id.Reason = StatusUnpinned, "unparseable Kubernetes version "+gitVersion
		}
		out = append(out, id)
	}
	for _, distro := range []string{"rke2", "k3s"} {
		if i := strings.Index(gitVersion, "+"+distro); i >= 0 {
			if d := byID(distro); d != nil && ok {
				dv := v
				dv.Build = gitVersion[i+1:] // keep the distro revision: v1.30.4+rke2r2 != +rke2r1
				out = append(out, Identification{Component: d, Ref: Ref{Raw: gitVersion}, Status: StatusIdentified, Version: &dv})
			}
		}
	}
	return out
}

// Version is a parsed numeric version. Parts says how many numbers the tag actually had (a floating
// tag like "16" has Parts=1).
type Version struct {
	Major, Minor, Patch int
	Parts               int
	Prerelease          string // e.g. rc1, beta.2 — empty for stable
	// Build is distribution build metadata, e.g. "rke2r1" in v1.30.4+rke2r1. Unlike semver it does
	// order releases (rke2r2 is a newer RKE2 release of the same Kubernetes version).
	Build    string
	Original string // the tag as found
}

func (v Version) String() string {
	s := strconv.Itoa(v.Major)
	if v.Parts >= 2 {
		s += "." + strconv.Itoa(v.Minor)
	}
	if v.Parts >= 3 {
		s += "." + strconv.Itoa(v.Patch)
	}
	if v.Prerelease != "" {
		s += "-" + v.Prerelease
	}
	if v.Build != "" {
		s += "+" + v.Build
	}
	return s
}

// Compare returns -1, 0, 1. Pre-releases sort before the release they precede.
func (v Version) Compare(o Version) int {
	for _, d := range [3]int{v.Major - o.Major, v.Minor - o.Minor, v.Patch - o.Patch} {
		if d < 0 {
			return -1
		} else if d > 0 {
			return 1
		}
	}
	switch {
	case v.Prerelease == o.Prerelease:
		return naturalCompare(v.Build, o.Build)
	case v.Prerelease == "":
		return 1
	case o.Prerelease == "":
		return -1
	}
	return naturalCompare(v.Prerelease, o.Prerelease)
}

// naturalCompare compares strings with embedded numbers numerically: rc.2 < rc.10, rke2r1 < rke2r2.
func naturalCompare(a, b string) int {
	for a != "" && b != "" {
		ai, bi := chunk(a), chunk(b)
		ca, cb := a[:ai], b[:bi]
		a, b = a[ai:], b[bi:]
		na, errA := strconv.Atoi(ca)
		nb, errB := strconv.Atoi(cb)
		switch {
		case errA == nil && errB == nil:
			if na != nb {
				if na < nb {
					return -1
				}
				return 1
			}
		case ca != cb:
			return strings.Compare(ca, cb)
		}
	}
	return strings.Compare(a, b) // the longer remainder sorts later
}

// chunk returns the length of the leading run of digits or non-digits.
func chunk(s string) int {
	digit := s[0] >= '0' && s[0] <= '9'
	i := 1
	for i < len(s) && (s[i] >= '0' && s[i] <= '9') == digit {
		i++
	}
	return i
}

var (
	versionRe = regexp.MustCompile(`^v?(\d+)(?:\.(\d+))?(?:\.(\d+))?(.*)$`)
	// prereleaseRe recognises real pre-releases in the suffix; anything else (hardened3, build2024…,
	// debian-12-r20, alpine, k3s1) is a vendor build suffix and ignored.
	prereleaseRe = regexp.MustCompile(`^[-.]?((?:alpha|beta|rc|pre|dev)[.-]?\d*)`)
)

// ParseVersion extracts the upstream version from an image tag, ignoring vendor build suffixes.
func ParseVersion(tag, prefix string) (Version, bool) {
	t := strings.TrimPrefix(tag, prefix)
	m := versionRe.FindStringSubmatch(t)
	if m == nil {
		return Version{}, false
	}
	v := Version{Original: tag, Parts: 1}
	v.Major, _ = strconv.Atoi(m[1])
	if m[2] != "" {
		v.Minor, _ = strconv.Atoi(m[2])
		v.Parts = 2
	}
	if m[3] != "" {
		v.Patch, _ = strconv.Atoi(m[3])
		v.Parts = 3
	}
	suffix := strings.ToLower(m[4])
	// A digit or letter glued to the number (e.g. "1.2.3a", "20240305") means this isn't a semver-ish tag.
	if suffix != "" && !strings.ContainsAny(suffix[:1], "-._+") {
		return Version{}, false
	}
	if i := strings.Index(m[4], "+"); i >= 0 {
		v.Build = m[4][i+1:] // e.g. rke2r1 in v1.30.4+rke2r1
		suffix = strings.ToLower(m[4][:i])
	}
	if pm := prereleaseRe.FindStringSubmatch(suffix); pm != nil {
		v.Prerelease = pm[1]
	}
	// Date-like single numbers (e.g. tag "20240305") are not versions.
	if v.Parts == 1 && v.Major > 10000 {
		return Version{}, false
	}
	return v, true
}
