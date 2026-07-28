#!/bin/bash
# ============================================
# AirCoins PisoWiFi - One-Click Installer
# ============================================
# Supports:
#   - Orange Pi One (ARM) - Full hardware support
#   - Ubuntu/Debian x86   - Development mode (no GPIO/WiFi AP)
#
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
echo "  ║     AirCoins PisoWiFi System v$VERSION     ║"
echo "  ║     $PLATFORM_NAME Installer  "
echo "  ╚══════════════════════════════════════════╝"
echo -e "${NC}"

if [ "$IS_X86" = true ]; then
    echo -e "${YELLOW}  ⚠ x86 Development Mode${NC}"
    echo -e "  ${YELLOW}GPIO and WiFi AP features disabled.${NC}"
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
    # Full ARM installation with WiFi AP support
    apt-get install -y -qq \
        hostapd \
        dnsmasq \
        lighttpd \
        iptables \
        iptables-persistent \
        netfilter-persistent \
        iw \
        rfkill \
        usbutils \
        wget \
        curl \
        jq \
        bc \
        postgresql \
        postgresql-contrib \
        golang-go
else
    # x86 installation - skip WiFi AP packages
    apt-get install -y -qq \
        lighttpd \
        wget \
        curl \
        jq \
        bc \
        postgresql \
        postgresql-contrib \
        golang-go
fi

echo -e "${GREEN}  ✓ Packages installed${NC}"

# ============================================
# STEP 3: Setup PostgreSQL Database
# ============================================
echo -e "${YELLOW}[3/10]${NC} Setting up PostgreSQL database..."

# Start PostgreSQL
systemctl start postgresql
systemctl enable postgresql

# Create database user and database
sudo -u postgres psql -c "CREATE USER aircoins WITH PASSWORD 'aircoins123';" 2>/dev/null || true
sudo -u postgres psql -c "CREATE DATABASE aircoins OWNER aircoins;" 2>/dev/null || true
sudo -u postgres psql -c "GRANT ALL PRIVILEGES ON DATABASE aircoins TO aircoins;" 2>/dev/null || true

# Run schema
psql -U aircoins -d aircoins -h localhost -f "$SYSTEM_DIR/database/schema.sql" 2>/dev/null || {
    echo -e "${YELLOW}  ⚠ Schema may already exist, continuing...${NC}"
}

echo -e "${GREEN}  ✓ PostgreSQL database ready${NC}"

# ============================================
# STEP 4: Build and Deploy Go API
# ============================================
echo -e "${YELLOW}[4/10]${NC} Building Go API server..."

cd "$SYSTEM_DIR/usr/local/bin/aircoins-api"

# Download dependencies
go mod download

# Build the binary
go build -o aircoins-api .

# Deploy
mkdir -p /usr/local/bin/aircoins-api
cp aircoins-api /usr/local/bin/aircoins-api/
chmod +x /usr/local/bin/aircoins-api/aircoins-api

echo -e "${GREEN}  ✓ Go API built and deployed${NC}"

cd "$SCRIPT_DIR"

# ============================================
# STEP 5: Install WiringOP (ARM only)
# ============================================
echo -e "${YELLOW}[5/10]${NC} Installing GPIO support..."

if [ "$IS_ARM" = true ]; then
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
else
    echo -e "${YELLOW}  ⚠ x86 platform - GPIO hardware not available${NC}"
    echo -e "  ${CYAN}GPIO listener will run in file-based test mode.${NC}"
    echo -e "  ${CYAN}To simulate coin: echo '5' > /var/lib/pisowifi/coin_event${NC}"
fi

# ============================================
# STEP 6: Deploy configuration files
# ============================================
echo -e "${YELLOW}[6/10]${NC} Deploying configuration files..."

# lighttpd (always needed)
cp "$SYSTEM_DIR/etc/lighttpd/lighttpd.conf" /etc/lighttpd/lighttpd.conf
echo "  ✓ lighttpd.conf"

if [ "$IS_ARM" = true ]; then
    # hostapd
    cp "$SYSTEM_DIR/etc/hostapd/hostapd.conf" /etc/hostapd/hostapd.conf
    echo "  ✓ hostapd.conf"

    # dnsmasq
    cp "$SYSTEM_DIR/etc/dnsmasq/dnsmasq.conf" /etc/dnsmasq.conf
    echo "  ✓ dnsmasq.conf"

    # iptables rules
    cp "$SYSTEM_DIR/etc/iptables/pisowifi.rules.sh" /etc/iptables/pisowifi.rules.sh
    chmod +x /etc/iptables/pisowifi.rules.sh
    echo "  ✓ iptables rules"
else
    echo -e "${YELLOW}  ⚠ Skipping hostapd/dnsmasq/iptables (x86 mode)${NC}"
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

echo -e "${GREEN}  ✓ Services deployed and enabled${NC}"

# ============================================
# STEP 10: Configure system
# ============================================
echo -e "${YELLOW}[10/10]${NC} Configuring system..."

if [ "$IS_ARM" = true ]; then
    # Enable IP forwarding
    echo "net.ipv4.ip_forward=1" > /etc/sysctl.d/99-pisowifi.conf
    sysctl -p /etc/sysctl.d/99-pisowifi.conf > /dev/null 2>&1
    echo "  ✓ IP forwarding enabled"

    # Disable NetworkManager on wlan0 (if exists)
    if [ -f /etc/NetworkManager/NetworkManager.conf ]; then
        cat > /etc/NetworkManager/conf.d/10-pisowifi.conf << EOF
[keyfile]
unmanaged-devices=interface-name:wlan0
EOF
        echo "  ✓ NetworkManager configured"
    fi

    # Configure hostapd to use our config
    if [ -f /etc/default/hostapd ]; then
        sed -i 's|#DAEMON_CONF=.*|DAEMON_CONF="/etc/hostapd/hostapd.conf"|' /etc/default/hostapd
    fi
else
    echo "  ✓ x86 mode - skipping WiFi/iptables configuration"
fi

# Set permissions
chmod 755 /var/lib/pisowifi
chmod 755 /var/log/pisowifi

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
    echo -e "  ${CYAN}Portal URL:${NC}  http://192.168.42.1"
    echo -e "  ${CYAN}WiFi SSID:${NC}   AirCoins_Free"
    echo -e "  ${CYAN}Admin:${NC}       http://192.168.42.1/admin.html"
    echo -e "  ${CYAN}Login:${NC}       admin / admin123"
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
read -p "Start PisoWiFi now? [y/N]: " start_now
if [[ "$start_now" =~ ^[Yy]$ ]]; then
    echo ""
    pisowifi-ctl start
fi
#!/bin/bash
# ============================================
# AirCoins PisoWiFi - One-Click Installer
# ============================================
# For Orange Pi One (Armbian / Orange Pi OS)
# Run as root: sudo bash install.sh
# ============================================

set -e

# ============================================
# CONFIGURATION
# ============================================
VERSION="1.0.0"
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
echo "  ║     AirCoins PisoWiFi System v$VERSION     ║"
echo "  ║     Orange Pi One Installer              ║"
echo "  ╚══════════════════════════════════════════╝"
echo -e "${NC}"

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
apt-get install -y -qq \
    hostapd \
    dnsmasq \
    lighttpd \
    iptables \
    iptables-persistent \
    netfilter-persistent \
    iw \
    rfkill \
    usbutils \
    wget \
    curl \
    jq \
    bc \
    postgresql \
    postgresql-contrib \
    golang-go

echo -e "${GREEN}  ✓ Packages installed${NC}"

# ============================================
# STEP 3: Setup PostgreSQL Database
# ============================================
echo -e "${YELLOW}[3/10]${NC} Setting up PostgreSQL database..."

# Start PostgreSQL
systemctl start postgresql
systemctl enable postgresql

# Create database user and database
sudo -u postgres psql -c "CREATE USER aircoins WITH PASSWORD 'aircoins123';" 2>/dev/null || true
sudo -u postgres psql -c "CREATE DATABASE aircoins OWNER aircoins;" 2>/dev/null || true
sudo -u postgres psql -c "GRANT ALL PRIVILEGES ON DATABASE aircoins TO aircoins;" 2>/dev/null || true

# Run schema
psql -U aircoins -d aircoins -h localhost -f "$SYSTEM_DIR/database/schema.sql" 2>/dev/null || {
    echo -e "${YELLOW}  ⚠ Schema may already exist, continuing...${NC}"
}

echo -e "${GREEN}  ✓ PostgreSQL database ready${NC}"

# ============================================
# STEP 4: Build and Deploy Go API
# ============================================
echo -e "${YELLOW}[4/10]${NC} Building Go API server..."

cd "$SYSTEM_DIR/usr/local/bin/aircoins-api"

# Download dependencies
go mod download

# Build the binary
go build -o aircoins-api .

# Deploy
mkdir -p /usr/local/bin/aircoins-api
cp aircoins-api /usr/local/bin/aircoins-api/
chmod +x /usr/local/bin/aircoins-api/aircoins-api

echo -e "${GREEN}  ✓ Go API built and deployed${NC}"

cd "$SCRIPT_DIR"

# ============================================
# STEP 5: Install WiringOP (GPIO library)
# ============================================
echo -e "${YELLOW}[5/10]${NC} Installing WiringOP (GPIO library)..."

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
                echo -e "${RED}  ✘ WiringOP source not available. GPIO listener will fail without GPIO hardware.${NC}"
                echo "  To install manually later:"
                echo "    git clone https://github.com/orangepi-xunlong/wiringOP.git"
                echo "    cd wiringOP && ./build && sudo ./build install"
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

# ============================================
# STEP 6: Deploy configuration files
# ============================================
echo -e "${YELLOW}[6/10]${NC} Deploying configuration files..."

# hostapd
cp "$SYSTEM_DIR/etc/hostapd/hostapd.conf" /etc/hostapd/hostapd.conf
echo "  ✓ hostapd.conf"

# dnsmasq
cp "$SYSTEM_DIR/etc/dnsmasq/dnsmasq.conf" /etc/dnsmasq.conf
echo "  ✓ dnsmasq.conf"

# lighttpd
cp "$SYSTEM_DIR/etc/lighttpd/lighttpd.conf" /etc/lighttpd/lighttpd.conf
echo "  ✓ lighttpd.conf"

# iptables rules
cp "$SYSTEM_DIR/etc/iptables/pisowifi.rules.sh" /etc/iptables/pisowifi.rules.sh
chmod +x /etc/iptables/pisowifi.rules.sh
echo "  ✓ iptables rules"

echo -e "${GREEN}  ✓ All configs deployed${NC}"

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

echo -e "${GREEN}  ✓ All scripts deployed${NC}"

# ============================================
# STEP 9: Deploy systemd services
# ============================================
echo -e "${YELLOW}[9/10]${NC} Deploying systemd services..."

# Create directories
mkdir -p /var/lib/pisowifi/sessions
mkdir -p /var/log/pisowifi

# Service files
cp "$SYSTEM_DIR/etc/systemd/system/gpio-coin-listener.service" /etc/systemd/system/
cp "$SYSTEM_DIR/etc/systemd/system/pisowifi-session.service" /etc/systemd/system/
cp "$SYSTEM_DIR/etc/systemd/system/aircoins-api.service" /etc/systemd/system/

# Reload systemd
systemctl daemon-reload

# Enable services on boot
systemctl enable aircoins-api.service
systemctl enable gpio-coin-listener.service
systemctl enable pisowifi-session.service

echo -e "${GREEN}  ✓ Services deployed and enabled${NC}"

# ============================================
# STEP 10: Configure system
# ============================================
echo -e "${YELLOW}[10/10]${NC} Configuring system..."

# Enable IP forwarding
echo "net.ipv4.ip_forward=1" > /etc/sysctl.d/99-pisowifi.conf
sysctl -p /etc/sysctl.d/99-pisowifi.conf > /dev/null 2>&1
echo "  ✓ IP forwarding enabled"

# Disable NetworkManager on wlan0 (if exists)
if [ -f /etc/NetworkManager/NetworkManager.conf ]; then
    cat > /etc/NetworkManager/conf.d/10-pisowifi.conf << EOF
[keyfile]
unmanaged-devices=interface-name:wlan0
EOF
    echo "  ✓ NetworkManager configured"
fi

# Configure hostapd to use our config
if [ -f /etc/default/hostapd ]; then
    sed -i 's|#DAEMON_CONF=.*|DAEMON_CONF="/etc/hostapd/hostapd.conf"|' /etc/default/hostapd
fi

# Set permissions
chmod 755 /var/lib/pisowifi
chmod 755 /var/log/pisowifi

echo -e "${GREEN}  ✓ System configured${NC}"

# ============================================
# DONE
# ============================================
echo ""
echo -e "${GREEN}╔══════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║     Installation Complete!               ║${NC}"
echo -e "${GREEN}╚══════════════════════════════════════════╝${NC}"
echo ""
echo -e "  ${CYAN}Portal URL:${NC}  http://192.168.42.1"
echo -e "  ${CYAN}WiFi SSID:${NC}   AirCoins_Free"
echo -e "  ${CYAN}Admin:${NC}       admin / admin123"
echo ""
echo -e "  ${YELLOW}Commands:${NC}"
echo -e "    pisowifi-ctl start    ${NC}- Start all services"
echo -e "    pisowifi-ctl stop     ${NC}- Stop all services"
echo -e "    pisowifi-ctl restart  ${NC}- Restart all services"
echo -e "    pisowifi-ctl status   ${NC}- Show status"
echo -e "    pisowifi-ctl logs     ${NC}- View logs"
echo ""
echo -e "  ${YELLOW}GPIO Wiring:${NC}"
echo -e "    Coin Acceptor Pulse → Physical Pin 7 (PA6)"
echo -e "    Coin Acceptor GND   → Orange Pi GND"
echo ""
echo -e "  ${YELLOW}To start now:${NC} sudo pisowifi-ctl start"
echo ""
echo -e "  ${RED}NOTE: A reboot is recommended before first use.${NC}"
echo -e "  ${RED}      sudo reboot${NC}"
echo ""

# Ask to start now
read -p "Start PisoWiFi now? [y/N]: " start_now
if [[ "$start_now" =~ ^[Yy]$ ]]; then
    echo ""
    pisowifi-ctl start
fi
