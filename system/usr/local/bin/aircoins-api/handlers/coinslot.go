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
func (h *CoinslotHandler) Arm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		DurationSec int `json:"duration_sec"`
	}
	// Body is optional; ignore decode errors and fall back to default
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&req)
	}

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

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"armed":      true,
		"armed_at":   armedAt,
		"expires_at": expiresAt,
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
			resolved, _, perr := MinutesForAmount(h.DB, ev.Value)
			if perr != nil {
				log.Printf("Error resolving pricing for P%d: %v", ev.Value, perr)
				resolved = 0
			}
			minutes = resolved
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
