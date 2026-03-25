package nwg

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/sys/kmod"
)

const (
	awgProxyDir         = "/opt/etc/awg-manager/modules"
	defaultKoPath       = awgProxyDir + "/awg_proxy.ko"
	expectedKmodVersion = "1.1.0" // minimum required awg_proxy.ko version
)

// KmodManager manages the awg_proxy.ko kernel module for NativeWG tunnels.
type KmodManager struct {
	mu      sync.Mutex
	tunnels map[string]kmodEntry // tunnelID → endpoint for del
	koPath  string
	log     *logger.Logger
}

// kmodEntry tracks a loaded tunnel's endpoint and proxy listen port.
type kmodEntry struct {
	endpointIP   string
	endpointPort int
	listenPort   int // proxy listen port on 127.0.0.1
}

// KmodConfig holds AWG obfuscation parameters for the kernel module.
type KmodConfig struct {
	EndpointIP   string
	EndpointPort int
	H1, H2, H3, H4        string // "min-max" or single value
	S1, S2, S3, S4         int
	Jc, Jmin, Jmax         int
	PubServerHex           string // 64-char hex
	PubClientHex           string // 64-char hex
	I1, I2, I3, I4, I5     string // CPS template strings
	BindIface              string // kernel iface for SO_BINDTODEVICE (e.g. "eth3")
}

// KmodResult holds the result of adding a tunnel to the kernel module.
type KmodResult struct {
	ListenPort int // proxy listen port on 127.0.0.1
}

// NewKmodManager creates a new KmodManager.
func NewKmodManager(log *logger.Logger) *KmodManager {
	return &KmodManager{
		tunnels: make(map[string]kmodEntry),
		log:     log,
	}
}

// resolveKoPath returns the path to awg_proxy.ko.
// Priority: per-model (KN-1011 HIGHMEM) → per-device (Xiaomi-R3P) → SoC (mt7621/mt7628) → arch default.
func (km *KmodManager) resolveKoPath() string {
	// 1. Per-model override (e.g. awg_proxy-KN-1011.ko for HIGHMEM)
	model := kmod.DetectModel()
	if model != "" {
		modelPath := fmt.Sprintf(awgProxyDir+"/awg_proxy-%s.ko", model)
		if _, err := os.Stat(modelPath); err == nil {
			km.log.Infof("kmod: using model-specific awg_proxy for %s", model)
			return modelPath
		}
	}

	// 2. Per-device override (e.g. awg_proxy-Xiaomi-R3P.ko for non-Keenetic HW)
	device := kmod.DetectDevice()
	if device != "" {
		devicePath := fmt.Sprintf(awgProxyDir+"/awg_proxy-%s.ko", device)
		if _, err := os.Stat(devicePath); err == nil {
			km.log.Infof("kmod: using device-specific awg_proxy for %s", device)
			return devicePath
		}
	}

	// 3. SoC-specific (e.g. awg_proxy-mt7628.ko for non-SMP mipsel)
	soc := kmod.DetectSoC()
	if soc != kmod.SoCUnknown {
		socPath := fmt.Sprintf(awgProxyDir+"/awg_proxy-%s.ko", string(soc))
		if _, err := os.Stat(socPath); err == nil {
			km.log.Infof("kmod: using SoC-specific awg_proxy for %s", string(soc))
			return socPath
		}
	}

	// 4. Arch default (fallback)
	return defaultKoPath
}

// EnsureLoaded loads awg_proxy.ko if not already loaded.
// If the module is loaded but has a different version, it is reloaded.
// Cleans up stale proxy slots left from previous daemon runs.
func (km *KmodManager) EnsureLoaded() error {
	km.mu.Lock()
	defer km.mu.Unlock()

	if km.koPath == "" {
		km.koPath = km.resolveKoPath()
	}

	if km.isLoadedLocked() {
		// Check version — reload if loaded version is below expected.
		loaded := km.readVersionLocked()
		if loaded != "" && compareKmodVersions(loaded, expectedKmodVersion) < 0 {
			// Don't reload if there are active proxy entries —
			// rmmod would destroy ALL running tunnels' proxies.
			if len(km.tunnels) > 0 {
				km.log.Warnf("kmod: outdated (loaded=%s, want>=%s) but %d active tunnels, skipping reload",
					loaded, expectedKmodVersion, len(km.tunnels))
				return nil
			}
			km.log.Infof("kmod: outdated (loaded=%s, want>=%s), reloading", loaded, expectedKmodVersion)
			_, _ = exec.Run(context.Background(), "rmmod", "awg_proxy")
			// Fall through to insmod below.
		} else {
			km.purgeStaleSlots()
			return nil
		}
	}

	result, err := exec.Run(context.Background(), "insmod", km.koPath)
	if err != nil {
		return fmt.Errorf("insmod %s: %w", km.koPath, err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("insmod %s: exit %d: %s", km.koPath, result.ExitCode, result.Stderr)
	}

	km.log.Infof("kmod: awg_proxy.ko loaded (expected>=%s)", expectedKmodVersion)
	return nil
}

// purgeStaleSlots reads /proc/awg_proxy/list and removes any proxy entries
// that are not tracked in km.tunnels. This handles daemon restarts where
// the in-memory map is empty but kernel slots are still active.
func (km *KmodManager) purgeStaleSlots() {
	data, err := os.ReadFile("/proc/awg_proxy/list")
	if err != nil {
		return
	}

	// Build set of known endpoints from tracked tunnels
	known := make(map[string]bool)
	for _, entry := range km.tunnels {
		known[fmt.Sprintf("%s:%d", entry.endpointIP, entry.endpointPort)] = true
	}

	// Parse each line: "IP:PORT listen=..."
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" || strings.HasPrefix(line, "(") {
			continue
		}
		endpoint := strings.SplitN(line, " ", 2)[0]
		if known[endpoint] {
			continue
		}
		// Stale slot — remove it
		km.log.Infof("kmod: purging stale proxy slot %s", endpoint)
		_ = os.WriteFile("/proc/awg_proxy/del", []byte(endpoint), 0)
	}
}

// readVersionLocked reads the loaded module version from /proc/awg_proxy/version.
func (km *KmodManager) readVersionLocked() string {
	data, err := os.ReadFile("/proc/awg_proxy/version")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// AddTunnel writes a tunnel config to /proc/awg_proxy/add and reads back the
// assigned proxy listen port from /proc/awg_proxy/list.
// Idempotent: removes any stale entry for the same endpoint before adding.
func (km *KmodManager) AddTunnel(tunnelID string, cfg KmodConfig) (KmodResult, error) {
	km.mu.Lock()
	defer km.mu.Unlock()

	// Remove stale entry if endpoint already registered (daemon restart, test.sh leftover, etc.)
	delLine := fmt.Sprintf("%s:%d", cfg.EndpointIP, cfg.EndpointPort)
	_ = os.WriteFile("/proc/awg_proxy/del", []byte(delLine), 0)

	line := buildProcLine(cfg)

	if err := os.WriteFile("/proc/awg_proxy/add", []byte(line), 0); err != nil {
		return KmodResult{}, fmt.Errorf("kmod add tunnel %s: %w", tunnelID, err)
	}

	// Read listen port from /proc/awg_proxy/list
	listenPort, err := km.readListenPortLocked(cfg.EndpointIP, cfg.EndpointPort)
	if err != nil {
		// Dump list contents for debugging
		if raw, rerr := os.ReadFile("/proc/awg_proxy/list"); rerr == nil {
			km.log.Warnf("kmod: /proc/awg_proxy/list contents:\n%s", string(raw))
		}
		km.log.Warnf("kmod: added tunnel %s but failed to read listen port: %v (endpoint=%s:%d)", tunnelID, err, cfg.EndpointIP, cfg.EndpointPort)
		return KmodResult{}, fmt.Errorf("kmod read listen port for %s: %w", tunnelID, err)
	}

	km.tunnels[tunnelID] = kmodEntry{
		endpointIP:   cfg.EndpointIP,
		endpointPort: cfg.EndpointPort,
		listenPort:   listenPort,
	}

	km.log.Infof("kmod: added tunnel %s (%s:%d -> 127.0.0.1:%d)", tunnelID, cfg.EndpointIP, cfg.EndpointPort, listenPort)
	return KmodResult{ListenPort: listenPort}, nil
}

// listenPortRe matches "listen=127.0.0.1:PORT" in the proxy list output.
var listenPortRe = regexp.MustCompile(`listen=127\.0\.0\.1:(\d+)`)

// readListenPortLocked reads /proc/awg_proxy/list and finds the listen port
// for the given endpoint. Must be called with km.mu held.
func (km *KmodManager) readListenPortLocked(endpointIP string, endpointPort int) (int, error) {
	data, err := os.ReadFile("/proc/awg_proxy/list")
	if err != nil {
		return 0, fmt.Errorf("read /proc/awg_proxy/list: %w", err)
	}

	// Each line: "IP:PORT listen=127.0.0.1:LPORT rx=... tx=..."
	target := fmt.Sprintf("%s:%d ", endpointIP, endpointPort)
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, target) {
			continue
		}
		m := listenPortRe.FindStringSubmatch(line)
		if m == nil {
			return 0, fmt.Errorf("listen port not found in line: %s", line)
		}
		port, err := strconv.Atoi(m[1])
		if err != nil {
			return 0, fmt.Errorf("parse listen port %q: %w", m[1], err)
		}
		return port, nil
	}

	return 0, fmt.Errorf("endpoint %s:%d not found in proxy list", endpointIP, endpointPort)
}

// GetListenPort returns the cached listen port for a tunnel.
func (km *KmodManager) GetListenPort(tunnelID string) (int, bool) {
	km.mu.Lock()
	defer km.mu.Unlock()
	entry, ok := km.tunnels[tunnelID]
	if !ok {
		return 0, false
	}
	return entry.listenPort, true
}

// RemoveTunnel writes endpoint to /proc/awg_proxy/del.
func (km *KmodManager) RemoveTunnel(tunnelID string) error {
	km.mu.Lock()
	defer km.mu.Unlock()

	entry, ok := km.tunnels[tunnelID]
	if !ok {
		return nil
	}

	line := fmt.Sprintf("%s:%d", entry.endpointIP, entry.endpointPort)
	if err := os.WriteFile("/proc/awg_proxy/del", []byte(line), 0); err != nil {
		return fmt.Errorf("kmod del tunnel %s: %w", tunnelID, err)
	}

	delete(km.tunnels, tunnelID)
	km.log.Infof("kmod: removed tunnel %s", tunnelID)
	return nil
}

// IsLoaded checks if /proc/awg_proxy/version exists.
func (km *KmodManager) IsLoaded() bool {
	km.mu.Lock()
	defer km.mu.Unlock()
	return km.isLoadedLocked()
}

func (km *KmodManager) isLoadedLocked() bool {
	_, err := os.Stat("/proc/awg_proxy/version")
	return err == nil
}

// HasTunnel checks if a tunnel is tracked.
func (km *KmodManager) HasTunnel(tunnelID string) bool {
	km.mu.Lock()
	defer km.mu.Unlock()
	_, ok := km.tunnels[tunnelID]
	return ok
}

// RemoveAllTunnels removes all tracked tunnels from the kernel module.
// The module itself stays loaded (safe for daemon restart).
func (km *KmodManager) RemoveAllTunnels() {
	km.mu.Lock()
	ids := make([]string, 0, len(km.tunnels))
	for id := range km.tunnels {
		ids = append(ids, id)
	}
	km.mu.Unlock()

	for _, id := range ids {
		if err := km.RemoveTunnel(id); err != nil {
			km.log.Warnf("kmod: failed to remove tunnel %s on shutdown: %v", id, err)
		}
	}
}

// compareKmodVersions compares two semver-like version strings.
// Returns -1 if a < b, 0 if a == b, 1 if a > b.
func compareKmodVersions(a, b string) int {
	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")
	maxLen := len(partsA)
	if len(partsB) > maxLen {
		maxLen = len(partsB)
	}
	for i := 0; i < maxLen; i++ {
		var numA, numB int
		if i < len(partsA) {
			numA, _ = strconv.Atoi(partsA[i])
		}
		if i < len(partsB) {
			numB, _ = strconv.Atoi(partsB[i])
		}
		if numA < numB {
			return -1
		}
		if numA > numB {
			return 1
		}
	}
	return 0
}

// buildProcLine builds the config line for /proc/awg_proxy/add.
// Format: IP:PORT H1=min-max H2=... S1=N ... Jc=N ... PUB_SERVER=hex PUB_CLIENT=hex I1="template"
func buildProcLine(cfg KmodConfig) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s:%d", cfg.EndpointIP, cfg.EndpointPort)
	fmt.Fprintf(&b, " H1=%s H2=%s H3=%s H4=%s", cfg.H1, cfg.H2, cfg.H3, cfg.H4)
	fmt.Fprintf(&b, " S1=%d S2=%d S3=%d S4=%d", cfg.S1, cfg.S2, cfg.S3, cfg.S4)
	fmt.Fprintf(&b, " Jc=%d Jmin=%d Jmax=%d", cfg.Jc, cfg.Jmin, cfg.Jmax)

	if cfg.PubServerHex != "" && cfg.PubClientHex != "" {
		fmt.Fprintf(&b, " PUB_SERVER=%s PUB_CLIENT=%s", cfg.PubServerHex, cfg.PubClientHex)
	}

	if cfg.I1 != "" {
		fmt.Fprintf(&b, " I1=\"%s\"", cfg.I1)
	}
	if cfg.I2 != "" {
		fmt.Fprintf(&b, " I2=\"%s\"", cfg.I2)
	}
	if cfg.I3 != "" {
		fmt.Fprintf(&b, " I3=\"%s\"", cfg.I3)
	}
	if cfg.I4 != "" {
		fmt.Fprintf(&b, " I4=\"%s\"", cfg.I4)
	}
	if cfg.I5 != "" {
		fmt.Fprintf(&b, " I5=\"%s\"", cfg.I5)
	}

	if cfg.BindIface != "" {
		fmt.Fprintf(&b, " BIND=%s", cfg.BindIface)
	}

	return b.String()
}

// pubKeyToHex converts a base64-encoded public key to hex.
func pubKeyToHex(base64Key string) string {
	b, err := base64.StdEncoding.DecodeString(base64Key)
	if err != nil || len(b) != 32 {
		return ""
	}
	return hex.EncodeToString(b)
}
