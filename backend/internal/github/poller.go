package github

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"
)

// Poller drives periodic refreshes of the configured repos and pushes
// the merged result onto the realtime hub as a GH_WORKFLOW_RUNS envelope
// (scoped to the virtual server ID "github" so the browser tab manager
// can route it next to the per-server views).
//
// The poller also serves as the canonical "current state" snapshot for
// the /api/v2/github/runs REST endpoint — Snapshot() is O(1) and lock-
// free for readers (RWMutex), so the dashboard can hit it on every
// page load without worrying about hammering the API.
type Poller struct {
	client   *Client
	repos    []string // "owner/repo"
	interval time.Duration
	broadcast func(string, string, interface{}) // (serverID, msgType, payload)

	mu       sync.RWMutex
	snapshot Snapshot
	stop     chan struct{}
}

// Snapshot is what the REST endpoint returns and what the hub broadcasts.
// Aggregates runs across every configured repo plus the most recent
// observed rate-limit window so the UI can warn before we're throttled.
type Snapshot struct {
	UpdatedAt time.Time     `json:"updatedAt"`
	Runs      []WorkflowRun `json:"runs"`
	Repos     []RepoStatus  `json:"repos"`
	Rate      RateLimit     `json:"rate"`
}

type RepoStatus struct {
	FullName    string    `json:"fullName"`
	LastFetched time.Time `json:"lastFetched"`
	LastError   string    `json:"lastError,omitempty"`
	RunCount    int       `json:"runCount"`
}

func NewPoller(client *Client, repos []string, interval time.Duration, broadcast func(string, string, interface{})) (*Poller, error) {
	if client == nil {
		return nil, errors.New("github: nil client")
	}
	if interval <= 0 {
		interval = 60 * time.Second
	}
	cleaned := make([]string, 0, len(repos))
	for _, r := range repos {
		r = strings.TrimSpace(r)
		if r == "" || !strings.Contains(r, "/") {
			continue
		}
		cleaned = append(cleaned, r)
	}
	return &Poller{
		client:    client,
		repos:     cleaned,
		interval:  interval,
		broadcast: broadcast,
		stop:      make(chan struct{}),
	}, nil
}

// Start blocks for one initial fetch (so the snapshot isn't empty on
// first page load) then runs the refresh loop in a goroutine.
func (p *Poller) Start(ctx context.Context) {
	p.refresh(ctx)
	go p.loop(ctx)
}

func (p *Poller) Stop() {
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
}

func (p *Poller) loop(ctx context.Context) {
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stop:
			return
		case <-t.C:
			p.refresh(ctx)
		}
	}
}

func (p *Poller) refresh(ctx context.Context) {
	statuses := make([]RepoStatus, 0, len(p.repos))
	allRuns := make([]WorkflowRun, 0, 64)

	for _, repo := range p.repos {
		owner, name, ok := splitRepo(repo)
		if !ok {
			continue
		}
		runs, err := p.client.ListWorkflowRuns(ctx, owner, name, 30)
		s := RepoStatus{
			FullName:    repo,
			LastFetched: time.Now(),
			RunCount:    len(runs),
		}
		if err != nil {
			s.LastError = err.Error()
			log.Printf("github: %s: %v", repo, err)
		} else {
			allRuns = append(allRuns, runs...)
		}
		statuses = append(statuses, s)
	}

	// Newest first — the dashboard sorts by created_at desc anyway, but
	// doing it here means the broadcast envelope is already in the
	// right order for any naive consumer.
	sortRunsByCreatedDesc(allRuns)

	snap := Snapshot{
		UpdatedAt: time.Now(),
		Runs:      allRuns,
		Repos:     statuses,
		Rate:      p.client.RateLimit(),
	}

	p.mu.Lock()
	p.snapshot = snap
	p.mu.Unlock()

	if p.broadcast != nil {
		p.broadcast("github", "GH_WORKFLOW_RUNS", snap)
	}
}

// Snapshot returns the most recent merged view. The returned value is
// a shallow copy; callers must not mutate the Runs slice in-place.
func (p *Poller) Snapshot() Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.snapshot
}

// Repos returns the configured "owner/repo" list (after normalization).
// Exposed so the REST endpoint can render the configuration view.
func (p *Poller) Repos() []string {
	out := make([]string, len(p.repos))
	copy(out, p.repos)
	return out
}

func splitRepo(s string) (owner, name string, ok bool) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func sortRunsByCreatedDesc(runs []WorkflowRun) {
	// Tiny inline insertion sort — N ≤ 30 * #repos in practice; the
	// O(N²) constant beats pulling in sort.Slice for this hot path.
	for i := 1; i < len(runs); i++ {
		j := i
		for j > 0 && runs[j-1].CreatedAt.Before(runs[j].CreatedAt) {
			runs[j-1], runs[j] = runs[j], runs[j-1]
			j--
		}
	}
}
