package auth

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var (
	ErrUserNotFound       = errors.New("user not found")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUserExists         = errors.New("user already exists")
)

// User represents a user account.
//
// v2 RBAC: every user has a Role. Existing users loaded from disk that
// predate this field default to RoleAdmin so single-user installs keep
// working (see UserStore.load — empty Role is auto-promoted).
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"passwordHash"`
	TOTPSecret   string    `json:"totpSecret,omitempty"`
	TOTPEnabled  bool      `json:"totpEnabled"`
	Role         string    `json:"role,omitempty"`           // "admin" | "operator" | "viewer"
	AllowedServers []string `json:"allowedServers,omitempty"` // empty = all
	CreatedAt    time.Time `json:"createdAt"`
	LastLogin    time.Time `json:"lastLogin,omitempty"`
}

// Role constants used in JWT claims + middleware checks.
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"
)

// CanOpenTerminal returns true for users allowed to open a PTY against
// the given server. Admins always allowed; operators allowed unless the
// server falls outside their allow-list; viewers never allowed.
func (u *User) CanOpenTerminal(serverID string) bool {
	if u == nil { return false }
	if u.Role == RoleViewer { return false }
	if u.Role == RoleAdmin { return true }
	if u.Role == RoleOperator {
		if len(u.AllowedServers) == 0 { return true }
		for _, id := range u.AllowedServers {
			if id == serverID { return true }
		}
		return false
	}
	// Unset role (e.g. legacy user just loaded) — treat as admin so
	// existing single-user setups don't lock themselves out.
	return u.Role == ""
}

// UserStore manages user persistence
type UserStore struct {
	filePath string
	users    map[string]*User
	mu       sync.RWMutex
}

// NewUserStore creates a new user store
func NewUserStore(filePath string) (*UserStore, error) {
	store := &UserStore{
		filePath: filePath,
		users:    make(map[string]*User),
	}

	// Load existing users if file exists
	if _, err := os.Stat(filePath); err == nil {
		if err := store.load(); err != nil {
			return nil, err
		}
	}

	return store, nil
}

// CreateUser creates a new user with hashed password
func (s *UserStore) CreateUser(username, password string) (*User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if user already exists
	for _, u := range s.users {
		if u.Username == username {
			return nil, ErrUserExists
		}
	}

	// Hash password
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	// The first user created is always an admin; everyone after defaults
	// to operator so a fresh install lands on a sane permissions baseline
	// without forcing the operator into JSON edits.
	role := RoleOperator
	if len(s.users) == 0 {
		role = RoleAdmin
	}
	user := &User{
		ID:           generateID(),
		Username:     username,
		PasswordHash: string(hash),
		Role:         role,
		CreatedAt:    time.Now(),
	}

	s.users[user.ID] = user

	if err := s.save(); err != nil {
		delete(s.users, user.ID)
		return nil, err
	}

	return user, nil
}

// GetUser retrieves a user by ID
func (s *UserStore) GetUser(id string) (*User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	user, ok := s.users[id]
	if !ok {
		return nil, ErrUserNotFound
	}

	return user, nil
}

// GetUserByUsername retrieves a user by username
func (s *UserStore) GetUserByUsername(username string) (*User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, u := range s.users {
		if u.Username == username {
			return u, nil
		}
	}

	return nil, ErrUserNotFound
}

// ValidatePassword checks if the password is correct for the user
func (s *UserStore) ValidatePassword(username, password string) (*User, error) {
	user, err := s.GetUserByUsername(username)
	if err != nil {
		return nil, err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}

	return user, nil
}

// UpdateUser updates user information
func (s *UserStore) UpdateUser(user *User) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.users[user.ID]; !ok {
		return ErrUserNotFound
	}

	s.users[user.ID] = user
	return s.save()
}

// UpdateLastLogin updates the last login timestamp
func (s *UserStore) UpdateLastLogin(userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, ok := s.users[userID]
	if !ok {
		return ErrUserNotFound
	}

	user.LastLogin = time.Now()
	return s.save()
}

// HasUsers checks if any users exist
func (s *UserStore) HasUsers() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.users) > 0
}

// load reads users from file
func (s *UserStore) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}

	var users []*User
	if err := json.Unmarshal(data, &users); err != nil {
		return err
	}

	for _, u := range users {
		// Backfill: pre-v2 users have no Role. Treat them as admin so an
		// in-place upgrade doesn't strip permissions from existing accounts.
		if u.Role == "" {
			u.Role = RoleAdmin
		}
		s.users[u.ID] = u
	}

	return nil
}

// save writes users to file
func (s *UserStore) save() error {
	users := make([]*User, 0, len(s.users))
	for _, u := range s.users {
		users = append(users, u)
	}

	data, err := json.MarshalIndent(users, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(s.filePath, data, 0600)
}

// generateID generates a secure unique ID
func generateID() string {
	return time.Now().Format("20060102150405") + "-" + randomString(8)
}

func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Fallback to less secure if crypto/rand fails (unlikely)
		return time.Now().String()
	}
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b)
}
