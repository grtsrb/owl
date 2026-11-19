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

// publicRegistries host images under user/org namespaces; a longer path there is a different image,
// never a mirror of a shorter one.
var publicRegistries = map[string]bool{
	"docker.io": true, "ghcr.io": true, "quay.io": true, "registry.k8s.io": true, "k8s.gcr.io": true,
	"gcr.io": true, "public.ecr.aws": true, "mcr.microsoft.com": true, "registry.gitlab.com": true,
	"docker.redpanda.com": true, "docker.elastic.co": true, "cr.fluentbit.io": true, "oci.external-secrets.io": true,
}

// mirrorOf reports whether r is pattern p served from a mirror/pull-through cache, i.e. p's path is a
// suffix of r's path on a segment boundary: harbor.example.com/dockerhub/library/nginx -> library/nginx.
func (r Ref) mirrorOf(p Ref) bool {
	return strings.HasSuffix(r.Repository, "/"+p.Repository)
}
