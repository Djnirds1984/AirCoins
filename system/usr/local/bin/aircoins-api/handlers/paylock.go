package handlers

import (
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// payLockTimeout is the hard ceiling after which a held lock is forcibly
// released. Protects against a client that armed, walked away and never
// called /api/session/start — the next client would otherwise be stuck
// until the API process restarts.
// F2: Bumped from 15s to 60s — creditSession on a loaded Pi with slow DB
// can exceed 15s. Combined with the generation counter (F1) this gives a
// proper safety ceiling without spurious force-releases.
const payLockTimeout = 60 * time.Second

// payLockEntry is one per-VLAN semaphore (buffered channel of size 1)
// together with the IP of the current holder for diagnostics and a
// generation counter to prevent stale watchdogs from draining a newer
// holder's semaphore (F1).
type payLockEntry struct {
	sem    chan struct{} // capacity 1: empty = free, full = held
	holder string        // IP of the current holder (empty when free)
	gen    uint64        // incremented on each acquire; watchdog checks gen
	mu     sync.Mutex    // protects holder + gen writes
}

// payLockRegistry is the process-wide map of per-VLAN locks. The API
// runs as a single process so no cross-process coordination is needed.
var payLockRegistry = struct {
	sync.Mutex
	entries map[string]*payLockEntry
}{entries: make(map[string]*payLockEntry)}

// getOrCreateEntry returns the lock entry for the given VLAN key,
// creating it on first use.
func getOrCreateEntry(vlanKey string) *payLockEntry {
	payLockRegistry.Lock()
	defer payLockRegistry.Unlock()
	e, ok := payLockRegistry.entries[vlanKey]
	if !ok {
		e = &payLockEntry{sem: make(chan struct{}, 1)}
		payLockRegistry.entries[vlanKey] = e
	}
	return e
}

// PayLockTryAcquire attempts a non-blocking lock on the caller's VLAN.
// Returns true on success (caller MUST call PayLockRelease when done),
// false when another client on the same VLAN already holds the lock.
// holder receives the current holder's IP (may be empty).
//
// If the caller's IP cannot be resolved to a VLAN (e.g. admin from WAN),
// the lock is skipped and true is returned — unknown callers are never
// blocked, only known same-VLAN races are serialized.
//
// F8: the resolved VLAN key is returned so Release can reuse it without
// a second syscall.
func PayLockTryAcquire(clientIP string) (acquired bool, holder string, vlanKey string) {
	vlan := resolveVLAN(clientIP)
	if vlan == "" {
		// Cannot resolve to a VLAN — let the request through.
		return true, "", ""
	}

	entry := getOrCreateEntry(vlan)
	select {
	case entry.sem <- struct{}{}:
		// Acquired. Record holder, bump generation, and start a watchdog
		// goroutine that force-releases after payLockTimeout so a stuck
		// client cannot wedge the VLAN indefinitely.
		// F1: The watchdog captures the current generation and only drains
		// if gen still matches at fire time — a stale watchdog from a
		// previous holder cannot steal the semaphore from a newer holder.
		entry.mu.Lock()
		entry.holder = clientIP
		entry.gen++
		myGen := entry.gen
		entry.mu.Unlock()

		go func(e *payLockEntry, key string, spawnGen uint64) {
			time.Sleep(payLockTimeout)
			e.mu.Lock()
			currentGen := e.gen
			e.mu.Unlock()
			if currentGen != spawnGen {
				// A newer holder acquired the lock after us — our
				// watchdog is stale; do nothing.
				return
			}
			select {
			case <-e.sem:
				// Drain succeeded — the holder didn't release in time.
				e.mu.Lock()
				old := e.holder
				e.holder = ""
				e.mu.Unlock()
				log.Printf("paylock: watchdog force-released VLAN %s (holder %s exceeded %v)", key, old, payLockTimeout)
			default:
				// Already released by the holder — nothing to do.
			}
		}(entry, vlan, myGen)

		return true, "", vlan
	default:
		// Semaphore is full — another client holds the lock.
		entry.mu.Lock()
		h := entry.holder
		entry.mu.Unlock()
		return false, h, vlan
	}
}

// PayLockRelease releases the per-VLAN lock previously acquired by
// PayLockTryAcquire. If the watchdog already drained the semaphore this
// is a harmless no-op (logged for post-mortem, F9).
//
// F8: accepts the pre-resolved vlanKey from TryAcquire to avoid a second
// `ip route get` syscall. If vlanKey is empty the caller was unresolvable
// and no lock was taken.
func PayLockRelease(clientIP, vlanKey string) {
	if vlanKey == "" {
		return
	}

	payLockRegistry.Lock()
	entry, ok := payLockRegistry.entries[vlanKey]
	payLockRegistry.Unlock()
	if !ok {
		return
	}

	select {
	case <-entry.sem:
		// Successfully drained — we were still the holder.
		entry.mu.Lock()
		entry.holder = ""
		entry.mu.Unlock()
	default:
		// F9: Watchdog already drained it — log for post-mortem of the
		// race in F1 if it ever re-appears.
		log.Printf("paylock: Release default case — watchdog likely already drained VLAN %s (client %s)", vlanKey, clientIP)
	}
}

// resolveVLAN returns the gateway interface name (e.g. "end0.22") for
// the given client IP by parsing `ip -o route get <ip>`. Returns ""
// when the IP cannot be resolved (loopback, WAN admin, unreachable).
// F7: if the route goes through a gateway hop ("via" token before "dev"),
// the output interface is the gateway's outgoing interface, not the
// client's VLAN — return "" in that case.
func resolveVLAN(ip string) string {
	// `ip -o route get <ip>` outputs a single line like:
	//   10.0.22.5 dev end0.22 table 100 src 10.0.22.1 uid 0
	// or for gateway-routed:
	//   10.0.22.5 via 10.0.0.1 dev eth0 table 100 src 10.0.0.1 uid 0
	// We extract the field after "dev", but only if no "via" precedes it.
	out, err := exec.Command("ip", "-o", "route", "get", ip).Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	for i, f := range fields {
		if f == "via" {
			// Gateway-routed — the dev is the gateway's outgoing iface,
			// not the client's VLAN. Let the request through without locking.
			return ""
		}
		if f == "dev" && i+1 < len(fields) {
			dev := fields[i+1]
			// Ignore loopback — it's not a real VLAN.
			if dev == "lo" {
				return ""
			}
			return dev
		}
	}
	return ""
}
