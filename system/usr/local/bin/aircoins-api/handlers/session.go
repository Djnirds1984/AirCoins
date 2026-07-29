package handlers

import (
	"aircoins-api/models"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
)

type SessionHandler struct {
	DB *sql.DB
}

// GetCurrent returns the active session for a client IP
func (h *SessionHandler) GetCurrent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	clientIP := r.URL.Query().Get("ip")
	if clientIP == "" {
		clientIP = getClientIP(r)
	}

	var session models.Session
	err := h.DB.QueryRow(`
		SELECT id, client_ip, client_mac, coins_inserted, total_seconds, 
		       remaining_seconds, status, started_at, activated_at, expired_at
		FROM sessions
		WHERE client_ip = $1 AND status = 'active' AND remaining_seconds > 0
		ORDER BY started_at DESC
		LIMIT 1
	`, clientIP).Scan(
		&session.ID, &session.ClientIP, &session.ClientMAC, &session.CoinsInserted,
		&session.TotalSeconds, &session.RemainingSeconds, &session.Status,
		&session.StartedAt, &session.ActivatedAt, &session.ExpiredAt,
	)

	if err == sql.ErrNoRows {
		sendJSON(w, http.StatusOK, models.SessionStatusResponse{HasActive: false})
		return
	} else if err != nil {
		log.Printf("Error fetching session: %v", err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to fetch session"})
		return
	}

	sendJSON(w, http.StatusOK, models.SessionStatusResponse{
		HasActive:        true,
		Session:          &session,
		RemainingSeconds: session.RemainingSeconds,
		CoinsInserted:    session.CoinsInserted,
	})
}

// Start creates a new session or adds time to existing one
func (h *SessionHandler) Start(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ClientIP  string `json:"client_ip"`
		ClientMAC string `json:"client_mac"`
		Seconds   int    `json:"seconds"`
		Coins     int    `json:"coins"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid request body"})
		return
	}

	if req.ClientIP == "" {
		req.ClientIP = getClientIP(r)
	}

	// Check for existing active session
	var existingID int
	err := h.DB.QueryRow(`
		SELECT id FROM sessions 
		WHERE client_ip = $1 AND status = 'active'
	`, req.ClientIP).Scan(&existingID)

	if err == nil {
		// Extend existing session
		_, err = h.DB.Exec(`
			UPDATE sessions 
			SET coins_inserted = coins_inserted + $1,
			    total_seconds = total_seconds + $2,
			    remaining_seconds = remaining_seconds + $2
			WHERE id = $3
		`, req.Coins, req.Seconds, existingID)
		if err != nil {
			log.Printf("Error extending session: %v", err)
			sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to extend session"})
			return
		}
		logAction(h.DB, "INFO", "session", "Session extended for "+req.ClientIP)
		sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Session extended", Data: map[string]int{"session_id": existingID}})
		return
	}

	// Create new session
	var newID int
	err = h.DB.QueryRow(`
		INSERT INTO sessions (client_ip, client_mac, coins_inserted, total_seconds, remaining_seconds, status, started_at, activated_at)
		VALUES ($1, $2, $3, $4, $4, 'active', NOW(), NOW())
		RETURNING id
	`, req.ClientIP, req.ClientMAC, req.Coins, req.Seconds).Scan(&newID)

	if err != nil {
		log.Printf("Error creating session: %v", err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to create session"})
		return
	}

	logAction(h.DB, "INFO", "session", "New session created for "+req.ClientIP)
	sendJSON(w, http.StatusCreated, models.APIResponse{
		Success: true,
		Message: "Session created",
		Data:    map[string]int{"session_id": newID},
	})
}

// Extend adds time to an existing session
func (h *SessionHandler) Extend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		SessionID int `json:"session_id"`
		Seconds   int `json:"seconds"`
		Coins     int `json:"coins"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid request body"})
		return
	}

	result, err := h.DB.Exec(`
		UPDATE sessions 
		SET coins_inserted = coins_inserted + $1,
		    total_seconds = total_seconds + $2,
		    remaining_seconds = remaining_seconds + $2
		WHERE id = $3 AND status = 'active'
	`, req.Coins, req.Seconds, req.SessionID)

	if err != nil {
		log.Printf("Error extending session: %v", err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to extend session"})
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		sendJSON(w, http.StatusNotFound, models.APIResponse{Success: false, Message: "Session not found or not active"})
		return
	}

	logAction(h.DB, "INFO", "session", "Session extended")
	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Session extended"})
}

// End terminates a session
func (h *SessionHandler) End(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		SessionID int    `json:"session_id"`
		Reason    string `json:"reason"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid request body"})
		return
	}

	status := "expired"
	if req.Reason == "cancelled" {
		status = "cancelled"
	}

	result, err := h.DB.Exec(`
		UPDATE sessions 
		SET status = $1, expired_at = NOW(), remaining_seconds = 0
		WHERE id = $2
	`, status, req.SessionID)

	if err != nil {
		log.Printf("Error ending session: %v", err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to end session"})
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		sendJSON(w, http.StatusNotFound, models.APIResponse{Success: false, Message: "Session not found"})
		return
	}

	logAction(h.DB, "INFO", "session", "Session ended: "+status)
	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Session ended"})
}

// Status returns overall session status for frontend polling
func (h *SessionHandler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	clientIP := r.URL.Query().Get("ip")
	if clientIP == "" {
		clientIP = getClientIP(r)
	}

	// Get active session
	var session models.Session
	err := h.DB.QueryRow(`
		SELECT id, client_ip, coins_inserted, total_seconds, remaining_seconds, status, started_at
		FROM sessions
		WHERE client_ip = $1 AND status = 'active' AND remaining_seconds > 0
		ORDER BY started_at DESC
		LIMIT 1
	`, clientIP).Scan(
		&session.ID, &session.ClientIP, &session.CoinsInserted,
		&session.TotalSeconds, &session.RemainingSeconds, &session.Status, &session.StartedAt,
	)

	response := map[string]interface{}{
		"timestamp":   makeTimestamp(),
		"has_session": err == nil,
	}

	if err == nil {
		response["session"] = map[string]interface{}{
			"id":                session.ID,
			"status":            session.Status,
			"coins":             session.CoinsInserted,
			"total":             session.TotalSeconds,
			"remaining":         session.RemainingSeconds,
			"started":           session.StartedAt.Unix(),
		}
	}

	// Get system status
	response["system"] = map[string]interface{}{
		"online": checkLighttpdRunning(),
		"ip":     clientIP,
	}

	sendJSON(w, http.StatusOK, response)
}

// TickSession decrements remaining seconds (called by session manager)
func (h *SessionHandler) TickSession(sessionID int) error {
	_, err := h.DB.Exec(`
		UPDATE sessions 
		SET remaining_seconds = GREATEST(remaining_seconds - 1, 0),
		    status = CASE WHEN remaining_seconds <= 1 THEN 'expired' ELSE status END,
		    expired_at = CASE WHEN remaining_seconds <= 1 THEN NOW() ELSE expired_at END
		WHERE id = $1 AND status = 'active' AND remaining_seconds > 0
	`, sessionID)
	return err
}

// Helper to get client IP from request
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header (from proxy)
	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded != "" {
		return forwarded
	}

	// Check X-Real-IP header
	realIP := r.Header.Get("X-Real-IP")
	if realIP != "" {
		return realIP
	}

	// Fall back to RemoteAddr
	return r.RemoteAddr
}

// Helper to log actions
func logAction(db *sql.DB, level, component, message string) {
	_, err := db.Exec(`
		INSERT INTO system_logs (level, component, message, created_at)
		VALUES ($1, $2, $3, NOW())
	`, level, component, message)
	if err != nil {
		log.Printf("Failed to log action: %v", err)
	}
}

// Helper to make timestamp
func makeTimestamp() int64 {
	return makeTimestampFunc()
}

var makeTimestampFunc = func() int64 {
	return int64(0) // Will be replaced with time.Now().Unix()
}

func init() {
	makeTimestampFunc = func() int64 {
		return int64(0)
	}
}
