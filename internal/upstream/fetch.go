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
	"strings"
	"time"

	"owl/internal/store"
)

// Fetcher does cached HTTP requests. Responses are stored in the database; fresh entries are served
// without a request, expired ones are revalidated with their ETag (a GitHub 304 doesn't count against
// the rate limit), and if the upstream fails the last good body is served with stale=true.
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

// fetchFunc performs the actual request. etag is the cached ETag ("" if none); it returns the new
// body and ETag, or notModified=true to keep the cached body.
type fetchFunc func(ctx context.Context, etag string) (body []byte, newETag string, notModified bool, err error)

// cached is the single caching policy used for every upstream call.
func (f *Fetcher) cached(ctx context.Context, key string, ttl time.Duration, fetch fetchFunc) (*result, error) {
	now := time.Now
	if f.now != nil {
		now = f.now
	}
	entry, err := f.Cache.GetCache(ctx, key)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if entry != nil && now().Sub(entry.FetchedAt) < ttl {
		return &result{Body: entry.Body}, nil
	}
	etag := ""
	if entry != nil {
		etag = entry.ETag
	}
	body, newETag, notModified, err := fetch(ctx, etag)
	switch {
	case err == nil && notModified && entry != nil:
		_ = f.Cache.TouchCache(ctx, key, now())
		return &result{Body: entry.Body}, nil
	case err == nil && !notModified:
		_ = f.Cache.PutCache(ctx, store.CacheEntry{Key: key, FetchedAt: now(), ETag: newETag, Body: body})
		return &result{Body: body}, nil
	case err == nil:
		err = errors.New("not modified, but nothing cached")
	}
	if entry != nil {
		return &result{Body: entry.Body, Stale: true}, nil
	}
	return nil, err
}

func (f *Fetcher) get(ctx context.Context, key, url string) (*result, error) {
	return f.getTTL(ctx, key, url, f.TTL)
}

func (f *Fetcher) getTTL(ctx context.Context, key, url string, ttl time.Duration) (*result, error) {
	return f.cached(ctx, key, ttl, func(ctx context.Context, etag string) ([]byte, string, bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, "", false, err
		}
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		return f.send(req)
	})
}

// postJSON does an uncached POST (callers wrap it in cached() with their own key).
func (f *Fetcher) postJSON(ctx context.Context, url string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	b, _, _, err := f.send(req)
	return b, err
}

// send performs a request with standard headers; 304 is reported as notModified, other non-200 as error.
func (f *Fetcher) send(req *http.Request) ([]byte, string, bool, error) {
	req.Header.Set("User-Agent", f.UserAgent)
	req.Header.Set("Accept", "application/json")
	if f.GitHubToken != "" && strings.HasPrefix(req.URL.String(), "https://api.github.com/") {
		req.Header.Set("Authorization", "Bearer "+f.GitHubToken)
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		return body, resp.Header.Get("ETag"), false, err
	case http.StatusNotModified:
		return nil, "", true, nil
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil, "", false, fmt.Errorf("%s %s: HTTP %d", req.Method, req.URL, resp.StatusCode)
}
