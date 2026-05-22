// Package hub holds the v2 multi-server hub logic: a server registry, agent
// connection manager, token minting, and routing of agent-originated messages
// onto the existing realtime.Hub for browser consumption.
package hub

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// LocalServerID is the implicit ID for the in-process self-agent that ships
// inside any single-binary hub install.
const LocalServerID = "local"

// Server is a registered, possibly-connected agent.
type Server struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	OS        string            `json:"os,omitempty"`
	Version   string            `json:"version,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`
	LastSeen  time.Time         `json:"lastSeen"`
	Status    string            `json:"status"` // "online" | "offline"
	CreatedAt time.Time         `json:"createdAt"`
}

// Registry persists known servers in SQLite and tracks runtime status in
// memory. The local self-agent (ID=LocalServerID) is always present and
// always considered online — it can't be deleted.
type Registry struct {
	mu     sync.RWMutex
	db     *sql.DB
	cache  map[string]*Server
}

func NewRegistry(db *sql.DB) (*Registry, error) {
	r := &Registry{
		db:    db,
		cache: make(map[string]*Server),
	}
	if err := r.load(); err != nil {
		return nil, err
	}
	// Ensure the implicit local server always exists.
	if _, ok := r.cache[LocalServerID]; !ok {
		now := time.Now()
		local := &Server{
			ID:        LocalServerID,
			Name:      "Local",
			Status:    "online",
			LastSeen:  now,
			CreatedAt: now,
		}
		if err := r.upsert(local, ""); err != nil {
			return nil, fmt.Errorf("seed local: %w", err)
		}
		r.cache[LocalServerID] = local
	}
	return r, nil
}

func (r *Registry) load() error {
	rows, err := r.db.Query(`SELECT id, name, COALESCE(os, ''), COALESCE(version, ''), COALESCE(tags, ''), COALESCE(last_seen, 0), created_at FROM servers`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id, name, os, version, tagsRaw string
			lastSeenMs, createdAtMs        int64
		)
		if err := rows.Scan(&id, &name, &os, &version, &tagsRaw, &lastSeenMs, &createdAtMs); err != nil {
			return err
		}
		s := &Server{
			ID:        id,
			Name:      name,
			OS:        os,
			Version:   version,
			LastSeen:  time.UnixMilli(lastSeenMs),
			CreatedAt: time.UnixMilli(createdAtMs),
			Status:    "offline",
		}
		if id == LocalServerID {
			s.Status = "online"
		}
		if tagsRaw != "" {
			_ = json.Unmarshal([]byte(tagsRaw), &s.Tags)
		}
		r.cache[id] = s
	}
	return nil
}

// upsert writes a server row. tokenHash is bcrypt(token); pass "" to leave
// the existing token unchanged.
func (r *Registry) upsert(s *Server, tokenHash string) error {
	tagsRaw := ""
	if len(s.Tags) > 0 {
		b, _ := json.Marshal(s.Tags)
		tagsRaw = string(b)
	}
	if tokenHash != "" {
		_, err := r.db.Exec(
			`INSERT INTO servers (id, name, token_hash, os, version, tags, last_seen, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET
			   name=excluded.name,
			   token_hash=excluded.token_hash,
			   os=excluded.os,
			   version=excluded.version,
			   tags=excluded.tags,
			   last_seen=excluded.last_seen`,
			s.ID, s.Name, tokenHash, s.OS, s.Version, tagsRaw,
			s.LastSeen.UnixMilli(), s.CreatedAt.UnixMilli(),
		)
		return err
	}
	_, err := r.db.Exec(
		`INSERT INTO servers (id, name, os, version, tags, last_seen, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   name=excluded.name,
		   os=excluded.os,
		   version=excluded.version,
		   tags=excluded.tags,
		   last_seen=excluded.last_seen`,
		s.ID, s.Name, s.OS, s.Version, tagsRaw,
		s.LastSeen.UnixMilli(), s.CreatedAt.UnixMilli(),
	)
	return err
}

// Mint creates a new server entry and returns the plaintext token. The token
// is shown to the operator exactly once — only its bcrypt hash is persisted.
func (r *Registry) Mint(name string, tags map[string]string) (server *Server, plaintextToken string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	token, err := generateToken()
	if err != nil {
		return nil, "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(token), bcrypt.DefaultCost)
	if err != nil {
		return nil, "", err
	}

	now := time.Now()
	s := &Server{
		ID:        uuid.NewString(),
		Name:      name,
		Tags:      tags,
		Status:    "offline",
		CreatedAt: now,
		LastSeen:  now,
	}
	if err := r.upsert(s, string(hash)); err != nil {
		return nil, "", err
	}
	r.cache[s.ID] = s
	return s, token, nil
}

// Authenticate verifies a token belongs to the given server ID. Constant-time
// via bcrypt. The local server cannot be authenticated remotely.
func (r *Registry) Authenticate(serverID, token string) (*Server, error) {
	if serverID == LocalServerID {
		return nil, errors.New("local server is in-process; remote auth not allowed")
	}
	r.mu.RLock()
	s, ok := r.cache[serverID]
	r.mu.RUnlock()
	if !ok {
		return nil, errors.New("unknown server")
	}

	var hash string
	if err := r.db.QueryRow(`SELECT token_hash FROM servers WHERE id = ?`, serverID).Scan(&hash); err != nil {
		return nil, err
	}
	if hash == "" {
		return nil, errors.New("server has no token")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(token)); err != nil {
		return nil, errors.New("invalid token")
	}
	return s, nil
}

// Delete removes a server. The local server is protected — attempts to
// delete it are rejected.
func (r *Registry) Delete(serverID string) error {
	if serverID == LocalServerID {
		return errors.New("cannot delete the local server")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.cache[serverID]; !ok {
		return errors.New("unknown server")
	}
	if _, err := r.db.Exec(`DELETE FROM servers WHERE id = ?`, serverID); err != nil {
		return err
	}
	delete(r.cache, serverID)
	return nil
}

func (r *Registry) List() []*Server {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Server, 0, len(r.cache))
	for _, s := range r.cache {
		// Return a copy so callers can't mutate cache state.
		copy := *s
		out = append(out, &copy)
	}
	return out
}

func (r *Registry) Get(id string) (*Server, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.cache[id]
	if !ok {
		return nil, false
	}
	copy := *s
	return &copy, true
}

// MarkOnline updates last-seen + status when an agent connects or heartbeats.
// Cheap: hits the DB so a hub restart doesn't lose the timestamp.
func (r *Registry) MarkOnline(serverID, os, version string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.cache[serverID]
	if !ok {
		return
	}
	s.Status = "online"
	s.LastSeen = time.Now()
	if os != "" {
		s.OS = os
	}
	if version != "" {
		s.Version = version
	}
	_ = r.upsert(s, "")
}

// MarkOffline flips the in-memory status. The DB row keeps last_seen so the
// "offline since X" UI still works after a hub restart.
func (r *Registry) MarkOffline(serverID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.cache[serverID]; ok {
		s.Status = "offline"
	}
}

func generateToken() (string, error) {
	// 32 bytes → 64 hex chars. Prefix "aw_" so leaked tokens are grep-able
	// in logs and code search.
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "aw_" + hex.EncodeToString(buf), nil
}
