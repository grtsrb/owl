// Package registry reads an image's OCI labels from a public container registry, anonymously, to
// suggest the source repository when someone adds a component in the UI (milestone 11). Requests go
// only to the public registries in Hosts (and their token services in tokenHosts) — never a private
// registry — and only when an admin opens the form. Blob downloads may follow the registry's https
// redirect to its storage; no credentials exist to leak.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Hosts maps an image's registry to the API host Owl may contact for it.
var Hosts = map[string]string{
	"docker.io":       "registry-1.docker.io",
	"ghcr.io":         "ghcr.io",
	"quay.io":         "quay.io",
	"registry.k8s.io": "registry.k8s.io",
	"public.ecr.aws":  "public.ecr.aws",
}

// tokenHosts are the only hosts a registry's Bearer challenge may send Owl to for an anonymous token.
// A realm elsewhere (an internal address, say) is refused: no request goes to a host from a response.
var tokenHosts = map[string]bool{"auth.docker.io": true, "ghcr.io": true, "quay.io": true, "registry.k8s.io": true, "public.ecr.aws": true}

// SourceLabel is the OCI annotation/label naming an image's source repository.
const SourceLabel = "org.opencontainers.image.source"

const maxDoc = 4 << 20 // manifests and configs are small; never read more

var manifestTypes = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// Client fetches labels. HTTP defaults to a client with a timeout that only follows https redirects
// (registries redirect blob downloads to storage).
type Client struct {
	HTTP      *http.Client
	UserAgent string
	// base overrides the https://<host> URL (tests).
	base func(host string) string
}

var ErrNotPublic = errors.New("not a public registry Owl reads labels from")

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) > 5 {
			return errors.New("redirect refused")
		}
		return nil
	}}
}

var reRepo = regexp.MustCompile(`^[a-z0-9]+([._/-][a-z0-9]+)*$`)
var reTag = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)

// Labels returns the image config's labels (plus the manifest's annotations) for registry/repository:tag.
func (c *Client) Labels(ctx context.Context, registry, repository, tag string) (map[string]string, error) {
	host, ok := Hosts[registry]
	if !ok {
		return nil, ErrNotPublic
	}
	if !reRepo.MatchString(repository) || !reTag.MatchString(tag) {
		return nil, errors.New("invalid repository or tag")
	}
	base := "https://" + host
	if c.base != nil {
		base = c.base(host)
	}
	s := &session{c: c, base: base, repo: repository}
	var m struct {
		MediaType   string            `json:"mediaType"`
		Annotations map[string]string `json:"annotations"`
		Manifests   []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"`
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := s.getJSON(ctx, "/v2/"+repository+"/manifests/"+tag, manifestTypes, &m); err != nil {
		return nil, err
	}
	labels := map[string]string{}
	for k, v := range m.Annotations {
		labels[k] = v
	}
	if len(m.Manifests) > 0 { // an index: take linux/amd64 (or the first) image
		d := m.Manifests[0].Digest
		for _, x := range m.Manifests {
			if x.Platform.OS == "linux" && x.Platform.Architecture == "amd64" {
				d = x.Digest
				break
			}
		}
		m.Manifests, m.Config.Digest = nil, ""
		if err := s.getJSON(ctx, "/v2/"+repository+"/manifests/"+d, manifestTypes, &m); err != nil {
			return nil, err
		}
		for k, v := range m.Annotations {
			labels[k] = v
		}
	}
	if m.Config.Digest != "" {
		var cfg struct {
			Config struct {
				Labels map[string]string `json:"Labels"`
			} `json:"config"`
		}
		if err := s.getJSON(ctx, "/v2/"+repository+"/blobs/"+m.Config.Digest, "*/*", &cfg); err != nil {
			return nil, err
		}
		for k, v := range cfg.Config.Labels {
			labels[k] = v
		}
	}
	return labels, nil
}

type session struct {
	c     *Client
	base  string
	repo  string
	token string
}

func (s *session) getJSON(ctx context.Context, path, accept string, v any) error {
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", accept)
		req.Header.Set("User-Agent", s.c.UserAgent)
		if s.token != "" {
			req.Header.Set("Authorization", "Bearer "+s.token)
		}
		resp, err := s.c.http().Do(req)
		if err != nil {
			return err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxDoc))
		resp.Body.Close()
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusUnauthorized && s.token == "" && attempt == 0 {
			if err := s.anonymousToken(ctx, resp.Header.Get("WWW-Authenticate")); err != nil {
				return err
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("registry answered %d", resp.StatusCode)
		}
		return json.Unmarshal(body, v)
	}
	return errors.New("registry refused anonymous access")
}

var reParam = regexp.MustCompile(`(\w+)="([^"]*)"`)

// anonymousToken follows the registry's Bearer challenge without credentials. The token realm must be
// https (a challenge can't send Owl to plain http or to fetch arbitrary URLs with credentials — it has none).
func (s *session) anonymousToken(ctx context.Context, challenge string) error {
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return errors.New("registry wants credentials")
	}
	p := map[string]string{}
	for _, m := range reParam.FindAllStringSubmatch(challenge, -1) {
		p[strings.ToLower(m[1])] = m[2]
	}
	realm, err := url.Parse(p["realm"])
	if err != nil {
		return errors.New("invalid registry token realm")
	}
	if s.c.base == nil && (realm.Scheme != "https" || !tokenHosts[realm.Hostname()]) {
		return errors.New("registry token realm is not a known registry host")
	}
	q := realm.Query()
	if p["service"] != "" {
		q.Set("service", p["service"])
	}
	scope := p["scope"]
	if scope == "" {
		scope = "repository:" + s.repo + ":pull"
	}
	q.Set("scope", scope)
	realm.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", s.c.UserAgent)
	resp, err := s.c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("registry token: %d", resp.StatusCode)
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t); err != nil {
		return err
	}
	s.token = t.Token
	if s.token == "" {
		s.token = t.AccessToken
	}
	if s.token == "" {
		return errors.New("registry gave no token")
	}
	return nil
}

var reGitHubURL = regexp.MustCompile(`^(?:https?://|git@)?(?:www\.)?github\.com[/:]([A-Za-z0-9][A-Za-z0-9-]{0,38})/([A-Za-z0-9._-]{1,100}?)(?:\.git)?(?:[/#?].*)?$`)

// GitHubRepo extracts owner/repo from a source label ("https://github.com/atmoz/sftp",
// "git@github.com:org/app.git", "https://github.com/org/app/tree/main"); "" if it isn't GitHub.
func GitHubRepo(source string) string {
	m := reGitHubURL.FindStringSubmatch(strings.TrimSpace(source))
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}
