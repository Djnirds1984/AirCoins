package handlers

import (
	"aircoins-api/models"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// subvendo.go implements the SUB-VENDO system: NodeMCU (ESP8266) remote
// coin slots. Architecture:
//
//   - SHARED TOKEN: one api_token_hash for ALL units (set via env or admin).
//   - REGISTRATION: NodeMCU connects with device_id + SSID → appears as 'pending'.
//   - SSID→VLAN MAP: admin maps SSID names to portal VLAN ifaces; the server
//     auto-binds a registering unit to its VLAN from the SSID it reports.
//   - APPROVAL: admin clicks Accept (→ online) or Reject on the Sub-Vendos page.
//   - MULTI-UNIT VLAN: many units on the same VLAN → portal shows a picker.
//   - ISOLATION: VLAN 2's units can never see VLAN 3's units — the server
//     resolves the CALLER's VLAN (resolveVLAN) server-side; sub-vendos only
//     serve their bound VLAN. Coins recorded as source='subvendo:<id>'.
//
// Device routes (public, shared-token auth):
//   - POST /api/subvendo/register  {device_id, ssid} → creates pending row
//   - GET  /api/subvendo/state     (arm window + approval gate)
//   - POST /api/subvendo/coins     {pulses, window_id} → ₱1 rows
//
// Admin routes (admin session):
//   - GET    /api/admin/subvendos         list all units
//   - POST   /api/admin/subvendos/accept  {id} → pending → online
//   - POST   /api/admin/subvendos/reject  {id} → rejected
//   - PATCH  /api/admin/subvendos/:id     update name/site/vlan/enabled
//   - DELETE /api/admin/subvendos/:id     remove unit
//   - GET    /api/admin/ssid-vlan-map     list SSID→VLAN mappings
//   - POST   /api/admin/ssid-vlan-map     add mapping
//   - DELETE /api/admin/ssid-vlan-map/:id remove mapping

type SubVendoHandler struct {
	DB *sql.DB
}

// subVendo is the admin-facing JSON shape of a sub_vendos row.
type subVendo struct {
	ID         int64      `json:"id"`
	DeviceID   string     `json:"device_id"`
	Name       string     `json:"name"`
	Site       string     `json:"site"`
	VlanIface  string     `json:"vlan_iface"`
	SSID       string     `json:"ssid"`
	MAC        string     `json:"mac"`
	Status     string     `json:"status"`
	Enabled    bool       `json:"enabled"`
	Online     bool       `json:"online"`
	TotalCoins int        `json:"total_coins"`
	Armed      bool       `json:"armed"`
	LastSeen   *time.Time `json:"last_seen"`
	CreatedAt  time.Time  `json:"created_at"`
}

// onlineWindow: last_seen newer than this = device considered online.
// Short on purpose — a powered-off unit drops to Offline quickly.
const onlineWindow = 2 * time.Minute

// sharedSubvendoToken is the single token all NodeMCU units use to authenticate.
var sharedSubvendoToken = ""

// SetSharedSubvendoToken sets the shared device token (called from main on startup).
func SetSharedSubvendoToken(t string) { sharedSubvendoToken = t }

// hashToken returns the SHA-256 hex of a token for constant-time comparison.
func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// validMACFormat reports whether s looks like aa:bb:cc:dd:ee:ff (or with
// dashes). Used to sanity-check the MAC a NodeMCU reports before it is
// fed to the captive-portal bypass script.
func validMACFormat(s string) bool {
	if len(s) != 17 {
		return false
	}
	sep := s[2]
	if sep != ':' && sep != '-' {
		return false
	}
	for i := 0; i < 17; i++ {
		switch i % 3 {
		case 2:
			if s[i] != sep {
				return false
			}
		default:
			c := s[i]
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

// captiveBypassPath is the captive-portal rules script that whitelists a
// paying client by MAC. Sub-vendo units are infrastructure devices and get
// the same bypass so the portal's DNS/HTTP capture cannot block their
// calls to the API.
const captiveBypassPath = "/usr/local/bin/aircoins-captive-rules"

// captiveBypass grants (grant=true) or revokes (grant=false) the
// captive-portal bypass for a MAC. Never fatal: the portal rules may be
// absent (fresh install, x64 server without portal duties) and the unit
// can still work on networks without portal capture.
func captiveBypass(mac string, grant bool) {
	if mac == "" || !validMACFormat(mac) {
		return
	}
	if _, err := os.Stat(captiveBypassPath); err != nil {
		return
	}
	action := "unauth"
	if grant {
		action = "auth"
	}
	cmd := exec.Command(captiveBypassPath, action, mac)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[subvendo] captive %s %s: %v (%s)", action, mac, err, strings.TrimSpace(string(out)))
	} else {
		log.Printf("[subvendo] captive %s %s: ok", action, mac)
	}
}

// ============================================
// HELPERS (shared with coinslot.go / session.go)
// ============================================

// hasLocalGPIO reports whether this deployment has a physical coin slot
// wired to the server. x64/Ubuntu builds have none — coinslots come
// exclusively from sub-vendos there.
func hasLocalGPIO() bool {
	switch runtime.GOARCH {
	case "amd64", "386":
		return false
	}
	return true
}

// vlanForSSID looks up the portal VLAN iface for a WiFi SSID, or "" if unmapped.
func vlanForSSID(db *sql.DB, ssid string) string {
	if db == nil || ssid == "" {
		return ""
	}
	var iface string
	err := db.QueryRow(
		`SELECT vlan_iface FROM ssid_vlan_map WHERE ssid = $1 AND active = true`, ssid,
	).Scan(&iface)
	if err != nil {
		return ""
	}
	return iface
}

// subvendoIDForVLAN returns one enabled+online sub-vendo bound to a portal
// interface (e.g. "end0.22"), or 0 when there is none. When multiple units
// share the VLAN, the caller (portal Options) returns all of them as a list.
func subvendoIDForVLAN(db *sql.DB, iface string) (int64, error) {
	if db == nil || iface == "" {
		return 0, nil
	}
	var id int64
	err := db.QueryRow(
		`SELECT id FROM sub_vendos WHERE vlan_iface = $1 AND enabled = true AND status = 'online'
		 ORDER BY id LIMIT 1`,
		iface,
	).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

// subvendoArmWindow returns the arm window stored on the sub_vendos row:
// (armed, windowStartedAt, expiresAt) in epoch seconds.
func subvendoArmWindow(db *sql.DB, id int64) (bool, int64, int64) {
	var armedUntil, windowStart int64
	err := db.QueryRow(
		`SELECT armed_until, window_started_at FROM sub_vendos WHERE id = $1`, id,
	).Scan(&armedUntil, &windowStart)
	if err != nil {
		return false, 0, 0
	}
	now := time.Now().Unix()
	if armedUntil <= now {
		return false, windowStart, armedUntil
	}
	return true, windowStart, armedUntil
}

// armSubvendo opens a coin window for the unit (portal Arm handoff).
func armSubvendo(db *sql.DB, id int64, durationSec int) error {
	now := time.Now().Unix()
	_, err := db.Exec(
		`UPDATE sub_vendos SET armed_until = $2, window_started_at = $3 WHERE id = $1`,
		id, now+int64(durationSec), now,
	)
	return err
}

// disarmSubvendo closes the window (portal cancel / watchdog).
func disarmSubvendo(db *sql.DB, id int64) {
	if id <= 0 {
		return
	}
	db.Exec(`UPDATE sub_vendos SET armed_until = 0 WHERE id = $1`, id)
}

// ============================================
// ADMIN CRUD
// ============================================

// List handles GET /api/admin/subvendos
func (h *SubVendoHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.Query(`
		SELECT id, device_id, name, site, vlan_iface, ssid, COALESCE(mac,''), status, enabled,
		       COALESCE(total_coins,0), armed_until, last_seen, created_at
		FROM sub_vendos ORDER BY status, site, name`)
	if err != nil {
		log.Printf("[subvendo] List error: %v", err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to list sub-vendos: " + err.Error()})
		return
	}
	defer rows.Close()

	units := []subVendo{}
	for rows.Next() {
		var u subVendo
		var armedUntil int64
		var lastSeen sql.NullTime
		if err := rows.Scan(&u.ID, &u.DeviceID, &u.Name, &u.Site, &u.VlanIface,
			&u.SSID, &u.MAC, &u.Status, &u.Enabled, &u.TotalCoins, &armedUntil, &lastSeen, &u.CreatedAt); err != nil {
			continue
		}
		if lastSeen.Valid {
			t := lastSeen.Time
			u.LastSeen = &t
			u.Online = time.Since(t) < onlineWindow
		}
		u.Armed = armedUntil > time.Now().Unix()
		units = append(units, u)
	}
	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: units})
}

// ============================================
// ADMIN: SSID → VLAN MAP
// ============================================

// SSIDVLANMap handles GET (list) and POST (add) /api/admin/ssid-vlan-map
func (h *SubVendoHandler) SSIDVLANMap(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		rows, err := h.DB.Query(`SELECT id, ssid, vlan_iface, site, active FROM ssid_vlan_map ORDER BY ssid`)
		if err != nil {
			sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: err.Error()})
			return
		}
		defer rows.Close()
		var out []map[string]interface{}
		for rows.Next() {
			var id int
			var ssid, vlanIface, site string
			var active bool
			if rows.Scan(&id, &ssid, &vlanIface, &site, &active) == nil {
				out = append(out, map[string]interface{}{"id": id, "ssid": ssid, "vlan_iface": vlanIface, "site": site, "active": active})
			}
		}
		sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: out})

	case "POST":
		var req struct {
			SSID      string `json:"ssid"`
			VlanIface string `json:"vlan_iface"`
			Site      string `json:"site"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.SSID) == "" || strings.TrimSpace(req.VlanIface) == "" {
			sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "ssid and vlan_iface required"})
			return
		}
		_, err := h.DB.Exec(`INSERT INTO ssid_vlan_map (ssid, vlan_iface, site) VALUES ($1,$2,$3)
			ON CONFLICT (ssid) DO UPDATE SET vlan_iface = EXCLUDED.vlan_iface, site = EXCLUDED.site, active = true`,
			strings.TrimSpace(req.SSID), strings.TrimSpace(req.VlanIface), strings.TrimSpace(req.Site))
		if err != nil {
			sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: err.Error()})
			return
		}
		sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Mapping saved"})

	default:
		sendJSON(w, http.StatusMethodNotAllowed, models.APIResponse{Success: false})
	}
}

// SSIDVLANMapDelete handles DELETE /api/admin/ssid-vlan-map/:id
func (h *SubVendoHandler) SSIDVLANMapDelete(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	id, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid id"})
		return
	}
	h.DB.Exec(`DELETE FROM ssid_vlan_map WHERE id = $1`, id)
	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Deleted"})
}

// Dispatch handles /api/admin/subvendos/<id>[/<action>] for the mux prefix.
func (h *SubVendoHandler) Dispatch(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 {
		sendJSON(w, http.StatusNotFound, models.APIResponse{Success: false, Message: "Not found"})
		return
	}
	id, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid sub-vendo id"})
		return
	}
	action := "update"
	if len(parts) >= 5 {
		action = parts[4]
	}

	switch action {
	case "update":
		h.update(w, r, id)
	case "delete":
		h.delete(w, r, id)
	default:
		sendJSON(w, http.StatusNotFound, models.APIResponse{Success: false, Message: "Unknown action"})
	}
}

// update handles POST /api/admin/subvendos/<id> {name?, site?, vlan_iface?, enabled?}
func (h *SubVendoHandler) update(w http.ResponseWriter, r *http.Request, id int64) {
	var req struct {
		Name      *string `json:"name"`
		Site      *string `json:"site"`
		VlanIface *string `json:"vlan_iface"`
		Enabled   *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid body"})
		return
	}
	if req.Name != nil {
		h.DB.Exec(`UPDATE sub_vendos SET name = $1 WHERE id = $2`, strings.TrimSpace(*req.Name), id)
	}
	if req.Site != nil {
		h.DB.Exec(`UPDATE sub_vendos SET site = $1 WHERE id = $2`, strings.TrimSpace(*req.Site), id)
	}
	if req.VlanIface != nil {
		h.DB.Exec(`UPDATE sub_vendos SET vlan_iface = $1 WHERE id = $2`, strings.TrimSpace(*req.VlanIface), id)
	}
	if req.Enabled != nil {
		h.DB.Exec(`UPDATE sub_vendos SET enabled = $1 WHERE id = $2`, *req.Enabled, id)
		if !*req.Enabled {
			disarmSubvendo(h.DB, id)
		}
	}
	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Updated"})
}

// delete handles POST /api/admin/subvendos/<id>/delete
func (h *SubVendoHandler) delete(w http.ResponseWriter, r *http.Request, id int64) {
	// Revoke the captive-portal bypass BEFORE the row disappears — the MAC
	// lookup needs the row to still exist.
	var mac string
	h.DB.QueryRow(`SELECT COALESCE(mac,'') FROM sub_vendos WHERE id = $1`, id).Scan(&mac)
	captiveBypass(mac, false)

	if _, err := h.DB.Exec(`DELETE FROM sub_vendos WHERE id = $1`, id); err != nil {
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to delete"})
		return
	}
	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Deleted"})
}

// Accept handles POST /api/admin/subvendos/accept {id, vlan_iface?} → pending → online
// SIMPLIFIED MODEL (v1.25.0): no VLAN binding required, no isolation. Any
// registered unit can be accepted. An optional vlan_iface may still be stored
// for reporting, but nothing is gated on it.
func (h *SubVendoHandler) Accept(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID        int64  `json:"id"`
		VlanIface string `json:"vlan_iface"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID <= 0 {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "id required"})
		return
	}

	// Optional VLAN label — informational only, never a gate.
	if vlan := strings.TrimSpace(req.VlanIface); vlan != "" {
		h.DB.Exec(`UPDATE sub_vendos SET vlan_iface = $1 WHERE id = $2`, vlan, req.ID)
	}

	res, err := h.DB.Exec(`
		UPDATE sub_vendos SET status = 'online', enabled = true, last_seen = NOW()
		WHERE id = $1 AND status != 'online'`, req.ID)
	if err != nil {
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: err.Error()})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Unit not found or already online."})
		return
	}

	// Best-effort captive-portal bypass for the unit's MAC. If the portal is
	// not present or the MAC is unknown this is a no-op — units are never
	// blocked on it in the simplified model.
	var mac string
	h.DB.QueryRow(`SELECT COALESCE(mac,'') FROM sub_vendos WHERE id = $1`, req.ID).Scan(&mac)
	captiveBypass(mac, true)

	log.Printf("[subvendo] admin accepted unit #%d → online", req.ID)
	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Unit accepted and online"})
}

// Reject handles POST /api/admin/subvendos/reject {id} → rejected
func (h *SubVendoHandler) Reject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID <= 0 {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "id required"})
		return
	}
	if _, err := h.DB.Exec(`UPDATE sub_vendos SET status = 'rejected', enabled = false WHERE id = $1`, req.ID); err != nil {
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: err.Error()})
		return
	}
	// Revoke the captive-portal bypass — a rejected unit must not keep
	// portal-free access to the network.
	var mac string
	h.DB.QueryRow(`SELECT COALESCE(mac,'') FROM sub_vendos WHERE id = $1`, req.ID).Scan(&mac)
	captiveBypass(mac, false)
	log.Printf("[subvendo] admin rejected unit #%d", req.ID)
	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Unit rejected"})
}

// ============================================
// DEVICE API (NodeMCU, shared-token auth)
// ============================================

// authDevice validates the shared Bearer token and resolves the device_id
// to a sub-vendo row. The shared token is compared in constant time.
func (h *SubVendoHandler) authDevice(r *http.Request, deviceID string) (*subVendo, bool) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return nil, false
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	if sharedSubvendoToken == "" || token != sharedSubvendoToken {
		return nil, false
	}
	u, err := subvendoByID(h.DB, deviceID)
	if err != nil {
		return nil, false
	}
	return u, true
}

// Register handles POST /api/subvendo/register
// Body: {"device_id": "nodemcu-xxxx", "ssid": "MyHotspot"}.
// One shared token for all units. The server auto-binds the unit to its VLAN
// from the SSID→VLAN map and creates a 'pending' row. The admin then Accepts.
func (h *SubVendoHandler) Register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		DeviceID string `json:"device_id"`
		SSID     string `json:"ssid"`
		MAC      string `json:"mac"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid body"})
		return
	}
	deviceID := strings.TrimSpace(req.DeviceID)
	if deviceID == "" {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "device_id required"})
		return
	}

	// Shared-token auth: the NodeMCU sends the same token on register as on poll
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		sendJSON(w, http.StatusUnauthorized, models.APIResponse{Success: false, Message: "Unauthorized"})
		return
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	if sharedSubvendoToken == "" || token != sharedSubvendoToken {
		sendJSON(w, http.StatusUnauthorized, models.APIResponse{Success: false, Message: "Unauthorized"})
		return
	}

	// Auto-bind VLAN from SSID map
	vlanIface := vlanForSSID(h.DB, req.SSID)

	// Normalize the reported MAC (aa:bb:cc:dd:ee:ff) — used for the
	// captive-portal bypass punched in when the admin accepts the unit.
	mac := strings.ToLower(strings.TrimSpace(req.MAC))
	if mac != "" && !validMACFormat(mac) {
		log.Printf("[subvendo] device %s reported a malformed MAC %q — ignoring it (captive bypass will need the MAC set manually)", deviceID, req.MAC)
		mac = ""
	}

	// Insert or refresh pending row (idempotent)
	var id int64
	var existingStatus string
	err := h.DB.QueryRow(`
		INSERT INTO sub_vendos (device_id, name, ssid, mac, vlan_iface, status, enabled)
		VALUES ($1, $2, $3, $4, $5, 'pending', true)
		ON CONFLICT (device_id) DO UPDATE
		  SET ssid = EXCLUDED.ssid, mac = EXCLUDED.mac,
		      vlan_iface = EXCLUDED.vlan_iface,
		      status = CASE WHEN sub_vendos.status = 'rejected' THEN 'pending' ELSE sub_vendos.status END,
		      last_seen = NOW()
		RETURNING id, status`,
		deviceID, "Sub-Vendo "+deviceID, req.SSID, mac, vlanIface,
	).Scan(&id, &existingStatus)
	if err != nil {
		log.Printf("[subvendo] register failed: %v", err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Registration failed"})
		return
	}

	log.Printf("[subvendo] device %s registered (ssid=%s, vlan=%s, id=%d, status=%s)", deviceID, req.SSID, vlanIface, id, existingStatus)
	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: map[string]interface{}{
		"id":           id,
		"vlan_iface":   vlanIface,
		"status":       existingStatus,
		"approved":     existingStatus == "online",
		"idle_poll_ms": 2000,
		"arm_poll_ms":  500,
		"message":      "Registered",
	}})
}

// State handles GET /api/subvendo/state?device_id=xxx — device poll + heartbeat.
// Returns 402 "pending" until the admin Accepts the unit (approval gate).
func (h *SubVendoHandler) State(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	deviceID := r.URL.Query().Get("device_id")
	u, ok := h.authDevice(r, deviceID)
	if !ok {
		sendJSON(w, http.StatusUnauthorized, models.APIResponse{Success: false, Message: "Unauthorized"})
		return
	}
	// Approval gate: pending/rejected units get 402, not arm data
	if u.Status != "online" {
		sendJSON(w, http.StatusPaymentRequired, models.APIResponse{Success: false, Data: map[string]interface{}{
			"status":       u.Status,
			"idle_poll_ms": 3000,
			"message":      "Waiting for admin approval",
		}})
		return
	}
	h.DB.Exec(`UPDATE sub_vendos SET last_seen = NOW() WHERE id = $1`, u.ID)

	armed, windowStart, expiresAt := subvendoArmWindow(h.DB, u.ID)
	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: map[string]interface{}{
		"armed":        armed,
		"armed_at":     windowStart,
		"expires_at":   expiresAt,
		"server_time":  time.Now().Unix(),
		"idle_poll_ms": 2000,
		"arm_poll_ms":  500,
	}})
}

// Coins handles POST /api/subvendo/coins  {device_id, pulses}
func (h *SubVendoHandler) Coins(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		DeviceID string `json:"device_id"`
		Pulses   int    `json:"pulses"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	u, ok := h.authDevice(r, req.DeviceID)
	if !ok {
		sendJSON(w, http.StatusUnauthorized, models.APIResponse{Success: false, Message: "Unauthorized"})
		return
	}
	// Only online (accepted) units may report coins
	if u.Status != "online" {
		sendJSON(w, http.StatusPaymentRequired, models.APIResponse{Success: false, Message: "Not approved yet"})
		return
	}
	if req.Pulses < 1 || req.Pulses > 50 {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "pulses must be 1-50"})
		return
	}

	id := u.ID
	armed, _, _ := subvendoArmWindow(h.DB, id)
	if !armed {
		armSubvendo(h.DB, id, 30)
	}

	source := "subvendo:" + strconv.FormatInt(id, 10)
	for i := 0; i < req.Pulses; i++ {
		if _, err := h.DB.Exec(
			`INSERT INTO coin_events (coin_value, source) VALUES (1, $1)`, source,
		); err != nil {
			log.Printf("[subvendo #%d] coin insert: %v", id, err)
			sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to record coin"})
			return
		}
	}

	now := time.Now().Unix()
	newExpiry := now + int64(defaultArmDuration)*int64(req.Pulses)
	if cap := now + int64(maxArmDuration); newExpiry > cap {
		newExpiry = cap
	}
	h.DB.Exec(`UPDATE sub_vendos
		SET armed_until = GREATEST(armed_until, $2), total_coins = total_coins + $3, last_seen = NOW()
		WHERE id = $1`, id, newExpiry, req.Pulses)

	log.Printf("[subvendo #%d] +%d coin(s)", id, req.Pulses)
	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: map[string]interface{}{
		"credited":   req.Pulses,
		"expires_at": newExpiry,
	}})
}

// Approved handles GET /api/subvendo/approved?device_id=xxx — lightweight
// poll for pending units to check if the admin has accepted them.
func (h *SubVendoHandler) Approved(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	deviceID := r.URL.Query().Get("device_id")
	u, ok := h.authDevice(r, deviceID)
	if !ok {
		sendJSON(w, http.StatusUnauthorized, models.APIResponse{Success: false, Message: "Unauthorized"})
		return
	}
	if u.Status == "online" {
		sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: map[string]interface{}{
			"approved": true,
			"status":   "online",
			"vlan_iface": u.VlanIface,
		}})
		return
	}
	// Not yet approved — tell the firmware how long to wait
	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: map[string]interface{}{
		"approved":      false,
		"status":        u.Status,
		"idle_poll_ms":  3000,
	}})
}

// subvendoByID fetches a single unit by device_id.
func subvendoByID(db *sql.DB, deviceID string) (*subVendo, error) {
	var u subVendo
	var armedUntil int64
	var lastSeen sql.NullTime
	err := db.QueryRow(`
		SELECT id, device_id, name, site, vlan_iface, ssid, COALESCE(mac,''), status, enabled,
		       COALESCE(total_coins,0), armed_until, last_seen, created_at
		FROM sub_vendos WHERE device_id = $1`, deviceID,
	).Scan(&u.ID, &u.DeviceID, &u.Name, &u.Site, &u.VlanIface, &u.SSID, &u.MAC,
		&u.Status, &u.Enabled, &u.TotalCoins, &armedUntil, &lastSeen, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	if lastSeen.Valid {
		t := lastSeen.Time
		u.LastSeen = &t
		u.Online = time.Since(t) < onlineWindow
	}
	u.Armed = armedUntil > time.Now().Unix()
	return &u, nil
}

// subvendosForVLAN returns all enabled+online units for the portal picker.
// SIMPLIFIED MODEL (v1.25.0): no VLAN isolation — every online unit is
// offered to every portal client. The iface argument is kept for signature
// compatibility but is ignored.
func subvendosForVLAN(db *sql.DB, iface string) ([]subVendo, error) {
	if db == nil {
		return nil, nil
	}
	rows, err := db.Query(`
		SELECT id, device_id, name, site, vlan_iface, ssid, COALESCE(mac,''), status, enabled,
		       COALESCE(total_coins,0), armed_until, last_seen, created_at
		FROM sub_vendos WHERE enabled = true AND status = 'online'
		ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var units []subVendo
	for rows.Next() {
		var u subVendo
		var armedUntil int64
		var lastSeen sql.NullTime
		if err := rows.Scan(&u.ID, &u.DeviceID, &u.Name, &u.Site, &u.VlanIface,
			&u.SSID, &u.MAC, &u.Status, &u.Enabled, &u.TotalCoins, &armedUntil, &lastSeen, &u.CreatedAt); err != nil {
			continue
		}
		if lastSeen.Valid {
			t := lastSeen.Time
			u.LastSeen = &t
			u.Online = time.Since(t) < onlineWindow
		}
		u.Armed = armedUntil > time.Now().Unix()
		units = append(units, u)
	}
	return units, nil
}


