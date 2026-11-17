package registry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLabelsThroughIndexWithAnonymousToken(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			if r.URL.Query().Get("scope") != "repository:atmoz/sftp:pull" || r.Header.Get("Authorization") != "" {
				w.WriteHeader(400)
				return
			}
			io.WriteString(w, `{"token":"anon"}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer anon" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="registry.docker.io",scope="repository:atmoz/sftp:pull"`, srv.URL))
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/v2/atmoz/sftp/manifests/alpine":
			io.WriteString(w, `{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[
				{"digest":"sha256:arm","platform":{"os":"linux","architecture":"arm64"}},
				{"digest":"sha256:amd","platform":{"os":"linux","architecture":"amd64"}}]}`)
		case "/v2/atmoz/sftp/manifests/sha256:amd":
			io.WriteString(w, `{"config":{"digest":"sha256:cfg"},"annotations":{"org.opencontainers.image.title":"sftp"}}`)
		case "/v2/atmoz/sftp/blobs/sha256:cfg":
			io.WriteString(w, `{"config":{"Labels":{"org.opencontainers.image.source":"https://github.com/atmoz/sftp"}}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := &Client{base: func(string) string { return srv.URL }}
	labels, err := c.Labels(context.Background(), "docker.io", "atmoz/sftp", "alpine")
	if err != nil {
		t.Fatal(err)
	}
	if labels[SourceLabel] != "https://github.com/atmoz/sftp" || labels["org.opencontainers.image.title"] != "sftp" {
		t.Errorf("labels: %v", labels)
	}
}

func TestTokenRealmMustBeKnownHost(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="https://10.0.0.5/admin",service="x"`)
		w.WriteHeader(401)
	}))
	defer srv.Close()
	s := &session{c: &Client{HTTP: srv.Client()}, base: srv.URL, repo: "a/b"}
	var v any
	if err := s.getJSON(context.Background(), "/v2/a/b/manifests/1", "*/*", &v); err == nil || !strings.Contains(err.Error(), "known registry host") {
		t.Fatalf("followed a realm to an unknown host: %v", err)
	}
}

func TestOnlyPublicRegistries(t *testing.T) {
	c := &Client{}
	for _, reg := range []string{"registry.gitlab.example.com", "169.254.169.254", "localhost:5000", "harbor.internal"} {
		if _, err := c.Labels(context.Background(), reg, "a/b", "1"); err != ErrNotPublic {
			t.Errorf("%s: %v", reg, err)
		}
	}
	if _, err := c.Labels(context.Background(), "docker.io", "../../etc", "1"); err == nil || err == ErrNotPublic {
		t.Errorf("path traversal: %v", err)
	}
}

func TestOversizedManifestIsCut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"annotations":{"x":"`+strings.Repeat("a", maxDoc)+`"}}`)
	}))
	defer srv.Close()
	c := &Client{base: func(string) string { return srv.URL }}
	if _, err := c.Labels(context.Background(), "ghcr.io", "a/b", "1"); err == nil {
		t.Error("read a document over the limit")
	}
}

func TestGitHubRepo(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/atmoz/sftp":               "atmoz/sftp",
		"https://github.com/org/app.git":              "org/app",
		"git@github.com:org/app.git":                  "org/app",
		"https://github.com/org/app/tree/main/docker": "org/app",
		"https://gitlab.com/org/app":                  "",
		"https://github.com.evil.example/org/app":     "",
		"": "",
	} {
		if got := GitHubRepo(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
