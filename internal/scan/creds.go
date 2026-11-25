package scan

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// Credential is a registry login for images under Prefix, e.g. a GitLab group deploy token with
// read_registry: from OWL_REGISTRY_AUTH_FILE or from the UI (milestone 15.1, sealed in the DB).
type Credential struct {
	Prefix   string `json:"prefix"`
	Username string `json:"username"`
	Password string `json:"password"`
	Expires  string `json:"expires,omitempty"` // file only: YYYY-MM-DD

	ExpiresAt *time.Time `json:"-"` // end of the expiry day (UTC)
	FromFile  bool       `json:"-"`
}

// ExpiryWarning: a credential expires within this long.
const ExpiryWarning = 14 * 24 * time.Hour

// EndOfDay parses YYYY-MM-DD as the last moment of that day (UTC).
func EndOfDay(day string) (time.Time, error) {
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		return time.Time{}, err
	}
	return d.Add(24*time.Hour - time.Millisecond), nil
}

// Expiring is a credential that has expired or expires within ExpiryWarning.
type Expiring struct {
	Prefix    string    `json:"prefix"`
	ExpiresAt time.Time `json:"expiresAt"`
	Expired   bool      `json:"expired"`
}

// Expiring lists the credentials that need renewing soon, soonest first.
func (cs Credentials) Expiring(now time.Time) []Expiring {
	out := []Expiring{}
	for _, c := range cs {
		if c.ExpiresAt != nil && c.ExpiresAt.Sub(now) <= ExpiryWarning {
			out = append(out, Expiring{Prefix: c.Prefix, ExpiresAt: *c.ExpiresAt, Expired: !c.ExpiresAt.After(now)})
		}
	}
	slices.SortFunc(out, func(a, b Expiring) int { return a.ExpiresAt.Compare(b.ExpiresAt) })
	return out
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
		if c.Expires != "" {
			t, err := EndOfDay(c.Expires)
			if err != nil {
				return nil, fmt.Errorf("registry credentials %s: expires must be a date like 2027-03-31", c.Prefix)
			}
			c.ExpiresAt = &t
		}
		c.FromFile = true
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

// NormPrefix normalizes a credential prefix and checks it (ok=false: not a registry host or host/path).
func NormPrefix(p string) (string, bool) {
	n := normPrefix(p)
	return n, n != "" && !strings.ContainsAny(n, " @\t") && !strings.HasPrefix(n, "/") && len(n) <= 300
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
