// Package github wraps just enough of the GitHub Actions REST API for the
// Phase 4 dashboard: list recent workflow runs per repo, with ETag-based
// conditional GETs so a poll loop hitting the API every minute doesn't
// chew through the per-account rate limit budget.
//
// Phase 4 supports PAT auth only; GitHub App auth (private key + JWT
// minting + installation token refresh) lands in Phase 4.5. The Auth
// interface below is the seam where the App path slots in.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const defaultBaseURL = "https://api.github.com"

// Auth is the minimal contract a credential provider must satisfy.
// PAT auth is a static "Bearer <token>" header; GitHub App auth needs
// to mint a fresh JWT and exchange it for a short-lived installation
// token periodically — same Apply() entry point either way.
type Auth interface {
	Apply(req *http.Request) error
	Name() string
}

// PATAuth is the simplest Auth: a long-lived personal access token.
type PATAuth struct{ Token string }

func (p *PATAuth) Apply(req *http.Request) error {
	if p.Token == "" {
		return errors.New("github: empty PAT")
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	return nil
}
func (p *PATAuth) Name() string { return "pat" }

// Client speaks the GitHub REST API on behalf of one set of credentials.
// Safe for concurrent use; ETag caches are per (repo, endpoint) and
// protected by a single mutex (the cache is tiny so contention is fine).
type Client struct {
	auth    Auth
	baseURL string
	http    *http.Client

	mu        sync.Mutex
	etagCache map[string]etagEntry
	rate      RateLimit
}

type etagEntry struct {
	etag string
	body []byte
	at   time.Time
}

// RateLimit mirrors what GitHub returns in the X-RateLimit-* headers so
// the dashboard can surface "you have 4823 requests left this hour".
type RateLimit struct {
	Limit     int
	Remaining int
	ResetAt   time.Time
}

type Options struct {
	BaseURL string // defaults to api.github.com; tests / GH Enterprise override
}

func New(auth Auth, opts Options) *Client {
	base := opts.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	return &Client{
		auth:    auth,
		baseURL: base,
		http: &http.Client{
			Timeout: 15 * time.Second,
		},
		etagCache: make(map[string]etagEntry),
	}
}

// Ping verifies credentials by fetching /user (PAT) or /app (App).
// Returns the authenticated identity name on success.
func (c *Client) Ping(ctx context.Context) (string, error) {
	body, err := c.do(ctx, "GET", "/user", "")
	if err != nil {
		return "", err
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return "", err
	}
	return u.Login, nil
}

// WorkflowRun is the dashboard projection of a /actions/runs item. The
// upstream API returns ~60 fields per run; we keep the ones the UI
// actually renders + the URL so click-throughs work.
type WorkflowRun struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`           // workflow name
	HeadBranch string    `json:"head_branch"`
	HeadSHA    string    `json:"head_sha"`
	Status     string    `json:"status"`         // queued | in_progress | completed
	Conclusion string    `json:"conclusion"`     // success | failure | cancelled | …
	Event      string    `json:"event"`
	RunNumber  int       `json:"run_number"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	HTMLURL    string    `json:"html_url"`
	Actor      struct {
		Login     string `json:"login"`
		AvatarURL string `json:"avatar_url"`
	} `json:"actor"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// ListWorkflowRuns fetches recent runs for a repo with conditional-GET
// caching. On a 304 we return the previously-decoded payload — the
// caller can't tell the difference from a fresh fetch, but the API
// quota stays intact.
func (c *Client) ListWorkflowRuns(ctx context.Context, owner, repo string, perPage int) ([]WorkflowRun, error) {
	if perPage <= 0 || perPage > 100 {
		perPage = 50
	}
	path := fmt.Sprintf("/repos/%s/%s/actions/runs?per_page=%d", owner, repo, perPage)
	body, err := c.do(ctx, "GET", path, "")
	if err != nil {
		return nil, err
	}
	var resp struct {
		WorkflowRuns []WorkflowRun `json:"workflow_runs"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	for i := range resp.WorkflowRuns {
		// The /actions/runs payload omits repo.full_name; fill it from
		// the URL we already have so the UI doesn't need to know the
		// owner/repo separately.
		if resp.WorkflowRuns[i].Repository.FullName == "" {
			resp.WorkflowRuns[i].Repository.FullName = owner + "/" + repo
		}
	}
	return resp.WorkflowRuns, nil
}

// RateLimit returns the most-recently observed rate-limit window. Cheap
// to call; updated on every successful request.
func (c *Client) RateLimit() RateLimit {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rate
}

// do is the single HTTP plumbing point. It applies auth, reads + caches
// ETag, decodes rate-limit headers, and surfaces 4xx/5xx as errors.
func (c *Client) do(ctx context.Context, method, path, _body string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if err := c.auth.Apply(req); err != nil {
		return nil, err
	}

	cacheKey := method + " " + path
	c.mu.Lock()
	cached, hasCache := c.etagCache[cacheKey]
	c.mu.Unlock()
	if hasCache && cached.etag != "" {
		req.Header.Set("If-None-Match", cached.etag)
	}

	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	c.mu.Lock()
	c.rate = parseRate(res.Header)
	c.mu.Unlock()

	if res.StatusCode == http.StatusNotModified && hasCache {
		// Served from cache. Touch the entry so a stale-pruner (Phase 6+)
		// can tell the difference between hot and cold cache rows.
		c.mu.Lock()
		cached.at = time.Now()
		c.etagCache[cacheKey] = cached
		c.mu.Unlock()
		return cached.body, nil
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		return nil, &APIError{Status: res.StatusCode, Body: string(body)}
	}

	if etag := res.Header.Get("ETag"); etag != "" {
		c.mu.Lock()
		c.etagCache[cacheKey] = etagEntry{etag: etag, body: body, at: time.Now()}
		c.mu.Unlock()
	}
	return body, nil
}

// APIError is what callers see when GitHub returns a non-2xx response.
// Implements `error` and exposes Status so the UI can render 401/403
// differently from a generic 500.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("github: %d %s", e.Status, e.Body)
}

func parseRate(h http.Header) RateLimit {
	r := RateLimit{}
	if s := h.Get("X-RateLimit-Limit"); s != "" {
		fmt.Sscanf(s, "%d", &r.Limit)
	}
	if s := h.Get("X-RateLimit-Remaining"); s != "" {
		fmt.Sscanf(s, "%d", &r.Remaining)
	}
	if s := h.Get("X-RateLimit-Reset"); s != "" {
		var ts int64
		if _, err := fmt.Sscanf(s, "%d", &ts); err == nil && ts > 0 {
			r.ResetAt = time.Unix(ts, 0)
		}
	}
	return r
}
