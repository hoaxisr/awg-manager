package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/hydraroute"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/kmod"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
	"github.com/hoaxisr/awg-manager/internal/sys/routerclock"
	"github.com/hoaxisr/awg-manager/internal/sys/routerinfo"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
)

// ── Response DTOs ────────────────────────────────────────────────

// SystemInfoBackendAvailability shows which tunnel backends are available.
type SystemInfoBackendAvailability struct {
	Nativewg bool `json:"nativewg" example:"true"`
	Kernel   bool `json:"kernel" example:"false"`
}

// SystemInfoSingbox shows sing-box component info embedded in system info.
type SystemInfoSingbox struct {
	Installed bool   `json:"installed" example:"true"`
	Version   string `json:"version" example:"1.9.3"`
}

// SystemInfoData is the payload returned by GET /system/info.
type SystemInfoData struct {
	Version                     string                        `json:"version" example:"2.5.0"`
	GoVersion                   string                        `json:"goVersion" example:"go1.23.0"`
	GoArch                      string                        `json:"goArch" example:"arm64"`
	GoOS                        string                        `json:"goOS" example:"linux"`
	KeeneticOS                  string                        `json:"keeneticOS" example:"ndms"`
	IsOS5                       bool                          `json:"isOS5" example:"true"`
	FirmwareVersion             string                        `json:"firmwareVersion" example:"4.2.1"`
	SupportsExtendedASC         bool                          `json:"supportsExtendedASC" example:"true"`
	SupportsHRanges             bool                          `json:"supportsHRanges" example:"true"`
	SupportsPingCheck           bool                          `json:"supportsPingCheck" example:"true"`
	TotalMemoryMB               int                           `json:"totalMemoryMB" example:"512"`
	IsLowMemory                 bool                          `json:"isLowMemory" example:"false"`
	GcMemLimit                  string                        `json:"gcMemLimit" example:"128MiB"`
	Gogc                        string                        `json:"gogc" example:"25"`
	DisableMemorySaving         bool                          `json:"disableMemorySaving" example:"false"`
	KernelModuleExists          bool                          `json:"kernelModuleExists" example:"true"`
	KernelModuleLoaded          bool                          `json:"kernelModuleLoaded" example:"false"`
	KernelModuleModel           string                        `json:"kernelModuleModel" example:"MT7981"`
	KernelModuleVersion         string                        `json:"kernelModuleVersion" example:""`
	IsAarch64                   bool                          `json:"isAarch64" example:"true"`
	ActiveBackend               string                        `json:"activeBackend" example:"nativewg"`
	RouterIP                    string                        `json:"routerIP" example:"192.168.1.1"`
	RouterTime                  string                        `json:"routerTime" example:"2026-05-20T14:32:10+03:00"`
	RouterTimezone              string                        `json:"routerTimezone" example:"MSK"`
	RouterTimezoneOffsetMinutes int                           `json:"routerTimezoneOffsetMinutes" example:"180"`
	BootInProgress              bool                          `json:"bootInProgress" example:"false"`
	SlowRequestThresholdMs      int                           `json:"slowRequestThresholdMs" example:"0"`
	BackendAvailability         SystemInfoBackendAvailability `json:"backendAvailability"`
	Singbox                     SystemInfoSingbox             `json:"singbox"`
	RouterDetails               *RouterDetails                `json:"routerDetails,omitempty"`
}

// RouterDetails contains extended router metadata derived from NDMS/RCI and local procfs.
type RouterDetails = routerinfo.RouterDetails

// SystemInfoResponse is the envelope for GET /system/info.
type SystemInfoResponse struct {
	Success bool           `json:"success" example:"true"`
	Data    SystemInfoData `json:"data"`
}

// HydraRouteStatusData mirrors frontend HydraRouteStatus.
type HydraRouteStatusData struct {
	Installed    bool   `json:"installed" example:"true"`
	Running      bool   `json:"running" example:"true"`
	Version      string `json:"version,omitempty" example:"2.4.1"`
	PID          int    `json:"pid,omitempty" example:"12345"`
	StalePID     int    `json:"stalePid,omitempty" example:"12345"`
	ProcessState string `json:"processState" example:"running" enums:"not_installed,stopped,running,dead"`
	LastError    string `json:"lastError,omitempty" example:"neo restart: exit status 1"`
}

// HydraRouteStatusResponse is the envelope for GET /system/hydraroute-status.
type HydraRouteStatusResponse struct {
	Success bool                 `json:"success" example:"true"`
	Data    HydraRouteStatusData `json:"data"`
}

func hydraRouteStatusData(s hydraroute.Status) HydraRouteStatusData {
	state := s.ProcessState
	if state == "" {
		if !s.Installed {
			state = hydraroute.StateNotInstalled
		} else if s.Running {
			state = hydraroute.StateRunning
		} else {
			state = hydraroute.StateStopped
		}
	}
	return HydraRouteStatusData{
		Installed:    s.Installed,
		Running:      s.Running,
		Version:      s.Version,
		PID:          s.PID,
		StalePID:     s.StalePID,
		ProcessState: string(state),
		LastError:    s.LastError,
	}
}

// WANInterfaceDTO mirrors frontend WANInterface.
type WANInterfaceDTO struct {
	Name  string `json:"name" example:"ISP1"`
	Label string `json:"label" example:"Home Internet"`
	State string `json:"state" example:"up"`
}

// WANInterfacesResponse is the envelope for GET /system/wan-interfaces.
type WANInterfacesResponse struct {
	Success bool              `json:"success" example:"true"`
	Data    []WANInterfaceDTO `json:"data"`
}

// RouterInterfaceDTO mirrors frontend RouterInterface.
type RouterInterfaceDTO struct {
	Name  string `json:"name" example:"br0"`
	Label string `json:"label" example:"Home Network"`
	Up    bool   `json:"up" example:"true"`
}

// AllInterfacesResponse is the envelope for GET /system/all-interfaces.
type AllInterfacesResponse struct {
	Success bool                 `json:"success" example:"true"`
	Data    []RouterInterfaceDTO `json:"data"`
}

// WANInterfaceStatusDTO is a single WAN interface status.
type WANInterfaceStatusDTO struct {
	Up    bool   `json:"up" example:"true"`
	Label string `json:"label" example:"Home Internet"`
}

// WANInterfaceStatusDTO is a single WAN interface status entry.
type WANInterfaceStatusItemDTO struct {
	Up    bool   `json:"up" example:"true"`
	Label string `json:"label" example:"Home Internet"`
}

// SettingsProvider provides access to settings.
type SettingsProvider interface {
	Get() (*storage.Settings, error)
}

// KmodLoader provides kernel module status.
type KmodLoader interface {
	ModuleExists() bool
	IsLoaded() bool
	Model() string
	SoC() kmod.SoC
	OnDiskVersion() string
}

// SystemHandler handles system information endpoints.
type SystemHandler struct {
	version                string
	settingsStore          SettingsProvider
	settingsWriter         *storage.SettingsStore
	activeBackend          backend.Backend
	kmodLoader             KmodLoader
	tunnelService          TunnelService
	pingCheckService       PingCheckService
	ndmsQueries            *ndmsquery.Queries
	restartFn              func()
	bootStatusFn           func() bool // returns true if boot is still in progress
	slowRequestThresholdMs int         // 0 = slow HTTP profiling disabled
	hydra                  *hydraroute.Service
	singboxOp              *singbox.Operator
	bus                    *events.Bus

	singboxInfoMu                sync.RWMutex
	singboxVersionCached         string
	singboxVersionFetchedAt      time.Time
	singboxVersionRefreshRunning bool
	singboxBinaryFingerprint     string

	routerDetailsMu       sync.RWMutex
	routerDetailsCache    *RouterDetails
	routerDetailsCachedAt time.Time
}

const singboxVersionCacheTTL = 45 * time.Second

// routerDetailsCacheTTL caps how often the router makes RCI calls when the
// settings page is refreshed rapidly. Static fields (model, firmware) never
// change; dynamic fields (temps, memory) are also polled every 30 s by the
// frontend, so 15 s staleness is imperceptible.
const routerDetailsCacheTTL = 15 * time.Second

// SetEventBus wires the SSE bus so HR Neo control actions emit
// `routing.hydrarouteStatus` resource:invalidated hints.
func (h *SystemHandler) SetEventBus(bus *events.Bus) { h.bus = bus }

// NewSystemHandler creates a new system handler.
func NewSystemHandler(version string) *SystemHandler {
	return &SystemHandler{version: version}
}

// SetSettingsStore sets the settings provider.
func (h *SystemHandler) SetSettingsStore(sp SettingsProvider) {
	h.settingsStore = sp
}

// SetActiveBackend sets the active backend for status reporting.
func (h *SystemHandler) SetActiveBackend(b backend.Backend) {
	h.activeBackend = b
}

// SetKmodLoader sets the kernel module loader for status reporting.
func (h *SystemHandler) SetKmodLoader(l KmodLoader) {
	h.kmodLoader = l
}

// SetTunnelService sets the tunnel service for stopping tunnels on backend change.
func (h *SystemHandler) SetTunnelService(svc TunnelService) {
	h.tunnelService = svc
}

// SetSettingsWriter sets the writable settings store for saving.
func (h *SystemHandler) SetSettingsWriter(sw *storage.SettingsStore) {
	h.settingsWriter = sw
}

// SetPingCheckService sets the ping check service for stopping monitoring on restart.
func (h *SystemHandler) SetPingCheckService(svc PingCheckService) {
	h.pingCheckService = svc
}

// SetNDMSQueries sets the NDMS query registry for the new CQRS layer.
func (h *SystemHandler) SetNDMSQueries(q *ndmsquery.Queries) {
	h.ndmsQueries = q
}

// SetRestartFunc sets the callback to trigger daemon self-restart.
func (h *SystemHandler) SetRestartFunc(fn func()) {
	h.restartFn = fn
}

// SetBootStatusFunc sets the callback to check if boot is in progress.
func (h *SystemHandler) SetBootStatusFunc(fn func() bool) {
	h.bootStatusFn = fn
}

// SetSlowRequestThresholdMs exposes the -slow-request-ms runtime flag to the UI.
func (h *SystemHandler) SetSlowRequestThresholdMs(ms int) {
	if ms < 0 {
		ms = 0
	}
	h.slowRequestThresholdMs = ms
}

// SetHydraRoute sets the HydraRoute Neo service for status/control endpoints.
func (h *SystemHandler) SetHydraRoute(svc *hydraroute.Service) {
	h.hydra = svc
}

// SetSingboxOperator provides access to the sing-box operator for
// reporting install status in system info.
func (h *SystemHandler) SetSingboxOperator(op *singbox.Operator) {
	h.singboxOp = op
}

// RestartDaemon triggers a self-restart of the AWG Manager daemon.
//
//	@Summary		Restart daemon
//	@Tags			system
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/system/restart [post]
func (h *SystemHandler) RestartDaemon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.restartFn == nil {
		response.Error(w, "restart not available", "RESTART_UNAVAILABLE")
		return
	}
	response.Success(w, map[string]string{"status": "restarting"})
	h.restartFn()
}

// HydraRouteStatus returns HydraRoute Neo detection status.
//
//	@Summary		HydraRoute status (system)
//	@Tags			system
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	HydraRouteStatusResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/system/hydraroute-status [get]
func (h *SystemHandler) HydraRouteStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if h.hydra == nil {
		response.Success(w, hydraRouteStatusData(hydraroute.Status{
			Installed:    false,
			Running:      false,
			ProcessState: hydraroute.StateNotInstalled,
		}))
		return
	}
	response.Success(w, hydraRouteStatusData(h.hydra.RefreshStatus()))
}

// HydraRouteControl starts/stops/restarts the HydraRoute daemon.
//
//	@Summary		HydraRoute control (system)
//	@Tags			system
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/system/hydraroute-control [post]
func (h *SystemHandler) HydraRouteControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.hydra == nil {
		response.Error(w, "HydraRoute not available", "HYDRAROUTE_UNAVAILABLE")
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid request", "INVALID_REQUEST")
		return
	}
	if err := h.hydra.Control(req.Action); err != nil {
		response.Error(w, err.Error(), "HYDRAROUTE_CONTROL_ERROR")
		return
	}
	publishInvalidated(h.bus, ResourceRoutingHydrarouteStatus, "control-"+req.Action)
	response.Success(w, hydraRouteStatusData(h.hydra.RefreshStatus()))
}

// Info returns system information.
//
//	@Summary		System info
//	@Tags			system
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SystemInfoResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/system/info [get]
func (h *SystemHandler) Info(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	// Get current settings
	var disableMemorySaving bool
	if h.settingsStore != nil {
		if settings, err := h.settingsStore.Get(); err == nil {
			disableMemorySaving = settings.DisableMemorySaving
		}
	}

	// Get GC environment for display
	gcEnv := osdetect.GetGCEnv(disableMemorySaving)
	var gcMemLimit string
	var gogc string
	if gcEnv == nil {
		gcMemLimit = "Unlimited"
		gogc = "default"
	} else {
		for _, env := range gcEnv {
			if len(env) > 11 && env[:11] == "GOMEMLIMIT=" {
				gcMemLimit = env[11:]
			}
			if len(env) > 5 && env[:5] == "GOGC=" {
				gogc = env[5:]
			}
		}
		if gcMemLimit == "" {
			gcMemLimit = "Unlimited"
		}
	}

	// Get kernel module and backend info
	var kernelModuleExists, kernelModuleLoaded bool
	var kernelModuleModel string
	var kernelModuleVersion string
	var isAarch64 bool
	if h.kmodLoader != nil {
		kernelModuleExists = h.kmodLoader.ModuleExists()
		kernelModuleLoaded = h.kmodLoader.IsLoaded()
		kernelModuleModel = h.kmodLoader.Model()
		kernelModuleVersion = h.kmodLoader.OnDiskVersion()
		isAarch64 = h.kmodLoader.SoC().IsAARCH64()
	}
	activeBackendType := "kernel"
	if h.activeBackend != nil {
		activeBackendType = h.activeBackend.Type().String()
	}

	// Router LAN IP (from br0 interface)
	routerIP := getBr0IP()

	info := h.buildSystemInfo(disableMemorySaving, gcMemLimit, gogc, kernelModuleExists, kernelModuleLoaded, kernelModuleModel, kernelModuleVersion, isAarch64, activeBackendType, routerIP)

	response.Success(w, info)
}

// BuildSystemInfo returns system info for SSE snapshot.
func (h *SystemHandler) BuildSystemInfo() map[string]interface{} {
	var disableMemorySaving bool
	if h.settingsStore != nil {
		if settings, err := h.settingsStore.Get(); err == nil {
			disableMemorySaving = settings.DisableMemorySaving
		}
	}

	gcEnv := osdetect.GetGCEnv(disableMemorySaving)
	var gcMemLimit, gogc string
	if gcEnv == nil {
		gcMemLimit = "Unlimited"
		gogc = "default"
	} else {
		for _, env := range gcEnv {
			if len(env) > 11 && env[:11] == "GOMEMLIMIT=" {
				gcMemLimit = env[11:]
			}
			if len(env) > 5 && env[:5] == "GOGC=" {
				gogc = env[5:]
			}
		}
		if gcMemLimit == "" {
			gcMemLimit = "Unlimited"
		}
	}

	var kernelModuleExists, kernelModuleLoaded bool
	var kernelModuleModel, kernelModuleVersion string
	var isAarch64 bool
	if h.kmodLoader != nil {
		kernelModuleExists = h.kmodLoader.ModuleExists()
		kernelModuleLoaded = h.kmodLoader.IsLoaded()
		kernelModuleModel = h.kmodLoader.Model()
		kernelModuleVersion = h.kmodLoader.OnDiskVersion()
		isAarch64 = h.kmodLoader.SoC().IsAARCH64()
	}
	activeBackendType := "kernel"
	if h.activeBackend != nil {
		activeBackendType = h.activeBackend.Type().String()
	}
	routerIP := getBr0IP()

	return h.buildSystemInfo(disableMemorySaving, gcMemLimit, gogc, kernelModuleExists, kernelModuleLoaded, kernelModuleModel, kernelModuleVersion, isAarch64, activeBackendType, routerIP)
}

func (h *SystemHandler) buildSystemInfo(disableMemorySaving bool, gcMemLimit, gogc string, kernelModuleExists, kernelModuleLoaded bool, kernelModuleModel, kernelModuleVersion string, isAarch64 bool, activeBackendType, routerIP string) map[string]interface{} {
	singboxInstalled, singboxVersion := h.getSingboxInfoFast()
	routerDetails := h.getRouterDetailsCached()
	clock := routerclock.Get()
	nativewgAvail, nativewgReason := nativewgStatus()

	return map[string]interface{}{
		"version":                     h.version,
		"goVersion":                   runtime.Version(),
		"goArch":                      runtime.GOARCH,
		"goOS":                        runtime.GOOS,
		"keeneticOS":                  string(osdetect.Get()),
		"isOS5":                       osdetect.Is5(),
		"firmwareVersion":             osdetect.ReleaseString(),
		"supportsExtendedASC":         osdetect.AtLeast(5, 1),
		"supportsHRanges":             ndmsinfo.SupportsHRanges(),
		"supportsPingCheck":           ndmsinfo.HasPingCheckComponent(),
		"totalMemoryMB":               osdetect.GetTotalMemoryMB(),
		"isLowMemory":                 osdetect.IsLowMemoryDevice(),
		"gcMemLimit":                  gcMemLimit,
		"gogc":                        gogc,
		"disableMemorySaving":         disableMemorySaving,
		"kernelModuleExists":          kernelModuleExists,
		"kernelModuleLoaded":          kernelModuleLoaded,
		"kernelModuleModel":           kernelModuleModel,
		"kernelModuleVersion":         kernelModuleVersion,
		"isAarch64":                   isAarch64,
		"activeBackend":               activeBackendType,
		"routerIP":                    routerIP,
		"routerTime":                  clock.Now.Format(time.RFC3339),
		"routerTimezone":              clock.ZoneName,
		"routerTimezoneOffsetMinutes": clock.OffsetMinutes,
		"bootInProgress":              h.bootStatusFn != nil && h.bootStatusFn(),
		"slowRequestThresholdMs":      h.slowRequestThresholdMs,
		"backendAvailability": map[string]bool{
			"nativewg": nativewgAvail,
			// Kernel backend works on any OS where amneziawg.ko is loaded.
			// On OS5 it uses the OpkgTun two-layer architecture (NDMS + kernel).
			"kernel": kernelModuleLoaded,
		},
		// Why NativeWG is gated (empty when available) — lets the UI explain
		// the disabled toggle instead of silently greying it out.
		"nativewgReason": nativewgReason,
		"singbox": map[string]interface{}{
			"installed": singboxInstalled,
			"version":   singboxVersion,
		},
		"routerDetails": routerDetails,
	}
}

// getRouterDetailsCached returns cached router details, refreshing in the
// background when the TTL expires. Concurrent refreshes are coalesced: only
// one goroutine runs RCI calls at a time; subsequent callers get the stale
// cached value until the refresh completes.
func (h *SystemHandler) getRouterDetailsCached() *RouterDetails {
	now := time.Now()

	h.routerDetailsMu.RLock()
	cached := h.routerDetailsCache
	cachedAt := h.routerDetailsCachedAt
	h.routerDetailsMu.RUnlock()

	if cached != nil && now.Sub(cachedAt) < routerDetailsCacheTTL {
		return cached
	}

	// Cache miss or expired — collect synchronously on first call so the
	// response contains real data, then reuse cached value on rapid retries.
	fresh := routerinfo.Collect()

	h.routerDetailsMu.Lock()
	h.routerDetailsCache = fresh
	h.routerDetailsCachedAt = now
	h.routerDetailsMu.Unlock()

	return fresh
}

// getSingboxInfoFast returns sing-box install/version data without blocking
// system/info on slow version probes. Version is served from short-lived cache;
// stale/missing cache is refreshed in background.
func (h *SystemHandler) getSingboxInfoFast() (bool, string) {
	if h.singboxOp == nil {
		return false, ""
	}

	// Fast presence check: avoid running external process on hot path.
	if !h.singboxOp.IsPresent() {
		h.resetSingboxVersionCacheLocked()
		return false, ""
	}

	now := time.Now()
	currentFingerprint := h.currentSingboxBinaryFingerprint()
	h.singboxInfoMu.RLock()
	cachedVersion := h.singboxVersionCached
	fetchedAt := h.singboxVersionFetchedAt
	cachedFingerprint := h.singboxBinaryFingerprint
	h.singboxInfoMu.RUnlock()

	// Lifecycle safety: install/update/replace changes binary fingerprint.
	// Invalidate stale version immediately so next refresh reads new banner.
	if currentFingerprint != "" && cachedFingerprint != "" && currentFingerprint != cachedFingerprint {
		h.singboxInfoMu.Lock()
		h.singboxVersionCached = ""
		h.singboxVersionFetchedAt = time.Time{}
		h.singboxBinaryFingerprint = currentFingerprint
		h.singboxInfoMu.Unlock()
		cachedVersion = ""
		fetchedAt = time.Time{}
	}

	if !fetchedAt.IsZero() && now.Sub(fetchedAt) < singboxVersionCacheTTL {
		return true, cachedVersion
	}

	h.startSingboxVersionRefresh(currentFingerprint)
	return true, cachedVersion
}

func (h *SystemHandler) startSingboxVersionRefresh(binaryFingerprint string) {
	h.singboxInfoMu.Lock()
	if h.singboxVersionRefreshRunning {
		h.singboxInfoMu.Unlock()
		return
	}
	h.singboxVersionRefreshRunning = true
	if binaryFingerprint != "" {
		h.singboxBinaryFingerprint = binaryFingerprint
	}
	h.singboxInfoMu.Unlock()

	go func() {
		_, version := h.singboxOp.IsInstalled()
		h.singboxInfoMu.Lock()
		h.singboxVersionCached = version
		h.singboxVersionFetchedAt = time.Now()
		h.singboxVersionRefreshRunning = false
		h.singboxInfoMu.Unlock()
	}()
}

func (h *SystemHandler) resetSingboxVersionCacheLocked() {
	h.singboxInfoMu.Lock()
	h.singboxVersionCached = ""
	h.singboxVersionFetchedAt = time.Time{}
	h.singboxVersionRefreshRunning = false
	h.singboxBinaryFingerprint = ""
	h.singboxInfoMu.Unlock()
}

func (h *SystemHandler) currentSingboxBinaryFingerprint() string {
	if h.singboxOp == nil {
		return ""
	}
	binPath := h.singboxOp.Binary()
	if binPath == "" {
		return ""
	}
	st, err := os.Stat(binPath)
	if err != nil || st.IsDir() {
		return ""
	}
	return fmt.Sprintf(
		"%s|%s|%s|%d",
		filepath.Clean(binPath),
		st.ModTime().UTC().Format(time.RFC3339Nano),
		st.Mode().String(),
		st.Size(),
	)
}

// Reasons NativeWG is unavailable, surfaced to the UI via system/info so the
// disabled toggle can explain itself instead of silently greying out.
const (
	nwgReasonNoComponent   = "no-component"   // firmware lacks the 'wireguard' component
	nwgReasonNoObfuscation = "no-obfuscation" // component present but no ASC firmware and awg_proxy.ko not loaded
)

// evalNativewg decides whether the NativeWG backend can work and, when it
// cannot, why. Pure (all router state passed in) so it stays unit-testable.
// NativeWG needs: (1) the firmware 'wireguard' component, AND (2) either
// native WireGuard ASC firmware (>= 5.01.A.3) or a loaded awg_proxy.ko for
// obfuscation on older firmware.
func evalNativewg(hasWireguardComponent, supportsASC, awgProxyLoaded bool) (available bool, reason string) {
	if !hasWireguardComponent {
		return false, nwgReasonNoComponent
	}
	if supportsASC || awgProxyLoaded {
		return true, ""
	}
	return false, nwgReasonNoObfuscation
}

// nativewgStatus wires evalNativewg to live router state.
func nativewgStatus() (available bool, reason string) {
	_, err := os.Stat("/proc/awg_proxy/version")
	return evalNativewg(ndmsinfo.HasWireguardComponent(), ndmsinfo.SupportsWireguardASC(), err == nil)
}

// getBr0IP returns the first IPv4 address of the br0 (Bridge0) interface.
func getBr0IP() string {
	iface, err := net.InterfaceByName("br0")
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok {
			if ip4 := ipnet.IP.To4(); ip4 != nil {
				return ip4.String()
			}
		}
	}
	return ""
}

// wanInterfaceJSON is the JSON response for a single WAN interface.
type wanInterfaceJSON struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	State string `json:"state"`
}

// WANInterfaces returns available WAN interfaces for routing.
// GET /api/system/wan-interfaces
//
//	@Summary		WAN interfaces
//	@Tags			system
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	WANInterfacesResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/system/wan-interfaces [get]
func (h *SystemHandler) WANInterfaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	model := h.tunnelService.WANModel()
	ifaces := model.ForUI()

	result := make([]wanInterfaceJSON, 0, len(ifaces))
	for _, iface := range ifaces {
		state := "down"
		if iface.Up {
			state = "up"
		}
		result = append(result, wanInterfaceJSON{
			Name:  iface.Name,
			Label: iface.Label,
			State: state,
		})
	}

	response.Success(w, result)
}

// AllInterfaces returns all router interfaces for routing configuration.
// GET /api/system/all-interfaces
//
//	@Summary		All interfaces
//	@Tags			system
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	AllInterfacesResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/system/all-interfaces [get]
func (h *SystemHandler) AllInterfaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	if h.ndmsQueries == nil {
		response.InternalError(w, "NDMS queries not available")
		return
	}

	ifaces, err := h.ndmsQueries.Interfaces.ListAll(r.Context())
	if err != nil {
		response.InternalError(w, "Failed to query interfaces: "+err.Error())
		return
	}

	response.Success(w, ifaces)
}
