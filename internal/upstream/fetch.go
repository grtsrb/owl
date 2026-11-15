// Package upstream fetches public data about components: release lines and EOL (endoflife.date),
// releases and archived status (GitHub), and security advisories (GitHub repo advisories, OSV.dev).
//
// Only public component names/versions are sent to these services — never cluster data.
package upstream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"service-inventory/internal/store"
)

// Fetcher does cached HTTP GETs. Responses are stored in the database with their ETag; fresh entries
// are served without a request, stale ones are revalidated (a GitHub 304 doesn't count against the
// rate limit), and if the upstream is down the last good body is served with stale=true.
type Fetcher struct {
	HTTP        *http.Client
	Cache       store.Store
	GitHubToken string
	UserAgent   string
	TTL         time.Duration
	now         func() time.Time
}

const maxBody = 20 << 20

type result struct {
	Body  []byte
	Stale bool // upstream failed; this is the last good copy
}

func (f *Fetcher) get(ctx context.Context, key, url string) (*result, error) {
	return f.getTTL(ctx, key, url, f.TTL)
}

func (f *Fetcher) getTTL(ctx context.Context, key, url string, ttl time.Duration) (*result, error) {
	now := time.Now
	if f.now != nil {
		now = f.now
	}
	cached, err := f.Cache.GetCache(ctx, key)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if cached != nil && now().Sub(cached.FetchedAt) < ttl {
		return &result{Body: cached.Body}, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.UserAgent)
	req.Header.Set("Accept", "application/json")
	if f.GitHubToken != "" && isGitHubAPI(url) {
		req.Header.Set("Authorization", "Bearer "+f.GitHubToken)
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	if cached != nil && cached.ETag != "" {
		req.Header.Set("If-None-Match", cached.ETag)
	}

	body, etag, status, err := f.do(req)
	switch {
	case err == nil && status == http.StatusNotModified && cached != nil:
		cached.FetchedAt = now()
		_ = f.Cache.PutCache(ctx, *cached)
		return &result{Body: cached.Body}, nil
	case err == nil && status == http.StatusOK:
		_ = f.Cache.PutCache(ctx, store.CacheEntry{Key: key, FetchedAt: now(), ETag: etag, Body: body})
		return &result{Body: body}, nil
	case err == nil:
		err = fmt.Errorf("GET %s: HTTP %d", url, status)
	}
	if cached != nil {
		return &result{Body: cached.Body, Stale: true}, nil
	}
	return nil, err
}

func (f *Fetcher) do(req *http.Request) (body []byte, etag string, status int, err error) {
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return nil, "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, "", resp.StatusCode, nil
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return body, resp.Header.Get("ETag"), resp.StatusCode, err
}

func newPost(ctx context.Context, url string, body []byte, ua string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func cacheEntry(key string, at time.Time, etag string, body []byte) store.CacheEntry {
	return store.CacheEntry{Key: key, FetchedAt: at, ETag: etag, Body: body}
}

func isGitHubAPI(url string) bool {
	const p = "https://api.github.com/"
	return len(url) >= len(p) && url[:len(p)] == p
}
