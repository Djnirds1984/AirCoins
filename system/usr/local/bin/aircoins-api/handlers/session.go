package handlers

import (
	"aircoins-api/models"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/lib/pq"
)

type SessionHandler struct {
	DB *sql.DB
}

// Sessions are wall-clock based: a session is alive while
// expires_at > NOW() (DB clock). remaining_seconds is kept as a snapshot
// for display/legacy rows only — every response computes the remaining
// time from expires_at inside SQL so no timezone assumptions leak out.

// remainingSQL computes the live remaining seconds of a session row.
const remainingSQL = `GREATEST(0, EXTRACT(EPOCH FROM (expires_at - NOW())))::int`

// ============================================
// COIN WINDOW (unprocessed coin_events)
// ============================================

// windowCoins is the credit accumulated in the current armed window.
type windowCoins struct {
	ids          []int64
	count        int
	totalValue   int
	totalMinutes int
}

// unprocessedWindowCoins sums the UNPROCESSED coin_events of the current
// armed window — the same window logic as GET /api/coinslot/status
// (age-based, so DB/API timezone disagreements cannot break it). If the
// armed markers are already gone (portal closed the modal first, API
// restarted mid-window) it falls back to the widest possible window so a
// paying customer is never robbed of freshly inserted coins: the GPIO
// listener only records pulses while armed, so recent unprocessed rows
// can only belong to the customer at the coin slot.
func (h *SessionHandler) unprocessedWindowCoins() windowCoins {
	coins := windowCoins{}

	armed, expiresAt := readArmedState()
	armedAt := readArmedAt(expiresAt)

	windowAge := int64(maxArmDuration)
	if armed && armedAt > 0 {
		windowAge = time.Now().Unix() - armedAt + 1
		if windowAge < 1 {
			windowAge = 1
		}
	}

	rows, err := h.DB.Query(`
		SELECT id, coin_value
		FROM coin_events
		WHERE processed = false
		  AND detected_at >= NOW() - ($1::int * INTERVAL '1 second')
		ORDER BY id ASC
		LIMIT $2
	`, windowAge, maxWindowCoins)
	if err != nil {
		log.Printf("Error fetching unprocessed coin events: %v", err)
		return coins
	}
	defer rows.Close()

	minutesByValue := make(map[int]int)
	for rows.Next() {
		var id int64
		var value int
		if err := rows.Scan(&id, &value); err != nil {
			log.Printf("Error scanning coin event: %v", err)
			continue
		}

		minutes, cached := minutesByValue[value]
		if !cached {
			resolved, _, perr := MinutesForAmount(h.DB, value)
			if perr != nil {
				log.Printf("Error resolving pricing for P%d: %v", value, perr)
				resolved = 0
			}
			minutes = resolved
			minutesByValue[value] = minutes
		}

		coins.ids = append(coins.ids, id)
		coins.count++
		coins.totalValue += value
		coins.totalMinutes += minutes
	}
	if err := rows.Err(); err != nil {
		log.Printf("Error reading coin events: %v", err)
	}
	return coins
}

// ============================================
// SESSION START (public, portal "Done Paying")
// ============================================

// Start converts the coins of the current armed window into an internet
// session for the CALLING device. The credited time comes exclusively
// from unprocessed coin_events + the pricing table — the request body is
// ignored, so a client cannot grant itself time.
func (h *SessionHandler) Start(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	clientIP := clientIPFromRequest(r)
	clientMAC := resolveClientMAC(clientIP)
	if clientMAC == "" {
		log.Printf("session start: no MAC resolved for %s (continuing IP-only)", clientIP)
	}

	coins := h.unprocessedWindowCoins()
	if coins.totalMinutes == 0 {
		msg := "no credited coins"
		if coins.count > 0 {
			msg = "no credited coins: no pricing tier configured for the inserted amount"
		}
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: msg})
		return
	}

	session, extended, err := h.creditSession(clientIP, clientMAC, coins.totalValue, coins.totalMinutes, coins.ids)
	if err != nil {
		log.Printf("Error crediting session for %s: %v", clientIP, err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to create session"})
		return
	}

	reason := "start"
	verb := "started"
	if extended {
		reason = "extend"
		verb = "extended"
	}

	// Open the client's internet access. Non-fatal if the iptables layer
	// is absent (dev box) — the session row exists either way.
	runCaptiveRules(h.DB, "auth", clientMAC, reason)

	// The purchase is complete: close the armed window so the GPIO
	// listener goes back to idle.
	os.Remove(armedFile)
	os.Remove(armedAtFile)

	logAction(h.DB, "INFO", "session", "Session "+verb+" for "+clientIP+" ("+clientMAC+"): P"+
		strconv.Itoa(coins.totalValue)+" = "+strconv.Itoa(coins.totalMinutes)+" min")

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"message":  "Session " + verb,
		"extended": extended,
		"session":  session,
	})
}

// creditSession creates a new active session or extends the caller's
// existing one, and marks the credited coin_events processed — all in one
// transaction so a crash can neither double-credit nor eat coins.
func (h *SessionHandler) creditSession(clientIP, clientMAC string, coinValue, minutes int, eventIDs []int64) (map[string]interface{}, bool, error) {
	addSeconds := minutes * 60

	tx, err := h.DB.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	// Existing ACTIVE session for this device (MAC first, IP fallback)?
	var existingID int
	err = tx.QueryRow(`
		SELECT id FROM sessions
		WHERE status = 'active' AND expires_at > NOW()
		  AND (($1 <> '' AND client_mac = $1) OR client_ip = $2)
		ORDER BY started_at DESC
		LIMIT 1
	`, clientMAC, clientIP).Scan(&existingID)
	if err != nil && err != sql.ErrNoRows {
		return nil, false, err
	}
	extended := err == nil

	var (
		id, coinsTotal, totalSeconds, remaining int
		expiresAt                               time.Time
	)

	if extended {
		// EXTEND: push expires_at out by the new minutes (never create a
		// duplicate row for a device that is already online).
		err = tx.QueryRow(`
			UPDATE sessions
			SET coins_inserted = coins_inserted + $1,
			    total_seconds = total_seconds + $2,
			    expires_at = GREATEST(expires_at, NOW()) + ($2 * INTERVAL '1 second'),
			    remaining_seconds = `+remainingSQL+` + $2,
			    client_ip = $3,
			    client_mac = CASE WHEN $4 <> '' THEN $4 ELSE client_mac END
			WHERE id = $5
			RETURNING id, coins_inserted, total_seconds, `+remainingSQL+`, expires_at
		`, coinValue, addSeconds, clientIP, clientMAC, existingID).
			Scan(&id, &coinsTotal, &totalSeconds, &remaining, &expiresAt)
	} else {
		err = tx.QueryRow(`
			INSERT INTO sessions (client_ip, client_mac, coins_inserted, total_seconds,
			                      remaining_seconds, status, started_at, activated_at, expires_at)
			VALUES ($1, $2, $3, $4, $4, 'active', NOW(), NOW(), NOW() + ($4 * INTERVAL '1 second'))
			RETURNING id, coins_inserted, total_seconds, `+remainingSQL+`, expires_at
		`, clientIP, clientMAC, coinValue, addSeconds).
			Scan(&id, &coinsTotal, &totalSeconds, &remaining, &expiresAt)
	}
	if err != nil {
		return nil, false, err
	}

	// Consume the coins so a second "Done Paying" cannot credit them again.
	if len(eventIDs) > 0 {
		if _, err = tx.Exec(`
			UPDATE coin_events SET processed = true, session_id = $1
			WHERE id = ANY($2)
		`, id, pq.Array(eventIDs)); err != nil {
			return nil, false, err
		}
	}

	if err = tx.Commit(); err != nil {
		return nil, false, err
	}

	return map[string]interface{}{
		"id":         id,
		"status":     "active",
		"ip":         clientIP,
		"mac":        clientMAC,
		"coins":      coinsTotal,
		"total":      totalSeconds,
		"remaining":  remaining,
		"expires_at": expiresAt.Format(time.RFC3339),
	}, extended, nil
}

// ============================================
// SESSION STATUS (public, portal poll)
// ============================================

// Status returns the calling device's session for the portal poll.
// Response shape is what index.html apiPoll() expects:
// {has_session, session:{status,coins,total,remaining,...}, system:{online,ip}}
func (h *SessionHandler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Lazy expiry: never report a session the ticker has not caught yet.
	ExpireOverdueSessions(h.DB, "expire")

	clientIP := r.URL.Query().Get("ip")
	if clientIP == "" {
		clientIP = clientIPFromRequest(r)
	}
	// Cheap neighbour-table lookup only — this poll runs every few
	// seconds, so no ping probe here.
	clientMAC := neighborMAC(clientIP)

	var (
		id, coinsTotal, totalSeconds, remaining int
		mac                                     string
		startedAt, expiresAt                    time.Time
	)
	err := h.DB.QueryRow(`
		SELECT id, COALESCE(client_mac, ''), coins_inserted, total_seconds,
		       `+remainingSQL+`, started_at, expires_at
		FROM sessions
		WHERE status = 'active' AND expires_at > NOW()
		  AND (($1 <> '' AND client_mac = $1) OR client_ip = $2)
		ORDER BY started_at DESC
		LIMIT 1
	`, clientMAC, clientIP).Scan(&id, &mac, &coinsTotal, &totalSeconds, &remaining, &startedAt, &expiresAt)

	if err != nil && err != sql.ErrNoRows {
		log.Printf("Error fetching session status: %v", err)
	}

	response := map[string]interface{}{
		"timestamp":   makeTimestamp(),
		"has_session": err == nil,
	}

	if err == nil {
		response["session"] = map[string]interface{}{
			"id":         id,
			"status":     "active",
			"coins":      coinsTotal,
			"total":      totalSeconds,
			"remaining":  remaining,
			"started":    startedAt.Unix(),
			"mac":        mac,
			"expires_at": expiresAt.Format(time.RFC3339),
		}
	}

	response["system"] = map[string]interface{}{
		"online": checkLighttpdRunning(),
		"ip":     clientIP,
	}

	sendJSON(w, http.StatusOK, response)
}

// GetCurrent returns the active session for a client IP (legacy shape)
func (h *SessionHandler) GetCurrent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	clientIP := r.URL.Query().Get("ip")
	if clientIP == "" {
		clientIP = clientIPFromRequest(r)
	}
	clientMAC := neighborMAC(clientIP)

	var session models.Session
	err := h.DB.QueryRow(`
		SELECT id, client_ip, client_mac, coins_inserted, total_seconds,
		       `+remainingSQL+`, status, started_at, activated_at, expired_at, expires_at
		FROM sessions
		WHERE status = 'active' AND expires_at > NOW()
		  AND (($1 <> '' AND client_mac = $1) OR client_ip = $2)
		ORDER BY started_at DESC
		LIMIT 1
	`, clientMAC, clientIP).Scan(
		&session.ID, &session.ClientIP, &session.ClientMAC, &session.CoinsInserted,
		&session.TotalSeconds, &session.RemainingSeconds, &session.Status,
		&session.StartedAt, &session.ActivatedAt, &session.ExpiredAt, &session.ExpiresAt,
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

// ============================================
// ADMIN SESSION MANAGEMENT (AuthMiddleware'd in main.go)
// ============================================

// AdminCreate lets the operator grant a session manually (Sessions tab).
func (h *SessionHandler) AdminCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ClientIP string `json:"client_ip"`
		Minutes  int    `json:"minutes"`
		Coins    int    `json:"coins"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid request body"})
		return
	}
	if req.ClientIP == "" || req.Minutes <= 0 {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "client_ip and minutes are required"})
		return
	}

	clientMAC := resolveClientMAC(req.ClientIP)
	session, extended, err := h.creditSession(req.ClientIP, clientMAC, req.Coins, req.Minutes, nil)
	if err != nil {
		log.Printf("Error creating admin session for %s: %v", req.ClientIP, err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to create session"})
		return
	}

	runCaptiveRules(h.DB, "auth", clientMAC, "admin-create")
	logAction(h.DB, "INFO", "session", "Admin session for "+req.ClientIP+" ("+clientMAC+"): "+strconv.Itoa(req.Minutes)+" min")

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"message":  "Session created",
		"extended": extended,
		"session":  session,
	})
}

// Extend adds time to an existing session (admin only)
func (h *SessionHandler) Extend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		SessionID int `json:"session_id"`
		Minutes   int `json:"minutes"`
		Seconds   int `json:"seconds"` // legacy field, used when minutes is absent
		Coins     int `json:"coins"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid request body"})
		return
	}

	addSeconds := req.Minutes * 60
	if addSeconds <= 0 {
		addSeconds = req.Seconds
	}
	if addSeconds <= 0 {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "minutes must be positive"})
		return
	}

	var mac string
	err := h.DB.QueryRow(`
		UPDATE sessions
		SET coins_inserted = coins_inserted + $1,
		    total_seconds = total_seconds + $2,
		    expires_at = GREATEST(expires_at, NOW()) + ($2 * INTERVAL '1 second'),
		    remaining_seconds = `+remainingSQL+` + $2
		WHERE id = $3 AND status = 'active'
		RETURNING COALESCE(client_mac, '')
	`, req.Coins, addSeconds, req.SessionID).Scan(&mac)

	if err == sql.ErrNoRows {
		sendJSON(w, http.StatusNotFound, models.APIResponse{Success: false, Message: "Session not found or not active"})
		return
	} else if err != nil {
		log.Printf("Error extending session: %v", err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to extend session"})
		return
	}

	// Re-auth in case the client's rules were lost (idempotent).
	runCaptiveRules(h.DB, "auth", mac, "admin-extend")

	logAction(h.DB, "INFO", "session", "Session "+strconv.Itoa(req.SessionID)+" extended by admin (+"+strconv.Itoa(addSeconds/60)+" min)")
	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Session extended"})
}

// End terminates a session (admin only) and revokes internet access
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

	var mac string
	err := h.DB.QueryRow(`
		UPDATE sessions
		SET status = $1, expired_at = NOW(), expires_at = NOW(), remaining_seconds = 0
		WHERE id = $2
		RETURNING COALESCE(client_mac, '')
	`, status, req.SessionID).Scan(&mac)

	if err == sql.ErrNoRows {
		sendJSON(w, http.StatusNotFound, models.APIResponse{Success: false, Message: "Session not found"})
		return
	} else if err != nil {
		log.Printf("Error ending session: %v", err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to end session"})
		return
	}

	runCaptiveRules(h.DB, "unauth", mac, "admin-terminate")

	logAction(h.DB, "INFO", "session", "Session "+strconv.Itoa(req.SessionID)+" ended by admin: "+status)
	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Session ended"})
}

// ============================================
// EXPIRY ENFORCEMENT
// ============================================

// ExpireOverdueSessions expires every active session whose expires_at has
// passed (or that predates the expires_at column — legacy demo rows) and
// closes each client's internet access. Called by the 30s ticker, lazily
// on every /api/session/status poll, and once at startup.
func ExpireOverdueSessions(db *sql.DB, reason string) int {
	rows, err := db.Query(`
		UPDATE sessions
		SET status = 'expired', expired_at = NOW(), remaining_seconds = 0
		WHERE status = 'active' AND (expires_at <= NOW() OR expires_at IS NULL)
		RETURNING id, COALESCE(client_mac, ''), COALESCE(client_ip, '')
	`)
	if err != nil {
		log.Printf("Error expiring sessions: %v", err)
		return 0
	}
	defer rows.Close()

	expired := 0
	for rows.Next() {
		var id int
		var mac, ip string
		if err := rows.Scan(&id, &mac, &ip); err != nil {
			log.Printf("Error scanning expired session: %v", err)
			continue
		}
		expired++
		log.Printf("session %d expired (%s, ip=%s, mac=%s)", id, reason, ip, mac)
		logAction(db, "INFO", "session", "Session "+strconv.Itoa(id)+" expired ("+ip+")")
		runCaptiveRules(db, "unauth", mac, reason)
	}
	if err := rows.Err(); err != nil {
		log.Printf("Error reading expired sessions: %v", err)
	}
	return expired
}

// StartExpiryEnforcer recovers the iptables state after a reboot/restart
// (auth every still-active MAC, expire+unauth the overdue ones) and then
// enforces expiry every 30 seconds in the background.
func StartExpiryEnforcer(db *sql.DB) {
	// Startup recovery: clear the overdue first so a stale session cannot
	// be re-authorized below.
	if n := ExpireOverdueSessions(db, "startup-recovery"); n > 0 {
		log.Printf("startup recovery: expired %d overdue session(s)", n)
	}

	rows, err := db.Query(`
		SELECT DISTINCT client_mac FROM sessions
		WHERE status = 'active' AND expires_at > NOW()
		  AND COALESCE(client_mac, '') <> ''
	`)
	if err != nil {
		log.Printf("startup recovery: failed to list active sessions: %v", err)
	} else {
		defer rows.Close()
		for rows.Next() {
			var mac string
			if err := rows.Scan(&mac); err != nil {
				continue
			}
			runCaptiveRules(db, "auth", mac, "startup-recovery")
		}
	}

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			ExpireOverdueSessions(db, "expire")
		}
	}()
}

// ============================================
// HELPERS
// ============================================

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
	return time.Now().Unix()
}
