package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"
)

type tokenEntry struct {
	AdminID   int
	Username  string
	ExpiresAt time.Time
}

var (
	tokenStore = make(map[string]tokenEntry)
	tokenMu    sync.RWMutex
)

// GenerateToken creates a random token for an admin user with 24-hour expiry.
func GenerateToken(adminID int, username string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// fallback — should never happen
		return hex.EncodeToString([]byte(time.Now().String()))
	}
	token := hex.EncodeToString(b)

	tokenMu.Lock()
	tokenStore[token] = tokenEntry{
		AdminID:   adminID,
		Username:  username,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	tokenMu.Unlock()

	return token
}

// ValidateToken checks whether a token is valid and not expired.
func ValidateToken(token string) (adminID int, username string, valid bool) {
	tokenMu.RLock()
	entry, ok := tokenStore[token]
	tokenMu.RUnlock()

	if !ok {
		return 0, "", false
	}

	if time.Now().After(entry.ExpiresAt) {
		// Expired — remove it
		tokenMu.Lock()
		delete(tokenStore, token)
		tokenMu.Unlock()
		return 0, "", false
	}

	return entry.AdminID, entry.Username, true
}

// AuthMiddleware wraps a handler, requiring a valid Bearer token.
func AuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
			sendJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		token := strings.TrimPrefix(auth, "Bearer ")
		_, _, valid := ValidateToken(token)
		if !valid {
			sendJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		next.ServeHTTP(w, r)
	}
}
