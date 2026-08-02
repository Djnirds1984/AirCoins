package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// UpdaterHandler caches Supabase manifest lookups to avoid hammering the API.
type UpdaterHandler struct {
	mu       sync.RWMutex
	cachedAt time.Time
	cached   *updateManifest
	client   *http.Client
}

type updateManifest struct {
	Version      string `json:"version"`
	ReleasedAt   string `json:"released_at"`
	ReleaseNotes string `json:"release_notes"`
	Tarball      string `json:"tarball"`
	SHA256       string `json:"sha256"`
}

// NewUpdaterHandler creates an UpdaterHandler with a 10-second HTTP client.
func NewUpdaterHandler() *UpdaterHandler {
	return &UpdaterHandler{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// fetchLatest returns the latest update manifest from Supabase Storage,
// using the cache when possible. If force is true the cache is bypassed.
func (h *UpdaterHandler) fetchLatest(force bool) (*updateManifest, error) {
	// Check cache (unless forced refresh)
	h.mu.RLock()
	if !force && h.cached != nil && time.Since(h.cachedAt) < 1*time.Hour {
		manifest := *h.cached
		h.mu.RUnlock()
		return &manifest, nil
	}
	h.mu.RUnlock()

	supabaseURL := os.Getenv("SUPABASE_URL")
	if supabaseURL == "" {
		return nil, fmt.Errorf("SUPABASE_URL not set")
	}

	manifestURL := fmt.Sprintf("%s/storage/v1/object/public/aircoins/manifest.json", supabaseURL)

	// Use a 5-second timeout client for manifest fetch
	manifestClient := &http.Client{Timeout: 5 * time.Second}
	resp, err := manifestClient.Get(manifestURL)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var manifest updateManifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}

	// Update cache
	h.mu.Lock()
	h.cached = &manifest
	h.cachedAt = time.Now()
	h.mu.Unlock()

	return &manifest, nil
}

// CheckForUpdate queries the latest Supabase manifest and compares versions.
func (h *UpdaterHandler) CheckForUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	force := r.URL.Query().Get("force") == "true"

	manifest, err := h.fetchLatest(force)
	if err != nil {
		sendJSON(w, 200, map[string]interface{}{
			"current_version":  apiVersion,
			"update_available": false,
			"error":            fmt.Sprintf("Unable to check for updates: %v", err),
		})
		return
	}

	updateAvailable := compareSemver(apiVersion, manifest.Version) < 0

	sendJSON(w, 200, map[string]interface{}{
		"current_version":  apiVersion,
		"latest_version":   manifest.Version,
		"update_available": updateAvailable,
		"release_notes":    manifest.ReleaseNotes,
		"published_at":     manifest.ReleasedAt,
		"checked_at":       time.Now().UTC().Format(time.RFC3339),
	})
}

// DownloadUpdate fetches the manifest, downloads the tarball from Supabase
// Storage, verifies its SHA256, and saves it to /opt/aircoins/updates/.
func (h *UpdaterHandler) DownloadUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get manifest for tarball URL
	manifest, err := h.fetchLatest(false)
	if err != nil {
		sendJSON(w, http.StatusOK, map[string]interface{}{
			"success": false, "message": "Cannot fetch manifest: " + err.Error(),
		})
		return
	}

	// Create download directory
	os.MkdirAll("/opt/aircoins/updates", 0755)

	// Construct full download URL
	supabaseURL := os.Getenv("SUPABASE_URL")
	downloadURL := fmt.Sprintf("%s/storage/v1/object/public/aircoins/%s", supabaseURL, manifest.Tarball)

	// Use a separate client with 5-minute timeout for large downloads
	dlClient := &http.Client{Timeout: 5 * time.Minute}
	resp, err := dlClient.Get(downloadURL)
	if err != nil {
		sendJSON(w, http.StatusOK, map[string]interface{}{
			"success": false, "message": "Download failed: " + err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		sendJSON(w, http.StatusOK, map[string]interface{}{
			"success": false, "message": fmt.Sprintf("Download returned status %d", resp.StatusCode),
		})
		return
	}

	filename := fmt.Sprintf("aircoins-v%s.tar.gz", strings.TrimPrefix(manifest.Version, "v"))
	filePath := "/opt/aircoins/updates/" + filename

	outFile, err := os.Create(filePath)
	if err != nil {
		sendJSON(w, http.StatusOK, map[string]interface{}{
			"success": false, "message": "Cannot create file: " + err.Error(),
		})
		return
	}
	defer outFile.Close()

	// Stream download to file
	if _, err := io.Copy(outFile, resp.Body); err != nil {
		sendJSON(w, http.StatusOK, map[string]interface{}{
			"success": false, "message": "Failed writing file: " + err.Error(),
		})
		return
	}

	// Verify SHA256 after download
	downloadedFile, err := os.ReadFile(filePath)
	if err != nil {
		sendJSON(w, http.StatusOK, map[string]interface{}{
			"success": false, "message": "Cannot read downloaded file for verification: " + err.Error(),
		})
		return
	}
	hash := sha256.Sum256(downloadedFile)
	actualSHA := hex.EncodeToString(hash[:])

	if manifest.SHA256 != "" && actualSHA != manifest.SHA256 {
		os.Remove(filePath)
		sendJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("SHA256 mismatch: expected %s, got %s", manifest.SHA256, actualSHA),
		})
		return
	}

	// Return success JSON
	sendJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"message":  "Download complete",
		"version":  manifest.Version,
		"filename": filename,
	})
}

// CheckDownloadedFile checks if a downloaded update tarball exists in
// /opt/aircoins/updates/ and returns its metadata.
func (h *UpdaterHandler) CheckDownloadedFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	updateDir := "/opt/aircoins/updates"
	entries, err := os.ReadDir(updateDir)
	if err != nil {
		sendJSON(w, http.StatusOK, map[string]interface{}{
			"found": false,
		})
		return
	}

	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "aircoins-v") && strings.HasSuffix(e.Name(), ".tar.gz") {
			info, _ := e.Info()
			// Extract version from filename: "aircoins-v1.8.0.tar.gz" -> "v1.8.0"
			version := strings.TrimPrefix(strings.TrimSuffix(e.Name(), ".tar.gz"), "aircoins-")
			sendJSON(w, http.StatusOK, map[string]interface{}{
				"found":    true,
				"filename": e.Name(),
				"version":  version,
				"size":     info.Size(),
			})
			return
		}
	}

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"found": false,
	})
}

// PerformUpdate installs from a previously downloaded local tarball file.
func (h *UpdaterHandler) PerformUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Find the downloaded tarball in /opt/aircoins/updates/
	updateDir := "/opt/aircoins/updates"
	entries, _ := os.ReadDir(updateDir)
	var tarballPath string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "aircoins-v") && strings.HasSuffix(e.Name(), ".tar.gz") {
			tarballPath = updateDir + "/" + e.Name()
			break
		}
	}
	if tarballPath == "" {
		sendJSON(w, http.StatusOK, map[string]interface{}{
			"success": false, "message": "No downloaded update file found. Please download first.",
		})
		return
	}

	// Extract to temp directory
	tmpDir := "/tmp/aircoins-update"
	os.RemoveAll(tmpDir)
	extractDir := tmpDir + "/extracted"
	os.MkdirAll(extractDir, 0755)
	extractCmd := exec.Command("tar", "-xzf", tarballPath, "-C", extractDir)
	if output, err := extractCmd.CombinedOutput(); err != nil {
		sendJSON(w, http.StatusOK, map[string]interface{}{
			"success": false, "message": "Extract failed: " + string(output),
		})
		return
	}

	// Find extracted directory
	dirEntries, _ := os.ReadDir(extractDir)
	var installDir string
	for _, e := range dirEntries {
		if e.IsDir() {
			installDir = extractDir + "/" + e.Name()
			break
		}
	}
	if installDir == "" {
		sendJSON(w, http.StatusOK, map[string]interface{}{
			"success": false, "message": "No directory found in extracted tarball",
		})
		return
	}

	// Send response BEFORE install (install kills the server)
	sendJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Installing update...",
	})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// Clear cache so next check fetches fresh
	h.mu.Lock()
	h.cachedAt = time.Time{}
	h.cached = nil
	h.mu.Unlock()

	// Targeted file replacement — does NOT run install.sh (which destroys the system).
	// Only copies binary, HTML, scripts, CGI, recovery, and changelog.
	// Preserves: .env, database, systemd units, network config.
	updateScript := fmt.Sprintf(`
echo "=== AirCoins Update: $(date) ===" >> /tmp/aircoins-update.log
DIR="%s"
sudo systemctl stop aircoins-api 2>/dev/null
sleep 1
# Update binary
if [ -f "$DIR/system/usr/local/bin/aircoins-api/aircoins-api" ]; then
    sudo cp "$DIR/system/usr/local/bin/aircoins-api/aircoins-api" /usr/local/bin/aircoins-api/aircoins-api
    sudo chmod +x /usr/local/bin/aircoins-api/aircoins-api
    echo "Binary updated" >> /tmp/aircoins-update.log
fi
# Update HTML
[ -f "$DIR/admin.html" ] && sudo cp "$DIR/admin.html" /var/www/html/admin.html
[ -f "$DIR/index.html" ] && sudo cp "$DIR/index.html" /var/www/html/index.html
echo "HTML updated" >> /tmp/aircoins-update.log
# Update scripts
for s in gpio-coin-listener pisowifi-api-update pisowifi-ctl pisowifi-session-manager; do
    [ -f "$DIR/system/usr/local/bin/$s" ] && sudo cp "$DIR/system/usr/local/bin/$s" /usr/local/bin/$s && sudo chmod +x /usr/local/bin/$s
done
echo "Scripts updated" >> /tmp/aircoins-update.log
# Update recovery
[ -f "$DIR/aircoins-recover.sh" ] && sudo cp "$DIR/aircoins-recover.sh" /opt/aircoins/aircoins-recover.sh && sudo chmod +x /opt/aircoins/aircoins-recover.sh
# Update CGI
[ -d "$DIR/system/usr/lib/cgi-bin" ] && sudo cp "$DIR/system/usr/lib/cgi-bin/"* /usr/lib/cgi-bin/ 2>/dev/null && sudo chmod +x /usr/lib/cgi-bin/* 2>/dev/null
# Update CHANGELOG
[ -f "$DIR/CHANGELOG.md" ] && sudo cp "$DIR/CHANGELOG.md" /opt/aircoins/CHANGELOG.md
# Restart services
sudo systemctl start aircoins-api
sleep 2
sudo systemctl restart lighttpd
sudo systemctl restart dnsmasq
(sudo systemctl restart hostapd 2>/dev/null || true)
sleep 2
sudo systemctl status aircoins-api --no-pager >> /tmp/aircoins-update.log 2>&1
sudo systemctl status lighttpd --no-pager >> /tmp/aircoins-update.log 2>&1
# Clean up downloaded tarball
rm -f /opt/aircoins/updates/aircoins-v*.tar.gz
echo "=== Update Complete: $(date) ===" >> /tmp/aircoins-update.log
`, installDir)
	updateCmd := exec.Command("bash", "-c", updateScript)
	updateCmd.Dir = installDir
	// Log output to a file for debugging
	logFile, _ := os.Create("/tmp/aircoins-update.log")
	updateCmd.Stdout = logFile
	updateCmd.Stderr = logFile
	// Start but don't wait — the server will be killed during restart
	updateCmd.Start()
}

// DeleteDownload removes downloaded update tarballs from /opt/aircoins/updates/.
func (h *UpdaterHandler) DeleteDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	updateDir := "/opt/aircoins/updates"
	entries, _ := os.ReadDir(updateDir)
	deleted := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "aircoins-v") && strings.HasSuffix(e.Name(), ".tar.gz") {
			os.Remove(updateDir + "/" + e.Name())
			deleted++
		}
	}

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Deleted %d file(s)", deleted),
	})
}

// compareSemver compares two semantic version strings.
// Returns -1 if a < b, 0 if equal, +1 if a > b.
// "dev" is always considered less than any version.
func compareSemver(a, b string) int {
	if a == "dev" && b == "dev" {
		return 0
	}
	if a == "dev" {
		return -1
	}
	if b == "dev" {
		return 1
	}

	a = strings.TrimLeft(a, "vV")
	b = strings.TrimLeft(b, "vV")

	aParts := strings.SplitN(a, ".", 3)
	bParts := strings.SplitN(b, ".", 3)

	if len(aParts) < 3 || len(bParts) < 3 {
		return 0
	}

	for i := 0; i < 3; i++ {
		// Strip pre-release suffix (e.g., "0-beta" → "0")
		if idx := strings.IndexAny(aParts[i], "-+"); idx >= 0 {
			aParts[i] = aParts[i][:idx]
		}
		if idx := strings.IndexAny(bParts[i], "-+"); idx >= 0 {
			bParts[i] = bParts[i][:idx]
		}
		av, err := strconv.Atoi(aParts[i])
		if err != nil {
			return 0
		}
		bv, err := strconv.Atoi(bParts[i])
		if err != nil {
			return 0
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}
