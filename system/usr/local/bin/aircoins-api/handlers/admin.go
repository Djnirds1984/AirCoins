package handlers

import (
	"aircoins-api/models"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type AdminHandler struct {
	DB *sql.DB
}

// Login handles admin authentication
func (h *AdminHandler) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req models.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid request body"})
		return
	}

	// Get user from database
	var user models.AdminUser
	err := h.DB.QueryRow(
		"SELECT id, username, password_hash FROM admin_users WHERE username = $1",
		req.Username,
	).Scan(&user.ID, &user.Username, &user.PasswordHash)

	if err == sql.ErrNoRows {
		sendJSON(w, http.StatusUnauthorized, models.LoginResponse{Success: false, Message: "Invalid credentials"})
		return
	} else if err != nil {
		log.Printf("Database error: %v", err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Internal error"})
		return
	}

	// Compare password with hash
	err = bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password))
	if err != nil {
		sendJSON(w, http.StatusUnauthorized, models.LoginResponse{Success: false, Message: "Invalid credentials"})
		return
	}

	// Update last login
	_, err = h.DB.Exec("UPDATE admin_users SET last_login = NOW() WHERE id = $1", user.ID)
	if err != nil {
		log.Printf("Failed to update last login: %v", err)
	}

	// Log the login
	h.logAction("INFO", "admin", "Admin login successful: "+req.Username)

	// Return success (in production, generate JWT token here)
	sendJSON(w, http.StatusOK, models.LoginResponse{
		Success: true,
		Message: "Login successful",
		Token:   "admin_session_" + req.Username, // Simple token for now
	})
}

// GetStats returns dashboard statistics
func (h *AdminHandler) GetStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var stats models.StatsResponse

	// Get today's stats
	err := h.DB.QueryRow(`
		SELECT COALESCE(total_earnings, 0), COALESCE(total_coins, 0), COALESCE(total_sessions, 0)
		FROM daily_stats WHERE date = CURRENT_DATE
	`).Scan(&stats.TodayEarnings, &stats.TodayCoins, &stats.TodaySessions)
	if err == sql.ErrNoRows {
		stats.TodayEarnings = 0
		stats.TodayCoins = 0
		stats.TodaySessions = 0
	} else if err != nil {
		log.Printf("Error fetching today stats: %v", err)
	}

	// Get total stats (all time)
	err = h.DB.QueryRow(`
		SELECT COALESCE(SUM(total_earnings), 0), COALESCE(SUM(total_coins), 0), COALESCE(SUM(total_sessions), 0)
		FROM daily_stats
	`).Scan(&stats.TotalEarnings, &stats.TotalCoins, &stats.TotalSessions)
	if err != nil {
		log.Printf("Error fetching total stats: %v", err)
	}

	// Get active sessions count
	err = h.DB.QueryRow(`
		SELECT COUNT(*) FROM sessions WHERE status = 'active' AND remaining_seconds > 0
	`).Scan(&stats.ActiveSessions)
	if err != nil {
		log.Printf("Error fetching active sessions: %v", err)
	}

	// Check if system is online (hostapd running)
	stats.SystemOnline = checkHostapdRunning()

	sendJSON(w, http.StatusOK, stats)
}

// GetSessions returns session history
func (h *AdminHandler) GetSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rows, err := h.DB.Query(`
		SELECT id, client_ip, client_mac, coins_inserted, total_seconds, remaining_seconds, 
		       status, started_at, activated_at, expired_at
		FROM sessions 
		ORDER BY started_at DESC 
		LIMIT 100
	`)
	if err != nil {
		log.Printf("Error fetching sessions: %v", err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to fetch sessions"})
		return
	}
	defer rows.Close()

	var sessions []models.Session
	for rows.Next() {
		var s models.Session
		err := rows.Scan(
			&s.ID, &s.ClientIP, &s.ClientMAC, &s.CoinsInserted, &s.TotalSeconds,
			&s.RemainingSeconds, &s.Status, &s.StartedAt, &s.ActivatedAt, &s.ExpiredAt,
		)
		if err != nil {
			log.Printf("Error scanning session: %v", err)
			continue
		}
		sessions = append(sessions, s)
	}

	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: sessions})
}

// Settings handles system settings (GET and POST)
func (h *AdminHandler) Settings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.getSettings(w, r)
	case http.MethodPost:
		h.updateSettings(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *AdminHandler) getSettings(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.Query("SELECT key, value FROM system_settings")
	if err != nil {
		log.Printf("Error fetching settings: %v", err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to fetch settings"})
		return
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			log.Printf("Error scanning setting: %v", err)
			continue
		}
		settings[key] = value
	}

	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: settings})
}

func (h *AdminHandler) updateSettings(w http.ResponseWriter, r *http.Request) {
	var req models.SettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid request body"})
		return
	}

	for key, value := range req.Settings {
		_, err := h.DB.Exec(`
			INSERT INTO system_settings (key, value, updated_at) 
			VALUES ($1, $2, NOW())
			ON CONFLICT (key) DO UPDATE SET value = $2, updated_at = NOW()
		`, key, value)
		if err != nil {
			log.Printf("Error updating setting %s: %v", key, err)
		}
	}

	h.logAction("INFO", "admin", "System settings updated")
	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Settings updated"})
}

// GetLogs returns system logs
func (h *AdminHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rows, err := h.DB.Query(`
		SELECT id, level, component, message, created_at
		FROM system_logs
		ORDER BY created_at DESC
		LIMIT 100
	`)
	if err != nil {
		log.Printf("Error fetching logs: %v", err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to fetch logs"})
		return
	}
	defer rows.Close()

	var logs []models.SystemLog
	for rows.Next() {
		var l models.SystemLog
		if err := rows.Scan(&l.ID, &l.Level, &l.Component, &l.Message, &l.CreatedAt); err != nil {
			log.Printf("Error scanning log: %v", err)
			continue
		}
		logs = append(logs, l)
	}

	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: logs})
}

// Helper function to log actions
func (h *AdminHandler) logAction(level, component, message string) {
	_, err := h.DB.Exec(`
		INSERT INTO system_logs (level, component, message, created_at)
		VALUES ($1, $2, $3, NOW())
	`, level, component, message)
	if err != nil {
		log.Printf("Failed to log action: %v", err)
	}
}

// Helper to check if hostapd is running
func checkHostapdRunning() bool {
	// This would normally check if hostapd process is running
	// For now, return true (implement with os/exec if needed)
	return true
}

// Helper to send JSON response
func sendJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// GeneratePasswordHash creates a bcrypt hash for a password
func GeneratePasswordHash(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(bytes), err
}

func init() {
	// This can be used to generate password hashes for new admin users
	_ = GeneratePasswordHash
	_ = time.Now
}
