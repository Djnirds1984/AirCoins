package handlers

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// LicenseState is stored as JSON in system_settings key 'license_state'.
type LicenseState struct {
	Status                 string     `json:"status"`
	TrialStartedAt         time.Time  `json:"trial_started_at"`
	TrialExpiresAt         time.Time  `json:"trial_expires_at"`
	LicenseKey             string     `json:"license_key"`
	ActivatedAt            *time.Time `json:"activated_at"`
	ExpiresAt              *time.Time `json:"expires_at"`
	OwnerEmail             string     `json:"owner_email"`
	HardwareID             string     `json:"hardware_id"`
	LastHeartbeatAt        *time.Time `json:"last_heartbeat_at"`
	LastSupabaseResponseAt *time.Time `json:"last_supabase_response_at"`
	LastSupabaseError      string     `json:"last_supabase_error"`
}

// LicenseHandler manages the license state for this AirCoins device.
type LicenseHandler struct {
	db    *sql.DB
	mu    sync.RWMutex
	state LicenseState
}

const licenseSettingKey = "license_state"
const licenseSettingDesc = "License management state (trial/active/locked/revoked)"

// NewLicenseHandler creates a LicenseHandler backed by the given DB.
func NewLicenseHandler(db *sql.DB) *LicenseHandler {
	return &LicenseHandler{db: db}
}

// HardwareFingerprint returns a stable hardware identifier:
// the CPU serial from /proc/cpuinfo, falling back to /etc/machine-id.
func HardwareFingerprint() string {
	f, err := os.Open("/proc/cpuinfo")
	if err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "Serial") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					serial := strings.TrimSpace(parts[1])
					// Reject all-zeros serial (virtual machines / some boards)
					allZero := true
					for _, c := range serial {
						if c != '0' {
							allZero = false
							break
						}
					}
					if serial != "" && !allZero {
						return serial
					}
				}
			}
		}
	}

	// Fallback: /etc/machine-id
	data, err := os.ReadFile("/etc/machine-id")
	if err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id
		}
	}

	return "unknown"
}

// InitOrLoadLicense initialises the license state from the database, or
// seeds a fresh 7-day trial if no row exists. Safe to call on every start.
// If the hardware ID has changed (SD card cloned to a new board), it resets
// to a fresh 7-day trial bound to the new hardware — this is the expected
// business flow for deploying images to multiple buyers.
func (h *LicenseHandler) InitOrLoadLicense() error {
	var value string
	err := h.db.QueryRow(
		"SELECT value FROM system_settings WHERE key = $1", licenseSettingKey,
	).Scan(&value)

	if err == sql.ErrNoRows {
		// First run — seed a trial
		now := time.Now().UTC()
		hwID := HardwareFingerprint()
		h.mu.Lock()
		h.state = LicenseState{
			Status:         "trial",
			TrialStartedAt: now,
			TrialExpiresAt: now.Add(7 * 24 * time.Hour),
			HardwareID:     hwID,
		}
		h.mu.Unlock()
		h.saveState()
		log.Printf("license: seeded 7-day trial (hardware_id=%s)", hwID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("license: DB read failed: %w", err)
	}

	var st LicenseState
	if err := json.Unmarshal([]byte(value), &st); err != nil {
		return fmt.Errorf("license: corrupt JSON in system_settings: %w", err)
	}

	// CRITICAL: Validate hardware identity on every load.
	// If the hardware ID changed (SD card cloned to new board), reset to a
	// fresh 7-day trial. This is the expected business flow: flash image →
	// new board → new hardware ID → 7-day trial → buyer purchases license.
	liveHW := HardwareFingerprint()
	if st.HardwareID != "" && st.HardwareID != liveHW {
		log.Printf("license: HARDWARE CHANGE DETECTED — old=%s new=%s — resetting to fresh 7-day trial", st.HardwareID, liveHW)
		now := time.Now().UTC()
		st = LicenseState{
			Status:         "trial",
			TrialStartedAt: now,
			TrialExpiresAt: now.Add(7 * 24 * time.Hour),
			HardwareID:     liveHW,
		}
	} else if st.HardwareID == "" {
		// No hardware ID stored (legacy state) — bind to current hardware
		st.HardwareID = liveHW
		log.Printf("license: binding to hardware_id=%s (was empty)", liveHW)
	}

	h.mu.Lock()
	h.state = st
	h.mu.Unlock()
	h.saveState()
	log.Printf("license: loaded state status=%s hardware_id=%s", st.Status, st.HardwareID)
	return nil
}

// isLicenseValidLocked must be called with h.mu at least RLocked.
func (h *LicenseHandler) isLicenseValidLocked() bool {
	switch h.state.Status {
	case "trial":
		if time.Now().After(h.state.TrialExpiresAt) {
			return false
		}
	case "active":
		// ok
	default:
		return false
	}
	if h.state.LastSupabaseResponseAt != nil {
		if time.Since(*h.state.LastSupabaseResponseAt) > 24*time.Hour {
			return false
		}
	}
	return true
}

// IsLicenseValid reports whether the device is currently licensed to operate.
// Valid states: "trial" (not expired) or "active".
// If a Supabase heartbeat has occurred, the last response must be < 24 h old.
func (h *LicenseHandler) IsLicenseValid() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.isLicenseValidLocked()
}

// supabaseRequest is a helper for Supabase REST API calls.
func supabaseRequest(method, url string, body io.Reader) (*http.Request, error) {
	apiKey := os.Getenv("SUPABASE_SERVICE_ROLE_KEY")
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", apiKey)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if method == http.MethodPatch || method == http.MethodPost {
		req.Header.Set("Prefer", "return=representation")
	}
	return req, nil
}

// HeartbeatSupabase phones home to the Supabase license table.
// Non-fatal on any error — the device keeps working with the last known
// state so a network outage doesn't kill a running piso WiFi machine.
func (h *LicenseHandler) HeartbeatSupabase() error {
	baseURL := os.Getenv("SUPABASE_URL")
	apiKey := os.Getenv("SUPABASE_SERVICE_ROLE_KEY")

	if baseURL == "" || apiKey == "" {
		log.Printf("license: SUPABASE_URL or SUPABASE_SERVICE_ROLE_KEY not set — skipping heartbeat")
		h.mu.Lock()
		h.state.LastSupabaseError = "Supabase credentials not configured"
		h.mu.Unlock()
		h.saveState()
		return nil
	}

	h.mu.RLock()
	licenseKey := h.state.LicenseKey
	hwID := h.state.HardwareID
	status := h.state.Status
	h.mu.RUnlock()

	// Use the live hardware ID for heartbeat (in case it changed since init)
	liveHW := HardwareFingerprint()
	if liveHW != "" && liveHW != "unknown" {
		hwID = liveHW
	}

	client := &http.Client{Timeout: 15 * time.Second}
	now := time.Now().UTC()

	if licenseKey != "" {
		// Licensed device: GET current row, then PATCH heartbeat
		getURL := fmt.Sprintf("%s/rest/v1/aircoins_licenses?license_key=eq.%s&select=*", baseURL, licenseKey)
		req, err := supabaseRequest(http.MethodGet, getURL, nil)
		if err != nil {
			return h.recordHeartbeatError(err, "build GET request")
		}
		resp, err := client.Do(req)
		if err != nil {
			return h.recordHeartbeatError(err, "Supabase GET")
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)

		if resp.StatusCode >= 400 {
			return h.recordHeartbeatError(fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body)), "Supabase GET")
		}

		var rows []map[string]interface{}
		if err := json.Unmarshal(body, &rows); err != nil || len(rows) == 0 {
			return h.recordHeartbeatError(fmt.Errorf("no license row found for key"), "Supabase GET")
		}
		row := rows[0]

		// Sync remote status
		remoteStatus, _ := row["status"].(string)
		h.mu.Lock()
		if remoteStatus != "" && remoteStatus != "active" && remoteStatus != "available" {
			h.state.Status = remoteStatus
		}
		h.state.LastHeartbeatAt = &now
		h.state.LastSupabaseResponseAt = &now
		h.state.LastSupabaseError = ""
		h.mu.Unlock()
		h.saveState()

		// PATCH heartbeat timestamp on Supabase
		patchURL := fmt.Sprintf("%s/rest/v1/aircoins_licenses?license_key=eq.%s", baseURL, licenseKey)
		patchBody, _ := json.Marshal(map[string]interface{}{
			"last_heartbeat_at": now.Format(time.RFC3339),
			"hardware_id":       hwID,
		})
		patchReq, err := supabaseRequest(http.MethodPatch, patchURL, bytes.NewReader(patchBody))
		if err == nil {
			if pResp, err := client.Do(patchReq); err == nil {
				pResp.Body.Close()
			}
		}
	} else {
		// Trial device: POST a heartbeat row so Supabase knows this device exists
		postURL := baseURL + "/rest/v1/aircoins_license_heartbeats"
		postBody, _ := json.Marshal(map[string]interface{}{
			"hardware_id": hwID,
			"status":      "trial",
		})
		req, err := supabaseRequest(http.MethodPost, postURL, bytes.NewReader(postBody))
		if err != nil {
			return h.recordHeartbeatError(err, "build POST request")
		}
		resp, err := client.Do(req)
		if err != nil {
			return h.recordHeartbeatError(err, "Supabase POST heartbeat")
		}
		defer resp.Body.Close()

		h.mu.Lock()
		h.state.LastHeartbeatAt = &now
		h.state.LastSupabaseResponseAt = &now
		h.state.LastSupabaseError = ""
		h.mu.Unlock()
		h.saveState()
	}

	log.Printf("license: heartbeat ok (status=%s)", status)
	return nil
}

// recordHeartbeatError stores an error string without changing the license
// status, so a network failure never locks the device.
func (h *LicenseHandler) recordHeartbeatError(err error, context string) error {
	msg := fmt.Sprintf("%s: %v", context, err)
	log.Printf("license: heartbeat error: %s", msg)
	h.mu.Lock()
	h.state.LastSupabaseError = msg
	h.mu.Unlock()
	h.saveState()
	return fmt.Errorf("%s", msg)
}

// ActivateLicense validates a license key against Supabase and, if eligible,
// activates it for this device.
func (h *LicenseHandler) ActivateLicense(key, email string) error {
	baseURL := os.Getenv("SUPABASE_URL")
	apiKey := os.Getenv("SUPABASE_SERVICE_ROLE_KEY")
	if baseURL == "" || apiKey == "" {
		return fmt.Errorf("Supabase credentials not configured")
	}

	client := &http.Client{Timeout: 15 * time.Second}

	// Fetch the license row
	getURL := fmt.Sprintf("%s/rest/v1/aircoins_licenses?license_key=eq.%s&select=*", baseURL, key)
	req, err := supabaseRequest(http.MethodGet, getURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Supabase request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("Supabase error HTTP %d: %s", resp.StatusCode, string(body))
	}

	var rows []map[string]interface{}
	if err := json.Unmarshal(body, &rows); err != nil || len(rows) == 0 {
		return fmt.Errorf("license key not found")
	}
	row := rows[0]

	remoteStatus, _ := row["status"].(string)
	if remoteStatus != "available" {
		return fmt.Errorf("license is not available (current status: %s)", remoteStatus)
	}

	remoteHW, _ := row["hardware_id"].(string)
	h.mu.RLock()
	localHW := h.state.HardwareID
	h.mu.RUnlock()
	if remoteHW != "" && remoteHW != localHW {
		return fmt.Errorf("license is bound to different hardware")
	}

	// PATCH: activate
	now := time.Now().UTC()
	patchURL := fmt.Sprintf("%s/rest/v1/aircoins_licenses?license_key=eq.%s", baseURL, key)
	patchBody, _ := json.Marshal(map[string]interface{}{
		"status":            "active",
		"activated_at":      now.Format(time.RFC3339),
		"owner_email":       email,
		"hardware_id":       localHW,
		"last_heartbeat_at": now.Format(time.RFC3339),
	})
	patchReq, err := supabaseRequest(http.MethodPatch, patchURL, bytes.NewReader(patchBody))
	if err != nil {
		return fmt.Errorf("build PATCH request: %w", err)
	}
	pResp, err := client.Do(patchReq)
	if err != nil {
		return fmt.Errorf("Supabase PATCH failed: %w", err)
	}
	defer pResp.Body.Close()
	if pResp.StatusCode >= 400 {
		pBody, _ := io.ReadAll(pResp.Body)
		return fmt.Errorf("Supabase PATCH error HTTP %d: %s", pResp.StatusCode, string(pBody))
	}

	h.mu.Lock()
	h.state.Status = "active"
	h.state.LicenseKey = key
	h.state.OwnerEmail = email
	h.state.ActivatedAt = &now
	h.state.LastHeartbeatAt = &now
	h.state.LastSupabaseResponseAt = &now
	h.state.LastSupabaseError = ""
	h.mu.Unlock()
	h.saveState()
	log.Printf("license: activated key=%s email=%s", key, email)
	return nil
}

// DeactivateLicense revokes the license on Supabase and moves the local
// state to "locked".
func (h *LicenseHandler) DeactivateLicense() error {
	baseURL := os.Getenv("SUPABASE_URL")
	apiKey := os.Getenv("SUPABASE_SERVICE_ROLE_KEY")

	h.mu.RLock()
	key := h.state.LicenseKey
	h.mu.RUnlock()

	if key == "" {
		return fmt.Errorf("no license key to deactivate")
	}

	if baseURL != "" && apiKey != "" {
		client := &http.Client{Timeout: 15 * time.Second}
		patchURL := fmt.Sprintf("%s/rest/v1/aircoins_licenses?license_key=eq.%s", baseURL, key)
		patchBody, _ := json.Marshal(map[string]interface{}{
			"status":      "revoked",
			"hardware_id": nil,
		})
		patchReq, err := supabaseRequest(http.MethodPatch, patchURL, bytes.NewReader(patchBody))
		if err != nil {
			return fmt.Errorf("remote deactivation failed: %w", err)
		}
		pResp, err := client.Do(patchReq)
		if err != nil {
			return fmt.Errorf("remote deactivation failed: %w", err)
		}
		defer pResp.Body.Close()
		if pResp.StatusCode >= 400 {
			return fmt.Errorf("remote deactivation failed (HTTP %d) — license still active on server", pResp.StatusCode)
		}
	}

	h.mu.Lock()
	h.state.Status = "locked"
	h.mu.Unlock()
	h.saveState()
	log.Printf("license: deactivated (local status=locked)")
	return nil
}

// saveState marshals the current state and upserts it into system_settings.
func (h *LicenseHandler) saveState() {
	h.mu.RLock()
	st := h.state
	h.mu.RUnlock()

	doc, err := json.Marshal(st)
	if err != nil {
		log.Printf("license: marshal error: %v", err)
		return
	}

	_, err = h.db.Exec(`
		INSERT INTO system_settings (key, value, description, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (key) DO UPDATE SET value = $2, updated_at = NOW()
	`, licenseSettingKey, string(doc), licenseSettingDesc)
	if err != nil {
		log.Printf("license: saveState DB error: %v", err)
	}
}

// ============================================
// HTTP HANDLERS
// ============================================

// Status returns the current license state and whether it is valid.
func (h *LicenseHandler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h.mu.RLock()
	st := h.state
	valid := h.isLicenseValidLocked()
	h.mu.RUnlock()

	maskedKey := ""
	if len(st.LicenseKey) > 4 {
		maskedKey = "****" + st.LicenseKey[len(st.LicenseKey)-4:]
	} else if len(st.LicenseKey) > 0 {
		maskedKey = "****"
	}

	resp := map[string]interface{}{
		"status":                    st.Status,
		"trial_started_at":          st.TrialStartedAt.Format(time.RFC3339),
		"trial_expires_at":          st.TrialExpiresAt.Format(time.RFC3339),
		"license_key":               maskedKey,
		"owner_email":               st.OwnerEmail,
		"hardware_id":               st.HardwareID,
		"live_hardware_id":          HardwareFingerprint(),
		"valid":                     valid,
		"last_heartbeat_at":         formatTimePtr(st.LastHeartbeatAt),
		"last_supabase_response_at": formatTimePtr(st.LastSupabaseResponseAt),
		"last_supabase_error":       st.LastSupabaseError,
	}
	if st.ActivatedAt != nil {
		resp["activated_at"] = st.ActivatedAt.Format(time.RFC3339)
	}
	if st.ExpiresAt != nil {
		resp["expires_at"] = st.ExpiresAt.Format(time.RFC3339)
	}
	sendJSON(w, http.StatusOK, resp)
}

// Activate handles POST /api/admin/license/activate.
func (h *LicenseHandler) Activate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		LicenseKey string `json:"license_key"`
		OwnerEmail string `json:"owner_email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false, "message": "Invalid request body",
		})
		return
	}
	if req.LicenseKey == "" {
		sendJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false, "message": "license_key is required",
		})
		return
	}
	if err := h.ActivateLicense(req.LicenseKey, req.OwnerEmail); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false, "message": err.Error(),
		})
		return
	}
	sendJSON(w, http.StatusOK, map[string]interface{}{
		"success": true, "message": "License activated",
	})
}

// Deactivate handles POST /api/admin/license/deactivate.
func (h *LicenseHandler) Deactivate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := h.DeactivateLicense(); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false, "message": err.Error(),
		})
		return
	}
	sendJSON(w, http.StatusOK, map[string]interface{}{
		"success": true, "message": "License deactivated",
	})
}

func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}
