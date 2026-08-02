# Changelog

All notable changes to AirCoins are documented in this file.

---

## v1.9.0 — Updater Self-Recovery + Change Password

**Release date:** August 2026

### Fixed
- **Updater kills itself during install** — update script now runs in its own systemd transient unit (`systemd-run`), completely outside the API's cgroup. `systemctl stop aircoins-api` no longer kills the update process.
- **Services not restarting after update** — retry logic added for service restart, all services (aircoins-api, lighttpd, dnsmasq, hostapd) reliably restart
- **Staging directory visibility** — moved from /tmp (private namespace) to /opt/aircoins/updates/staging for cross-unit access

### Added
- **Change Password** — new card on Settings page to change admin password (current password verification, min 6 chars)

---

## v1.8.2 — UTF-8 BOM Fix

**Release date:** August 2026

### Fixed
- **Manifest parse error** — strip UTF-8 BOM from Supabase manifest.json before JSON decoding
- **Defensive BOM handling** — Go code now handles BOM-prefixed JSON gracefully

---

## v1.8.1 — Updater Download Fix

**Release date:** August 2026

### Fixed
- **Download 404 error** — trim trailing slashes from SUPABASE_URL, fix URL construction
- **SHA256 case mismatch** — case-insensitive hash comparison
- **Better error messages** — download errors now include response details
- **Debug logging** — logs exact URLs being fetched for troubleshooting

---

## v1.8.0 — Supabase Storage Updater

**Release date:** August 2026

### Added
- **Two-step update flow** — separate Download and Install buttons for safer updates
- **Progress bar** — visual status indicator during download and installation
- **Supabase Storage integration** — updater now uses Supabase Storage instead of GitHub Releases
- **publish-release.sh** — new script to upload releases to Supabase Storage bucket

### Changed
- **Updater backend** — refactored to fetch manifest.json from Supabase Storage (public bucket, no auth needed)
- **Install from local file** — PerformUpdate now installs from pre-downloaded tarball in /opt/aircoins/updates/
- **SHA256 verification** — downloaded tarballs are verified against manifest checksum
- **5-minute download timeout** — fixed 10-second timeout that was too short for large files

### Removed
- **GitHub Releases dependency** — no longer uses GitHub API for update checks
- **GITHUB_TOKEN** — deprecated in favor of Supabase Storage

---

## v1.7.0 — Portal & License UI Improvements

**Release date:** August 2026

### Added
- **License key display** — License page now shows the panel's license key

### Changed
- **Portal buttons** — Pause Time and Rates buttons now match Insert Coin button size with distinct colors

---

## v1.6.2 — Critical Updater Fix

**Release date:** August 2026

### Fixed
- **Updater destroying system** — replaced full install.sh with targeted file replacement
- **Preserves configs** — .env, database, systemd units untouched during update
- **Clean service restart** — stops API, copies files, starts all services in order

---

## v1.6.1 — Private Repo Support & Self-Recovery

**Release date:** August 2026

### Fixed
- **Updater 404 on private repos** — added GITHUB_TOKEN authentication via environment variable
- **System unreachable after update** — now restarts all critical services (lighttpd, dnsmasq, hostapd) after update completes
- **Self-recovery** — service status logged to /tmp/aircoins-update.log for debugging

---

## v1.6.0 — Faster Coin Detection

**Release date:** August 2026

### Fixed
- **Coin detection delay** — reduced polling interval for faster coin insertion detection on the portal page

---

## v1.5.0 — Updater Fix, Mobile UI & Privacy

**Release date:** August 2026

### Fixed
- **Updater "Failed to fetch" bug** — response now sent before install.sh runs, auto-answers prompts, ensures service restart
- **Admin panel mobile layout** — hamburger menu, slide-in sidebar, full-width cards, scrollable tables, proper touch targets

### Changed
- Removed "View on GitHub" links from updater page to keep repository private

---

## v1.4.0 — Changelog Display & Updater Improvements

**Release date:** August 2026

### Added
- **Changelog display** — Updater page now shows release notes even when the system is up to date
- **GitHub release link** — Direct link to view release on GitHub from the updater page

### Changed
- Improved updater UI consistency across update-available and up-to-date states

---

## v1.3.0 — One-Click Update & Version Display Fix

**Release date:** August 2026

### Added
- **Update Now button** — download and install the latest release directly from the admin panel with one click
- **Post-update changelog** — shows success card with new version after update completes

### Fixed
- **Double-v version display** — updater page now shows correct version format (v1.3.0 not vv1.3.0)
- **Shared fetch logic** — extracted `fetchLatest` method for reuse between check and update endpoints

---

## v1.2.0 — System Reboot & Update Checker

**Release date:** August 2026

### Added
- **Reboot button** on the System page — allows admin to remotely reboot the SBC with confirmation dialog and auto-reconnect after 60 seconds
- **Update checker** (`/api/admin/updater/check`) — Admin sidebar page that checks GitHub for new releases, shows version comparison, changelog, and links to the release download

---

## v1.1.0

**Release date:** August 2026

### Added

- **Update checker** (`/api/admin/updater/check`) — Admin endpoint that queries the latest AirCoins release from GitHub and reports whether an update is available. Results are cached for 1 hour to avoid hammering the GitHub API. Supports `?force=true` to bypass the cache.

---

## v1.0.0 — First Production Release

**Release date:** August 2026  
**Target hardware:** Orange Pi PC (1GB RAM), Orange Pi One (512MB RAM)  
**Architecture:** Allwinner H3, ARMv7 (linux/arm/7)

---

### Features

- **Captive Portal** — Customer-facing WiFi portal with customizable appearance, per-VLAN splash pages, and multi-device captive detection (iOS, Android, Windows, macOS, Linux)
- **Admin Dashboard** — Single-page admin panel with real-time monitoring, session management, pricing configuration, GPIO pin control, VLAN management, and system statistics
- **License Management** — Supabase-backed licensing system with 7-day free trial from first boot, 24-hour heartbeat verification, CPU hardware ID binding, and automatic lockdown when license is invalid or expired
- **VLAN Support** — 802.1Q VLAN provisioning for network segmentation, per-VLAN DHCP (dnsmasq), per-VLAN captive portal rules, and per-VLAN traffic shaping
- **GPIO Coin Acceptor** — Hardware coin pulse detection via Orange Pi GPIO pins with configurable pin mapping, pulse counting, and credit accumulation
- **Session Management** — Time-based WiFi sessions with automatic expiry, idle timeout, extend/renew support, and real-time bandwidth tracking (tc/htb traffic shaping)
- **Traffic Shaping** — Per-session upload/download bandwidth limits using Linux tc/htb with per-VLAN rate configuration
- **Reverse Proxy** — lighttpd reverse proxy routing `/api/*` to the Go backend, with captive portal detection for 12+ OS probe paths
- **Emergency Recovery** — `aircoins-recover.sh` script to strip VLANs and restore the main network interface when configuration changes break connectivity

### Supported Hardware

| Board | RAM | CPU | Status |
|-------|-----|-----|--------|
| Orange Pi PC | 1GB | Allwinner H3 (Cortex-A7) | Supported |
| Orange Pi One | 512MB | Allwinner H3 (Cortex-A7) | Supported |

### System Requirements

- **OS**: Armbian 23.x+ (Bookworm or Bullseye recommended)
- **RAM**: 512MB minimum (1GB recommended)
- **Storage**: 500MB free disk space
- **Network**: Ethernet with VLAN-capable switch (for multi-VLAN setups)
- **Internet**: Required for initial installation (apt packages) and Supabase license verification (after 7-day trial)

### Quick Start

1. **Extract the release tarball:**
   ```bash
   tar xzf aircoins-v1.0.0.tar.gz
   cd aircoins-v1.0.0
   ```

2. **Run the installer:**
   ```bash
   sudo bash install.sh
   ```

3. **Configure Supabase licensing (optional for first 7 days):**
   ```bash
   sudo cp /opt/aircoins/.env.example /opt/aircoins/.env
   sudo nano /opt/aircoins/.env
   # Fill in your Supabase URL, anon key, and service role key
   sudo systemctl restart aircoins-api
   ```

4. **Reboot:**
   ```bash
   sudo reboot
   ```

5. **Access the admin panel:**
   Navigate to `http://<device-ip>/admin.html`

### Default Credentials

| Field | Value |
|-------|-------|
| Username | `admin` |
| Password | `admin123` |

> **Change the default password immediately after first login.**

### Supabase Setup (License System)

The license system requires a Supabase project. During the 7-day trial, no Supabase configuration is needed.

1. Create a project at [supabase.com](https://supabase.com)
2. Open the SQL Editor and run the contents of `system/database/supabase_aircoins_licenses.sql`
3. Copy your project credentials:
   - Project URL (Settings → API)
   - Anon public key (Settings → API)
   - Service role key (Settings → API → Show service role key)
4. Configure on the device:
   ```bash
   sudo cp /opt/aircoins/.env.example /opt/aircoins/.env
   sudo nano /opt/aircoins/.env
   ```
5. Restart the API:
   ```bash
   sudo systemctl restart aircoins-api
   ```

### Recovery

If network configuration changes break connectivity:
```bash
sudo bash /opt/aircoins/aircoins-recover.sh
```
This strips all VLAN sub-interfaces and restores the main physical interface IP.

### Known Limitations

- **Fresh install only** — No built-in upgrade path from previous versions. Back up configurations before upgrading.
- **Network interface naming** — Armbian may name the Ethernet interface `end0` instead of `eth0`. Check with `ip route show default` and edit `/etc/pisowifi/pisowifi.conf` if needed.
- **WiringOP GPIO** — The GPIO coin listener requires WiringOP, which may need build tools (`gcc`, `make`) not included in the default install.
- **Single WAN interface** — The system expects one upstream WAN connection for internet sharing.

### What's in the Tarball

```
aircoins-v1.0.0/
├── install.sh              # Automated installer
├── aircoins-recover.sh     # Emergency network recovery
├── .env.example            # Supabase credentials template
├── index.html              # Customer captive portal
├── admin.html              # Admin dashboard
├── DEPLOYMENT.md           # Full deployment guide
├── CHANGELOG.md            # This file
└── system/
    ├── database/           # PostgreSQL schema + migrations
    ├── etc/                # Config files + systemd units
    └── usr/                # API binary + shell scripts + CGI
```

### Verify Integrity

```bash
sha256sum -c aircoins-v1.0.0.sha256
```
