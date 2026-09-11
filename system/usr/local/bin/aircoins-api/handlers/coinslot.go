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
// Body: {"duration_sec": 60, "coinslot": "auto"} (optional)
//   coinslot: "auto" (default) — prefer sub-vendo on VLAN, fall back to GPIO
//            "gpio"           — arm the local GPIO coinslot
//            "subvendo:N"     — arm a specific sub-vendo (must be on caller's VLAN)
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
		DurationSec int    `json:"duration_sec"`
		Coinslot    string `json:"coinslot"`
	}
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&req)
	}
	if req.Coinslot == "" {
		req.Coinslot = "auto"
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

	// Resolve the caller's VLAN and the sub-vendo bound to it (if any).
	// The mapping is server-side (never a client-supplied parameter), which
	// is what makes one VLAN = one coinslot and units mutually invisible.
	iface := resolveVLAN(clientIP)
	svID, svErr := subvendoIDForVLAN(h.DB, iface)
	if svErr != nil {
		log.Printf("[subvendo] lookup for %s: %v", iface, svErr)
		svID = 0
	}

	// Determine which coinslot to arm based on the "coinslot" parameter:
	//   "auto"        — prefer ANY online sub-vendo, fall back to GPIO
	//   "gpio"        — force local GPIO (only if hasLocalGPIO)
	//   "subvendo:N"  — force a specific sub-vendo by id (no VLAN check)
	targetSvID := int64(0)
	switch req.Coinslot {
	case "auto":
		targetSvID = svID
		if targetSvID == 0 {
			// No unit bound to this caller's VLAN — fall back to any online unit.
			if units, err := subvendosForVLAN(h.DB, ""); err == nil && len(units) > 0 {
				targetSvID = units[0].ID
			}
		}
	case "gpio":
		if !hasLocalGPIO() {
			sendJSON(w, http.StatusNotImplemented, map[string]interface{}{
				"armed":  false,
				"code":   "no_gpio",
				"message": "This server has no local GPIO coinslot.",
			})
			return
		}
		targetSvID = 0 // GPIO
	default:
		if strings.HasPrefix(req.Coinslot, "subvendo:") {
			id, perr := strconv.ParseInt(strings.TrimPrefix(req.Coinslot, "subvendo:"), 10, 64)
			if perr != nil || id <= 0 {
				sendJSON(w, http.StatusBadRequest, map[string]interface{}{
					"armed":  false,
					"code":   "invalid_coinslot",
					"message": "Invalid coinslot identifier.",
				})
				return
			}
			// SIMPLIFIED MODEL (v1.25.0): no VLAN check — any online unit is armable.
			targetSvID = id
		} else {
			sendJSON(w, http.StatusBadRequest, map[string]interface{}{
				"armed":  false,
				"code":   "invalid_coinslot",
				"message": "Invalid coinslot. Use 'auto', 'gpio', or 'subvendo:N'.",
			})
			return
		}
	}

	if targetSvID > 0 {
		if err := armSubvendo(h.DB, targetSvID, duration); err != nil {
			log.Printf("[subvendo #%d] arm: %v", targetSvID, err)
			sendJSON(w, http.StatusInternalServerError, map[string]interface{}{
				"armed": false, "error": "Failed to arm coin slot",
			})
			return
		}
		// Arm succeeded — don't release the lock in the defer (Start will).
		lockHeld = false
		sendJSON(w, http.StatusOK, map[string]interface{}{
			"armed": true, "armed_at": armedAt, "expires_at": expiresAt,
			"pay_ticket": ticket, "coinslot": req.Coinslot,
		})
		return
	}

	// No sub-vendo target — arm local GPIO.
	if !hasLocalGPIO() {
		sendJSON(w, http.StatusNotImplemented, map[string]interface{}{
			"armed":   false,
			"code":    "no_coinslot",
			"message": "No coinslot configured for this network. Please contact the operator.",
		})
		return
	}

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

// Options handles GET /api/coinslot/options
// Returns the coinslot choices available on the caller's VLAN so the
// captive portal can render a dropdown (or auto-select when there is
// only one). This is the endpoint that makes multi-vendo deployments
// work on a single VLAN — the portal learns what it can arm without
// ever being able to reach into another VLAN's unit.
//
// Response shape:
//   {
//     "default": "auto",
//     "gpio": true,            // server has a local GPIO coinslot
//     "subvendo": {            // null when no sub-vendo on this VLAN
//        "id": 3, "name": "Vendo A", "status": "online", "coin_value": 1
//     }
//   }
func (h *CoinslotHandler) Options(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodOptions {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	resp := map[string]interface{}{
		"default": "auto",
		"gpio":    hasLocalGPIO(),
		"vlan":    resolveVLAN(clientIPFromRequest(r)),
	}

	iface := resolveVLAN(clientIPFromRequest(r))
	units, err := subvendosForVLAN(h.DB, iface)
	if err == nil && len(units) > 0 {
		subvendos := []map[string]interface{}{}
		for _, u := range units {
			status := "offline"
			if u.Online {
				status = "online"
			}
			subvendos = append(subvendos, map[string]interface{}{
				"id":         u.ID,
				"name":       u.Name,
				"status":     status,
				"coin_value": 1, // NodeMCU: 1 pulse = 1 peso
			})
		}
		resp["subvendos"] = subvendos
		resp["default"] = "auto"
		if len(subvendos) == 1 && !hasLocalGPIO() {
			resp["default"] = "subvendo:" + strconv.FormatInt(units[0].ID, 10)
		}
	} else {
		resp["subvendos"] = []map[string]interface{}{}
	}

	sendJSON(w, http.StatusOK, resp)
}

// Disarm handles POST /api/coinslot/disarm
// Body: {"coinslot": "auto"} (optional)
//   "auto"        — disarm whichever slot is armed on this VLAN
//   "gpio"        — disarm only local GPIO
//   "subvendo:N"  — disarm only that sub-vendo (must be on caller's VLAN)
func (h *CoinslotHandler) Disarm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	clientIP := clientIPFromRequest(r)
	vlan := resolveVLAN(clientIP)

	var req struct {
		Coinslot string `json:"coinslot"`
	}
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&req)
	}
	if req.Coinslot == "" {
		req.Coinslot = "auto"
	}

	// Determine which slot to disarm. Same switch as Arm — keeping the
	// two in sync means the portal can always reverse an arm.
	iface := clientIP
	svID, _ := subvendoIDForVLAN(h.DB, resolveVLAN(iface))

	switch req.Coinslot {
	case "auto":
		// Disarm whichever is armed: sub-vendo first, then GPIO.
		if svID > 0 {
			disarmSubvendo(h.DB, svID)
		}
		os.Remove(armedFile)
		os.Remove(armedAtFile)
	case "gpio":
		os.Remove(armedFile)
		os.Remove(armedAtFile)
	default:
		if strings.HasPrefix(req.Coinslot, "subvendo:") {
			id, _ := strconv.ParseInt(strings.TrimPrefix(req.Coinslot, "subvendo:"), 10, 64)
			if id > 0 {
				disarmSubvendo(h.DB, id)
			}
		}
	}

	// Release the per-VLAN lock if this client still holds it
	// (e.g. user cancelled the modal before tapping Done Paying).
	// PayLockRelease is safe to call even if no lock is held.
	if vlan != "" {
		PayLockRelease(clientIP, vlan)
	}

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"armed": false,
	})
}

// Status handles GET /api/coinslot/status?coinslot=auto|gpio|subvendo:N
// The coinslot query param tells the server which slot's window to report.
// Without it, the server falls back to "auto" (sub-vendo on VLAN, else GPIO).
func (h *CoinslotHandler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Coins detected since this window was armed. The GPIO listener posts
	// every pulse to POST /api/gpio/coin, which inserts a coin_events row
	// — that table is the only source of truth the portal can poll.
	iface := resolveVLAN(clientIPFromRequest(r))
	svID, svErr := subvendoIDForVLAN(h.DB, iface)
	if svErr != nil {
		svID = 0
	}

	coinslot := r.URL.Query().Get("coinslot")
	if coinslot == "" {
		coinslot = "auto"
	}

	// Resolve which slot to report on. SIMPLIFIED MODEL (v1.25.0): the
	// requested sub-vendo is honored regardless of VLAN.
	targetSvID := int64(0)
	switch coinslot {
	case "auto":
		targetSvID = svID
	case "gpio":
		targetSvID = 0
	default:
		if strings.HasPrefix(coinslot, "subvendo:") {
			id, _ := strconv.ParseInt(strings.TrimPrefix(coinslot, "subvendo:"), 10, 64)
			targetSvID = id // any unit; no VLAN check
		}
	}

	coins := emptyCoinWindow()
	var armed bool
	var armedAt, expiresAt int64
	if targetSvID > 0 {
		armed, armedAt, expiresAt = subvendoArmWindow(h.DB, targetSvID)
		if armed {
			coins = h.coinsSinceArm(armedAt, "subvendo:"+strconv.FormatInt(targetSvID, 10))
		}
	} else {
		armed, expiresAt = readArmedState()
		armedAt = readArmedAt(expiresAt)
		if armed {
			coins = h.coinsSinceArm(armedAt, "local_gpio")
		}
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
// Minutes come from the pricing table (the same resolution used when the
// backend credits time), so an unpriced coin is listed with 0 minutes.
// source restricts the window to one coinslot ('local_gpio' or
// 'subvendo:<id>') — sub-vendo coins never leak into local windows and
// vice versa.
func (h *CoinslotHandler) coinsSinceArm(armedAt int64, source string) coinWindow {
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
		  AND source = $3
		ORDER BY id ASC
		LIMIT $2
	`, windowAge, maxWindowCoins, source)
	if err != nil {
		log.Printf("Error fetching coin events since arm: %v", err)
		return window
	}
	defer rows.Close()

	minutesByValue := make(map[int]int)

	for rows.Next() {
		var ev coinWindowEvent
		var ageSec sql.NullInt64
		if err := rows.Scan(&ev.ID, &ev.Value, &ageSec); err != nil {
			log.Printf("Error scanning coin event: %v", err)
			continue
		}
		ev.AgeSec = int(ageSec.Int64)

		minutes, cached := minutesByValue[ev.Value]
		if !cached {
			m, perr := MinutesForAmount(h.DB, ev.Value)
			if perr != nil {
				log.Printf("Error resolving pricing for P%d: %v", ev.Value, perr)
				m = PricingMatch{}
			}
			minutes = m.Minutes
			minutesByValue[ev.Value] = minutes
		}
		ev.Minutes = minutes

		window.Coins = append(window.Coins, ev)
		window.Count++
		window.TotalValue += ev.Value
		window.TotalMinutes += minutes
		window.LastID = ev.ID
	}
	if err := rows.Err(); err != nil {
		log.Printf("Error reading coin events: %v", err)
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
