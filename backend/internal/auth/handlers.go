package auth

import (
	"encoding/json"
	"net/http"
	"time"
)

// Manager coordinates all auth operations
type Manager struct {
	userStore   *UserStore
	jwtManager  *JWTManager
	totpManager *TOTPManager
}

// NewManager creates a new auth manager
func NewManager(usersFile, jwtSecret string, tokenExpiration int) (*Manager, error) {
	userStore, err := NewUserStore(usersFile)
	if err != nil {
		return nil, err
	}

	return &Manager{
		userStore:   userStore,
		jwtManager:  NewJWTManager(jwtSecret, tokenExpiration),
		totpManager: NewTOTPManager("ArgonWatchGo"),
	}, nil
}

// GetUserStore returns the user store
func (m *Manager) GetUserStore() *UserStore {
	return m.userStore
}

// GetJWTManager returns the JWT manager
func (m *Manager) GetJWTManager() *JWTManager {
	return m.jwtManager
}

// GetTOTPManager returns the TOTP manager
func (m *Manager) GetTOTPManager() *TOTPManager {
	return m.totpManager
}

// Request/Response types
type SetupRequest struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	Enable2FA bool   `json:"enable2fa"`
}

type SetupResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	QRCode  string `json:"qrCode,omitempty"`
	Secret  string `json:"secret,omitempty"`
	Token   string `json:"token,omitempty"`
}

type LoginRequest struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	TOTPToken string `json:"totpToken,omitempty"`
}

type LoginResponse struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	Token       string `json:"token,omitempty"`
	Requires2FA bool   `json:"requires2fa"`
	TempToken   string `json:"tempToken,omitempty"`
}

type Enable2FAResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	QRCode  string `json:"qrCode,omitempty"`
	Secret  string `json:"secret,omitempty"`
}

type Verify2FARequest struct {
	TempToken string `json:"tempToken"`
	TOTPToken string `json:"totpToken"`
}

type UserResponse struct {
	ID          string    `json:"id"`
	Username    string    `json:"username"`
	TOTPEnabled bool      `json:"totpEnabled"`
	CreatedAt   time.Time `json:"createdAt"`
	LastLogin   time.Time `json:"lastLogin"`
}

// Handlers

// HandleCheckSetup checks if initial setup is required
func (m *Manager) HandleCheckSetup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{
		"setupRequired": !m.userStore.HasUsers(),
	})
}

// HandleSetup handles initial admin user creation
func (m *Manager) HandleSetup(w http.ResponseWriter, r *http.Request) {
	// Only allow setup if no users exist
	if m.userStore.HasUsers() {
		http.Error(w, "Setup already completed", http.StatusForbidden)
		return
	}

	var req SetupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// Validate input
	if req.Username == "" || req.Password == "" {
		http.Error(w, "Username and password required", http.StatusBadRequest)
		return
	}

	// Create user
	user, err := m.userStore.CreateUser(req.Username, req.Password)
	if err != nil {
		http.Error(w, "Failed to create user", http.StatusInternalServerError)
		return
	}

	resp := SetupResponse{
		Success: true,
		Message: "Setup completed successfully",
	}

	// Setup 2FA if requested
	if req.Enable2FA {
		key, err := m.totpManager.GenerateSecret(user.Username)
		if err != nil {
			http.Error(w, "Failed to generate 2FA secret", http.StatusInternalServerError)
			return
		}

		qrCode, err := m.totpManager.GenerateQRCode(key)
		if err != nil {
			http.Error(w, "Failed to generate QR code", http.StatusInternalServerError)
			return
		}

		user.TOTPSecret = key.Secret()
		user.TOTPEnabled = true
		if err := m.userStore.UpdateUser(user); err != nil {
			http.Error(w, "Failed to update user", http.StatusInternalServerError)
			return
		}

		resp.QRCode = qrCode
		resp.Secret = key.Secret()
	}

	// Generate token
	token, err := m.jwtManager.GenerateTokenWithRole(user.ID, user.Username, user.Role)
	if err != nil {
		http.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	resp.Token = token

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// HandleLogin handles user login
func (m *Manager) HandleLogin(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// Validate credentials
	user, err := m.userStore.ValidatePassword(req.Username, req.Password)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(LoginResponse{
			Success: false,
			Message: "Invalid credentials",
		})
		return
	}

	// Check if 2FA is enabled
	if user.TOTPEnabled {
		// If TOTP token provided, validate it
		if req.TOTPToken != "" {
			if !m.totpManager.ValidateToken(user.TOTPSecret, req.TOTPToken) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(LoginResponse{
					Success: false,
					Message: "Invalid 2FA token",
				})
				return
			}
		} else {
			// Return temp token for 2FA verification
			tempToken, err := m.jwtManager.GenerateTokenWithRole(user.ID, user.Username, user.Role)
			if err != nil {
				http.Error(w, "Failed to generate token", http.StatusInternalServerError)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(LoginResponse{
				Success:     true,
				Requires2FA: true,
				TempToken:   tempToken,
				Message:     "2FA required",
			})
			return
		}
	}

	// Update last login
	m.userStore.UpdateLastLogin(user.ID)

	// Generate token
	token, err := m.jwtManager.GenerateTokenWithRole(user.ID, user.Username, user.Role)
	if err != nil {
		http.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(LoginResponse{
		Success: true,
		Token:   token,
		Message: "Login successful",
	})
}

// HandleVerify2FA verifies 2FA token
func (m *Manager) HandleVerify2FA(w http.ResponseWriter, r *http.Request) {
	var req Verify2FARequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// Validate temp token
	claims, err := m.jwtManager.ValidateToken(req.TempToken)
	if err != nil {
		http.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	// Get user
	user, err := m.userStore.GetUser(claims.UserID)
	if err != nil {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	// Validate TOTP
	if !m.totpManager.ValidateToken(user.TOTPSecret, req.TOTPToken) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(LoginResponse{
			Success: false,
			Message: "Invalid 2FA token",
		})
		return
	}

	// Update last login
	m.userStore.UpdateLastLogin(user.ID)

	// Generate new token
	token, err := m.jwtManager.GenerateTokenWithRole(user.ID, user.Username, user.Role)
	if err != nil {
		http.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(LoginResponse{
		Success: true,
		Token:   token,
		Message: "Login successful",
	})
}

// HandleGetMe returns current user info
func (m *Manager) HandleGetMe(w http.ResponseWriter, r *http.Request) {
	claims, ok := GetUserFromContext(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	user, err := m.userStore.GetUser(claims.UserID)
	if err != nil {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(UserResponse{
		ID:          user.ID,
		Username:    user.Username,
		TOTPEnabled: user.TOTPEnabled,
		CreatedAt:   user.CreatedAt,
		LastLogin:   user.LastLogin,
	})
}

// HandleEnable2FA enables 2FA for current user
func (m *Manager) HandleEnable2FA(w http.ResponseWriter, r *http.Request) {
	claims, ok := GetUserFromContext(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	user, err := m.userStore.GetUser(claims.UserID)
	if err != nil {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	// Generate TOTP secret
	key, err := m.totpManager.GenerateSecret(user.Username)
	if err != nil {
		http.Error(w, "Failed to generate secret", http.StatusInternalServerError)
		return
	}

	// Generate QR code
	qrCode, err := m.totpManager.GenerateQRCode(key)
	if err != nil {
		http.Error(w, "Failed to generate QR code", http.StatusInternalServerError)
		return
	}

	// Update user
	user.TOTPSecret = key.Secret()
	user.TOTPEnabled = true
	if err := m.userStore.UpdateUser(user); err != nil {
		http.Error(w, "Failed to update user", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(Enable2FAResponse{
		Success: true,
		Message: "2FA enabled successfully",
		QRCode:  qrCode,
		Secret:  key.Secret(),
	})
}

// HandleDisable2FA disables 2FA for current user
func (m *Manager) HandleDisable2FA(w http.ResponseWriter, r *http.Request) {
	claims, ok := GetUserFromContext(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	user, err := m.userStore.GetUser(claims.UserID)
	if err != nil {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	// Disable 2FA
	user.TOTPSecret = ""
	user.TOTPEnabled = false
	if err := m.userStore.UpdateUser(user); err != nil {
		http.Error(w, "Failed to update user", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "2FA disabled successfully",
	})
}

// HandleLogout handles user logout
func (m *Manager) HandleLogout(w http.ResponseWriter, r *http.Request) {
	// In a stateless JWT system, logout is handled client-side by removing the token
	// We just return success
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Logged out successfully",
	})
}
