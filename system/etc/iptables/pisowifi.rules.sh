#!/bin/bash
# AirCoins PisoWiFi - iptables rules
# NAT + Captive Portal + Traffic Control
# Install to: /etc/iptables/pisowifi.rules

# ============================================
# VARIABLES
# ============================================
WIFI_IFACE="wlan0"      # WiFi AP interface (clients)
ETH_IFACE="eth0"        # Internet uplink (Ethernet)
WIFI_NET="192.168.42.0/24"
PORTAL_IP="192.168.42.1"

# Central interface/network config (overrides the defaults above if present)
PISOWIFI_CONF="/etc/pisowifi/pisowifi.conf"
[ -f "$PISOWIFI_CONF" ] && source "$PISOWIFI_CONF"

# ============================================
# FLUSH EXISTING RULES
# ============================================
iptables -F
iptables -t nat -F
iptables -t mangle -F
iptables -X

# ============================================
# DEFAULT POLICIES
# ============================================
iptables -P INPUT DROP
iptables -P FORWARD DROP
iptables -P OUTPUT ACCEPT

# ============================================
# NAT TABLE - Internet Sharing
# ============================================
# Enable MASQUERADE on the internet interface
iptables -t nat -A POSTROUTING -o $ETH_IFACE -j MASQUERADE

# ============================================
# CAPTIVE PORTAL - Redirect all traffic to portal
# ============================================
# Redirect all HTTP (port 80) to portal
iptables -t nat -A PREROUTING -i $WIFI_IFACE -p tcp --dport 80 -j DNAT --to-destination $PORTAL_IP:80

# Redirect HTTPS (port 443) to portal (for captive portal detection)
iptables -t nat -A PREROUTING -i $WIFI_IFACE -p tcp --dport 443 -j DNAT --to-destination $PORTAL_IP:80

# ============================================
# MANGLE TABLE - Mark traffic for QoS
# ============================================
# Mark DNS traffic
iptables -t mangle -A PREROUTING -i $WIFI_IFACE -p udp --dport 53 -j MARK --set-mark 1

# ============================================
# INPUT CHAIN - What the Orange Pi accepts
# ============================================
# Allow loopback
iptables -A INPUT -i lo -j ACCEPT

# Allow established connections
iptables -A INPUT -m state --state ESTABLISHED,RELATED -j ACCEPT

# Allow DHCP from WiFi clients
iptables -A INPUT -i $WIFI_IFACE -p udp --dport 67:68 -j ACCEPT

# Allow DNS from WiFi clients
iptables -A INPUT -i $WIFI_IFACE -p udp --dport 53 -j ACCEPT
iptables -A INPUT -i $WIFI_IFACE -p tcp --dport 53 -j ACCEPT

# Allow HTTP (portal) from WiFi clients
iptables -A INPUT -i $WIFI_IFACE -p tcp --dport 80 -j ACCEPT

# Allow SSH (for admin access) from WiFi
iptables -A INPUT -i $WIFI_IFACE -p tcp --dport 22 -j ACCEPT

# Allow ICMP (ping)
iptables -A INPUT -p icmp --icmp-type echo-request -j ACCEPT

# ============================================
# FORWARD CHAIN - Traffic from WiFi to Internet
# ============================================
# Allow forwarding from WiFi to Ethernet
iptables -A FORWARD -i $WIFI_IFACE -o $ETH_IFACE -j ACCEPT

# Allow return traffic
iptables -A FORWARD -i $ETH_IFACE -o $WIFI_IFACE -m state --state ESTABLISHED,RELATED -j ACCEPT

# ============================================
# CLIENT AUTHENTICATION (managed by session manager)
# ============================================
# Create a custom chain for allowed clients
iptables -N ALLOWED_CLIENTS 2>/dev/null || iptables -F ALLOWED_CLIENTS

# Insert at the beginning of FORWARD chain
iptables -I FORWARD -i $WIFI_IFACE -o $ETH_IFACE -j ALLOWED_CLIENTS

# Default: drop in ALLOWED_CLIENTS chain (no clients allowed initially)
iptables -A ALLOWED_CLIENTS -j DROP

# ============================================
# RATE LIMITING - Prevent abuse
# ============================================
# Limit new connections per client (anti-DDoS)
iptables -A INPUT -i $WIFI_IFACE -p tcp --syn -m connlimit --connlimit-above 20 -j REJECT

# ============================================
# LOGGING (for debugging)
# ============================================
# Log dropped packets (optional - comment out in production)
# iptables -A INPUT -j LOG --log-prefix "AIRCOINS-DROP: " --log-level 4
# iptables -A FORWARD -j LOG --log-prefix "AIRCOINS-FWD-DROP: " --log-level 4

echo "[OK] iptables rules applied successfully"
