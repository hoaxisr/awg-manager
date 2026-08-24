package nwg

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/sys/kmod"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
	"github.com/hoaxisr/awg-manager/internal/sys/semver"
)

const (
	awgProxyDir         = "/opt/etc/awg-manager/modules"
	defaultKoPath       = awgProxyDir + "/awg_proxy.ko"
	expectedKmodVersion = "1.3.0" // minimum required awg_proxy.ko version (dual-family: IPv6 endpoint support)
	// kmodVersionIPv6 is the first awg_proxy.ko version whose procfs
	// parser understands "[v6]:port" endpoints. Older parsers run the
	// legacy strrchr(':')+in_aton path on that string and silently
	// create a bogus IPv4 slot — addFreshLocked fails loudly instead.
	kmodVersionIPv6 = "1.3.0"
	// kmodMaxSlots — AWG_MAX_TUNNELS в kmod/awg-proxy/src/proxy.h: столько
	// одновременных прокси-слотов (туннелей с обфускацией) держит
	// awg_proxy.ko. При добавлении сверх лимита ядро отвечает -ENOSPC —
	// addFreshLocked переводит это в понятное сообщение.
	kmodMaxSlots = 16
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

	// execFn isolates external commands (insmod/rmmod/modprobe) so unit
	// tests can run EnsureLoaded against a fake runner. Default: exec.Run.
	execFn func(ctx context.Context, name string, args ...string) (*exec.Result, error)
	// isLoadedFn reports whether awg_proxy is currently loaded
	// (/proc/awg_proxy/version exists). Stubbed in tests.
	isLoadedFn func() bool
	// modLoadedFn reports whether an arbitrary kernel module is loaded
	// (/proc/modules scan). Stubbed in tests.
	modLoadedFn func(name string) bool
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
	// AWG 3.1 (awg_proxy >= header-protection build).
	HeaderProtectionKeyHex string // 64-char hex; empty = no header protection
	RandomTrailers         bool   // append/accept random handshake trailers
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
		execFn:      exec.Run,
		isLoadedFn: func() bool {
			_, err := os.Stat("/proc/awg_proxy/version")
			return err == nil
		},
		modLoadedFn: isModuleLoadedProc,
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
// If the loaded module version is below expected, it is upgraded (rmmod +
// insmod of the on-disk .ko) only when the kernel proxy has no live slots;
// with active slots the upgrade is deferred — rmmod would destroy ALL
// running tunnels' proxies.
func (km *KmodManager) EnsureLoaded() error {
	km.mu.Lock()
	defer km.mu.Unlock()

	ctx := context.Background()

	if km.koPath == "" {
		km.koPath = km.resolveKoPath()
	}

	if km.isLoadedLocked() {
		// Check version — upgrade if loaded version is below expected.
		loaded := km.readVersionLocked()
		if loaded != "" && semver.Compare(loaded, expectedKmodVersion) < 0 {
			// Don't reload if there are active proxy entries —
			// rmmod would destroy ALL running tunnels' proxies.
			if activeSlots := km.loadedSlotCountLocked(); activeSlots > 0 {
				km.appLog.Warn("reload", "", fmt.Sprintf("outdated (loaded=%s, want>=%s), %d active slots — upgrade deferred until module is idle", loaded, expectedKmodVersion, activeSlots))
				return nil
			}
			km.appLog.Info("reload", "", fmt.Sprintf("upgrading awg_proxy: loaded=%s, want>=%s, no active slots — rmmod + insmod %s", loaded, expectedKmodVersion, km.koPath))
			_, _ = km.execFn(ctx, "rmmod", "awg_proxy")
			// Fall through to insmod below.
		} else {
			// Do not purge unknown slots here. After daemon restart km.tunnels
			// is empty while /proc/awg_proxy/list may still contain live slots
			// used by NDMS. RestoreTunnel adopts the matching slot on the
			// reconnect path; AddTunnel always installs fresh.
			return nil
		}
	}

	// awg_proxy.ko >= 1.3.0 gained depends=udp_tunnel,udp_tunnel6 (the v6
	// TX path references udp_tunnel6_xmit_skb whenever the target kernel
	// has CONFIG_IPV6=y/m) and bare insmod resolves no dependencies —
	// preflight-load them best-effort so v4-only setups don't lose the
	// module after upgrade.
	km.preloadDepsLocked(ctx)

	result, err := km.execFn(ctx, "insmod", km.koPath)
	var stderr string
	if result != nil {
		stderr = result.Stderr
	}
	if err == nil && result != nil && result.ExitCode == 0 {
		km.appLog.Info("load", "", "awg_proxy.ko loaded (expected>="+expectedKmodVersion+")")
		return nil
	}

	var insmodErr error
	if err != nil {
		insmodErr = fmt.Errorf("insmod %s: %w: %s", km.koPath, err, strings.TrimSpace(stderr))
	} else {
		insmodErr = fmt.Errorf("insmod %s: exit %d: %s", km.koPath, result.ExitCode, strings.TrimSpace(stderr))
	}
	// «Unknown symbol» here almost always means the kernel lacks the
	// udp_tunnel6 symbols (Keenetic with the IPv6 system component
	// disabled) — name the fix instead of the raw symbol soup.
	if strings.Contains(strings.ToLower(stderr), "unknown symbol") {
		insmodErr = fmt.Errorf("%w — ядро без IPv6-компонента: udp_tunnel6.ko недоступен — установите/включите компонент IPv6 либо используйте сборку awg_proxy без IPv6", insmodErr)
	}
	return insmodErr
}

// awgProxyDeps lists the modules awg_proxy.ko depends on, in load order.
//
//   - udp_tunnel: dependency since v1.2.0 (udp_tunnel_xmit_skb / rx encap).
//     On real Keenetic targets it has effectively always been loaded before
//     us — NDMS pulls it in for its own tunnels at boot — which is why the
//     historical bare insmod got away without preloading it. Preflighted
//     anyway: it costs one /proc/modules check and covers firmwares where
//     nothing else loaded it yet.
//   - udp_tunnel6: new dependency in v1.3.0 (udp_tunnel6_xmit_skb). On
//     Keenetic it is modular and only present when the IPv6 system
//     component is installed.
var awgProxyDeps = []string{"udp_tunnel", "udp_tunnel6"}

// preloadDepsLocked best-effort loads awg_proxy.ko's module dependencies
// before insmod. Mirrors ensureKernelModule in
// internal/singbox/router/iptables.go (the xt_* preload used by QoS/TPROXY):
// skip if already in /proc/modules, try modprobe (resolves paths and deps
// itself), else insmod from the flat /lib/modules/$(uname -r) layout.
// Soft-fail by design: already-loaded and built-in (no .ko on disk) are
// success, and a genuinely unloadable dep is only logged — the awg_proxy
// insmod that follows surfaces the real verdict («Unknown symbol»), where
// EnsureLoaded appends a user-facing hint about the missing IPv6 component.
func (km *KmodManager) preloadDepsLocked(ctx context.Context) {
	for _, name := range awgProxyDeps {
		if km.modLoadedFn(name) {
			continue
		}
		if res, err := km.execFn(ctx, "modprobe", name); err == nil && res != nil && res.ExitCode == 0 {
			continue
		}
		// modprobe unavailable or failed — insmod from the standard
		// module dir (flat on Keenetic, same path the xt_* loader uses).
		kernel := osdetect.KernelRelease()
		if kernel == "" {
			continue
		}
		path := filepath.Join("/lib/modules", kernel, name+".ko")
		if _, err := os.Stat(path); err != nil {
			continue // built-in or absent — awg_proxy insmod decides
		}
		if res, err := km.execFn(ctx, "insmod", path); err != nil || res == nil || res.ExitCode != 0 {
			km.appLog.Warn("preload-dep", name, fmt.Sprintf("best-effort insmod %s failed (continuing): %v", path, err))
		}
	}
}

// isModuleLoadedProc checks /proc/modules for the given module name.
// Same helper as internal/singbox/router — duplicated to avoid importing
// the sing-box router package from the tunnel layer.
func isModuleLoadedProc(name string) bool {
	data, err := os.ReadFile("/proc/modules")
	if err != nil {
		return false
	}
	prefix := name + " "
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
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
		km.appLog.Info("adopt-tunnel", tunnelID, fmt.Sprintf("%s -> 127.0.0.1:%d", endpointKey(cfg.EndpointIP, cfg.EndpointPort), listenPort))
		return KmodResult{ListenPort: listenPort, Adopted: true}, nil
	}

	km.appLog.Info("restore-tunnel", tunnelID, fmt.Sprintf("no live slot for %s, adding fresh", endpointKey(cfg.EndpointIP, cfg.EndpointPort)))
	return km.addFreshLocked(tunnelID, cfg)
}

// addFreshLocked writes a new slot to /proc/awg_proxy/add. Must be called
// with km.mu held. On -EEXIST (duplicate endpoint per awg_proxy_add in
// proxy.c) the stale slot is del'd and add is retried — without this
// retry an orphan slot from a prior Delete would block a fresh install.
func (km *KmodManager) addFreshLocked(tunnelID string, cfg KmodConfig) (KmodResult, error) {
	// IPv6 endpoints need kmod >= kmodVersionIPv6 (see the const) —
	// gate before writing so the failure names the fix instead of a
	// silently mis-parsed slot.
	if strings.Contains(cfg.EndpointIP, ":") {
		loaded := km.readVersionLocked()
		if loaded == "" || semver.Compare(loaded, kmodVersionIPv6) < 0 {
			if loaded == "" {
				loaded = "unknown"
			}
			return KmodResult{}, fmt.Errorf("kmod add tunnel %s: IPv6 endpoint %s requires awg_proxy.ko >= %s (loaded: %s)",
				tunnelID, cfg.EndpointIP, kmodVersionIPv6, loaded)
		}
	}

	delLine := endpointKey(cfg.EndpointIP, cfg.EndpointPort)
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
		// -ENOSPC из /proc/awg_proxy/add: свободных слотов в ядре нет
		// (см. "Find free slot" в kmod/awg-proxy/src/proxy.c).
		if errors.Is(err, syscall.ENOSPC) {
			return KmodResult{}, fmt.Errorf("kmod add tunnel %s: %w — достигнут предел awg_proxy: %d туннелей с обфускацией одновременно", tunnelID, err, kmodMaxSlots)
		}
		return KmodResult{}, fmt.Errorf("kmod add tunnel %s: %w", tunnelID, err)
	}

	listenPort, err := km.readListenPortLocked(cfg.EndpointIP, cfg.EndpointPort)
	if err != nil {
		if raw, rerr := km.procReadFn("/proc/awg_proxy/list"); rerr == nil {
			km.appLog.Warn("read-listen-port", "", "/proc/awg_proxy/list contents:\n"+string(raw))
		}
		km.appLog.Warn("add-tunnel", tunnelID, fmt.Sprintf("failed to read listen port (endpoint=%s): %s", endpointKey(cfg.EndpointIP, cfg.EndpointPort), err.Error()))
		return KmodResult{}, fmt.Errorf("kmod read listen port for %s: %w", tunnelID, err)
	}

	km.tunnels[tunnelID] = kmodEntry{
		endpointIP:   cfg.EndpointIP,
		endpointPort: cfg.EndpointPort,
		listenPort:   listenPort,
	}
	km.appLog.Info("add-tunnel", tunnelID, fmt.Sprintf("%s -> 127.0.0.1:%d", endpointKey(cfg.EndpointIP, cfg.EndpointPort), listenPort))
	return KmodResult{ListenPort: listenPort}, nil
}

// endpointKey renders an endpoint exactly the way awg_proxy.ko prints and
// parses it: "1.2.3.4:51820" for IPv4, "[2001:db8::1]:51820" for IPv6
// (bracketed form, accepted by /proc add/del and printed in list rows
// since kmod v1.3.0; the kernel's %pI6c and Go's net.IP.String() both
// emit RFC 5952 compressed lowercase, so prefixes match byte-for-byte).
func endpointKey(ip string, port int) string {
	return net.JoinHostPort(ip, strconv.Itoa(port))
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

	// Each line: "ENDPOINT listen=127.0.0.1:LPORT rx=... tx=..." where
	// ENDPOINT is "IP:PORT" (v4) or "[IPV6]:PORT" (v6, kmod >= 1.3.0).
	//
	// Fast path: exact string prefix — for ordinary addresses the kernel's
	// %pI6c and Go's net.IP.String() both emit RFC 5952 compressed
	// lowercase, so the prefix matches byte-for-byte. Slow path: parsed
	// comparison — %pI6c renders an embedded-IPv4 tail (ISATAP
	// "::5efe:192.0.2.1", v4-compat, …) in dotted-quad form while Go prints
	// hex groups ("::5efe:c000:201"); without the parse the freshly added
	// slot is "not found", the caller errors, and the live slot stays
	// orphaned in the kernel.
	target := endpointKey(endpointIP, endpointPort) + " "
	expIP := net.ParseIP(endpointIP)
	for line := range strings.SplitSeq(string(data), "\n") {
		if !strings.HasPrefix(line, target) && !rowEndpointEquals(line, expIP, endpointPort) {
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

// rowEndpointEquals reports whether the first field of a /proc list row
// ("IP:PORT ..." or "[V6]:PORT ...") denotes the same address and port as
// expIP/expPort, comparing parsed addresses instead of strings.
func rowEndpointEquals(line string, expIP net.IP, expPort int) bool {
	if expIP == nil {
		return false
	}
	ep, _, _ := strings.Cut(line, " ")
	host, portStr, err := net.SplitHostPort(ep)
	if err != nil {
		return false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port != expPort {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.Equal(expIP)
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

	line := endpointKey(entry.endpointIP, entry.endpointPort)
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

// SupportsIPv6 сообщает, понимает ли загруженный awg_proxy.ko эндпоинты
// вида "[v6]:port" (см. kmodVersionIPv6). Тот же предикат стоит гейтом
// внутри addFreshLocked, но там он срабатывает уже ПОСЛЕ того, как
// вызывающий снял прежний слот; отдельный публичный вид нужен, чтобы
// проверить осуществимость пересборки до сноса живого слота.
func (km *KmodManager) SupportsIPv6() bool {
	km.mu.Lock()
	defer km.mu.Unlock()
	loaded := km.readVersionLocked()
	return loaded != "" && semver.Compare(loaded, kmodVersionIPv6) >= 0
}

// IsLoaded checks if /proc/awg_proxy/version exists.
func (km *KmodManager) IsLoaded() bool {
	km.mu.Lock()
	defer km.mu.Unlock()
	return km.isLoadedLocked()
}

func (km *KmodManager) isLoadedLocked() bool {
	return km.isLoadedFn()
}

// HasTunnel checks if a tunnel is tracked.
func (km *KmodManager) HasTunnel(tunnelID string) bool {
	km.mu.Lock()
	defer km.mu.Unlock()
	_, ok := km.tunnels[tunnelID]
	return ok
}

// buildProcLine builds the config line for /proc/awg_proxy/add.
// Format: ENDPOINT H1=min-max H2=... S1=N ... Jc=N ... PUB_SERVER=hex PUB_CLIENT=hex I1="template"
// ENDPOINT is "IP:PORT" for IPv4 (unchanged) or "[IPV6]:PORT" (kmod >= 1.3.0).
func buildProcLine(cfg KmodConfig) string {
	var b strings.Builder
	b.WriteString(endpointKey(cfg.EndpointIP, cfg.EndpointPort))
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

	// AWG 3.1: header protection (HP_KEY) and random trailers (RT). Emitted
	// only when set, so an AWG 1.x/2.0 line stays byte-identical.
	if cfg.HeaderProtectionKeyHex != "" {
		fmt.Fprintf(&b, " HP_KEY=%s", cfg.HeaderProtectionKeyHex)
	}
	if cfg.RandomTrailers {
		b.WriteString(" RT=1")
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
