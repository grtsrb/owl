package scan

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"sigs.k8s.io/yaml"
)

// Credential is a registry login for images under Prefix (OWL_REGISTRY_AUTH_FILE), e.g. a GitLab group
// deploy token with read_registry.
type Credential struct {
	Prefix   string `json:"prefix"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// Credentials are matched by the longest prefix; no match = anonymous.
type Credentials []Credential

// LoadCredentials reads the credentials file ("" = none).
func LoadCredentials(path string) (Credentials, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseCredentials(b)
}

// ParseCredentials parses and checks the YAML list. Errors never contain a password.
func ParseCredentials(b []byte) (Credentials, error) {
	var cs Credentials
	if err := yaml.UnmarshalStrict(b, &cs); err != nil {
		return nil, fmt.Errorf("registry credentials: a YAML list of {prefix, username, password}: %v", redact(err))
	}
	seen := map[string]bool{}
	for i := range cs {
		c := &cs[i]
		c.Prefix = normPrefix(c.Prefix)
		switch {
		case c.Prefix == "" || strings.ContainsAny(c.Prefix, " @\t"):
			return nil, fmt.Errorf("registry credentials entry %d: prefix must be a registry host or host/path, e.g. registry.gitlab.example.com/team-a", i+1)
		case c.Username == "" || c.Password == "":
			return nil, fmt.Errorf("registry credentials %s: username and password are required", c.Prefix)
		case seen[c.Prefix]:
			return nil, fmt.Errorf("registry credentials: %s is listed twice", c.Prefix)
		}
		seen[c.Prefix] = true
	}
	return cs, nil
}

// redact keeps only the first line of a YAML error and drops anything after a colon that could quote a value.
func redact(err error) error {
	msg := strings.SplitN(err.Error(), "\n", 2)[0]
	if i := strings.Index(msg, "unknown field"); i >= 0 {
		return errors.New(msg[i:])
	}
	return errors.New("invalid YAML")
}

// normPrefix: lower-case host, no scheme, no trailing slash; Docker Hub's short forms become docker.io.
func normPrefix(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(strings.TrimPrefix(p, "https://"), "http://")
	p = strings.TrimRight(p, "/")
	host, rest, _ := strings.Cut(p, "/")
	host = strings.ToLower(host)
	if host == "index.docker.io" || host == "registry-1.docker.io" {
		host = "docker.io"
	}
	if rest == "" {
		return host
	}
	return host + "/" + rest
}

// For returns the credential for ref (repository@digest or repository:tag), longest prefix first, at a
// path boundary: "…/team-a" matches "…/team-a/app", never "…/team-ab/app".
func (cs Credentials) For(ref string) *Credential {
	name := normPrefix(ref)
	var best *Credential
	for i := range cs {
		c := &cs[i]
		if matches(name, c.Prefix) && (best == nil || len(c.Prefix) > len(best.Prefix)) {
			best = c
		}
	}
	return best
}

func matches(name, prefix string) bool {
	if name == prefix {
		return true
	}
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	switch name[len(prefix)] {
	case '/', '@':
		return true
	case ':': // a tag after a repository path; after a bare host it would be a port (another registry)
		return strings.Contains(prefix, "/")
	}
	return false
}
