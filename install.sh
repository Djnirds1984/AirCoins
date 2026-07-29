#!/bin/bash
# ============================================
# AirCoins PisoNet - One-Click Installer
# ============================================
# Supports:
#   - Orange Pi One (ARM) - Full hardware support
#   - Ubuntu/Debian x86   - Development mode (no GPIO)
#
# Wired-only operation — no WiFi, no captive portal.
# Run as root: sudo bash install.sh
# ============================================

set -e

# ============================================
# CONFIGURATION
# ============================================
VERSION="1.1.0"
INSTALL_DIR="/opt/aircoins"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SYSTEM_DIR="$SCRIPT_DIR/system"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

# ============================================
# DETECT ARCHITECTURE
# ============================================
ARCH=$(uname -m)
IS_ARM=false
IS_X86=false

case "$ARCH" in
    armv7l|aarch64|armhf|arm)
        IS_ARM=true
        PLATFORM_NAME="Orange Pi One (ARM)"
        ;;
    x86_64|i686|i386)
        IS_X86=true
        PLATFORM_NAME="Ubuntu/Debian x86_64"
        ;;
    *)
        echo -e "${RED}Unsupported architecture: $ARCH${NC}"
        exit 1
        ;;
esac

# ============================================
# CHECK ROOT
# ============================================
if [ "$EUID" -ne 0 ]; then
    echo -e "${RED}ERROR: This script must be run as root!${NC}"
    echo "Usage: sudo bash install.sh"
    exit 1
fi

# ============================================
# BANNER
# ============================================
echo -e "${CYAN}"
echo "  ╔══════════════════════════════════════════╗"
echo "  ║       AirCoins PisoNet System v$VERSION     ║"
echo "  ║       $PLATFORM_NAME Installer  "
echo "  ╚══════════════════════════════════════════╝"
echo -e "${NC}"

if [ "$IS_X86" = true ]; then
    echo -e "${YELLOW}  ⚠ x86 Development Mode${NC}"
    echo -e "  ${YELLOW}GPIO hardware features disabled.${NC}"
    echo -e "  ${YELLOW}Use API calls or file-based coin events for testing.${NC}"
    echo ""
fi

# ============================================
# VALIDATE PROJECT STRUCTURE
# ============================================
if [ ! -d "$SYSTEM_DIR" ]; then
    echo -e "${RED}ERROR: Project structure not found!${NC}"
    echo ""
    echo -e "  ${YELLOW}Current directory:${NC} $SCRIPT_DIR"
    echo -e "  ${YELLOW}Expected:${NC} $SYSTEM_DIR"
    echo ""
    echo -e "  ${RED}This script must be run from inside the AirCoins project directory.${NC}"
    echo ""
    echo -e "  ${CYAN}Fix:${NC}"
    echo -e "    cd /opt/AirCoins         # Navigate to project directory"
    echo -e "    sudo bash install.sh     # Run installer from there"
    echo ""
    exit 1
fi

if [ ! -d "$SYSTEM_DIR/usr/local/bin/aircoins-api" ]; then
    echo -e "${RED}ERROR: Go API source not found!${NC}"
    echo ""
    echo -e "  ${YELLOW}Missing:${NC} $SYSTEM_DIR/usr/local/bin/aircoins-api"
    echo ""
    echo -e "  ${RED}The project may be incomplete. Please clone the full repository:${NC}"
    echo ""
    echo -e "    git clone https://github.com/Djnirds1984/AirCoins.git /opt/AirCoins"
    echo -e "    cd /opt/AirCoins"
    echo -e "    sudo bash install.sh"
    echo ""
    exit 1
fi

# ============================================
# STOP EXISTING SERVICES (reinstall safety)
# ============================================
# On a reinstall the running aircoins-api binary holds a file lock, which makes
# the STEP 4 'cp' fail with "Text file busy". Stop everything before copying.
stop_existing_services() {
    echo -e "${YELLOW}Stopping existing services...${NC}"

    # Prefer the control script if a previous install left one behind
    if command -v pisowifi-ctl &> /dev/null; then
        pisowifi-ctl stop 2>/dev/null || true
    fi

    # Stop systemd units (ignore errors if a unit does not exist yet)
    systemctl stop aircoins-api gpio-coin-listener pisowifi-session lighttpd 2>/dev/null || true

    # Kill any leftover processes by name as a fallback
    pkill -f aircoins-api 2>/dev/null || true
    pkill -f pisowifi-session 2>/dev/null || true
    pkill -f gpio-coin-listener 2>/dev/null || true

    # Give processes a moment to release file locks
    sleep 1

    echo -e "${GREEN}  ✓ Existing services stopped${NC}"
}

stop_existing_services

# ============================================
# STEP 1: Update system
# ============================================
echo -e "${YELLOW}[1/10]${NC} Updating system packages..."
apt-get update -qq
apt-get upgrade -y -qq
echo -e "${GREEN}  ✓ System updated${NC}"

# ============================================
# STEP 2: Install required packages
# ============================================
echo -e "${YELLOW}[2/10]${NC} Installing required packages..."

if [ "$IS_ARM" = true ]; then
    # Full ARM installation (wired-only)
    apt-get install -y -qq \
        lighttpd \
        usbutils \
        wget \
        curl \
        jq \
        bc \
        postgresql \
        postgresql-contrib \
        golang-go \
        vlan \
        dnsmasq
else
    # x86 installation
    apt-get install -y -qq \
        lighttpd \
        wget \
        curl \
        jq \
        bc \
        postgresql \
        postgresql-contrib \
        golang-go \
        vlan \
        dnsmasq
fi

echo -e "${GREEN}  ✓ Packages installed${NC}"

# VLAN support
modprobe 8021q 2>/dev/null || true
grep -q "^8021q" /etc/modules 2>/dev/null || echo "8021q" >> /etc/modules

# Disable the default dnsmasq service — we use per-interface instances via dnsmasq@.service
systemctl disable dnsmasq 2>/dev/null || true
systemctl stop dnsmasq 2>/dev/null || true

# ============================================
# STEP 3: Setup PostgreSQL Database
# ============================================
echo -e "${YELLOW}[3/10]${NC} Setting up PostgreSQL database..."

# Start PostgreSQL
systemctl start postgresql
systemctl enable postgresql

# Ensure pg_hba.conf allows password auth over TCP (localhost)
# This is required so 'psql -U aircoins -h localhost' and the Go API can connect.
PG_HBA=$(find /etc/postgresql -name pg_hba.conf 2>/dev/null | head -1)
if [ -n "$PG_HBA" ] && ! grep -q "^host.*aircoins.*md5" "$PG_HBA" 2>/dev/null; then
    echo "  Configuring pg_hba.conf for password authentication..."
    # Add entries before any 'reject' rules so they take precedence
    sed -i '/^# DO NOT DISABLE/i \
# AirCoins: allow password auth for the aircoins user\nhost    aircoins    aircoins    127.0.0.1/32    md5\nhost    aircoins    aircoins    ::1/128         md5' "$PG_HBA" 2>/dev/null || \
    echo -e "${YELLOW}  ⚠ Could not auto-edit pg_hba.conf. If schema import fails, add manually:${NC}" \
              "${YELLOW}    host  aircoins  aircoins  127.0.0.1/32  md5${NC}"
    systemctl reload postgresql
fi

# Create database user and database
sudo -u postgres psql -c "CREATE USER aircoins WITH PASSWORD 'aircoins123';" 2>/dev/null || true
sudo -u postgres psql -c "CREATE DATABASE aircoins OWNER aircoins;" 2>/dev/null || true
sudo -u postgres psql -c "GRANT ALL PRIVILEGES ON DATABASE aircoins TO aircoins;" 2>/dev/null || true

# Run schema (idempotent — skip if tables already exist)
TABLES_EXIST=$(psql -U aircoins -d aircoins -h localhost -tAc "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='sessions';" 2>/dev/null || echo "0")
if [ "$TABLES_EXIST" = "1" ]; then
    echo -e "${GREEN}  ✓ Schema already applied, skipping${NC}"
else
    echo "  Applying database schema..."
    if ! psql -U aircoins -d aircoins -h localhost -f "$SYSTEM_DIR/database/schema.sql"; then
        echo -e "${RED}  ✘ FATAL: Schema import failed!${NC}"
        echo -e "${RED}    Please check PostgreSQL is running and try manually:${NC}"
        echo -e "${RED}    psql -U aircoins -d aircoins -h localhost -f $SYSTEM_DIR/database/schema.sql${NC}"
        exit 1
    fi
    echo -e "${GREEN}  ✓ Schema applied successfully${NC}"
fi

echo -e "${GREEN}  ✓ PostgreSQL database ready${NC}"

# ============================================
# STEP 4: Build and Deploy Go API
# ============================================
echo -e "${YELLOW}[4/10]${NC} Building Go API server..."

cd "$SYSTEM_DIR/usr/local/bin/aircoins-api"

# Download dependencies
go mod tidy

# Build the binary
go build -o aircoins-api .

# Deploy
mkdir -p /usr/local/bin/aircoins-api
cp aircoins-api /usr/local/bin/aircoins-api/
chmod +x /usr/local/bin/aircoins-api/aircoins-api

echo -e "${GREEN}  ✓ Go API built and deployed${NC}"

cd "$SCRIPT_DIR"

# ============================================
# STEP 5: Install GPIO support (board-aware)
# ============================================
echo -e "${YELLOW}[5/10]${NC} Installing GPIO support..."

# Detect board for GPIO install decisions
DETECTED_BOARD=""
if [ -f /proc/device-tree/model ]; then
    DETECTED_BOARD=$(tr -d '\0' < /proc/device-tree/model 2>/dev/null | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
fi
DETECTED_BOARD_LOWER=$(echo "$DETECTED_BOARD" | tr '[:upper:]' '[:lower:]')

if [ "$IS_ARM" = true ]; then
    # Install gpiod package for libgpiod fallback (both OPi and RPi)
    apt-get install -y -qq gpiod 2>/dev/null || true

    if echo "$DETECTED_BOARD_LOWER" | grep -qi "raspberry.pi\|raspberrypi"; then
        # Raspberry Pi: ensure raspi-gpio is available (usually pre-installed)
        if command -v raspi-gpio &>/dev/null; then
            echo -e "${GREEN}  ✓ raspi-gpio already available${NC}"
        else
            apt-get install -y -qq raspi-gpio 2>/dev/null || true
            echo -e "${GREEN}  ✓ raspi-gpio installed${NC}"
        fi
        echo -e "  ${CYAN}Board: Raspberry Pi — using raspi-gpio${NC}"
    else
        # Orange Pi (or other ARM): install WiringOP
        if command -v gpio &> /dev/null; then
            echo -e "${GREEN}  ✓ WiringOP already installed${NC}"
        else
            # Try to install from Armbian repo first
            if apt-cache show wiringop &> /dev/null; then
                apt-get install -y -qq wiringop
                echo -e "${GREEN}  ✓ WiringOP installed from repo${NC}"
            else
                # Manual install from GitHub
                echo "  Installing WiringOP from source..."
                cd /tmp
                if [ ! -d "WiringOP" ]; then
                    git clone https://github.com/orangepi-xunlong/wiringOP.git 2>/dev/null || {
                        echo -e "${YELLOW}  ⚠ WiringOP source not available.${NC}"
                    }
                fi
                if [ -d "WiringOP" ]; then
                    cd WiringOP
                    ./build
                    ./build install
                    cd ..
                    rm -rf WiringOP
                    echo -e "${GREEN}  ✓ WiringOP installed from source${NC}"
                fi
            fi
        fi
        echo -e "  ${CYAN}Board: $DETECTED_BOARD — using WiringOP${NC}"
    fi
else
    echo -e "${YELLOW}  ⚠ x86 platform - GPIO hardware not available${NC}"
    echo -e "  ${CYAN}GPIO listener will run in file-based test mode.${NC}"
    echo -e "  ${CYAN}To simulate coin: echo '5' > /var/lib/pisowifi/coin_event${NC}"
fi

# Deploy shared GPIO library
cp "$SYSTEM_DIR/usr/local/bin/aircoins-gpio-lib" /usr/local/bin/aircoins-gpio-lib
chmod +x /usr/local/bin/aircoins-gpio-lib
echo -e "  ✓ aircoins-gpio-lib deployed"

# ============================================
# STEP 6: Deploy configuration files
# ============================================
echo -e "${YELLOW}[6/10]${NC} Deploying configuration files..."

# lighttpd (always needed)
cp "$SYSTEM_DIR/etc/lighttpd/lighttpd.conf" /etc/lighttpd/lighttpd.conf
echo "  ✓ lighttpd.conf"

# Central pisowifi config (do NOT overwrite an existing one on reinstall)
mkdir -p /etc/pisowifi
if [ -f /etc/pisowifi/pisowifi.conf ]; then
    echo "  ✓ pisowifi.conf (kept existing)"
else
    cp "$SYSTEM_DIR/etc/pisowifi/pisowifi.conf" /etc/pisowifi/pisowifi.conf
    echo "  ✓ pisowifi.conf"
fi

echo -e "${GREEN}  ✓ Configs deployed${NC}"

# ============================================
# STEP 7: Deploy web portal files
# ============================================
echo -e "${YELLOW}[7/10]${NC} Deploying web portal..."

mkdir -p /var/www/html
mkdir -p /var/www/html/api
cp "$SCRIPT_DIR/index.html" /var/www/html/index.html
cp "$SCRIPT_DIR/admin.html" /var/www/html/admin.html
chown -R www-data:www-data /var/www/html
chmod -R 755 /var/www/html

# Deploy CGI scripts for admin API
mkdir -p /usr/lib/cgi-bin
cp "$SYSTEM_DIR/usr/lib/cgi-bin/set_gpio_config" /usr/lib/cgi-bin/set_gpio_config
chmod +x /usr/lib/cgi-bin/set_gpio_config
chown root:www-data /usr/lib/cgi-bin/set_gpio_config

cp "$SYSTEM_DIR/usr/lib/cgi-bin/test_gpio" /usr/lib/cgi-bin/test_gpio
chmod +x /usr/lib/cgi-bin/test_gpio
chown root:www-data /usr/lib/cgi-bin/test_gpio

# Enable CGI in lighttpd (fallback if not already in config)
if ! grep -q "mod_cgi" /etc/lighttpd/lighttpd.conf 2>/dev/null; then
    cat >> /etc/lighttpd/lighttpd.conf << 'CGIEOF'

# CGI support (for admin API)
server.modules += ( "mod_cgi" )
$HTTP["url"] =~ "^/cgi-bin/" {
    cgi.assign = ( "" => "/bin/bash" )
}
CGIEOF
fi

echo -e "${GREEN}  ✓ Portal deployed to /var/www/html/${NC}"

# ============================================
# STEP 8: Deploy scripts
# ============================================
echo -e "${YELLOW}[8/10]${NC} Deploying scripts..."

# GPIO coin listener
cp "$SYSTEM_DIR/usr/local/bin/gpio-coin-listener" /usr/local/bin/gpio-coin-listener
chmod +x /usr/local/bin/gpio-coin-listener
echo "  ✓ gpio-coin-listener"

# Session manager
cp "$SYSTEM_DIR/usr/local/bin/pisowifi-session-manager" /usr/local/bin/pisowifi-session-manager
chmod +x /usr/local/bin/pisowifi-session-manager
echo "  ✓ pisowifi-session-manager"

# Control script
cp "$SYSTEM_DIR/usr/local/bin/pisowifi-ctl" /usr/local/bin/pisowifi-ctl
chmod +x /usr/local/bin/pisowifi-ctl
echo "  ✓ pisowifi-ctl"

# API status generator
cp "$SYSTEM_DIR/usr/local/bin/pisowifi-api-update" /usr/local/bin/pisowifi-api-update
chmod +x /usr/local/bin/pisowifi-api-update
echo "  ✓ pisowifi-api-update"

# Deploy VLAN support
cp "$SYSTEM_DIR/usr/local/bin/aircoins-vlan-apply" /usr/local/bin/
chmod +x /usr/local/bin/aircoins-vlan-apply
echo "  ✓ aircoins-vlan-apply"

echo -e "${GREEN}  ✓ All scripts deployed${NC}"

# ============================================
# STEP 9: Deploy systemd services
# ============================================
echo -e "${YELLOW}[9/10]${NC} Deploying systemd services..."

# Create directories
mkdir -p /var/lib/pisowifi/sessions
mkdir -p /var/log/pisowifi

# Service files
cp "$SYSTEM_DIR/etc/systemd/system/aircoins-api.service" /etc/systemd/system/
cp "$SYSTEM_DIR/etc/systemd/system/aircoins-vlans.service" /etc/systemd/system/

# dnsmasq template service for per-VLAN DHCP
cp "$SYSTEM_DIR/etc/systemd/system/dnsmasq@.service" /etc/systemd/system/
mkdir -p /etc/dnsmasq.d

# Prevent systemd-networkd from managing VLAN sub-interfaces (no DHCP on end0.*)
mkdir -p /etc/systemd/network
cp "$SYSTEM_DIR/etc/systemd/network/05-aircoins-vlans.network" /etc/systemd/network/
networkctl reload 2>/dev/null || true

if [ "$IS_ARM" = true ]; then
    cp "$SYSTEM_DIR/etc/systemd/system/gpio-coin-listener.service" /etc/systemd/system/
    cp "$SYSTEM_DIR/etc/systemd/system/pisowifi-session.service" /etc/systemd/system/
else
    # x86: Deploy services but GPIO listener uses test mode
    cp "$SYSTEM_DIR/etc/systemd/system/gpio-coin-listener.service" /etc/systemd/system/
    cp "$SYSTEM_DIR/etc/systemd/system/pisowifi-session.service" /etc/systemd/system/
fi

# Reload systemd
systemctl daemon-reload

# Enable services on boot
systemctl enable aircoins-api.service
systemctl enable gpio-coin-listener.service
systemctl enable pisowifi-session.service
systemctl enable aircoins-vlans.service

echo -e "${GREEN}  ✓ Services deployed and enabled${NC}"

# ============================================
# STEP 10: Configure system
# ============================================
echo -e "${YELLOW}[10/10]${NC} Configuring system..."

# Set permissions
chmod 755 /var/lib/pisowifi
chmod 755 /var/log/pisowifi

# Create VLAN config file if not exists
mkdir -p /etc/pisowifi
if [ ! -f /etc/pisowifi/vlans.conf ]; then
    echo "# AirCoins VLAN Configuration" > /etc/pisowifi/vlans.conf
    echo "# Format: interface vlan_id ip/cidr description [portal]" >> /etc/pisowifi/vlans.conf
fi

echo -e "${GREEN}  ✓ System configured${NC}"

# ============================================
# DONE
# ============================================
echo ""
echo -e "${GREEN}╔══════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║     Installation Complete!               ║${NC}"
echo -e "${GREEN}╚══════════════════════════════════════════╝${NC}"
echo ""

if [ "$IS_ARM" = true ]; then
    echo -e "  ${CYAN}Admin Panel:${NC}  http://$(hostname -I 2>/dev/null | awk '{print $1}')/admin.html"
    echo -e "  ${CYAN}Login:${NC}        admin / admin123"
    echo ""
    echo -e "  ${YELLOW}GPIO Wiring:${NC}"
    echo -e "    Coin Acceptor Pulse → Physical Pin 7 (PA6)"
    echo -e "    Coin Acceptor GND   → Orange Pi GND"
else
    echo -e "  ${CYAN}Portal URL:${NC}  http://localhost"
    echo -e "  ${CYAN}Admin URL:${NC}   http://localhost/admin.html"
    echo -e "  ${CYAN}API URL:${NC}     http://localhost:8080/api/"
    echo -e "  ${CYAN}Login:${NC}       admin / admin123"
    echo ""
    echo -e "  ${YELLOW}Testing (x86 mode):${NC}"
    echo -e "    Simulate coin:    echo '5' > /var/lib/pisowifi/coin_event"
    echo -e "    Or use API:       curl -X POST http://localhost:8080/api/gpio/coin \\"
    echo -e "                        -H 'Content-Type: application/json' \\"
    echo -e "                        -d '{\"coin_value\": 5}'"
fi

echo ""
echo -e "  ${YELLOW}Commands:${NC}"
echo -e "    pisowifi-ctl start    ${NC}- Start all services"
echo -e "    pisowifi-ctl stop     ${NC}- Stop all services"
echo -e "    pisowifi-ctl restart  ${NC}- Restart all services"
echo -e "    pisowifi-ctl status   ${NC}- Show status"
echo -e "    pisowifi-ctl logs     ${NC}- View logs"
echo ""
echo -e "  ${YELLOW}To start now:${NC} sudo pisowifi-ctl start"
echo ""
echo -e "  ${RED}NOTE: A reboot is recommended before first use.${NC}"
echo -e "  ${RED}      sudo reboot${NC}"
echo ""

# Ask to start now
read -p "Start AirCoins now? [y/N]: " start_now
if [[ "$start_now" =~ ^[Yy]$ ]]; then
    echo ""
    pisowifi-ctl start
fi
