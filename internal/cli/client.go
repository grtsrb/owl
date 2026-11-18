// Package cli is the `owl` command-line client (milestone 12): it talks to the Owl server's HTTP API
// with a personal API token. It must stay light — standard library plus YAML and x/term — because it
// is also built alone as the downloadable client (no server, agent or Kubernetes code).
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// config is ~/.config/owl/config.yaml (mode 0600). OWL_SERVER / OWL_TOKEN override it (CI).
type config struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

func configPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "owl", "config.yaml"), nil
}

func loadConfig() (config, error) {
	var c config
	p, err := configPath()
	if err != nil {
		return c, err
	}
	raw, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	} else if err != nil {
		return c, err
	}
	if err := yaml.UnmarshalStrict(raw, &c); err != nil {
		return c, fmt.Errorf("%s: %w", p, err)
	}
	return c, nil
}

// saveConfig writes the file atomically with 0600 (it holds a token); an empty config removes it.
func saveConfig(c config) error {
	p, err := configPath()
	if err != nil {
		return err
	}
	if c.Server == "" && c.Token == "" {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	raw, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".config-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// checkServerURL: https, or http only to a loopback host (local development).
func checkServerURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("server URL %q: expected https://owl.example.com", raw)
	}
	if u.Scheme == "http" && !loopback(u.Hostname()) {
		return nil, fmt.Errorf("refusing plain http:// to %s: the token would travel unencrypted (use https://; http only for localhost)", u.Host)
	}
	return u, nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// client is one configured server + token.
type client struct {
	base    *url.URL
	token   string
	http    *http.Client
	version string
	header  http.Header // extra headers on every request (users apply: X-Owl-Source)
}

// apiError is an error answer from the server.
type apiError struct {
	Status int
	Msg    string
}

func (e *apiError) Error() string {
	switch e.Status {
	case http.StatusUnauthorized:
		return e.Msg + " (run `owl login` with a new token)"
	}
	return e.Msg
}

func newClient(server, token, version string) (*client, error) {
	u, err := checkServerURL(server)
	if err != nil {
		return nil, err
	}
	return &client{base: u, token: token, version: version, header: http.Header{},
		http: &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // never follow redirects with the token
		}}}, nil
}

// do sends a request and decodes a JSON answer into out (if not nil). Errors carry the server's
// message ({"error": "..."}).
func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, rd)
	if err != nil {
		return err
	}
	for k, v := range c.header {
		req.Header[k] = v
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "owl-cli/"+c.version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", c.base.Host, unwrapURLError(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			msg = e.Error
		} else if len(msg) > 200 || msg == "" {
			msg = resp.Status
		}
		return &apiError{Status: resp.StatusCode, Msg: msg}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if p, ok := out.(*json.RawMessage); ok {
		*p = append((*p)[:0], raw...)
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("unexpected answer from %s (is this an Owl server?): %w", c.base.Host, err)
	}
	return nil
}

func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err // the URL is known; don't repeat it
	}
	return err
}
