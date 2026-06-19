package nwg

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/sys/kmod"
	"github.com/hoaxisr/awg-manager/internal/sys/semver"
)

const (
	awgProxyDir         = "/opt/etc/awg-manager/modules"
	defaultKoPath       = awgProxyDir + "/awg_proxy.ko"
	expectedKmodVersion = "1.1.12" // minimum required awg_proxy.ko version (I1-I5 wire parity)
)

// KmodManager manages the awg_proxy.ko kernel module for NativeWG tunnels.
type KmodManager struct {
	mu      sync.Mutex
	tunnels map[string]kmodEntry // tunnelID → endpoint for del
	koPath  string
	appLog  *logging.ScopedLogger

	// procWriteFn / procReadFn isolate /proc/awg_proxy/* I/O so unit
	// tests can stub them without touching a real procfs. Default:
	// os.WriteFile / kmod.ReadProc (NOT os.ReadFile — its 512-byte
	// first chunk truncates the list on kmod < 1.1.11, issue #362).
	procWriteFn func(path string, data []byte) error
	procReadFn  func(path string) ([]byte, error)
}

// kmodEntry tracks a loaded tunnel's endpoint and proxy listen port.
type kmodEntry struct {
	endpointIP   string
	endpointPort int
	listenPort   int // proxy listen port on 127.0.0.1
}

// KmodConfig holds AWG obfuscation parameters for the kernel module.
type KmodConfig struct {
	EndpointIP         string
	EndpointPort       int
	H1, H2, H3, H4     string // "min-max" or single value
	S1, S2, S3, S4     int
	Jc, Jmin, Jmax     int
	PubServerHex       string // 64-char hex
	PubClientHex       string // 64-char hex
	I1, I2, I3, I4, I5 string // CPS template strings
	BindIface          string // kernel iface for SO_BINDTODEVICE (e.g. "eth3")
}

// KmodResult holds the result of adding a tunnel to the kernel module.
type KmodResult struct {
	ListenPort int  // proxy listen port on 127.0.0.1
	Adopted    bool // true when an existing live slot was reused
}

// NewKmodManager creates a new KmodManager.
func NewKmodManager(appLogger logging.AppLogger) *KmodManager {
	return &KmodManager{
		tunnels:     make(map[string]kmodEntry),
		appLog:      logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubKmod),
		procWriteFn: func(path string, data []byte) error { return os.WriteFile(path, data, 0) },
		procReadFn:  kmod.ReadProc,
	}
}

// resolveKoPath returns the path to awg_proxy.ko.
// Priority: per-model (KN-1011 HIGHMEM is currently the only model that
// genuinely needs its own build) → SoC default → arch default.
//
// The per-device tier (Xiaomi-R3P) is gone — the v1.1.2 IPK no longer
// ships a Xiaomi-specific .ko; non-Keenetic devices fall through to the
// SoC/arch default just like every other configuration. SHA256 audit of
// the v1.1.1 set showed all other per-model files (KN-1010, KN-1410,
// KN-2010, KN-3811) were bit-exact duplicates of their SoC defaults, so
// they are no longer shipped either.
func (km *KmodManager) resolveKoPath() string {
	// 1. Per-model override (currently only KN-1011 HIGHMEM is unique)
	model := kmod.DetectModel()
	if model != "" {
		modelPath := fmt.Sprintf(awgProxyDir+"/awg_proxy-%s.ko", model)
		if _, err := os.Stat(modelPath); err == nil {
			km.appLog.Info("select-binary", model, "using model-specific awg_proxy")
			return modelPath
		}
	}

	// 2. SoC-specific (e.g. awg_proxy-mt7628.ko for non-SMP mipsel)
	soc := kmod.DetectSoC()
	if soc != kmod.SoCUnknown {
		socPath := fmt.Sprintf(awgProxyDir+"/awg_proxy-%s.ko", string(soc))
		if _, err := os.Stat(socPath); err == nil {
			km.appLog.Info("select-binary", string(soc), "using SoC-specific awg_proxy")
			return socPath
		}
	}

	// 3. Arch default (fallback)
	return defaultKoPath
}

// EnsureLoaded loads awg_proxy.ko if not already loaded.
// If the module is loaded but has a different version, it is reloaded only
// when the kernel proxy has no live slots.
func (km *KmodManager) EnsureLoaded() error {
	km.mu.Lock()
	defer km.mu.Unlock()

	if km.koPath == "" {
		km.koPath = km.resolveKoPath()
	}

	if km.isLoadedLocked() {
		// Check version — reload if loaded version is below expected.
		loaded := km.readVersionLocked()
		if loaded != "" && semver.Compare(loaded, expectedKmodVersion) < 0 {
			// Don't reload if there are active proxy entries —
			// rmmod would destroy ALL running tunnels' proxies.
			if activeSlots := km.loadedSlotCountLocked(); activeSlots > 0 {
				km.appLog.Warn("reload", "", fmt.Sprintf("outdated (loaded=%s, want>=%s), %d active slots — skipping reload", loaded, expectedKmodVersion, activeSlots))
				return nil
			}
			km.appLog.Info("reload", "", fmt.Sprintf("outdated (loaded=%s, want>=%s), reloading", loaded, expectedKmodVersion))
			_, _ = exec.Run(context.Background(), "rmmod", "awg_proxy")
			// Fall through to insmod below.
		} else {
			// Do not purge unknown slots here. After daemon restart km.tunnels
			// is empty while /proc/awg_proxy/list may still contain live slots
			// used by NDMS. RestoreTunnel adopts the matching slot on the
			// reconnect path; AddTunnel always installs fresh.
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

	km.appLog.Info("load", "", "awg_proxy.ko loaded (expected>="+expectedKmodVersion+")")
	return nil
}

func (km *KmodManager) loadedSlotCountLocked() int {
	data, err := km.procReadFn("/proc/awg_proxy/list")
	if err != nil {
		return 0
	}
	return countProxySlotsList(string(data))
}

// readVersionLocked reads the loaded module version from /proc/awg_proxy/version.
func (km *KmodManager) readVersionLocked() string {
	data, err := km.procReadFn("/proc/awg_proxy/version")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// AddTunnel installs a fresh kmod proxy slot for tunnelID with cfg.
// Always writes a new slot — NEVER adopts an existing one. If kmod reports
// the endpoint already exists (-EEXIST), the stale slot is del'd and the
// add retried so the freshly-supplied keys/obfuscation are what end up
// running. Use this on user-initiated Start (startProxy), Update with
// runtime restart, and any path where the caller produced cfg from
// current storage.
//
// Issue #234 / Critical-1 (silent key/obfuscation mismatch on
// Delete→Create-same-endpoint): the pre-fix code adopted ANY slot whose
// (IP, port) matched. A new tunnel with the same endpoint but different
// keys silently took over an orphan slot configured with the OLD keys,
// and the handshake failed forever with no log line. Adopt is now a
// separate code path — RestoreTunnel — used only at daemon-restart
// reconnect, where the caller has independent reason to trust the slot's
// existing config.
func (km *KmodManager) AddTunnel(tunnelID string, cfg KmodConfig) (KmodResult, error) {
	km.mu.Lock()
	defer km.mu.Unlock()
	return km.addFreshLocked(tunnelID, cfg)
}

// RestoreTunnel reattaches an existing kmod proxy slot to tunnelID after
// the daemon restarted (syscall.Exec) — the userspace tracking map is
// empty but a slot for (IP, port) lives on in the kernel. If found, the
// slot is adopted as-is (no /proc/del, no re-add). If no matching slot
// exists, falls back to a fresh add — the caller-supplied cfg becomes
// the running config.
//
// CALLER GUARANTEE: cfg must match the configuration the slot was
// originally created with (i.e. the persisted storage record is the same
// one used at the last Start). Reconnect path satisfies this; user-
// initiated Start paths do NOT and MUST use AddTunnel instead.
func (km *KmodManager) RestoreTunnel(tunnelID string, cfg KmodConfig) (KmodResult, error) {
	km.mu.Lock()
	defer km.mu.Unlock()

	// Idempotency guard: if the caller already tracked this tunnel —
	// this is a redundant restore (e.g. flapping reconnect) — short-
	// circuit instead of falling through to addFreshLocked, which would
	// del the slot we already adopted, change the listen port, and
	// leave NDMS peer endpoint pointing at the old port.
	if entry, tracked := km.tunnels[tunnelID]; tracked {
		km.appLog.Info("restore-tunnel", tunnelID, fmt.Sprintf("already tracked → 127.0.0.1:%d (no-op)", entry.listenPort))
		return KmodResult{ListenPort: entry.listenPort, Adopted: true}, nil
	}

	if listenPort, err := km.readListenPortLocked(cfg.EndpointIP, cfg.EndpointPort); err == nil {
		km.tunnels[tunnelID] = kmodEntry{
			endpointIP:   cfg.EndpointIP,
			endpointPort: cfg.EndpointPort,
			listenPort:   listenPort,
		}
		km.appLog.Info("adopt-tunnel", tunnelID, fmt.Sprintf("%s:%d -> 127.0.0.1:%d", cfg.EndpointIP, cfg.EndpointPort, listenPort))
		return KmodResult{ListenPort: listenPort, Adopted: true}, nil
	}

	km.appLog.Info("restore-tunnel", tunnelID, fmt.Sprintf("no live slot for %s:%d, adding fresh", cfg.EndpointIP, cfg.EndpointPort))
	return km.addFreshLocked(tunnelID, cfg)
}

// addFreshLocked writes a new slot to /proc/awg_proxy/add. Must be called
// with km.mu held. On -EEXIST (duplicate endpoint per awg_proxy_add in
// proxy.c) the stale slot is del'd and add is retried — without this
// retry an orphan slot from a prior Delete would block a fresh install.
func (km *KmodManager) addFreshLocked(tunnelID string, cfg KmodConfig) (KmodResult, error) {
	delLine := fmt.Sprintf("%s:%d", cfg.EndpointIP, cfg.EndpointPort)
	line := buildProcLine(cfg)

	err := km.procWriteFn("/proc/awg_proxy/add", []byte(line))
	if err != nil && errors.Is(err, syscall.EEXIST) {
		km.appLog.Info("add-tunnel", tunnelID, fmt.Sprintf("%s exists, replacing", delLine))
		if delErr := km.procWriteFn("/proc/awg_proxy/del", []byte(delLine)); delErr != nil {
			km.appLog.Warn("add-tunnel", tunnelID, fmt.Sprintf("EEXIST fallback del failed (retry add will likely also fail): %s", delErr.Error()))
		}
		err = km.procWriteFn("/proc/awg_proxy/add", []byte(line))
	}
	if err != nil {
		return KmodResult{}, fmt.Errorf("kmod add tunnel %s: %w", tunnelID, err)
	}

	listenPort, err := km.readListenPortLocked(cfg.EndpointIP, cfg.EndpointPort)
	if err != nil {
		if raw, rerr := km.procReadFn("/proc/awg_proxy/list"); rerr == nil {
			km.appLog.Warn("read-listen-port", "", "/proc/awg_proxy/list contents:\n"+string(raw))
		}
		km.appLog.Warn("add-tunnel", tunnelID, fmt.Sprintf("failed to read listen port (endpoint=%s:%d): %s", cfg.EndpointIP, cfg.EndpointPort, err.Error()))
		return KmodResult{}, fmt.Errorf("kmod read listen port for %s: %w", tunnelID, err)
	}

	km.tunnels[tunnelID] = kmodEntry{
		endpointIP:   cfg.EndpointIP,
		endpointPort: cfg.EndpointPort,
		listenPort:   listenPort,
	}
	km.appLog.Info("add-tunnel", tunnelID, fmt.Sprintf("%s:%d -> 127.0.0.1:%d", cfg.EndpointIP, cfg.EndpointPort, listenPort))
	return KmodResult{ListenPort: listenPort}, nil
}

// listenPortRe matches "listen=127.0.0.1:PORT" in the proxy list output.
var listenPortRe = regexp.MustCompile(`listen=127\.0\.0\.1:(\d+)`)

func countProxySlotsList(data string) int {
	count := 0
	for line := range strings.SplitSeq(strings.TrimSpace(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "(") {
			continue
		}
		if strings.Contains(line, "listen=127.0.0.1:") {
			count++
		}
	}
	return count
}

// hasSlotListeningInList reports whether the /proc/awg_proxy/list contents contain
// a slot listening on 127.0.0.1:listenPort.
func hasSlotListeningInList(data string, listenPort int) bool {
	want := fmt.Sprintf("listen=127.0.0.1:%d", listenPort)
	for line := range strings.SplitSeq(data, "\n") {
		if slices.Contains(strings.Fields(line), want) {
			return true
		}
	}
	return false
}

// readListenPortLocked reads /proc/awg_proxy/list and finds the listen port
// for the given endpoint. Must be called with km.mu held.
func (km *KmodManager) readListenPortLocked(endpointIP string, endpointPort int) (int, error) {
	data, err := km.procReadFn("/proc/awg_proxy/list")
	if err != nil {
		return 0, fmt.Errorf("read /proc/awg_proxy/list: %w", err)
	}

	// Each line: "IP:PORT listen=127.0.0.1:LPORT rx=... tx=..."
	target := fmt.Sprintf("%s:%d ", endpointIP, endpointPort)
	for line := range strings.SplitSeq(string(data), "\n") {
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
	if err := km.procWriteFn("/proc/awg_proxy/del", []byte(line)); err != nil {
		return fmt.Errorf("kmod del tunnel %s: %w", tunnelID, err)
	}

	delete(km.tunnels, tunnelID)
	km.appLog.Info("remove-tunnel", tunnelID, "removed")
	return nil
}

// HasSlotListening reports whether a live kmod proxy slot is listening on
// 127.0.0.1:listenPort (read from /proc/awg_proxy/list).
func (km *KmodManager) HasSlotListening(listenPort int) bool {
	data, err := km.procReadFn("/proc/awg_proxy/list")
	if err != nil {
		return false
	}
	return hasSlotListeningInList(string(data), listenPort)
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
