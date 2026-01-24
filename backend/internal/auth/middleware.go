package auth

import (
	"context"
	"log"
	"net/http"
	"strings"
)

type contextKey string

const (
	userContextKey contextKey = "user"
)

// Middleware creates an authentication middleware
func Middleware(jwtManager *JWTManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract token from Authorization header or cookie
			token := extractToken(r)
			if token == "" {
				// Only log if it's not a public asset request (reduce noise)
				if r.URL.Path == "/ws" || strings.HasPrefix(r.URL.Path, "/api/") {
					// log.Printf("Debug: Auth Middleware - No token found for %s", r.URL.Path)
				}
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			// Validate token
			claims, err := jwtManager.ValidateToken(token)
			if err != nil {
				log.Printf("Debug: Auth Middleware - Invalid token for %s: %v", r.URL.Path, err)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			// Add claims to context
			ctx := context.WithValue(r.Context(), userContextKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractToken extracts JWT token from request
func extractToken(r *http.Request) string {
	// Try Authorization header first
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		parts := strings.Split(authHeader, " ")
		if len(parts) == 2 && parts[0] == "Bearer" {
			return parts[1]
		}
	}

	// Try cookie
	cookie, err := r.Cookie("auth_token")
	if err == nil {
		return cookie.Value
	}

	// Try query parameter (for WebSocket)
	queryToken := r.URL.Query().Get("token")
	if queryToken != "" {
		return queryToken
	}

	return ""
}

// GetUserFromContext retrieves user claims from request context
func GetUserFromContext(r *http.Request) (*Claims, bool) {
	claims, ok := r.Context().Value(userContextKey).(*Claims)
	return claims, ok
}
