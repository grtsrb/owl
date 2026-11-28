package scan

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// neverRegistry is NeverRegistry (tests with a registry on 127.0.0.1 replace it).
var neverRegistry = NeverRegistry

// Login results (Test in Settings → Registries).
const (
	LoginOK         = "ok"
	LoginRejected   = "rejected"   // the registry refused the username/password
	LoginDNS        = "dns"        // the host doesn't resolve from the Owl server
	LoginConnect    = "connect"    // no connection (refused, timeout, unreachable)
	LoginTLS        = "tls"        // TLS handshake / certificate problem
	LoginUnexpected = "unexpected" // not a registry answer Owl understands
)

// LoginResult is what a Test says. Message never contains the password.
type LoginResult struct {
	Outcome string    `json:"outcome"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

// TestLogin logs in to the credential's registry the way a pull would (OCI distribution auth): GET
// https://<host>/v2/; on a Bearer challenge, the token endpoint with Basic auth and a pull scope for repo
// (a repository path on that host, e.g. "devops/billing/api"; "" = the prefix's path, unchecked); on a Basic
// challenge, /v2/ again with Basic auth. client nil = a default client with a short timeout. Redirects and
// token realms that point at IP literals, localhost or metadata hosts are refused (unless they're the
// registry host itself): the realm is the registry's to choose.
//
// A token endpoint answers 200 for any valid login, also for a repository it can't read (GitLab: a token
// with an empty "access" list). So with a repo, a JWT's access claim must grant pull on it.
func TestLogin(ctx context.Context, client *http.Client, c Credential, repo string) LoginResult {
	host, path, _ := strings.Cut(c.Prefix, "/")
	if host == "docker.io" {
		host = "registry-1.docker.io"
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	cl := *client
	cl.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return safeURL(req.URL, host)
	}
	res := func(outcome, msg string) LoginResult {
		return LoginResult{Outcome: outcome, Message: msg, At: time.Now()}
	}
	if why := neverRegistry(host); why != "" {
		return res(LoginUnexpected, host+": "+why)
	}
	get := func(u string, basic bool) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		if basic {
			req.SetBasicAuth(c.Username, c.Password)
		}
		return cl.Do(req)
	}
	resp, err := get("https://"+host+"/v2/", false)
	if err != nil {
		return res(classifyLogin(err, host))
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return res(LoginUnexpected, host+" doesn't ask for a login (anonymous access) — the credential isn't needed or isn't checked here")
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return res(LoginUnexpected, host+"/v2/ answered "+resp.Status+" — is this a container registry?")
	}
	scheme, params := parseChallenge(resp.Header.Get("WWW-Authenticate"))
	switch scheme {
	case "basic":
		r2, err := get("https://"+host+"/v2/", true)
		if err != nil {
			return res(classifyLogin(err, host))
		}
		r2.Body.Close()
		return statusResult(r2.StatusCode, r2.Status, host)
	case "bearer":
		realm, err := url.Parse(params["realm"])
		if err != nil || params["realm"] == "" {
			return res(LoginUnexpected, host+" sent a login challenge without a token address")
		}
		if err := safeURL(realm, host); err != nil {
			return res(LoginUnexpected, err.Error())
		}
		q := realm.Query()
		if params["service"] != "" {
			q.Set("service", params["service"])
		}
		checked := repo != ""
		if !checked {
			repo = path
		}
		if repo == "" {
			repo = "owl-login-check"
		}
		q.Set("scope", "repository:"+repo+":pull")
		realm.RawQuery = q.Encode()
		r2, err := get(realm.String(), true)
		if err != nil {
			outcome, msg := classifyLogin(err, realm.Hostname())
			if !strings.EqualFold(realm.Hostname(), strings.Split(host, ":")[0]) {
				// GitLab & co. log in on another host (/jwt/auth on the web host). Behind a VPN / ziti that host
				// must be reachable too, or every pull looks like a wrong password.
				msg = host + " answered, but its login service on " + realm.Hostname() + " didn't: " + msg +
					". Owl needs both hosts (with a VPN / ziti, the login host must be exposed too)"
			}
			return res(outcome, msg)
		}
		defer r2.Body.Close()
		if r2.StatusCode == http.StatusOK {
			var tok struct {
				Token       string `json:"token"`
				AccessToken string `json:"access_token"`
			}
			if json.NewDecoder(io.LimitReader(r2.Body, 1<<20)).Decode(&tok) != nil || tok.Token == "" && tok.AccessToken == "" {
				return res(LoginUnexpected, "the token endpoint answered without a token")
			}
			switch {
			case !checked:
				r := statusResult(r2.StatusCode, r2.Status, host)
				r.Message += " (read access not checked: no running image below this prefix yet)"
				return r
			case grantsPull(cmpOr(tok.Token, tok.AccessToken), repo) == accessDenied:
				return res(LoginRejected, "the login works, but "+host+" gives it no read access to "+repo+
					" — a token of another group or project? (GitLab: a group deploy token reads only its own group)")
			}
			r := statusResult(r2.StatusCode, r2.Status, host)
			r.Message += ", with read access to " + repo
			return r
		}
		return statusResult(r2.StatusCode, r2.Status, host)
	}
	return res(LoginUnexpected, host+" asks for an unknown kind of login")
}

type access int

const (
	accessUnknown access = iota // not a JWT, or no access claim: can't tell
	accessGranted
	accessDenied
)

// grantsPull reads the (unverified — it's only a hint for Test) access claim of a registry JWT.
func grantsPull(token, repo string) access {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return accessUnknown
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return accessUnknown
	}
	var claims struct {
		Access *[]struct {
			Type    string   `json:"type"`
			Name    string   `json:"name"`
			Actions []string `json:"actions"`
		} `json:"access"`
	}
	if json.Unmarshal(b, &claims) != nil || claims.Access == nil {
		return accessUnknown
	}
	for _, a := range *claims.Access {
		if a.Type == "repository" && a.Name == repo && (slices.Contains(a.Actions, "pull") || slices.Contains(a.Actions, "*")) {
			return accessGranted
		}
	}
	return accessDenied
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func statusResult(code int, status, host string) LoginResult {
	r := LoginResult{At: time.Now()}
	switch {
	case code == http.StatusOK:
		r.Outcome, r.Message = LoginOK, "login accepted by "+host
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		r.Outcome, r.Message = LoginRejected, host+" rejected the username or password ("+status+") — expired or revoked token, or no read_registry scope?"
	default:
		r.Outcome, r.Message = LoginUnexpected, "login answered "+status
	}
	return r
}

// safeURL: https only, and not an IP literal / localhost / metadata host unless it is the registry itself.
func safeURL(u *url.URL, registry string) error {
	if u.Scheme != "https" {
		return errors.New("the registry pointed the login at a non-HTTPS address")
	}
	if h := strings.ToLower(u.Host); h == registry || u.Hostname() == registry {
		return nil
	}
	if ok, _ := RegistryAllowed(u.Host, nil); !ok || NeverRegistry(u.Host) != "" {
		return errors.New("the registry pointed the login at a local, metadata or IP address — refused")
	}
	return nil
}

// classifyLogin turns a transport error into an outcome and a message without URLs (they could carry a query).
func classifyLogin(err error, host string) (string, string) {
	var dns *net.DNSError
	var opErr *net.OpError
	var cert *tls.CertificateVerificationError
	var rec tls.RecordHeaderError
	switch {
	case errors.As(err, &dns):
		return LoginDNS, "can't resolve " + host + " from the Owl server — check its DNS (VPN / ziti / split DNS)"
	case errors.As(err, &cert), errors.As(err, &rec), strings.Contains(err.Error(), "tls:"), strings.Contains(err.Error(), "x509:"), strings.Contains(err.Error(), "HTTP response to HTTPS"):
		return LoginTLS, "TLS to " + host + " failed (certificate not trusted, or not HTTPS)"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &opErr), strings.Contains(err.Error(), "Client.Timeout"):
		return LoginConnect, "can't connect to " + host + " from the Owl server (refused, unreachable or timeout)"
	}
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return LoginUnexpected, ue.Err.Error()
	}
	return LoginUnexpected, "login check failed"
}

// parseChallenge reads `Bearer realm="…",service="…"` (scheme lower-cased).
func parseChallenge(h string) (string, map[string]string) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	params := map[string]string{}
	for rest != "" {
		rest = strings.TrimLeft(rest, " ,")
		k, v, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		k = strings.ToLower(strings.TrimSpace(k))
		if strings.HasPrefix(v, `"`) {
			end := strings.Index(v[1:], `"`)
			if end < 0 {
				params[k], rest = v[1:], ""
				break
			}
			params[k], rest = v[1:1+end], v[2+end:]
		} else {
			val, r, _ := strings.Cut(v, ",")
			params[k], rest = strings.TrimSpace(val), r
		}
	}
	return strings.ToLower(scheme), params
}
