package handlers

import (
	"aircoins-api/models"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// wan.go manages WAN configuration: auto-detect the true WAN port,
// persist mode/static/VLAN settings to system_settings, and optionally
// apply to the OS (dhcpcd.conf or VLAN interface creation).

// ============================================
// WAN PORT AUTO-DETECTION
// ============================================

// detectWANIface returns the interface carrying the default route by
// parsing `ip -o route show default`. Returns "" when no default route
// exists (e.g. no uplink connected).
func detectWANIface() string {
	out, err := exec.Command("ip", "-o", "route", "show", "default").Output()
	if err != nil || len(out) == 0 {
		return ""
	}
	// Typical output: "default via 192.168.1.1 dev eth0 proto dhcp metric 100"
	re := regexp.MustCompile(`dev\s+(\S+)`)
	m := re.FindStringSubmatch(string(out))
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// ============================================
// AVAILABLE VLANS (not claimed by any portal)
// ============================================

// availableVLANs returns VLANs that exist on the system but are NOT
// currently claimed by any portal server. The admin picks one of these
// as the ISP VLAN in "VLAN DHCP" mode.
func availableVLANs() []models.WANAvailableVLAN {
	// 1. All VLANs on the system (from ip link show type vlan)
	out, err := exec.Command("ip", "-d", "link", "show", "type", "vlan").Output()
	if err != nil {
		log.Printf("availableVLANs: failed to list VLANs: %v", err)
		out = []byte{}
	}

	vlanRe := regexp.MustCompile(`^\d+:\s+(\S+)@(\S+):`)
	idRe := regexp.MustCompile(`vlan\s+id\s+(\d+)`)

	type vlanEntry struct {
		iface string // e.g. eth0.100
		vid   int
	}
	var systemVLANs []vlanEntry
	lines := strings.Split(string(out), "\n")
	var currentIface string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if m := vlanRe.FindStringSubmatch(trimmed); m != nil {
			currentIface = m[1]
			continue
		}
		if currentIface == "" {
			continue
		}
		if m := idRe.FindStringSubmatch(trimmed); m != nil {
			vid, _ := strconv.Atoi(m[1])
			systemVLANs = append(systemVLANs, vlanEntry{iface: currentIface, vid: vid})
			currentIface = ""
		}
	}

	// Also include VLANs from the DB config that may not be in the kernel
	for _, cfg := range readVLANConfig() {
		name := fmt.Sprintf("%s.%d", cfg.Interface, cfg.VLANID)
		found := false
		for _, sv := range systemVLANs {
			if sv.iface == name {
				found = true
				break
			}
		}
		if !found {
			systemVLANs = append(systemVLANs, vlanEntry{iface: name, vid: cfg.VLANID})
		}
	}

	// 2. VLANs claimed by portal servers (from portal_servers table)
	portalIfaceSet := make(map[string]bool)
	rows, err := models.DB.Query("SELECT interface FROM portal_servers")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var iface string
			if err := rows.Scan(&iface); err == nil {
				portalIfaceSet[iface] = true
			}
		}
	}

	// 3. Filter: system VLANs minus portal-claimed ones
	var result []models.WANAvailableVLAN
	for _, sv := range systemVLANs {
		if portalIfaceSet[sv.iface] {
			continue
		}
		result = append(result, models.WANAvailableVLAN{
			VLANID: sv.vid,
			Iface:  sv.iface,
		})
	}
	if result == nil {
		result = []models.WANAvailableVLAN{}
	}
	return result
}

// ============================================
// GET /api/admin/wan
// ============================================

// WANGet returns the current WAN config, auto-detected interface, and
// the available (unused) VLAN list inline.
func WANGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	iface := detectWANIface()

	// Load persisted config from system_settings
	cfg := loadWANConfig()

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"success":         true,
		"iface":           iface,
		"mode":            cfg.Mode,
		"static_config":   cfg.StaticConfig,
		"vlan_id":         cfg.VLANID,
		"available_vlans": availableVLANs(),
	})
}

// ============================================
// GET /api/admin/wan/available-vlans
// ============================================

// WANAvailableVLANs returns just the VLAN list (for dropdown refresh).
func WANAvailableVLANs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sendJSON(w, http.StatusOK, map[string]interface{}{
		"available_vlans": availableVLANs(),
	})
}

// ============================================
// POST /api/admin/wan
// ============================================

// WANPost validates, persists, and optionally applies the WAN config.
func WANPost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req models.WANRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid JSON: " + err.Error()})
		return
	}

	// Validate mode
	switch req.Mode {
	case "dhcp":
		// No extra fields required
	case "static":
		if req.StaticConfig == nil {
			sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Static mode requires static_config"})
			return
		}
		if err := validateStaticConfig(req.StaticConfig); err != nil {
			sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: err.Error()})
			return
		}
	case "vlan_dhcp":
		if req.VLANID == nil || *req.VLANID < 1 || *req.VLANID > 4094 {
			sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "VLAN DHCP mode requires a valid vlan_id (1-4094)"})
			return
		}
	default:
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid mode: must be dhcp, static, or vlan_dhcp"})
		return
	}

	// Build the config to persist
	cfg := models.WANConfig{
		Mode:         req.Mode,
		StaticConfig: req.StaticConfig,
		VLANID:       req.VLANID,
	}

	// Persist to system_settings (key: wan_config, JSON value)
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to serialize config"})
		return
	}

	_, err = models.DB.Exec(`
		INSERT INTO system_settings (key, value, updated_at)
		VALUES ('wan_config', $1, NOW())
		ON CONFLICT (key) DO UPDATE SET value = $1, updated_at = NOW()
	`, string(cfgJSON))
	if err != nil {
		log.Printf("WANPost: failed to persist wan_config: %v", err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to save config to DB"})
		return
	}

	log.Printf("WANPost: wan_config saved — mode=%s", req.Mode)

	// Optionally apply to OS
	var applyResult map[string]interface{}
	if req.ApplyToOS {
		applyResult = applyWANToOS(cfg)
	}

	resp := map[string]interface{}{
		"success": true,
		"message": "WAN config saved",
	}
	if applyResult != nil {
		resp["apply"] = applyResult
	}
	sendJSON(w, http.StatusOK, resp)
}

// ============================================
// VALIDATION
// ============================================

// validateStaticConfig checks IP, subnet, gateway, DNS fields.
func validateStaticConfig(sc *models.WANStaticConfig) error {
	if sc.IP == "" || sc.Subnet == "" || sc.Gateway == "" || sc.DNS1 == "" || sc.DNS2 == "" {
		return fmt.Errorf("all static fields are required: ip, subnet, gateway, dns1, dns2")
	}
	if net.ParseIP(sc.IP) == nil {
		return fmt.Errorf("invalid IP address: %s", sc.IP)
	}
	if net.ParseIP(sc.Gateway) == nil {
		return fmt.Errorf("invalid gateway: %s", sc.Gateway)
	}
	if net.ParseIP(sc.DNS1) == nil {
		return fmt.Errorf("invalid DNS1: %s", sc.DNS1)
	}
	if net.ParseIP(sc.DNS2) == nil {
		return fmt.Errorf("invalid DNS2: %s", sc.DNS2)
	}
	// Subnet: accept CIDR (e.g. /24) or dotted-quad (e.g. 255.255.255.0)
	if !strings.HasPrefix(sc.Subnet, "/") {
		if net.ParseIP(sc.Subnet) == nil {
			// Try as CIDR suffix
			if _, err := strconv.Atoi(strings.TrimPrefix(sc.Subnet, "/")); err != nil {
				return fmt.Errorf("invalid subnet mask: %s (use CIDR like /24 or dotted-quad like 255.255.255.0)", sc.Subnet)
			}
		}
	}
	return nil
}

// ============================================
// DB HELPERS
// ============================================

// loadWANConfig reads the wan_config JSON from system_settings.
func loadWANConfig() models.WANConfig {
	var val string
	err := models.DB.QueryRow("SELECT value FROM system_settings WHERE key='wan_config'").Scan(&val)
	if err != nil {
		// No config yet — default to DHCP
		return models.WANConfig{Mode: "dhcp"}
	}
	var cfg models.WANConfig
	if err := json.Unmarshal([]byte(val), &cfg); err != nil {
		log.Printf("loadWANConfig: failed to parse stored config: %v", err)
		return models.WANConfig{Mode: "dhcp"}
	}
	return cfg
}

// ============================================
// OS APPLY
// ============================================

// subnetToCIDR converts a dotted-quad subnet mask to a CIDR prefix length.
// E.g. "255.255.255.0" -> "24". If already a CIDR suffix ("/24"), returns "24".
func subnetToCIDR(subnet string) string {
	s := strings.TrimPrefix(subnet, "/")
	if _, err := strconv.Atoi(s); err == nil {
		return s // already a CIDR prefix
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return "24" // fallback
	}
	mask := ip.To4()
	if mask == nil {
		return "24"
	}
	ones, _ := net.IPv4Mask(mask[0], mask[1], mask[2], mask[3]).Size()
	return strconv.Itoa(ones)
}

// applyWANToOS applies the WAN configuration to the operating system.
// It is defensive: errors are returned in the response but never cause
// a rollback — the admin opted in knowing the risks.
func applyWANToOS(cfg models.WANConfig) map[string]interface{} {
	wanIface := detectWANIface()
	if wanIface == "" {
		// Fallback: try the saved eth_interface setting
		var eth string
		if err := models.DB.QueryRow("SELECT value FROM system_settings WHERE key='eth_interface'").Scan(&eth); err == nil && eth != "" {
			wanIface = eth
		}
	}
	if wanIface == "" {
		return map[string]interface{}{
			"success": false,
			"error":   "no WAN interface detected and no eth_interface setting found",
		}
	}

	switch cfg.Mode {
	case "dhcp":
		return applyDHCP(wanIface)
	case "static":
		return applyStatic(wanIface, cfg.StaticConfig)
	case "vlan_dhcp":
		return applyVLANDHCP(wanIface, cfg)
	}
	return map[string]interface{}{"success": true, "message": "no OS changes needed"}
}

// applyDHCP ensures the WAN interface uses DHCP. If switching from
// Static, removes any static config from dhcpcd.conf and restarts.
func applyDHCP(wanIface string) map[string]interface{} {
	// Remove any static block we previously wrote to dhcpcd.conf
	if err := removeStaticFromDhcpcd(wanIface); err != nil {
		log.Printf("applyDHCP: warning: %v", err)
	}

	// Restart dhcpcd to pick up changes (best-effort)
	if out, err := exec.Command("systemctl", "restart", "dhcpcd").CombinedOutput(); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("systemctl restart dhcpcd failed: %s", string(out)),
		}
	}
	return map[string]interface{}{"success": true, "message": "DHCP applied to " + wanIface}
}

// applyStatic writes a static IP config to /etc/dhcpcd.conf and restarts
// the dhcpcd service.
// File written: /etc/dhcpcd.conf (appends a marked block)
func applyStatic(wanIface string, sc *models.WANStaticConfig) map[string]interface{} {
	cidr := subnetToCIDR(sc.Subnet)

	// Build the static block
	marker := "# --- AirCoins WAN static config BEGIN ---"
	endMarker := "# --- AirCoins WAN static config END ---"
	block := fmt.Sprintf(`%s
interface %s
static ip_address=%s/%s
static routers=%s
static domain_name_servers=%s %s
%s
`, marker, wanIface, sc.IP, cidr, sc.Gateway, sc.DNS1, sc.DNS2, endMarker)

	// Read existing dhcpcd.conf, strip any previous AirCoins block
	confPath := "/etc/dhcpcd.conf"
	existing, _ := os.ReadFile(confPath)
	cleaned := stripBlock(string(existing), marker, endMarker)

	newContent := strings.TrimRight(cleaned, "\n") + "\n\n" + block
	if err := os.WriteFile(confPath, []byte(newContent), 0644); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("failed to write %s: %v", confPath, err),
		}
	}

	// Restart dhcpcd
	if out, err := exec.Command("systemctl", "restart", "dhcpcd").CombinedOutput(); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("wrote %s but systemctl restart dhcpcd failed: %s", confPath, string(out)),
		}
	}
	return map[string]interface{}{"success": true, "message": fmt.Sprintf("Static IP %s/%s applied to %s", sc.IP, cidr, wanIface)}
}

// applyVLANDHCP creates the VLAN interface if missing, brings it up,
// and requests DHCP on it.
// NOTE: This brings the VLAN interface up with DHCP but does NOT set up
// policy routing or adjust the default route metric. The admin may need
// to configure routing separately if the ISP VLAN should become the
// primary WAN path.
func applyVLANDHCP(wanIface string, cfg models.WANConfig) map[string]interface{} {
	if cfg.VLANID == nil {
		return map[string]interface{}{"success": false, "error": "vlan_id is required"}
	}
	vid := *cfg.VLANID
	vlanName := fmt.Sprintf("%s.%d", wanIface, vid)

	// Create VLAN interface if it doesn't exist
	if _, err := os.Stat("/sys/class/net/" + vlanName); err != nil {
		if out, err := exec.Command("ip", "link", "add", "link", wanIface,
			"name", vlanName, "type", "vlan", "id", strconv.Itoa(vid)).CombinedOutput(); err != nil {
			return map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("failed to create VLAN interface %s: %s", vlanName, string(out)),
			}
		}
	}

	// Bring the interface up
	if out, err := exec.Command("ip", "link", "set", vlanName, "up").CombinedOutput(); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("failed to bring up %s: %s", vlanName, string(out)),
		}
	}

	// Request DHCP via dhcpcd on the VLAN interface
	if out, err := exec.Command("dhcpcd", vlanName).CombinedOutput(); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("dhcpcd %s failed: %s", vlanName, string(out)),
		}
	}

	return map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("VLAN %s created and DHCP requested (note: default route may need manual policy routing)", vlanName),
	}
}

// ============================================
// dhcpcd.conf HELPERS
// ============================================

// stripBlock removes a previously-written AirCoins block from a config file.
func stripBlock(content, beginMarker, endMarker string) string {
	beginIdx := strings.Index(content, beginMarker)
	if beginIdx < 0 {
		return content
	}
	endIdx := strings.Index(content, endMarker)
	if endIdx < 0 || endIdx < beginIdx {
		return content
	}
	endIdx += len(endMarker)
	// Consume the trailing newline after the end marker
	if endIdx < len(content) && content[endIdx] == '\n' {
		endIdx++
	}
	return content[:beginIdx] + content[endIdx:]
}

// removeStaticFromDhcpcd strips any AirCoins static block from dhcpcd.conf.
func removeStaticFromDhcpcd(iface string) error {
	confPath := "/etc/dhcpcd.conf"
	data, err := os.ReadFile(confPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	marker := "# --- AirCoins WAN static config BEGIN ---"
	endMarker := "# --- AirCoins WAN static config END ---"
	cleaned := stripBlock(string(data), marker, endMarker)
	if cleaned == string(data) {
		return nil // nothing to remove
	}
	return os.WriteFile(confPath, []byte(cleaned), 0644)
}
