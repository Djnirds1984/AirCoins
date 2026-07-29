package handlers

import (
	"encoding/json"
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
type CoinslotHandler struct{}

// armedFile holds the epoch timestamp (seconds) when the armed window
// expires. /run is tmpfs, so it is cleared automatically on reboot.
const armedFile = "/run/pisowifi/coinslot.armed"

const (
	defaultArmDuration = 60  // seconds
	maxArmDuration     = 300 // seconds
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

	expiresAt := time.Now().Unix() + int64(duration)

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

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"armed":      true,
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

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"armed":      armed,
		"expires_at": expiresAt,
	})
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
