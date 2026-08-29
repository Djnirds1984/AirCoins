# Changelog

## v1.21.1
- **Voucher printing fixed**: explicit A4 portrait `@page` size (no longer inherits the browser's last-used Landscape setting) and a fixed 2-column print grid, so vouchers fill the page instead of one-per-sheet.

## v1.21.0
- **Vouchers inherit the coin-rate pause rules**: the generator now has Rate Type (Pausable/Consumable) and Pause Expiry (hours/days, disabled for consumables). Rules are stored on the voucher and applied to the session at FIRST redemption only — an unused voucher never ages, the pause-expiry clock starts when the code is first used.
- Voucher table shows pause-rule badges (⏱ expiry window / C = consumable).
- New migration: `vouchers.pausable`, `vouchers.expiration_hours`.

## v1.20.5
- Pricing: coin resolution is now deterministic when two active tiers share the same coin value — the longest-duration tier wins (previously an old short tier could arbitrarily steal coins, e.g. a P1=240min tier shadowing a new P1=3-day tier).
- Pricing: admin UI warns when adding a tier for a coin value that already has an active tier.

## v1.20.4
All notable changes to AirCoins are documented in this file.

---

## v1.20.4 — VLAN Link Status Fix

**Release date:** August 2026

### Fixed
- VLAN Management no longer shows working VLANs as "Down": VLAN interfaces already live in the kernel but missed by the `ip -d link show type vlan` parse are now recognized by the heal path, and bridge/VLAN interfaces without an IP address report link up via the kernel `LOWER_UP` flag instead of the misleading RFC 2863 operstate.

---


## v1.20.3 — Duration & Expiry Unit Selectors

**Release date:** August 2026

### Changed
- Pricing tab: duration and pause-expiry inputs now use **Minutes / Hours / Days** unit dropdowns (add form and inline edit row), matching the CCNDS panel layout. Values are converted to minutes/hours on save.
- Consumable rates: both the expiry input and its unit dropdown are disabled (and forced to 0) when Rate Type is **Consumable**, in the add form and the edit row.

---

## v1.20.2 — Consumable Rate Expiry Guard

**Release date:** August 2026

### Fixed
- Pricing tab: selecting the **Consumable** rate type now disables the Pause Expiry field (and resets it to 0), preventing an accidental expiry on non-pausable rates. The field is re-enabled when switching back to Pausable.

---




## v1.20.1 — Admin Pause-Rule Controls for Pricing Rates

**Release date:** August 2026

### Added
- **Rate pause rules in the Pricing tab** — Each coin rate can now be set as **Pausable** (shows the Pause button in the portal) or **Consumable** (no pause) right from the Add Tier form and the inline edit row.
- **Pause expiry window** — Operators can set the maximum wall-clock pause window (`expiration_hours`) per rate directly in the admin UI. 0 keeps the legacy behaviour of a paused session staying frozen while paused.
- A new **Pause Rule** column in the pricing table displays each tier's rule (e.g. `Pausable · expires after 2h`, or `Consumable`).

### Fixed
- Toggling a rate Active/Inactive no longer silently resets its pause rules back to defaults.

### Action required
Update to v1.20.1 from Admin > System > Update, then reboot.

---
## v1.20.0 — Pausable & Consumable Rates + Pause Expiry Window

**Release date:** August 2026

### Added
- **Consumable vs. pausable rates** — Each coin rate is now flagged `pausable` (a consumable rate must be explicitly created with `pausable=false`). Consumable rates hide the Pause button in the captive portal. The purchased rate's behaviour is snapshotted onto each session.
- **Pause expiry window** — Operators can set a maximum wall-clock pause window (`expiration_hours`) per rate. A paused session that is not resumed before its `pause_expires_at` deadline is forcibly expired (remaining time → 0; the user must insert coin again). `0` keeps the legacy behaviour of a paused session being frozen indefinitely.
- **Sturdier portal image uploads** — Background & header images now fall back to the original file extension (`.jpg`/`.jpeg`/`.png`/`.webp`) when MIME sniffing returns `application/octet-stream`/ambiguous, and background images are persisted to `system_settings` with a correct `updated_at`.

### Fixed
- **Reports reset no longer wipes active sessions** — `ResetSalesReports` clears only financial statistics (`daily_stats`, `coin_events`) and deliberately preserves the `sessions` table so connected devices and their remaining time are never lost.
- **Coinslot pricing resolution** — Fixed the lookup that could return the wrong minute value for a coin pulse.

### Action required
Update to v1.20.0 from Admin > System > Update, then reboot.

---
## v1.19.7 — Reports Reset Isolation (Protect Active Sessions)

**Release date:** August 2026

### Fixed
- **Sales Reports Reset Isolation (`reports.go`)** — Updated `ResetSalesReports` so that resetting sales/earnings data clears only financial statistics (`daily_stats` and `coin_events`) and deliberately preserves the `sessions` table, ensuring active connected devices and user remaining times are never wiped out.

### Action required
Update to v1.19.7 from Admin > System > Update, then reboot.

---


## v1.19.6 — Portal Header Image Display Fix

**Release date:** August 2026

### Fixed
- **Portal Header Image Sync (`index.html`)** — Updated captive portal appearance loading (`applyPortalAppearance()`) to fetch and display the uploaded header image directly from the `/api/portal/appearance` endpoint instead of looking for non-existent local storage keys.

### Action required
Update to v1.19.6 from Admin > System > Update, then reboot.

---


## v1.19.5 — Bugfix: safePortalBg Helper Definition

**Release date:** August 2026

### Fixed
- **Admin Panel Background Preview Fix** — Defined the missing `safePortalBg()` helper function in `admin.html` to resolve the `safePortalBg is not defined` error when updating portal background thumbnails and live previews.

### Action required
Update to v1.19.5 from Admin > System > Update, then reboot.

---


## v1.19.4 — Robust Image Uploads & Portal UI Streamlining

**Release date:** August 2026

### Fixed & Improved
- **Portal Background & Header Image Uploader Fix** — Enhanced backend MIME-type validation (`appearance.go`) to gracefully fallback to original file extensions (`.jpg`, `.jpeg`, `.png`, `.webp`) and robustly process file uploads without corruption.
- **Streamlined Captive Portal UI** — Removed the irrelevant "Customize Portal" button from the portal home view (`index.html`).

### Action required
Update to v1.19.4 from Admin > System > Update, then reboot.

---


## v1.19.3 — System Health Diagnostics & API Enhancements

**Release date:** August 2026

### Added
- **New System Health Diagnostics API (`/api/health`)** — Upgraded the health check endpoint to dynamically verify database connection status, report exact API version, and return standard JSON structure with HTTP status codes for robust monitoring and automation.

### Action required
Update to v1.19.3 from Admin > System > Update, then reboot.

---

---

## v1.19.2 — Upload Bandwidth Limits & Instant Updater Refresh

**Release date:** August 2026

### Fixed
- **Upload Bandwidth Shaping** — Refactored traffic control (`qdisc.go`) to add `tc` ingress policing on client interfaces, properly enforcing per-device upload bandwidth limits.
- **Updater Cache Bypass** — Updated the admin interface and updater handler to force a live fetch when clicking the **Check for Updates** button, removing the 1-hour caching delay.

### Action required
Update to v1.19.2 from Admin > System > Update, then reboot.


## v1.19.1 — Hotfix: License System Stability & Admin Access

**Release date:** August 2026

### Fixed
- **Admin panel lockdown** — Resolved an issue where the admin panel would lock users out entirely if the license was missing. The navigation is now always accessible, with a warning notification guiding users to the license section for activation.
- **License verification logic** — Fixed a bug where "active" licenses were incorrectly invalidated if the device missed its 24-hour heartbeat check. Active licenses are now persistent and valid until their actual expiration date.

### Action required
Update to v1.19.1 from Admin > System > Update, then reboot.


## v1.19.0 — Voucher Upgrade: Batch Codes, Print Templates, QR Codes

**Release date:** August 2026

### Added
- **Auto-generated batch codes**: every voucher generation run now stamps all its vouchers with a unique batch code (`B-XXXXXX`, same unambiguous alphabet). The batch code is shown right after generating, with a one-click **Print Batch** button.
- **Print vouchers by batch**: new print modal opens from the generator result or from the printer icon on any voucher row — prints the whole batch at once.
- **3 beautiful print templates**: ✨ Premium Dark (gold-on-black business card), 📄 Classic Light (clean white/blue), and 🎫 Compact Ticket (thin dashed-cut strip) — live preview, 86×54 mm printer-safe cards with page-break protection.
- **QR code on every printed voucher**: each card carries a scannable QR that opens the portal with the code pre-filled (`/?voucher=CODE`) — customers scan and tap "Use Voucher".
- **Built-in offline QR encoder**: full ISO/IEC 18004 byte-mode encoder (EC level M, versions 1–10) embedded in the admin page — zero external dependencies since the device has no internet; validated with a decoder round-trip before shipping.
- **Batch column + batch filter** in the voucher table (filter bar and reset included).
- **Portal auto-fill**: the captive portal pre-fills the voucher input from the `?voucher=` URL parameter (used by the printed QR codes).
- New `vouchers.batch_code` column + index (migration 018, self-healed on boot — OTA updates need no manual SQL).

### Action required
Update to v1.19.0 from Admin > System > Update, then reboot. Vouchers generated before this release keep working; they simply have no batch code (and thus no print button).

---

## v1.18.1 — Voucher Improvements: Price per Voucher, Flexible Custom Duration, Professional UI

**Release date:** August 2026

### Added
- **Price per voucher (₱)**: set a sale price when generating a batch; the price is stored on every voucher, shown in a new table column, editable per voucher, and returned on redemption — enabling voucher sales reporting. 0 = free/promo.
- **Custom duration units**: the generator's "Custom..." option now takes a value plus a unit — Minutes, Hours, Days, or Months (1 month = 30 days) — instead of minutes only.
- **Voucher dashboard cards**: Total / Available / Redeemed / Disabled counters with icons at the top of the Voucher Management page.
- New `vouchers.price` column (migration 017, self-healed on boot — OTA updates need no manual SQL).

### Changed
- **Professional Voucher Management UI**: icon-led section titles, pill badges for plan (Time/Monthly) and status (Unused/Used/Disabled), styled generated-code chips, green token highlight, and crisp SVG icons for copy/edit/delete actions.
- Duration formatting now shows "1 month", "3 days", "2 hours" instead of "× 30 days" / "day(s)".

### Action required
Update to v1.18.1 from Admin > System > Update, then reboot. Existing vouchers get a price of ₱0.

---

## v1.18.0 — Feature: Voucher System (Pre-Paid Codes, Monthly Subscriptions, SSID Roaming)

**Release date:** August 2026

### Added
- **Voucher Management page in the admin sidebar** (🎟️ Vouchers): generate batches of up to 100 unique **6-character alphanumeric codes** (unambiguous alphabet — no 0/O/1/I/L), with duration presets (15 min up to 1 day, 7 days) or custom minutes, plus full CRUD: filter by status, search by code/notes, copy, edit (duration/status/notes), and delete.
- **Monthly subscription vouchers**: the "Monthly (30 days)" preset creates 30-day (43,200 min) vouchers marked `plan=monthly` — ideal for monthly WiFi subscribers.
- **Voucher redemption on the captive portal**: the portal page now has a "Have a voucher? Enter your code" input. Redeeming a code starts (or extends) the device's session exactly like a coin payment — no coins needed.
- **Voucher ↔ session token binding for SSID roaming**: on redemption the voucher is bound to the resulting session and its roaming `session_token`, so a voucher subscriber keeps internet when moving between your SSIDs/portals (same token-roaming used by coin sessions).
- New `vouchers` table (migration 016, self-healed on boot — OTA updates need no manual SQL).
- New API endpoints: `POST /api/admin/vouchers/generate`, `GET /api/admin/vouchers`, `GET/PATCH/DELETE /api/admin/vouchers/<id>` (admin-only), and public `POST /api/session/redeem`.

### Security
- Redemption is atomic (a code can never be double-redeemed); the caller is identified by its own IP/MAC exactly like coin payment — a client cannot grant itself time with someone else's code after use; banned MACs are rejected; per-IP throttling blocks code brute-forcing; failed session creation rolls the voucher back to `unused`.

### Action required
Update to v1.18.0 from Admin > System > Update, then reboot. Open the new **Vouchers** page in the sidebar, generate your first batch, and test by entering a code in the portal's voucher box.

---

## v1.17.7 — Anti-Tethering: Portal Toggle Is the Single Source of Truth for TTL=1

**Release date:** August 2026

### Changed
- **The Anti-Hotspot toggle in Portal Servers settings now fully owns the TTL=1 anti-tethering stamp.** It applies the correct download-path rule (`mangle POSTROUTING → AIRCOINS_TTL → -o <iface> -j TTL --ttl-set 1`) — the same effect as the common manual `iptables -A POSTROUTING -o end0.22 -j TTL --ttl-set 1`, but managed, per-portal, applied on every boot, and with no per-MAC bypass needed. No manual iptables commands and no `netfilter-persistent save` are required (and persistence must stay manual-free — AirCoins re-applies everything on boot).
- **Stray manual TTL rules are auto-cleaned.** On every apply (boot, toggle, portal heal), the API deletes leftover direct `POSTROUTING -o <iface> -j TTL --ttl-set 1|64` manual stamps for portal interfaces so they can never double-stamp or conflict with the managed chain. Combined with v1.17.6's legacy `-i` purge, the mangle table converges to exactly one correct stamp per anti-hotspot portal on any restart.
- Turning anti-hotspot OFF on the last portal now also removes the `AIRCOINS_TTL` hook from mangle POSTROUTING, leaving a clean table.

### Action required
Update to v1.17.7 from Admin > System > Update, then reboot (or restart `aircoins-api`). Do NOT run manual TTL commands or `netfilter-persistent save`; if you already did, the API removes them on its next apply. Verify: `sudo iptables -t mangle -S POSTROUTING` shows only `-j AIRCOINS_TTL` (no bare TTL rules), and `sudo iptables -t mangle -S AIRCOINS_TTL` shows only `-o <portal-iface> ... --ttl-set 1` lines, one per portal with anti-hotspot ON.

---

## v1.17.6 — Fix: Authorized Clients With Dead DNS (iPhones Never Get Internet on ISPs That Block 8.8.8.8)

**Release date:** August 2026

### Fixed
- **Paying clients now always get a working DNS resolver.** Root cause of iPhones showing "connected, no internet" after paying on some networks: `aircoins-captive-rules` hardcoded the authorized-client upstream DNS to `8.8.8.8`. On ISPs that block or reject external resolvers (common in the PH), every authorized client's DNS query was DNAT'ed to a black hole (`[UNREPLIED]` in conntrack) — the phone could not resolve anything, so iOS never left captive state and had no browsing. Android often escaped anyway because its Private DNS (DNS-over-TLS, port 853/443) bypasses the port-53 rule. The resolver is now **auto-detected from the gateway's own DNS** (`resolvectl` → `/run/systemd/resolve/resolv.conf` → `/etc/resolv.conf`, skipping 127.0.0.x stubs, `8.8.8.8` only as last resort), so it works out of the box on any ISP. A manual `UPSTREAM_DNS=` in `/etc/pisowifi/captive.conf` still overrides.
- Probe-release REDIRECT string matching now also catches absolute-form HTTP request lines (no `GET ` prefix requirement).
- **`unauth` (session end/delete/expire) now removes ALL of a client's rules even if `UPSTREAM_DNS` changed since they were authorized.** Previously rules were deleted by exact spec including the DNS destination, so a changed resolver value left orphan DNS rules that silently diverted the client's DNS to a dead address — killing both the captive popup and internet for that device with no way to recover except flushing the chain.
- **Anti-hotspot no longer breaks paying clients when legacy upstream (`-i`) TTL rules are present.** Root cause of "iPhone pays but has no internet while Android works": legacy/manual setups stamp TTL=1 on **incoming** client packets and depend on a per-MAC RETURN bypass that must be re-added at every auth. When that bypass went missing (session deleted and re-paid), the client's upstream packets left with TTL=1 and died at the ISP router — DNS to the router still worked, everything else was `[UNREPLIED]`. AirCoins' own design stamps TTL=1 on the **download** path, which needs no bypass at all. From v1.17.6 the API **purges all legacy `-i` TTL stamps and MAC bypasses** from `AIRCOINS_TTL` and removes stale mangle PREROUTING hooks on every startup, then (re)applies the download-path design — self-healing on any boot.

### Note
- Running `netfilter-persistent save` / `iptables-persistent` on a running gateway freezes that moment's MAC whitelist to disk and re-applies it on every boot — clients whose sessions later expire keep free internet across reboots, and stale auth rules mask the live state. AirCoins already re-applies all rules on boot (portal heal + session startup-recovery); do not persist iptables manually.

### Action required
Update to v1.17.6 from Admin > System > Update. Then re-authorize your current clients once (a new payment does this automatically): `sudo aircoins-captive-rules auth <mac>`. The auth log line must show your ISP's DNS (e.g. `dns -> 192.168.254.254`), not `8.8.8.8`.

---

## v1.17.5 — Fix: iPhone Stuck in Captive Portal After Paying

**Release date:** August 2026

### Fixed
- **iPhones now leave the captive portal after paying (the iOS "check" returns and internet works).** Root cause: while a phone is captive, the portal's DNS hijack answers `captive.apple.com` with the **gateway IP**, and **iOS caches that answer**. After payment the client is MAC-whitelisted and fresh DNS goes to a real resolver, but the iPhone keeps re-probing the **cached gateway IP**. Those probes landed on lighttpd and were 302-redirected straight back to the portal — so iOS concluded "still captive" forever, even with a valid session. Android escapes on its own because it re-resolves DNS and probes the real internet; that's why only iPhones were stuck. This is unrelated to TTL/anti-tethering.

### Changed
- **New probe-release responder in the Go API** (`handlers/probe.go`). For each authorized MAC, `aircoins-captive-rules auth` now installs nat rules that `REDIRECT` the client's captive-probe GETs still aimed at a LOCAL address (the cached gateway IP) to the API, which answers with the exact success content each OS expects (Apple `Success` HTML, Android `204`, Windows NCSI text, etc.). Real-internet destinations are untouched, and the portal page / `/api` for paying clients keep working.
- **`aircoins-captive-rules` is now included in OTA script updates.** It was missing from the updater copy list, so previous fixes to the captive release rules never reached devices that updated via OTA.

### Action required
Update to v1.17.5 from Admin > System > Update. Then have an iPhone **forget the WiFi network and rejoin**, pay, and press Done Paying — the captive portal should close on its own within a couple of seconds. Verify with `sudo aircoins-captive-rules status`: after payment you should see the client's MAC in `AIRCOINS_CAPTIVE` with the `REDIRECT ... 8080` probe-release rules.

---

## v1.17.4 — Fix: OTA Updates Never Reloaded the Coin Listener (Multi-Peso Coins Still Under-Counted)

**Release date:** August 2026

### Fixed
- **The v1.17.1 pulse-stream fix now actually reaches devices.** Root cause of "a 5-peso coin still reads 1 pulse": the OTA update script replaced `gpio-coin-listener` on disk but **never restarted the service** (bash daemons keep executing the code they read at startup), and it also **never updated `aircoins-gpio-lib`** where the edge-stream helper lives. Every device stayed on the pre-v1.17.1 lossy code until rebooted — so multi-peso coins kept under-counting even on "updated" devices.

### Changed
- **The API restarts `gpio-coin-listener` at startup.** The API is restarted by every OTA update and on every boot, making this the reliable moment to load whatever listener version is on disk.
- **The gpiomon edge stream is now inlined in `gpio-coin-listener`** — no dependency on the on-disk `aircoins-gpio-lib` version, so devices whose lib was never refreshed by past OTA updates get the full fix.
- OTA update script (applies from the *next* update onward): now also copies `aircoins-gpio-lib` and restarts `gpio-coin-listener` itself.
- New diagnostic log line `edge ignored by debounce (<Xms after last pulse)` — if a coin ever under-counts again, the journal now shows exactly which edges the debounce gate rejected.

### Action required
Update to v1.17.4 from Admin > System > Update. The API restart loads the fixed listener automatically — **no reboot needed**. Verify with `journalctl -u gpio-coin-listener -f`: you should see `gpiomon available - using edge-triggered waiting` and every pulse of a multi-peso coin (`Pulse #1` … `Pulse #5` for a 5-peso coin). Also check Settings > GPIO > Pulse Debounce is at **50 ms**.

---

## v1.17.3 — Fix: "Failed to save GPIO config" After OTA Update

**Release date:** August 2026

### Fixed
- **GPIO config save no longer fails with "Failed to save GPIO config"** on devices that received v1.17.2 via OTA. The OTA updater replaces files only and deliberately skips `install.sh`, so migration **015** (the `gpio_config.debounce_ms` column) was never applied — every GPIO GET/INSERT then failed with "column debounce_ms does not exist".

### Changed
- **The API now self-heals its schema at startup** — new `models.EnsureSchema()` runs idempotent `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` fixes before the server accepts requests. Any future OTA release that needs a new column only has to add one line there; devices heal themselves on the next service restart with no manual SQL.
- Fresh installs still get the column from `schema.sql` / `migrations.sql` as before.

### Action required
Update to v1.17.3 from Admin > System > Update. The service restart applies the missing column automatically; GPIO save works immediately after.

---

## v1.17.2 — GPIO: Tunable Pulse Debounce from Admin Panel

**Release date:** August 2026

### Added
- **Pulse Debounce (ms) is now a tunable setting** in Admin > Settings > GPIO Configuration. It sits next to the Coin Slot Pin and Pulse Mode fields, defaults to the recommended **50 ms**, and accepts 5-1000 ms.
- The **Save GPIO Config** button now persists the debounce value and **automatically restarts the coin listener** so the change applies immediately (falls back to a "restart manually" notice if the restart call fails).
- The setting **survives reboots**: it is stored both in the `gpio_config` database table and written to `/var/lib/pisowifi/gpio_config`, which the listener reads on every startup.

### Changed
- GPIO listener reads `DEBOUNCE_MS` from the config file (default declared before the file is sourced so a saved value is never overwritten) and logs `Pulse debounce: X ms` at startup.
- API validates the debounce range (5-1000 ms; 0 = keep the 50 ms default) and returns it in `GET /api/gpio/config`.
- Legacy `set_gpio_config` CGI handler also accepts and clamps `debounce_ms`.

### Database
- Migration **015**: `gpio_config` gains a `debounce_ms INTEGER NOT NULL DEFAULT 50` column (idempotent, safe on existing installs).

### Tuning guidance
If a multi-peso coin (e.g. 10-peso = 10 rapid pulses) still under-counts, lower the debounce (try 30 or 20 ms). If pulses get double-counted, raise it. Watch `journalctl -u gpio-coin-listener` for `Pulse #1` … `Pulse #N`.

---

## v1.17.1 — Fix: Multi-Peso Coins Losing Pulses (10-Peso Coin Read as ₱4)

**Release date:** August 2026

### Fixed
- **A single 10-peso coin now credits all 10 pulses** — Previously, dropping one 10-peso coin only registered ~4 pesos while ten 1-peso coins worked fine. The coin acceptor fires a 10-peso coin as 10 rapid pulses (~100 ms apart), and the GPIO listener went blind for ~250 ms after every pulse (debounce sleep + wait-for-HIGH polling + restarting `gpiomon` per pulse), so edges arriving in that gap were lost forever.

### Changed
- **GPIO listener now streams edges from one long-lived `gpiomon`** for the whole armed window (new `gpio_stream_falling_edges()` in `aircoins-gpio-lib`). The line stays claimed the entire time and the kernel queues every falling edge, so no pulse can be missed regardless of pulse-train speed. Handles both libgpiod v1 and v2 syntax; falls back to polling automatically if `gpiomon` is unavailable.
- **Debounce is now time-based (`DEBOUNCE_MS=50`) instead of a blocking sleep** — the `accept_pulse_edge()` gate suppresses contact bounce without pausing the listener, keeping the hot path fast enough for rapid pulse trains. The polling fallback loop uses the same gate.

---

## v1.17.0 — Reboot Survival: Portal + WAN Persistence

**Release date:** August 2026

### Added
- **Portal captive rules now survive reboots** — New `HealAllPortalsOnBoot()` function runs at API startup and re-applies the full portal stack (IP, dnsmasq, captive iptables rules) for every enabled portal. No more visiting the admin page after a reboot to restore the captive portal.
- **WAN settings now survive reboots** — New `RestoreWANOnBoot()` function writes persistent systemd-networkd configs for Static and VLAN DHCP modes. Static IP, gateway, and DNS now persist across reboots without relying on runtime `ip` commands.

### Changed
- Startup sequence now calls `HealAllPortalsOnBoot()` and `RestoreWANOnBoot()` after `ApplyAntiHotspotOnStartup()`.
- Static WAN mode writes `/etc/systemd/network/03-aircoins-wan-<iface>.network` for persistence.
- VLAN DHCP mode writes `.netdev` + `.network` files so the VLAN is auto-created on boot.

---

## v1.16.8 — Anti-Hotspot: Pure TTL=1 (POSTROUTING)

**Release date:** August 2026

### Changed
- **Anti-hotspot now uses mangle POSTROUTING with -o (output interface)** — matches MikroTik behavior.
- TTL=1 is set on ALL packets going OUT through the portal interface.
- The client device gets TTL=1 → if they enable hotspot, tethered devices cannot reach the internet.
- Removed AddTTL1Bypass/RemoveTTL1Bypass functions (no longer needed).
- Removed ifaceForClientMAC helper function.
- Updated admin panel hint text to clarify behavior.

---

## v1.16.4 — Anti-Hotspot: MikroTik-Style TTL=1 + Per-MAC RETURN

**Release date:** August 2026

### Changed
- **Anti-hotspot now uses MikroTik-style TTL=1 + per-MAC RETURN** — exactly like MikroTik hotspot server TTL=1.
- ALL packets from the portal interface get TTL set to 1 (die at first hop).
- The authorized client's MAC gets a RETURN rule in the mangle chain → bypasses TTL=1 → normal internet.
- Friends' packets (different MACs, no RETURN match) → TTL=1 → die at first hop → blocked.
- Uses mangle table PREROUTING hook, not filter table. Filter chain is unchanged.
- When you ping the gateway with anti-hotspot ON, the authorized client sees normal TTL (because of RETURN). Friends see TTL=1.

---

## v1.16.3 — Anti-Hotspot: Per-MAC Filtering (MikroTik-style 1:1)

**Release date:** August 2026

### Changed
- **Anti-hotspot now uses per-MAC filtering** instead of unreliable TTL-based detection. Only the authorized client's MAC address can forward traffic. Friends' devices (different MACs) are blocked — regardless of whether the phone decrements TTL or not.
- This is the same approach MikroTik uses for 1:1 hotspot connections.
- **How it works:** When anti-hotspot is ON, the catch-all ACCEPT is replaced with catch-all DROP. Only authorized clients (per-MAC ACCEPT rules added during auth) can forward. Tethered devices with different MACs are dropped.

---

## v1.16.2 — Fix: Anti-Hotspot No Longer Blocks Client

**Release date:** August 2026

### Fixed
- **Anti-hotspot now correctly blocks ONLY tethered devices** — Previous approach (TTL=1) blocked ALL packets from the portal including the paying client's own device. New approach DROPs packets with TTL≤63, which are the friends' packets (decremented by client's phone from 64→63). The client's own packets (TTL=64) pass through untouched.
- **How it works:** Most devices send TTL=64. When client enables hotspot, their phone acts as a router and decrements TTL by 1 on friends' packets (64→63). We DROP TTL≤63 at the portal — client (64) passes, friends (63) blocked.

---

## v1.16.1 — Fix: Captive Rules Fail When Anti-Hotspot Enabled

**Release date:** August 2026

### Fixed
- **Captive rules now work with anti-hotspot enabled** — Anti-hotspot TTL=1 rules are now applied directly by the Go API via iptables, instead of relying on the shell script. This fixes the issue where enabling anti-hotspot caused the Captive status to show "Fail" on devices with older script versions.
- **Backward compatibility** — The shell script is now always called with 3 args (action, iface, gateway), ensuring it works on all devices regardless of script version.

---

## v1.16.0 — Portal Servers CRUD + Anti-Hotspot (TTL=1)

**Release date:** August 2026

### Added
- **Anti-Hotspot (TTL=1 Hop Limit)** — New per-portal setting that forces IP TTL=1 on client packets, preventing tethering/hotspotting. When enabled, only the paying client's device gets internet access — their friends' devices cannot connect through them.
- **Portal Servers CRUD (Edit)** — Portal servers can now be edited after creation. Click the ✏️ button to modify IP, DHCP range, lease time, or anti-hotspot settings. Changes apply immediately to running portals.
- **Database migration 014** — Adds `anti_hotspot` column to `portal_servers` table

### Changed
- Portal config file format extended to 7 columns (backward compatible with 6-column format)
- Captive rules script (`aircoins-captive-rules`) accepts optional `anti_hotspot` parameter for TTL mangling via iptables mangle table

---

## v1.15.3 — Build Update

**Release date:** August 2026

### Changed
- **Version bump** — Build release v1.15.3

---

## v1.10.11 — Allow Admin Access When Locked

**Release date:** August 2026

### Fixed
- **Locked devices can now access License and Updater pages** — Previously, locked devices were completely blocked from the admin panel. Now users can access the License page (to purchase/activate) and the Updater page (to update software) even when locked.
- **Backend middleware** — Added `/api/admin/update/*` to the list of routes that bypass the license gate.
- **Frontend lockdown** — Updated `lockDownForLicense()` and `showSection()` to keep License and Updater nav items visible.

---

## v1.10.10 — WAN: Fix "Cannot find device eth0" on end0 Systems

**Release date:** August 2026

### Fixed
- **WAN interface detection now scans for end0, eth0, enp*, ens*, enx*** — Previously only used `ip route show default` which fails when there's no default route. Now falls back to scanning `/sys/class/net` for the first Ethernet-like interface that is UP.
- **Hardware fingerprint MAC detection** — Now scans for any Ethernet interface (end0, eth0, etc.) instead of hardcoded `eth0`.
- **Stored eth_interface fallback** — Only used as last resort, and now verifies the interface actually exists before returning it.

---

## v1.10.9 — WAN: Fix dhclient/dhcpcd Service Error

**Release date:** August 2026

### Fixed
- **No more "dhclient.service not found" error** — `applyDHCP()` now falls back to running the DHCP binary directly (`dhclient -v eth0`) when `systemctl restart` fails. No dummy service needed.
- **Static IP no longer depends on dhcpcd** — `applyStatic()` now uses `iproute2` commands directly (`ip addr add`, `ip route add`) instead of writing to `/etc/dhcpcd.conf` and restarting dhcpcd. Works on all modern Linux.
- **VLAN DHCP already uses direct request** — `applyVLANDHCP()` was already fixed in v1.10.2.

---

## v1.10.8 — Fix: Cloned SD Cards Now Get Fresh Trial

**Release date:** August 2026

### Fixed
- **Hardware fingerprint now detects cloned SD cards** — Added 3 new hardware ID sources: device tree serial (Allwinner SID), Ethernet MAC address, and auto-regeneration of `/etc/machine-id`. Previously, cloned SD cards had the same machine-id, so the system couldn't tell they were on a different board.
- **Auto-regenerate machine-id** — When the SD card is moved to a new board, the system detects the hardware change (via MAC address or SoC serial) and regenerates `/etc/machine-id` with a new unique ID. This triggers a fresh 7-day trial automatically.
- **Hardware stamp file** — New `/etc/machine-id.hardware-stamp` tracks which hardware the machine-id belongs to, so cloned cards are detected on first boot.

---

## v1.10.7 — Reset Sales Reports

**Release date:** August 2026

### Added
- **Reset Sales Reports button** — New section at the bottom of the Reports page with a "Reset All Sales Data" button. Deletes all daily stats, coin events, and session records. Includes double confirmation to prevent accidental resets.

---

## v1.10.6 — License: Auto-Reset Trial on Hardware Change

**Release date:** August 2026

### Changed
- **Hardware change = fresh 7-day trial** — When the SD card is cloned to a new SBC board, the system detects the new CPU serial and automatically starts a fresh 7-day trial. No more "locked" state from hardware mismatch.
- **Removed hardware mismatch lock** — The device no longer locks when the hardware ID changes. Instead, it treats the new board as a new device and starts a trial.
- **Heartbeat uses live hardware ID** — The Supabase heartbeat now reports the actual hardware ID of the current board.

### Business flow
1. Flash image to SD card → insert into new SBC board
2. System detects new CPU serial (hardware ID)
3. Automatic 7-day trial starts
4. Buyer purchases license → activates permanently
5. If trial expires without purchase → device locks (as expected)

---

## v1.10.5 — Updater Refactor: Version Cards & Rollback

**Release date:** August 2026

### Added
- **Version cards UI** — The updater now shows a card for each available version (up to 5), with individual Download and Install buttons per version.
- **Easy rollback** — Download and install any previous version directly from the updater. If a new version causes issues, roll back to a stable version with one click.
- **Auto-check on open** — The updater automatically checks for updates when you open the section. No more clicking "Check for Updates" manually.
- **Smooth UI** — No more blinking/flickering during update checks. Status badges show "Installed", "New", or "Older" for each version card.
- **Downloaded badge** — Already-downloaded versions show a blue "Downloaded" badge with an Install button.

---

## v1.10.4 — VLAN ISP IP Info & Internet Connectivity

**Release date:** August 2026

### Added
- **VLAN ISP Connection Info panel** — When in VLAN DHCP mode, a separate green panel shows the VLAN interface's IP, subnet, gateway, and DNS (e.g. `end0.500`). Makes it easy to verify the VLAN ISP is connected.
- **Internet connectivity check** — A status bar shows whether the device can actually reach the internet (pings 8.8.8.8). Green = connected, Red = no internet.
- **VLAN interface status** — Shows whether the VLAN interface is up/down with a helpful message if the ISP link is not active.

---

## v1.10.3 — WAN IP Info Display

**Release date:** August 2026

### Added
- **ISP Connection Info panel** — New card in WAN Settings showing the live IP address, subnet, gateway, and DNS obtained from the ISP. Includes interface status indicator (up/down). Refreshes automatically when you click the refresh button.

---

## v1.10.2 — VLAN WAN DHCP Client Auto-Detection

**Release date:** August 2026

### Fixed
- **VLAN WAN apply failed: "dhcpcd end0.X failed"** — The WAN handler hardcoded `dhcpcd` for obtaining DHCP on VLAN interfaces, but Armbian systems typically use `dhclient`, `udhcpc`, or NetworkManager. The system now auto-detects the available DHCP client (dhclient → dhcpcd → udhcpc → nmcli) and uses whichever is installed.
- **DHCP service restart also fixed** — Plain DHCP mode now restarts the correct DHCP client service instead of always trying `dhcpcd`.

---

## v1.10.1 — GPIO Multi-Pulse Coin Counting Fix

**Release date:** August 2026

### Fixed
- **Multi-peso coins only counted as 1 pulse** — The `handle_coin_pulse()` function in `gpio-coin-listener` was blocking for 150ms+ and then waiting for the pin to go HIGH after each pulse. This caused all subsequent pulses in a rapid train to be missed. A 10-peso coin (10 pulses) was read as a single 1-peso coin.
- **Debounce moved to callers** — The pin-wait logic is now handled by the polling loop and gpiomon loop (which need it for edge tracking), while `handle_coin_pulse()` returns immediately after a short 50ms electrical debounce. This allows every pulse in a multi-pulse coin to be detected.

---

## v1.10.0 — Bridge/VLAN Portal Management

**Release date:** August 2026

### Added
- **Network Bridges** — create and manage network bridges from the admin panel
- **Bridge Members** — manually add/remove VLAN interfaces as layer-2 bridge members
- **Portal on Bridge** — provision captive portals on bridge interfaces
- **Bridges UI** — new Bridges section in admin sidebar
- **Change Password** — new card on Settings page to change admin password
- **Permanent device tokens** — soft-delete sessions, cookie backup for tokens

### Changed
- Boot script Phase 1b for bridge creation
- Portal guards for bridge member VLANs
- Updater self-recovery via systemd-run

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
