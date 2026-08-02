package handlers

import (
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

// UpdaterHandler caches GitHub release lookups to avoid hammering the API.
type UpdaterHandler struct {
	mu       sync.RWMutex
	cachedAt time.Time
	cached   *releaseInfo
	client   *http.Client
}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type releaseInfo struct {
	TagName     string         `json:"tag_name"`
	Name        string         `json:"name"`
	Body        string         `json:"body"`
	HTMLURL     string         `json:"html_url"`
	PublishedAt string         `json:"published_at"`
	Assets      []releaseAsset `json:"assets"`
}

// NewUpdaterHandler creates an UpdaterHandler with a 10-second HTTP client.
func NewUpdaterHandler() *UpdaterHandler {
	return &UpdaterHandler{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// fetchLatest returns the latest release info, using the cache when possible.
// If force is true the cache is bypassed.
func (h *UpdaterHandler) fetchLatest(force bool) (*releaseInfo, error) {
	// Check cache (unless forced refresh)
	h.mu.RLock()
	if !force && h.cached != nil && time.Since(h.cachedAt) < 1*time.Hour {
		release := *h.cached
		h.mu.RUnlock()
		return &release, nil
	}
	h.mu.RUnlock()

	// Fetch latest release from GitHub
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/Djnirds1984/AirCoins/releases/latest", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "AirCoins-Updater")

	// Add authentication token if available (required for private repos)
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var release releaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}

	// Update cache
	h.mu.Lock()
	h.cached = &release
	h.cachedAt = time.Now()
	h.mu.Unlock()

	return &release, nil
}

// CheckForUpdate queries the latest GitHub release and compares versions.
func (h *UpdaterHandler) CheckForUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	force := r.URL.Query().Get("force") == "true"

	release, err := h.fetchLatest(force)
	if err != nil {
		sendJSON(w, 200, map[string]interface{}{
			"current_version":  apiVersion,
			"update_available": false,
			"error":            fmt.Sprintf("Unable to check for updates: %v", err),
		})
		return
	}

	updateAvailable := compareSemver(apiVersion, release.TagName) < 0

	sendJSON(w, 200, map[string]interface{}{
		"current_version":  apiVersion,
		"latest_version":   release.TagName,
		"update_available": updateAvailable,
		"release_url":      release.HTMLURL,
		"release_notes":    release.Body,
		"published_at":     release.PublishedAt,
		"checked_at":       time.Now().UTC().Format(time.RFC3339),
	})
}

// PerformUpdate downloads the latest release, extracts it, and performs targeted file replacement.
func (h *UpdaterHandler) PerformUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get the latest release info (use cached or fetch fresh)
	release, err := h.fetchLatest(false)
	if err != nil {
		sendJSON(w, http.StatusOK, map[string]interface{}{
			"success": false, "message": "Cannot fetch release info: " + err.Error(),
		})
		return
	}

	// Find the .tar.gz asset
	var downloadURL string
	for _, a := range release.Assets {
		if strings.HasSuffix(a.Name, ".tar.gz") {
			downloadURL = a.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		sendJSON(w, http.StatusOK, map[string]interface{}{
			"success": false, "message": "No .tar.gz asset found in release",
		})
		return
	}

	// Download the tarball
	tmpDir := "/tmp/aircoins-update"
	os.RemoveAll(tmpDir)
	os.MkdirAll(tmpDir, 0755)

	tarballPath := tmpDir + "/release.tar.gz"
	resp, err := h.client.Get(downloadURL)
	if err != nil {
		sendJSON(w, http.StatusOK, map[string]interface{}{
			"success": false, "message": "Download failed: " + err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		sendJSON(w, http.StatusOK, map[string]interface{}{
			"success": false, "message": fmt.Sprintf("Download returned status %d", resp.StatusCode),
		})
		return
	}

	outFile, err := os.Create(tarballPath)
	if err != nil {
		sendJSON(w, http.StatusOK, map[string]interface{}{
			"success": false, "message": "Cannot create temp file: " + err.Error(),
		})
		return
	}
	io.Copy(outFile, resp.Body)
	outFile.Close()

	// Extract
	extractDir := tmpDir + "/extracted"
	os.MkdirAll(extractDir, 0755)
	cmd := exec.Command("tar", "-xzf", tarballPath, "-C", extractDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		sendJSON(w, http.StatusOK, map[string]interface{}{
			"success": false, "message": "Extract failed: " + string(output),
		})
		return
	}

	// Find the extracted directory (e.g., /tmp/aircoins-update/extracted/aircoins-v1.2.0/)
	entries, _ := os.ReadDir(extractDir)
	var installDir string
	for _, e := range entries {
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

	// === CRITICAL FIX ===
	// Send success response BEFORE running the update, because the update
	// will kill this server when it restarts the aircoins-api service.
	sendJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Update downloaded and extracted. Installing now...",
		"version": release.TagName,
	})

	// Flush the response to ensure it's sent to the client immediately
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
