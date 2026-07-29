package handlers

import (
	"aircoins-api/models"
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const vlanConfigPath = "/etc/pisowifi/vlans.conf"

// captiveRulesPath is the helper that installs/removes the per-VLAN captive
// portal iptables rules (DNS + HTTP capture, MASQUERADE for paying clients).
const captiveRulesPath = "/usr/local/bin/aircoins-captive-rules"

// applyCaptiveRules runs aircoins-captive-rules for a VLAN. action is "add" or
// "del". Failures are logged but never fatal: a VLAN without captive rules is
// still usable, it just won't auto-pop the portal.
func applyCaptiveRules(action, vlanName, ipCIDR string) {
	gateway := strings.Split(ipCIDR, "/")[0]
	if gateway == "" {
		log.Printf("applyCaptiveRules: skipping %s for %s: no gateway IP", action, vlanName)
		return
	}

	if _, err := os.Stat(captiveRulesPath); err != nil {
		log.Printf("applyCaptiveRules: %s not installed; skipping captive rules for %s", captiveRulesPath, vlanName)
		return
	}

	if out, err := exec.Command(captiveRulesPath, action, vlanName, gateway).CombinedOutput(); err != nil {
		log.Printf("applyCaptiveRules: %s %s %s failed: %v — %s", action, vlanName, gateway, err, string(out))
		return
	}

	log.Printf("applyCaptiveRules: %s captive rules for %s (%s)", action, vlanName, gateway)
}

// writeNetworkdConfig writes a per-VLAN systemd-networkd .network file so
// networkd OWNS the static IP instead of flushing it as a foreign address on
// every `networkctl reload`. The 04- prefix makes it win (lexically) over both
// the 05-aircoins-vlans.network catch-all and the 10-netplan runtime config,
// since networkd applies only the FIRST matching .network file.
func writeNetworkdConfig(vlanName, cidr string) error {
	content := fmt.Sprintf(`[Match]
Name=%s

[Network]
Address=%s
DHCP=no
ConfigureWithoutCarrier=yes
`, vlanName, cidr)
	path := fmt.Sprintf("/etc/systemd/network/04-aircoins-%s.network", vlanName)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return err
	}
	exec.Command("networkctl", "reload").Run() // best-effort
	return nil
}

// removeNetworkdConfig deletes the per-VLAN networkd file and reloads networkd.
func removeNetworkdConfig(vlanName string) {
	os.Remove(fmt.Sprintf("/etc/systemd/network/04-aircoins-%s.network", vlanName))
	exec.Command("networkctl", "reload").Run()
}

// ============================================
// VLAN LIST
// ============================================

// VLANList returns all active VLANs merged with saved config descriptions.
func VLANList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get active VLANs from ip command
	out, err := exec.Command("ip", "-d", "link", "show", "type", "vlan").Output()
	if err != nil {
		log.Printf("Warning: failed to list active VLANs: %v", err)
		// Continue with empty active list; saved config entries will still be returned
		out = []byte{}
	}

	// Parse ip -d link show type vlan output
	// Block format:
	//   N: name@parent: <FLAGS> ...
	//       ...
	//       vlan id X ...
	vlanRe := regexp.MustCompile(`^\d+:\s+(\S+)@(\S+):`)
	idRe := regexp.MustCompile(`vlan\s+id\s+(\d+)`)

	var activeVLANs []models.VLANInfo
	lines := strings.Split(string(out), "\n")
	var currentIface, currentParent string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if m := vlanRe.FindStringSubmatch(trimmed); m != nil {
			currentIface = m[1]
			currentParent = m[2]
			continue
		}

		if currentIface == "" {
			continue
		}

		if m := idRe.FindStringSubmatch(trimmed); m != nil {
			vid, _ := strconv.Atoi(m[1])

			// Get IP for this VLAN interface
			ip := getVLANIP(currentIface)

			activeVLANs = append(activeVLANs, models.VLANInfo{
				Interface:  currentParent,
				VLANID:     vid,
				Name:       currentIface,
				IP:         ip,
				Active:     true,
				DHCPActive: isDHCPActive(currentIface),
			})

			currentIface = ""
			currentParent = ""
		}
	}

	// Merge with saved config (descriptions + portal flag)
	cfgEntries := readVLANConfig()
	cfgMap := make(map[string]*vlanConfigEntry)
	for i := range cfgEntries {
		key := fmt.Sprintf("%s.%d", cfgEntries[i].Interface, cfgEntries[i].VLANID)
		cfgMap[key] = &cfgEntries[i]
	}

	for i := range activeVLANs {
		key := fmt.Sprintf("%s.%d", activeVLANs[i].Interface, activeVLANs[i].VLANID)
		if cfg, ok := cfgMap[key]; ok {
			activeVLANs[i].Description = cfg.Description
			activeVLANs[i].IsPortal = cfg.IsPortal
			activeVLANs[i].StartIP = cfg.StartIP
			if cfg.IP != "" && activeVLANs[i].IP == "" {
				activeVLANs[i].IP = cfg.IP
			}
		}
	}

	// Heal active VLANs: regenerate outdated dnsmasq configs and make sure DHCP
	// plus the captive portal rules are actually in place.
	//
	// A config is outdated when it predates the captive portal support: those
	// configs use port=0 (DNS disabled) and lack the address=/#/ wildcard, so
	// probe requests are never captured and the portal never pops up.
	for i := range activeVLANs {
		confPath := fmt.Sprintf("/etc/dnsmasq.d/%s.conf", activeVLANs[i].Name)
		confBytes, err := os.ReadFile(confPath)
		if err != nil {
			continue
		}
		conf := string(confBytes)

		// Determine the start_ip from the saved config entry (if available)
		key := fmt.Sprintf("%s.%d", activeVLANs[i].Interface, activeVLANs[i].VLANID)
		var startIP string
		if cfg, ok := cfgMap[key]; ok {
			startIP = cfg.StartIP
		}

		ipCIDR := activeVLANs[i].IP
		if ipCIDR == "" {
			// Fall back to saved config IP
			if cfg, ok := cfgMap[key]; ok {
				ipCIDR = cfg.IP
			}
		}

		needsRegen := !strings.Contains(conf, "bind-dynamic") ||
			!strings.Contains(conf, "address=/#/") ||
			strings.Contains(conf, "port=0")
		unitName := fmt.Sprintf("dnsmasq@%s", activeVLANs[i].Name)

		switch {
		case needsRegen:
			log.Printf("VLANList: VLAN %s has outdated dnsmasq config (no captive DNS hijack); regenerating", activeVLANs[i].Name)
			if ipCIDR == "" {
				log.Printf("VLANList: cannot regenerate config for %s: no IP available", activeVLANs[i].Name)
				continue
			}
			// startDHCP stops the old service, writes new config, and starts it
			if err := startDHCP(activeVLANs[i].Name, activeVLANs[i].VLANID, ipCIDR, startIP); err != nil {
				log.Printf("VLANList: failed to regenerate+restart DHCP for %s: %v", activeVLANs[i].Name, err)
				continue
			}
			activeVLANs[i].DHCPActive = true
			log.Printf("VLANList: regenerated and restarted DHCP for %s", activeVLANs[i].Name)

		case !activeVLANs[i].DHCPActive:
			log.Printf("VLANList: VLAN %s is active but DHCP is not running; attempting auto-start", activeVLANs[i].Name)
			if out, err := exec.Command("systemctl", "start", unitName).CombinedOutput(); err != nil {
				log.Printf("VLANList: auto-start DHCP failed for %s: %v — %s", activeVLANs[i].Name, err, string(out))
				continue
			}
			if out, err := exec.Command("systemctl", "enable", unitName).CombinedOutput(); err != nil {
				log.Printf("VLANList: auto-start: warning: failed to enable %s: %v — %s", unitName, err, string(out))
			}
			log.Printf("VLANList: auto-started DHCP for %s", activeVLANs[i].Name)
			activeVLANs[i].DHCPActive = true
		}

		// Captive rules are idempotent, so this also repairs VLANs that were
		// created before captive portal support existed.
		if ipCIDR != "" {
			applyCaptiveRules("add", activeVLANs[i].Name, ipCIDR)
		}

		// Heal: make sure networkd owns the static IP so it survives
		// `networkctl reload`. Repairs VLANs created before this fix existed.
		netFile := fmt.Sprintf("/etc/systemd/network/04-aircoins-%s.network", activeVLANs[i].Name)
		if _, err := os.Stat(netFile); os.IsNotExist(err) && ipCIDR != "" {
			if err := writeNetworkdConfig(activeVLANs[i].Name, ipCIDR); err != nil {
				log.Printf("VLANList: warning: failed to write networkd config for %s: %v", activeVLANs[i].Name, err)
			} else {
				log.Printf("VLANList: created missing networkd config for %s", activeVLANs[i].Name)
			}
		}
	}

	// Build a set of active VLAN keys for quick lookup
	activeSet := make(map[string]bool)
	for i := range activeVLANs {
		key := fmt.Sprintf("%s.%d", activeVLANs[i].Interface, activeVLANs[i].VLANID)
		activeSet[key] = true
	}

	// Add saved config entries that are not currently active
	for _, cfg := range cfgEntries {
		key := fmt.Sprintf("%s.%d", cfg.Interface, cfg.VLANID)
		if !activeSet[key] {
			activeVLANs = append(activeVLANs, models.VLANInfo{
				Interface:   cfg.Interface,
				VLANID:      cfg.VLANID,
				Name:        fmt.Sprintf("%s.%d", cfg.Interface, cfg.VLANID),
				IP:          cfg.IP,
				Description: cfg.Description,
				IsPortal:    cfg.IsPortal,
				Active:      false,
				StartIP:     cfg.StartIP,
			})
		}
	}

	if activeVLANs == nil {
		activeVLANs = []models.VLANInfo{}
	}

	sendJSON(w, http.StatusOK, map[string]interface{}{"vlans": activeVLANs})
}

// ============================================
// VLAN CREATE
// ============================================

// VLANCreate creates a new VLAN interface and persists the config.
func VLANCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req models.VLANRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid JSON: " + err.Error()})
		return
	}

	// Validate VLAN ID
	if req.VLANID < 1 || req.VLANID > 4094 {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "VLAN ID must be between 1 and 4094"})
		return
	}

	// Validate interface exists and is not lo or a path-traversal attempt
	if !validateIfaceName(req.Interface) {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid interface name"})
		return
	}
	if _, err := os.Stat("/sys/class/net/" + req.Interface); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Interface does not exist: " + req.Interface})
		return
	}

	// Validate IP
	if net.ParseIP(req.IP) == nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid IP address"})
		return
	}

	// Validate netmask and convert to CIDR prefix length
	cidr, err := netmaskToCIDR(req.Netmask)
	if err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: err.Error()})
		return
	}

	vlanName := fmt.Sprintf("%s.%d", req.Interface, req.VLANID)
	ipCIDR := fmt.Sprintf("%s/%d", req.IP, cidr)

	// 1. Create VLAN interface
	cmdOut, err := exec.Command("ip", "link", "add", "link", req.Interface, "name", vlanName, "type", "vlan", "id", strconv.Itoa(req.VLANID)).CombinedOutput()
	if err != nil {
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to create VLAN: " + string(cmdOut)})
		return
	}

	// 2. Assign IP address
	cmdOut, err = exec.Command("ip", "addr", "add", ipCIDR, "dev", vlanName).CombinedOutput()
	if err != nil {
		exec.Command("ip", "link", "delete", vlanName).Run()
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to assign IP: " + string(cmdOut)})
		return
	}

	// 3. Bring interface up
	cmdOut, err = exec.Command("ip", "link", "set", vlanName, "up").CombinedOutput()
	if err != nil {
		exec.Command("ip", "link", "delete", vlanName).Run()
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to bring VLAN up: " + string(cmdOut)})
		return
	}

	// Save to database (UPSERT)
	var existing int
	err = models.DB.QueryRow(
		"SELECT id FROM vlan_config WHERE interface=$1 AND vlan_id=$2",
		req.Interface, req.VLANID,
	).Scan(&existing)

	if err == sql.ErrNoRows {
		_, err = models.DB.Exec(
			"INSERT INTO vlan_config (interface, vlan_id, ip_address, start_ip, description, is_portal) VALUES ($1,$2,$3,$4,$5,$6)",
			req.Interface, req.VLANID, ipCIDR, req.StartIP, req.Description, req.IsPortal,
		)
	} else if err == nil {
		_, err = models.DB.Exec(
			"UPDATE vlan_config SET ip_address=$3, start_ip=$4, description=$5, is_portal=$6 WHERE interface=$1 AND vlan_id=$2",
			req.Interface, req.VLANID, ipCIDR, req.StartIP, req.Description, req.IsPortal,
		)
	}
	if err != nil {
		log.Printf("Warning: failed to persist VLAN to DB: %v", err)
	}

	// Save to config file
	writeVLANConfigEntry(vlanConfigEntry{
		Interface:   req.Interface,
		VLANID:      req.VLANID,
		IP:          ipCIDR,
		StartIP:     req.StartIP,
		Description: req.Description,
		IsPortal:    req.IsPortal,
	})

	// Start DHCP (dnsmasq) for this VLAN
	dhcpActive := false
	if err := startDHCP(vlanName, req.VLANID, ipCIDR, req.StartIP); err != nil {
		log.Printf("VLANCreate: DHCP failed for %s: %v", vlanName, err)
	} else {
		dhcpActive = true
	}

	// Persist the static IP in a per-VLAN networkd file so networkd owns it
	// and won't flush it on reload. The `ip addr add` above stays for
	// immediate effect; this makes it stick.
	if err := writeNetworkdConfig(vlanName, ipCIDR); err != nil {
		log.Printf("VLANCreate: warning: failed to write networkd config for %s: %v", vlanName, err)
	}

	// Install the captive portal capture rules for this VLAN
	applyCaptiveRules("add", vlanName, ipCIDR)

	vlan := models.VLANInfo{
		Interface:   req.Interface,
		VLANID:      req.VLANID,
		Name:        vlanName,
		IP:          ipCIDR,
		Description: req.Description,
		IsPortal:    req.IsPortal,
		Active:      true,
		DHCPActive:  dhcpActive,
		StartIP:     req.StartIP,
	}

	sendJSON(w, http.StatusOK, map[string]interface{}{"success": true, "vlan": vlan})
}

// ============================================
// VLAN DELETE
// ============================================

// VLANDelete removes a VLAN interface and its persisted config.
func VLANDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Interface string `json:"interface"`
		VLANID    int    `json:"vlan_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid JSON: " + err.Error()})
		return
	}

	if req.VLANID < 1 || req.VLANID > 4094 {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "VLAN ID must be between 1 and 4094"})
		return
	}
	if !validateIfaceName(req.Interface) {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid interface name"})
		return
	}

	vlanName := fmt.Sprintf("%s.%d", req.Interface, req.VLANID)

	// Remove the captive portal rules while the interface still exists, so the
	// script can resolve the VLAN subnet for the MASQUERADE rule.
	applyCaptiveRules("del", vlanName, getVLANIP(vlanName))

	// Stop DHCP (dnsmasq) for this VLAN before removing the interface
	stopDHCP(vlanName)

	// Delete the interface
	cmdOut, err := exec.Command("ip", "link", "delete", vlanName).CombinedOutput()
	if err != nil {
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to delete VLAN: " + string(cmdOut)})
		return
	}

	// Remove the per-VLAN networkd file
	removeNetworkdConfig(vlanName)

	// Remove from config file
	removeVLANConfigEntry(req.Interface, req.VLANID)

	// Delete from database
	if _, err := models.DB.Exec("DELETE FROM vlan_config WHERE interface=$1 AND vlan_id=$2", req.Interface, req.VLANID); err != nil {
		log.Printf("Warning: failed to remove VLAN from DB: %v", err)
	}

	sendJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// ============================================
// VLAN INTERFACES
// ============================================

// VLANInterfaces returns physical network interfaces with their IPs and state.
func VLANInterfaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to read interfaces: " + err.Error()})
		return
	}

	var ifaces []models.InterfaceInfo
	for _, entry := range entries {
		name := entry.Name()
		if name == "lo" {
			continue
		}
		// Exclude VLAN sub-interfaces (e.g. eth0.100)
		if strings.Contains(name, ".") {
			continue
		}
		// Only include physical devices: check for /sys/class/net/<name>/device symlink
		// This excludes virtual interfaces like docker0, veth*, br-*, etc.
		devicePath := "/sys/class/net/" + name + "/device"
		if _, err := os.Stat(devicePath); err != nil {
			continue
		}

		ip := getInterfaceIP(name)
		state := getInterfaceState(name)

		ifaces = append(ifaces, models.InterfaceInfo{
			Name:  name,
			IP:    ip,
			State: state,
		})
	}

	if ifaces == nil {
		ifaces = []models.InterfaceInfo{}
	}

	sendJSON(w, http.StatusOK, map[string]interface{}{"interfaces": ifaces})
}

// ============================================
// IP HELPERS
// ============================================

// getVLANIP returns the first IPv4 address (with CIDR) for a VLAN interface.
func getVLANIP(iface string) string {
	out, err := exec.Command("ip", "-4", "addr", "show", iface).Output()
	if err != nil {
		return ""
	}
	return parseIPFromOutput(string(out))
}

// getInterfaceIP returns the first IPv4 address (with CIDR) for a physical interface.
func getInterfaceIP(iface string) string {
	out, err := exec.Command("ip", "-4", "addr", "show", iface).Output()
	if err != nil {
		return ""
	}
	return parseIPFromOutput(string(out))
}

// parseIPFromOutput extracts the first inet address from `ip addr show` output.
func parseIPFromOutput(out string) string {
	re := regexp.MustCompile(`inet\s+(\S+)`)
	for _, line := range strings.Split(out, "\n") {
		if m := re.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

// getInterfaceState reads the operstate of a network interface from sysfs.
func getInterfaceState(iface string) string {
	data, err := os.ReadFile("/sys/class/net/" + iface + "/operstate")
	if err != nil {
		return "unknown"
	}
	state := strings.TrimSpace(string(data))
	if state == "" {
		return "unknown"
	}
	return state
}

// ============================================
// VLAN CONFIG FILE HELPERS
// ============================================

// vlanConfigEntry represents one line in /etc/pisowifi/vlans.conf.
type vlanConfigEntry struct {
	Interface   string
	VLANID      int
	IP          string
	StartIP     string
	Description string
	IsPortal    bool
}

// readVLANConfig reads all entries from /etc/pisowifi/vlans.conf.
// New format: interface vlan_id ip/cidr start_ip description [portal]
// Old format: interface vlan_id ip/cidr description [portal]
// start_ip is "-" when not set; old files without start_ip are still supported.
// Example (new): eth0 100 192.168.100.1/24 - Portal VLAN portal
// Example (old): eth0 100 192.168.100.1/24 Portal VLAN portal
func readVLANConfig() []vlanConfigEntry {
	f, err := os.Open(vlanConfigPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var entries []vlanConfigEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}

		vid, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}

		entry := vlanConfigEntry{
			Interface: parts[0],
			VLANID:    vid,
			IP:        parts[2],
		}

		// Determine whether parts[3] is a start_ip field (new format) or
		// the beginning of the description (old format).
		// New-format indicator: parts[3] is "-" or a valid IP address.
		var rest []string
		if len(parts) > 3 {
			candidate := parts[3]
			isStartIP := candidate == "-"
			if !isStartIP && net.ParseIP(candidate) != nil {
				isStartIP = true
			}
			if isStartIP {
				// New format: parts[3] = start_ip
				if candidate != "-" {
					entry.StartIP = candidate
				}
				rest = parts[4:]
			} else {
				// Old format: no start_ip column
				rest = parts[3:]
			}
		}

		// Remaining tokens: description words + optional "portal" flag
		if len(rest) > 0 {
			if rest[len(rest)-1] == "portal" {
				entry.IsPortal = true
				rest = rest[:len(rest)-1]
			}
			entry.Description = strings.Join(rest, " ")
		}

		entries = append(entries, entry)
	}

	return entries
}

// writeVLANConfig rewrites the entire vlans.conf from the given entries.
func writeVLANConfig(entries []vlanConfigEntry) {
	if err := os.MkdirAll(filepath.Dir(vlanConfigPath), 0755); err != nil {
		log.Printf("Failed to create config dir: %v", err)
		return
	}

	f, err := os.OpenFile(vlanConfigPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		log.Printf("Failed to open VLAN config for writing: %v", err)
		return
	}
	defer f.Close()

	for _, e := range entries {
		startIPCol := "-"
		if e.StartIP != "" {
			startIPCol = e.StartIP
		}
		line := fmt.Sprintf("%s %d %s %s %s", e.Interface, e.VLANID, e.IP, startIPCol, e.Description)
		if e.IsPortal {
			line += " portal"
		}
		fmt.Fprintln(f, line)
	}
}

// writeVLANConfigEntry appends or updates a single entry, then rewrites the file.
func writeVLANConfigEntry(entry vlanConfigEntry) {
	entries := readVLANConfig()

	found := false
	for i := range entries {
		if entries[i].Interface == entry.Interface && entries[i].VLANID == entry.VLANID {
			entries[i] = entry
			found = true
			break
		}
	}
	if !found {
		entries = append(entries, entry)
	}

	writeVLANConfig(entries)
}

// removeVLANConfigEntry deletes an entry and rewrites the file.
func removeVLANConfigEntry(iface string, vlanID int) {
	entries := readVLANConfig()

	var filtered []vlanConfigEntry
	for _, e := range entries {
		if e.Interface == iface && e.VLANID == vlanID {
			continue
		}
		filtered = append(filtered, e)
	}

	writeVLANConfig(filtered)
}

// ============================================
// NETMASK HELPERS
// ============================================

// validateIfaceName rejects interface names that could cause path traversal or
// shell injection. Only alphanumeric chars, dots, hyphens, and underscores are
// allowed; the name must not start with a dot or contain "..".
func validateIfaceName(name string) bool {
	if name == "" || name == "lo" {
		return false
	}
	if strings.Contains(name, "..") || strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// netmaskToCIDR converts a dotted-decimal netmask to a CIDR prefix length.
// e.g. "255.255.255.0" → 24
func netmaskToCIDR(mask string) (int, error) {
	ip := net.ParseIP(mask)
	if ip == nil {
		return 0, fmt.Errorf("invalid netmask: %s", mask)
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return 0, fmt.Errorf("netmask must be IPv4")
	}

	ones, bits := net.IPMask(ip4).Size()
	if bits == 0 {
		return 0, fmt.Errorf("invalid subnet mask: %s", mask)
	}

	return ones, nil
}

// calculateDHCPRange returns a dnsmasq dhcp-range string for the given CIDR.
// e.g. "10.0.13.1/24", "" → "10.0.13.100,10.0.13.200,12h"
// If startIP is non-empty and valid within the subnet, it is used as the first
// address in the range; otherwise the default offset (network+100) is used.
func calculateDHCPRange(ipCIDR string, startIP string) string {
	ip, ipNet, err := net.ParseCIDR(ipCIDR)
	if err != nil {
		return ""
	}
	_ = ip // not used directly; we compute from network base

	// Get the 4-byte network address
	network := ipNet.IP.To4()
	if network == nil {
		return ""
	}

	// Convert network to uint32
	base := uint32(network[0])<<24 | uint32(network[1])<<16 | uint32(network[2])<<8 | uint32(network[3])

	// Compute subnet size for bounds checking
	ones, bits := ipNet.Mask.Size()
	hostBits := uint(bits - ones)
	maxHost := (uint32(1) << hostBits) - 1 // e.g. 255 for /24

	// Determine start offset
	var startOffset uint32 = 100 // default
	if startIP != "" {
		parsed := net.ParseIP(startIP)
		if parsed != nil {
			p4 := parsed.To4()
			if p4 != nil && ipNet.Contains(parsed) {
				ipVal := uint32(p4[0])<<24 | uint32(p4[1])<<16 | uint32(p4[2])<<8 | uint32(p4[3])
				offset := ipVal - base
				if offset > 0 && offset < maxHost {
					startOffset = offset
				}
			}
		}
	}

	start := base + startOffset
	end := base + startOffset + 100
	// Clamp end to broadcast-1
	if end-base >= maxHost {
		end = base + maxHost - 1
	}

	startAddr := net.IPv4(byte(start>>24), byte(start>>16), byte(start>>8), byte(start))
	endAddr := net.IPv4(byte(end>>24), byte(end>>16), byte(end>>8), byte(end))

	return fmt.Sprintf("%s,%s,12h", startAddr, endAddr)
}

// isDHCPActive checks whether the dnsmasq template instance is active for a
// given VLAN interface name (e.g. "end0.13").
func isDHCPActive(vlanName string) bool {
	cmd := exec.Command("systemctl", "is-active", fmt.Sprintf("dnsmasq@%s", vlanName))
	return cmd.Run() == nil
}

// startDHCP writes a dnsmasq config for the VLAN and starts+enables the template service.
// It stops any running instance before writing the new config to avoid stale state.
// startIP may be empty to use the default offset (network+100).
// Returns nil on success, or an error describing what went wrong.
func startDHCP(vlanName string, vlanID int, ipCIDR string, startIP string) error {
	gateway := strings.Split(ipCIDR, "/")[0]
	dhcpRange := calculateDHCPRange(ipCIDR, startIP)
	if dhcpRange == "" {
		err := fmt.Errorf("could not calculate DHCP range for %s", ipCIDR)
		log.Printf("startDHCP: %v", err)
		return err
	}

	// DNS is deliberately ENABLED here: address=/#/<gateway> answers every
	// hostname with the gateway IP, which is what captures the captive portal
	// detection probes (captive.apple.com, connectivitycheck.gstatic.com, ...)
	// and makes the portal pop up automatically.
	//
	// except-interface=lo together with bind-dynamic is what keeps multiple
	// per-VLAN instances from fighting over 127.0.0.1:53 — do NOT go back to
	// port=0 to fix a bind conflict, it disables the portal capture.
	//
	// Authorized clients are not affected: aircoins-captive-rules DNATs their
	// port 53 traffic to a real upstream resolver.
	configContent := fmt.Sprintf(`# Auto-generated by AirCoins for VLAN %d
interface=%s
bind-dynamic
except-interface=lo
no-resolv
no-hosts
address=/#/%s
dhcp-range=%s
dhcp-option=3,%s
dhcp-option=6,%s
`, vlanID, vlanName, gateway, dhcpRange, gateway, gateway)

	configPath := fmt.Sprintf("/etc/dnsmasq.d/%s.conf", vlanName)
	unitName := fmt.Sprintf("dnsmasq@%s", vlanName)

	// Stop the existing service before writing a new config so the old
	// process doesn't hold stale file descriptors or conflict.
	if out, err := exec.Command("systemctl", "stop", unitName).CombinedOutput(); err != nil {
		// Non-fatal: service may not have been running
		log.Printf("startDHCP: note: systemctl stop %s: %v — %s", unitName, err, string(out))
	}

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		err = fmt.Errorf("failed to write dnsmasq config for %s: %w", vlanName, err)
		log.Printf("startDHCP: %v", err)
		return err
	}

	if out, err := exec.Command("systemctl", "start", unitName).CombinedOutput(); err != nil {
		err = fmt.Errorf("failed to start %s: %w — %s", unitName, err, string(out))
		log.Printf("startDHCP: %v", err)
		return err
	}

	if out, err := exec.Command("systemctl", "enable", unitName).CombinedOutput(); err != nil {
		// Non-fatal: service is running but won't survive reboot
		log.Printf("startDHCP: warning: failed to enable %s: %v — %s", unitName, err, string(out))
	}

	log.Printf("startDHCP: successfully started and enabled %s", unitName)
	return nil
}

// stopDHCP disables and stops the dnsmasq template instance and removes its config file.
func stopDHCP(vlanName string) {
	unitName := fmt.Sprintf("dnsmasq@%s", vlanName)

	if out, err := exec.Command("systemctl", "disable", unitName).CombinedOutput(); err != nil {
		log.Printf("stopDHCP: warning: failed to disable %s: %v — %s", unitName, err, string(out))
	}

	if out, err := exec.Command("systemctl", "stop", unitName).CombinedOutput(); err != nil {
		log.Printf("stopDHCP: warning: failed to stop %s: %v — %s", unitName, err, string(out))
	}

	configPath := fmt.Sprintf("/etc/dnsmasq.d/%s.conf", vlanName)
	if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
		log.Printf("stopDHCP: warning: failed to remove dnsmasq config %s: %v", configPath, err)
	}

	log.Printf("stopDHCP: stopped and disabled %s", unitName)
}
