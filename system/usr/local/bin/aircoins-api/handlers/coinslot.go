package handlers

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// CoinslotHandler manages the on-demand arm/disarm window for the
// GPIO coin listener. When armed, gpio-coin-listener actively polls
// the coin slot; when disarmed it sits idle (near-zero CPU).
//
// These endpoints are PUBLIC (portal-facing) — customers arm the slot
// by tapping "Insert Coin" on the captive portal.
type CoinslotHandler struct {
	DB *sql.DB
}

// armedFile holds the epoch timestamp (seconds) when the armed window
// expires. /run is tmpfs, so it is cleared automatically on reboot.
const armedFile = "/run/pisowifi/coinslot.armed"

// armedAtFile holds the epoch timestamp (seconds) when the current
// window was armed. The listener rewrites armedFile on every coin
// (+30s), so the start of the window needs its own marker.
const armedAtFile = "/run/pisowifi/coinslot.armed_at"

const (
	defaultArmDuration = 60  // seconds
	maxArmDuration     = 300 // seconds
	maxWindowCoins     = 200 // coin events reported per armed window
)

// Arm handles POST /api/coinslot/arm
// Body: {"duration_sec": 60} (optional, default 60, max 300)
// Anti-abuse: each arm call counts as a tap in the sliding window for
// the caller's MAC. Exceeding max_taps in window_seconds triggers a
// per-device ban (ban_seconds long) and a 403 response.
func (h *CoinslotHandler) Arm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// --- Anti-abuse: record tap and enforce ban -----------------------
	clientIP := clientIPFromRequest(r)
	clientMAC := neighborMAC(clientIP)
	if clientMAC != "" {
		// Already banned?
		if until, _, _, banned := activeBan(h.DB, clientMAC); banned {
			sendJSON(w, http.StatusForbidden, map[string]interface{}{
				"armed":        false,
				"banned":       true,
				"banned_until": until.Format(time.RFC3339),
				"reason":       "tap_abuse",
				"message":      "Temporarily banned for tap abuse. Please try again later.",
			})
			return
		}
		// Record tap + sliding-window check
		rules := loadTapRules(h.DB)
		count := recordTap(h.DB, clientMAC, rules.WindowSeconds)
		if count > rules.MaxTaps {
			until := time.Now().Add(time.Duration(rules.BanSeconds) * time.Second)
			setBan(h.DB, clientMAC, until, "tap_abuse", count)
			sendJSON(w, http.StatusForbidden, map[string]interface{}{
				"armed":        false,
				"banned":       true,
				"banned_until": until.Format(time.RFC3339),
				"reason":       "tap_abuse",
				"message":      "Too many taps. You are temporarily banned.",
			})
			return
		}
	}

	// --- Parse body BEFORE acquiring lock (body is optional) -----------
	var req struct {
		DurationSec int `json:"duration_sec"`
	}
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&req)
	}

	// --- Per-VLAN pay lock: HOLD across arm→start lifecycle -----------
	// The lock is acquired here and NOT released until the session Start
	// handler validates the pay_ticket and releases it. This prevents a
	// second device on the same VLAN from arming while the first device
	// is inserting coins.
	acquired, holder, vlanKey, ticket := PayLockTryAcquire(clientIP)
	if !acquired {
		// If the SAME client already holds the lock (e.g. double-tap on
		// INSERT COIN), re-issue the existing ticket so the portal keeps
		// a valid handoff token. A different device on the same VLAN
		// gets the standard 423 rejection.
		if holder == clientIP {
			// Re-read the existing ticket from the lock entry so the
			// client's stash stays valid for Start.
			existingTicket := payLockCurrentTicket(vlanKey)
			sendJSON(w, http.StatusOK, map[string]interface{}{
				"armed":      true,
				"armed_at":   time.Now().Unix(),
				"expires_at": time.Now().Unix() + int64(defaultArmDuration),
				"pay_ticket": existingTicket,
			})
			return
		}
		sendJSON(w, http.StatusLocked, map[string]interface{}{
			"success": false,
			"armed":   false,
			"code":    "paying",
			"message": "SOMEBODY IS PAYING, PLEASE WAIT FOR YOUR TURN",
		})
		return
	}
	// Lock is HELD — the ticket is returned to the client and will be
	// validated by Start. If anything below fails, release the lock.
	lockHeld := true
	defer func() {
		if lockHeld {
			PayLockRelease(clientIP, vlanKey)
		}
	}()

	duration := req.DurationSec
	if duration <= 0 {
		duration = defaultArmDuration
	}
	if duration > maxArmDuration {
		duration = maxArmDuration
	}

	armedAt := time.Now().Unix()
	expiresAt := armedAt + int64(duration)

	if err := os.MkdirAll(filepath.Dir(armedFile), 0755); err != nil {
		sendJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"armed": false, "error": "Failed to create runtime dir",
		})
		return
	}
	if err := os.WriteFile(armedFile, []byte(strconv.FormatInt(expiresAt, 10)+"\n"), 0644); err != nil {
		sendJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"armed": false, "error": "Failed to arm coin slot",
		})
		return
	}

	// Remember when this window opened so Status only reports the coins
	// inserted after this arm request.
	if err := os.WriteFile(armedAtFile, []byte(strconv.FormatInt(armedAt, 10)+"\n"), 0644); err != nil {
		log.Printf("Failed to record coin slot arm time: %v", err)
	}

	// Arm succeeded — don't release the lock in the defer (Start will).
	lockHeld = false

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"armed":      true,
		"armed_at":   armedAt,
		"expires_at": expiresAt,
		"pay_ticket": ticket,
	})
}

// Disarm handles POST /api/coinslot/disarm
func (h *CoinslotHandler) Disarm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	os.Remove(armedFile)
	os.Remove(armedAtFile)

	// Release the per-VLAN lock if this client still holds it
	// (e.g. user cancelled the modal before tapping Done Paying).
	// PayLockRelease is safe to call even if no lock is held.
	clientIP := clientIPFromRequest(r)
	vlan := resolveVLAN(clientIP)
	if vlan != "" {
		PayLockRelease(clientIP, vlan)
	}

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"armed": false,
	})
}

// Status handles GET /api/coinslot/status
func (h *CoinslotHandler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	armed, expiresAt := readArmedState()
	armedAt := readArmedAt(expiresAt)

	// Coins detected since this window was armed. The GPIO listener posts
	// every pulse to POST /api/gpio/coin, which inserts a coin_events row
	// — that table is the only source of truth the portal can poll.
	coins := emptyCoinWindow()
	if armed {
		coins = h.coinsSinceArm(armedAt)
	}

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"armed":          armed,
		"armed_at":       armedAt,
		"expires_at":     expiresAt,
		"coins_inserted": coins,
	})
}

// coinWindowEvent is a single coin detected during the armed window.
// AgeSec is how long ago the coin was detected (the DB clock is the
// reference, so no timezone assumptions leak into the response).
// Minutes is the MARGINAL credit this coin added to the window: the window is
// priced as one accumulated amount, so the individual coins add up exactly to
// the window's TotalMinutes.
type coinWindowEvent struct {
	ID      int64 `json:"id"`
	Value   int   `json:"value"`
	Minutes int   `json:"minutes"`
	AgeSec  int   `json:"age_sec"`
}

// coinWindow summarises the coins inserted during the armed window.
type coinWindow struct {
	Count        int               `json:"count"`
	TotalValue   int               `json:"total_value"`
	TotalMinutes int               `json:"total_minutes"`
	TotalSeconds int               `json:"total_seconds"`
	LastID       int64             `json:"last_id"`
	Coins        []coinWindowEvent `json:"coins"`
}

func emptyCoinWindow() coinWindow {
	return coinWindow{Coins: []coinWindowEvent{}}
}

// coinsSinceArm aggregates the coin_events rows recorded after armedAt.
// The window is priced as ONE accumulated amount — the same tier resolution the
// backend uses to credit time — so the modal shows exactly the time the session
// will credit (0 minutes when no tier is configured).
func (h *CoinslotHandler) coinsSinceArm(armedAt int64) coinWindow {
	window := emptyCoinWindow()
	if h.DB == nil || armedAt <= 0 {
		return window
	}

	// Select by AGE instead of an absolute timestamp: detected_at is a
	// plain TIMESTAMP written with NOW(), so comparing it against a clock
	// value formatted here would break if the DB and the API disagree on
	// the timezone. An elapsed number of seconds cannot. +1s covers the
	// truncation of the arm timestamp to whole seconds.
	windowAge := time.Now().Unix() - armedAt + 1
	if windowAge < 1 {
		windowAge = 1
	}

	rows, err := h.DB.Query(`
		SELECT id, coin_value,
		       GREATEST(0, EXTRACT(EPOCH FROM (NOW() - detected_at)))::int AS age_sec
		FROM coin_events
		WHERE detected_at >= NOW() - ($1::int * INTERVAL '1 second')
		ORDER BY id ASC
		LIMIT $2
	`, windowAge, maxWindowCoins)
	if err != nil {
		log.Printf("Error fetching coin events since arm: %v", err)
		return window
	}
	defer rows.Close()

	// Resolve the rates ONCE for the whole window and price every pulse against
	// the accumulated amount — the same rule the session credit uses. Pricing
	// each pulse on its own would display 5 x the P1 rate for a P5 coin on a
	// pulse-train acceptor (e.g. 1h15m) while the session credited the P5 tier,
	// so the modal showed a time the customer never received.
	tiers, terr := loadActiveTiers(h.DB)
	if terr != nil {
		log.Printf("Error loading pricing tiers: %v", terr)
		tiers = nil
	}

	runningValue := 0
	for rows.Next() {
		var ev coinWindowEvent
		var ageSec sql.NullInt64
		if err := rows.Scan(&ev.ID, &ev.Value, &ageSec); err != nil {
			log.Printf("Error scanning coin event: %v", err)
			continue
		}
		ev.AgeSec = int(ageSec.Int64)

		// Per-coin minutes are the MARGINAL credit this pulse added, so the
		// listed coins add up exactly to TotalMinutes.
		_, before := resolveTier(tiers, runningValue)
		runningValue += ev.Value
		_, after := resolveTier(tiers, runningValue)
		ev.Minutes = after - before

		window.Coins = append(window.Coins, ev)
		window.Count++
		window.TotalValue = runningValue
		window.LastID = ev.ID
	}
	if err := rows.Err(); err != nil {
		log.Printf("Error reading coin events: %v", err)
	}

	// Window total = the credits for the inserted total, tiered and pro-rated
	// exactly like the session credit (0 when no tier is configured).
	if _, total := resolveTier(tiers, window.TotalValue); total > 0 {
		window.TotalMinutes = total
	}
	window.TotalSeconds = window.TotalMinutes * 60
	return window
}

// readArmedAt returns the epoch second the current window was armed, or 0
// when the slot is not armed. If the marker file is missing (e.g. the API
// restarted mid-window) it falls back to the widest possible window so
// inserted coins are never hidden from the portal.
func readArmedAt(expiresAt int64) int64 {
	if expiresAt <= 0 {
		return 0
	}

	if data, err := os.ReadFile(armedAtFile); err == nil {
		armedAt, perr := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		if perr == nil && armedAt > 0 && armedAt <= expiresAt {
			return armedAt
		}
	}

	return expiresAt - maxArmDuration
}

// readArmedState returns whether the coin slot is currently armed and
// the expiry epoch (0 when not armed).
func readArmedState() (bool, int64) {
	data, err := os.ReadFile(armedFile)
	if err != nil {
		return false, 0
	}

	expiresAt, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return false, 0
	}

	if time.Now().Unix() >= expiresAt {
		return false, 0
	}
	return true, expiresAt
}
