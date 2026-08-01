# Changelog

All notable changes to AirCoins are documented in this file.

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
