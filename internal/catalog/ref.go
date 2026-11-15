package catalog

import "strings"

// Ref is a parsed container image reference.
type Ref struct {
	Raw        string
	Registry   string // e.g. docker.io, registry.k8s.io, ghcr.io
	Repository string // path without registry, e.g. library/nginx, ingress-nginx/controller
	Tag        string
	Digest     string // sha256:...
}

// Name is registry + repository, e.g. docker.io/library/nginx.
func (r Ref) Name() string { return r.Registry + "/" + r.Repository }

// ParseRef parses an image reference the way container runtimes do: the first path segment is a
// registry only if it contains '.' or ':' or is "localhost"; Docker Hub single-segment names live
// under "library/".
func ParseRef(image string) Ref {
	r := Ref{Raw: image}
	rest := image
	if i := strings.Index(rest, "@"); i >= 0 {
		r.Digest = rest[i+1:]
		rest = rest[:i]
	}
	// A tag is after the last ':' that comes after the last '/' (a ':' before it is a registry port).
	if i := strings.LastIndex(rest, ":"); i >= 0 && i > strings.LastIndex(rest, "/") {
		r.Tag = rest[i+1:]
		rest = rest[:i]
	}
	if i := strings.Index(rest, "/"); i >= 0 {
		first := rest[:i]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			r.Registry, r.Repository = first, rest[i+1:]
		}
	}
	if r.Registry == "" {
		r.Registry, r.Repository = "docker.io", rest
	}
	if r.Registry == "index.docker.io" || r.Registry == "registry-1.docker.io" {
		r.Registry = "docker.io"
	}
	if r.Registry == "docker.io" && !strings.Contains(r.Repository, "/") {
		r.Repository = "library/" + r.Repository
	}
	if r.Tag == "" && r.Digest == "" {
		r.Tag = "latest" // runtime default
	}
	return r
}

// matches reports whether this ref is the given pattern ("registry/path" or bare "path"). Besides an
// exact match, the pattern's path may appear as a suffix after a registry mirror/proxy prefix, e.g.
// harbor.example.com/dockerhub/library/nginx matches docker.io/library/nginx.
func (r Ref) matches(pattern string) bool {
	p := ParseRef(pattern + ":x") // reuse normalisation; the dummy tag is ignored
	if r.Registry == p.Registry && r.Repository == p.Repository {
		return true
	}
	return strings.HasSuffix(r.Repository, "/"+p.Repository)
}
