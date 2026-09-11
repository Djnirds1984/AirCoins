// ============================================================
// AirCoins SUB-VENDO — NodeMCU ESP8266 Remote Coin Slot
// ============================================================
// One unit per portal VLAN. Polls the AirCoins API for the arm
// window, counts coin-acceptor pulses on GPIO4, and reports them
// to POST /api/subvendo/coins. 1 pulse = 1 peso = 1 credit unit
// — pricing is resolved server-side from the Pricing table.
// ============================================================

#include <ESP8266WiFi.h>
#include <ESP8266HTTPClient.h>
#include <WiFiClient.h>
#include <DNSServer.h>
#include <ESP8266WebServer.h>
#include <EEPROM.h>

// ---------------- Pins ----------------
// Raw GPIO numbers so the sketch compiles with any ESP8266 board target.
// On NodeMCU: GPIO4 = D2, GPIO5 = D1.
#define PIN_COIN   4   // GPIO4 (NodeMCU D2) — coin acceptor pulse (active low)
#define PIN_LED    LED_BUILTIN
#define PIN_RELAY  5   // GPIO5 (NodeMCU D1) — optional "insert coin" relay/lamp

// ---------------- Configuration Constants ----------------
static const char* SHARED_TOKEN = "aircoins-subvendo-shared-token";
static const uint32_t DEBOUNCE_MS = 30;              // Coin acceptor pulse debounce threshold
static const uint32_t RECONNECT_INTERVAL_MS = 5000;   // WiFi reconnect retry interval
static const uint32_t AP_TIMEOUT_MS = 300000UL;      // Setup AP auto-reboot timeout (5 minutes)
static const uint32_t DISCONNECT_AP_FALLBACK_MS = 600000UL; // Re-open setup AP after 10m offline

// EEPROM persistent storage structure
struct Config {
  char magic[4];       // Layout validator ("SV02")
  char ssid[33];
  char pass[65];
  char server[97];     // Base server address (e.g. http://10.0.22.1)
  char deviceID[25];   // Device identifier (e.g. "SV-1234ABCD")
};

Config cfg;
ESP8266WebServer setupServer(80);
DNSServer dnsServer;

// Operational State Variables
volatile uint32_t pulseCount = 0;       // Count of pending coin pulses
volatile uint32_t lastPulseMs = 0;       // Timestamp of last received interrupt pulse
bool armed = false;                      // Active coin acceptance window state
uint32_t lastPollMs = 0;                 // Timestamp of last server polling cycle
uint32_t pollIntervalMs = 2000;          // Dynamic polling interval (500ms when armed)
bool approved = false;                   // Admin authorization flag
uint32_t lastConnectedMs = 0;            // Timestamp of last successful network connection
uint32_t lastReconnectAttemptMs = 0;     // Non-blocking WiFi reconnect timer

// ============================================================
// Coin Interrupt Handler (Active Low, Hardware Debounced)
// ============================================================
void IRAM_ATTR coinISR() {
  uint32_t now = millis();
  if (now - lastPulseMs < DEBOUNCE_MS) return; // Debounce noise filter
  lastPulseMs = now;
  pulseCount++;
}

// ============================================================
// EEPROM Management Helpers
// ============================================================
void loadConfig() {
  EEPROM.begin(sizeof(Config));
  EEPROM.get(0, cfg);
  // SV02 magic validation: reset legacy or corrupted layout memory
  if (strncmp(cfg.magic, "SV02", 4) != 0) {
    memset(&cfg, 0, sizeof(cfg));
    strncpy(cfg.magic, "SV02", 4);
  }
}

void saveConfig() {
  EEPROM.begin(sizeof(Config));
  EEPROM.put(0, cfg);
  EEPROM.commit();
}

bool provisioned() { 
  return (strlen(cfg.deviceID) > 0 && strlen(cfg.ssid) > 0); 
}

// Generates a unique device ID derived from ESP8266 hardware chip ID
void genDeviceID() {
  if (strlen(cfg.deviceID) > 0) return;
  uint32_t chipId = ESP.getChipId();
  snprintf(cfg.deviceID, sizeof(cfg.deviceID), "SV-%06X", chipId & 0xFFFFFF);
  saveConfig();
}

// ============================================================
// Hardware Output & LED Indicator Helpers
// ============================================================
void led(bool on) { 
  digitalWrite(PIN_LED, on ? LOW : HIGH); 
  digitalWrite(PIN_RELAY, on ? HIGH : LOW); 
}

void blink(int times, int ms = 120) {
  for (int i = 0; i < times; i++) { 
    led(true); 
    delay(ms); 
    led(false); 
    delay(ms); 
  }
}

// ============================================================
// Network Communication Helpers (Buffer-optimized)
// ============================================================
bool apiGet(const char* path, String& body) {
  WiFiClient wc;
  HTTPClient http;
  char fullUrl[160];
  
  if (strchr(path, '?') != NULL) {
    snprintf(fullUrl, sizeof(fullUrl), "%s%s&device_id=%s", cfg.server, path, cfg.deviceID);
  } else {
    snprintf(fullUrl, sizeof(fullUrl), "%s%s?device_id=%s", cfg.server, path, cfg.deviceID);
  }

  if (!http.begin(wc, fullUrl)) return false;
  
  char authHeader[80];
  snprintf(authHeader, sizeof(authHeader), "Bearer %s", SHARED_TOKEN);
  http.addHeader("Authorization", authHeader);
  http.setTimeout(4000);
  
  int code = http.GET();
  if (code == 200) {
    body = http.getString();
  }
  http.end();
  return (code == 200);
}

bool apiPost(const char* path, const char* jsonPayload, String& body) {
  WiFiClient wc;
  HTTPClient http;
  char fullUrl[160];
  snprintf(fullUrl, sizeof(fullUrl), "%s%s", cfg.server, path);

  if (!http.begin(wc, fullUrl)) return false;

  char authHeader[80];
  snprintf(authHeader, sizeof(authHeader), "Bearer %s", SHARED_TOKEN);
  http.addHeader("Authorization", authHeader);
  http.addHeader("Content-Type", "application/json");
  http.setTimeout(4000);

  int code = http.POST(jsonPayload);
  if (code == 200) {
    body = http.getString();
  }
  http.end();
  return (code == 200);
}

// Parses simple numeric key values from small JSON state responses
long jsonLong(const String& body, const char* key) {
  int i = body.indexOf(key);
  if (i < 0) return 0;
  i = body.indexOf(':', i);
  if (i < 0) return 0;
  i++;
  while (i < (int)body.length() && body[i] == ' ') i++;
  return body.substring(i).toInt();
}

// ============================================================
// Sub-Vendo Registration & Status Logic
// ============================================================
bool registerDevice() {
  genDeviceID();
  
  String mac = WiFi.macAddress();
  mac.toLowerCase();

  char payload[160];
  snprintf(payload, sizeof(payload), 
           "{\"device_id\":\"%s\",\"ssid\":\"%s\",\"mac\":\"%s\"}", 
           cfg.deviceID, cfg.ssid, mac.c_str());

  String response;
  if (apiPost("/api/subvendo/register", payload, response)) {
    if (response.indexOf("\"approved\":true") >= 0 || response.indexOf("\"status\":\"online\"") >= 0) {
      approved = true;
    }
    return true;
  }
  return false;
}

void checkApproved() {
  char path[64];
  snprintf(path, sizeof(path), "/api/subvendo/approved?device_id=%s", cfg.deviceID);
  
  String response;
  if (apiGet(path, response)) {
    if (response.indexOf("\"approved\":true") >= 0) {
      approved = true;
    }
  }
}

// ============================================================
// Polling & Coin Reporting Routines
// ============================================================
void pollState() {
  String body;
  if (!apiGet("/api/subvendo/state", body)) {
    blink(2, 80); // API Communication Error
    return;
  }
  
  long expires = jsonLong(body, "\"expires_at\"");
  long now = jsonLong(body, "\"server_time\"");
  long idle = jsonLong(body, "\"idle_poll_ms\"");
  long armPoll = jsonLong(body, "\"arm_poll_ms\"");
  
  armed = (expires > now);
  pollIntervalMs = armed ? (armPoll > 0 ? armPoll : 500) : (idle > 0 ? idle : 2000);
  led(armed);
}

void reportCoins() {
  if (pulseCount == 0) return;

  // Safely extract pending pulse count
  noInterrupts();
  uint32_t pendingPulses = pulseCount;
  pulseCount = 0;
  interrupts();

  char payload[48];
  snprintf(payload, sizeof(payload), "{\"pulses\":%u}", pendingPulses);

  String response;
  if (apiPost("/api/subvendo/coins", payload, response)) {
    blink(pendingPulses > 3 ? 3 : (int)pendingPulses, 100); // Visual ACK
  } else {
    // Re-queue pulses if transaction fails
    noInterrupts();
    pulseCount += pendingPulses;
    interrupts();
    blink(2, 100);
  }
}

// ============================================================
// Non-blocking WiFi Initialization & Setup AP Portal
// ============================================================
bool connectSTA() {
  WiFi.mode(WIFI_STA);
  if (strlen(cfg.pass) > 0) {
    WiFi.begin(cfg.ssid, cfg.pass);
  } else {
    WiFi.begin(cfg.ssid);
  }
  
  uint32_t startMs = millis();
  while (WiFi.status() != WL_CONNECTED && millis() - startMs < 20000) {
    blink(1, 40);
    delay(50);
  }
  return (WiFi.status() == WL_CONNECTED);
}

void startSetupPortal() {
  WiFi.mode(WIFI_AP);
  WiFi.softAP("SubVendo-Setup", "insertcoin");
  dnsServer.start(53, "*", WiFi.softAPIP());

  setupServer.on("/scan", HTTP_GET, []() {
    int n = WiFi.scanNetworks(false, true);
    if (n < 0) {
      setupServer.send(500, "application/json", "{\"error\":\"scan failed\"}");
      WiFi.scanDelete();
      return;
    }
    
    int idx[64];
    for (int i = 0; i < n && i < 64; i++) idx[i] = i;
    for (int i = 0; i < n - 1 && i < 63; i++) {
      for (int j = i + 1; j < n && j < 64; j++) {
        if (WiFi.RSSI(idx[j]) > WiFi.RSSI(idx[i])) {
          int t = idx[i]; idx[i] = idx[j]; idx[j] = t;
        }
      }
    }
    
    String json = "{\"networks\":[";
    int count = (n < 64) ? n : 64;
    for (int k = 0; k < count; k++) {
      int i = idx[k];
      if (k) json += ",";
      String ssid = WiFi.SSID(i);
      ssid.replace("\\", "\\\\");
      ssid.replace("\"", "\\\"");
      json += "{\"ssid\":\"" + ssid + "\",\"rssi\":" + String(WiFi.RSSI(i)) +
              ",\"lock\":" + String(WiFi.encryptionType(i) != ENC_TYPE_NONE ? "true" : "false") + "}";
    }
    json += "]}";
    WiFi.scanDelete();
    setupServer.send(200, "application/json", json);
  });

  setupServer.on("/", HTTP_GET, []() {
    String html = "<html><head><meta name='viewport' content='width=device-width,initial-scale=1'>"
      "<style>body{font-family:sans-serif;}select,input{width:100%;padding:8px;margin:4px 0;box-sizing:border-box;}"
      "button{padding:10px 16px;margin-top:6px;}</style></head><body><h2>Sub-Vendo Setup</h2>"
      "<button type='button' onclick='doScan()'>Scan Wi-Fi</button><br>"
      "<select id='nets' onchange='pick()'><option value=''>-- scan first --</option></select>"
      "<form method='POST' action='/save'>"
      "WiFi SSID:<input name='ssid' id='ssid' required><br>"
      "WiFi Password:<input name='pass' id='pass' type='password' placeholder='leave blank for open hotspot'><br>"
      "Server URL:<input name='server' placeholder='http://10.0.22.1' required><br>"
      "<input type='submit' value='Save & Register'></form>"
      "<script>"
      "function doScan(){var b=document.querySelector('button');b.textContent='Scanning...';b.disabled=true;"
      "fetch('/scan',{signal:AbortSignal.timeout(8000)}).then(r=>r.json()).then(d=>{"
      "var s=document.getElementById('nets');s.innerHTML='<option value=\"\">-- select network --</option>';"
      "if(!d.networks||!d.networks.length){b.textContent='No networks found';b.disabled=false;return;}"
      "d.networks.forEach(function(n){"
      "var o=document.createElement('option');o.value=n.ssid;"
      "o.textContent=n.ssid+' ('+n.rssi+'dBm'+(n.lock?' locked':' open')+')';"
      "s.appendChild(o);});b.textContent='Scan Wi-Fi';b.disabled=false;})"
      ".catch(function(e){b.textContent='Scan timeout - try again';b.disabled=false;});}"
      "function pick(){var s=document.getElementById('nets');"
      "if(s.value){document.getElementById('ssid').value=s.value;"
      "document.getElementById('pass').value='';}}"
      "</script></body></html>";
    setupServer.send(200, "text/html", html);
  });

  setupServer.on("/save", HTTP_POST, []() {
    String ssid = setupServer.arg("ssid");
    String pass = setupServer.arg("pass");
    String server = setupServer.arg("server");
    
    if (ssid.length() && server.length()) {
      memset(&cfg, 0, sizeof(cfg));
      strncpy(cfg.magic, "SV02", 4);
      strncpy(cfg.ssid, ssid.c_str(), 32);
      strncpy(cfg.pass, pass.c_str(), 64);
      strncpy(cfg.server, server.c_str(), 96);
      
      genDeviceID();
      saveConfig();
      
      if (!connectSTA()) {
        setupServer.send(200, "text/html", "<html><body>Could not join Wi-Fi. <a href='/'>Back</a></body></html>");
        return;
      }
      
      if (registerDevice()) {
        setupServer.send(200, "text/html", "<html><body>Registered! Device ID: <b>" + String(cfg.deviceID) + "</b>. Waiting for admin approval...</body></html>");
        blink(3, 300);
        delay(500);
        ESP.restart();
        return;
      }
      setupServer.send(200, "text/html", "<html><body>Registration failed (check server URL). <a href='/'>Back</a></body></html>");
      return;
    }
    setupServer.send(400, "text/html", "SSID and Server are required.");
  });

  setupServer.begin();
  uint32_t portalStart = millis();
  
  while (true) {
    dnsServer.processNextRequest();
    setupServer.handleClient();
    
    // Reboot after 5 minutes to restore STA retry mode if left unattended
    if (millis() - portalStart > AP_TIMEOUT_MS) { 
      ESP.restart(); 
    }
    blink(1, 60);
    yield();
  }
}

// ============================================================
// Setup & Main Execution Loop
// ============================================================
void setup() {
  pinMode(PIN_COIN, INPUT_PULLUP);
  pinMode(PIN_LED, OUTPUT);
  pinMode(PIN_RELAY, OUTPUT);
  led(false);

  loadConfig();
  lastConnectedMs = millis();

  if (!provisioned()) {
    startSetupPortal();
  }

  WiFi.persistent(false);
  WiFi.setAutoReconnect(true);
  connectSTA();

  if (WiFi.status() != WL_CONNECTED) {
    blink(3, 100);
  } else if (provisioned() && !approved) {
    checkApproved();
  }

  attachInterrupt(digitalPinToInterrupt(PIN_COIN), coinISR, FALLING);
}

void loop() {
  uint32_t now = millis();

  // Non-blocking WiFi Reconnection State Machine
  if (WiFi.status() != WL_CONNECTED) {
    if (now - lastReconnectAttemptMs >= RECONNECT_INTERVAL_MS) {
      lastReconnectAttemptMs = now;
      WiFi.reconnect();
    }
    
    // Fall back to Setup AP if disconnected for 10 consecutive minutes
    if (now - lastConnectedMs > DISCONNECT_AP_FALLBACK_MS) {
      startSetupPortal();
    }
    
    blink(1, 150); // Single flash indicates network disconnect
    yield();
    return;
  }
  
  lastConnectedMs = now;

  // PENDING State: Waiting for Admin approval
  if (!approved) {
    if (now - lastPollMs >= 3000) {
      lastPollMs = now;
      if (registerDevice()) {
        if (approved) {
          blink(3, 400); // Admin approved
        } else {
          blink(1, 250); // Waiting for approval
        }
      } else {
        blink(2, 200); // Registration retry failed
      }
    }
    yield();
    return;
  }

  // ONLINE State: Operational Polling & Coin Reporting
  if (now - lastPollMs >= pollIntervalMs) {
    lastPollMs = now;
    pollState();
    reportCoins();
    
    // Heartbeat indicator pulse
    led(true); 
    delay(20); 
    led(armed);
  }
  
  yield();
}