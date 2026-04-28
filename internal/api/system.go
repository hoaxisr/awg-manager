package api

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"runtime"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/hydraroute"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/kmod"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
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
	Version              string                        `json:"version" example:"2.5.0"`
	GoVersion            string                        `json:"goVersion" example:"go1.23.0"`
	GoArch               string                        `json:"goArch" example:"arm64"`
	GoOS                 string                        `json:"goOS" example:"linux"`
	KeeneticOS           string                        `json:"keeneticOS" example:"ndms"`
	IsOS5                bool                          `json:"isOS5" example:"true"`
	FirmwareVersion      string                        `json:"firmwareVersion" example:"4.2.1"`
	SupportsExtendedASC  bool                          `json:"supportsExtendedASC" example:"true"`
	SupportsHRanges      bool                          `json:"supportsHRanges" example:"true"`
	SupportsPingCheck    bool                          `json:"supportsPingCheck" example:"true"`
	TotalMemoryMB        int                           `json:"totalMemoryMB" example:"512"`
	IsLowMemory          bool                          `json:"isLowMemory" example:"false"`
	GcMemLimit           string                        `json:"gcMemLimit" example:"128MiB"`
	Gogc                 string                        `json:"gogc" example:"25"`
	DisableMemorySaving  bool                          `json:"disableMemorySaving" example:"false"`
	KernelModuleExists   bool                          `json:"kernelModuleExists" example:"true"`
	KernelModuleLoaded   bool                          `json:"kernelModuleLoaded" example:"false"`
	KernelModuleModel    string                        `json:"kernelModuleModel" example:"MT7981"`
	KernelModuleVersion  string                        `json:"kernelModuleVersion" example:""`
	IsAarch64            bool                          `json:"isAarch64" example:"true"`
	ActiveBackend        string                        `json:"activeBackend" example:"nativewg"`
	RouterIP             string                        `json:"routerIP" example:"192.168.1.1"`
	BootInProgress       bool                          `json:"bootInProgress" example:"false"`
	BackendAvailability  SystemInfoBackendAvailability `json:"backendAvailability"`
	Singbox              SystemInfoSingbox             `json:"singbox"`
}

// SystemInfoResponse is the envelope for GET /system/info.
type SystemInfoResponse struct {
	Success bool           `json:"success" example:"true"`
	Data    SystemInfoData `json:"data"`
}

// HydraRouteStatusData mirrors frontend HydraRouteStatus.
type HydraRouteStatusData struct {
	Installed bool   `json:"installed" example:"true"`
	Running   bool   `json:"running" example:"true"`
	Version   string `json:"version,omitempty" example:"0.3.1"`
}

// HydraRouteStatusResponse is the envelope for GET /system/hydraroute-status.
type HydraRouteStatusResponse struct {
	Success bool                 `json:"success" example:"true"`
	Data    HydraRouteStatusData `json:"data"`
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
	version          string
	settingsStore    SettingsProvider
	settingsWriter   *storage.SettingsStore
	activeBackend    backend.Backend
	kmodLoader       KmodLoader
	tunnelService    TunnelService
	pingCheckService PingCheckService
	ndmsQueries      *ndmsquery.Queries
	restartFn        func()
	bootStatusFn     func() bool // returns true if boot is still in progress
	hydra            *hydraroute.Service
	singboxOp        *singbox.Operator
	bus              *events.Bus
}

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
		response.Success(w, hydraroute.Status{})
		return
	}
	response.Success(w, h.hydra.RefreshStatus())
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
	response.Success(w, h.hydra.GetStatus())
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
	singboxInstalled, singboxVersion := false, ""
	if h.singboxOp != nil {
		singboxInstalled, singboxVersion = h.singboxOp.IsInstalled()
	}

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
		"routerIP":            routerIP,
		"bootInProgress":      h.bootStatusFn != nil && h.bootStatusFn(),
		"backendAvailability": map[string]bool{
			"nativewg": nativewgAvailable(),
			// Kernel backend works on any OS where amneziawg.ko is loaded.
			// On OS5 it uses the OpkgTun two-layer architecture (NDMS + kernel).
			"kernel": kernelModuleLoaded,
		},
		"singbox": map[string]interface{}{
			"installed": singboxInstalled,
			"version":   singboxVersion,
		},
	}
}

// nativewgAvailable returns true if NativeWG backend can work:
// (1) the firmware has the 'wireguard' component installed, AND
// (2) either firmware supports WireGuard ASC natively (>= 5.01.A.4)
//     or awg_proxy.ko is loaded (provides obfuscation proxy for older firmware).
func nativewgAvailable() bool {
	if !ndmsinfo.HasWireguardComponent() {
		return false
	}
	if ndmsinfo.SupportsWireguardASC() {
		return true
	}
	_, err := os.Stat("/proc/awg_proxy/version")
	return err == nil
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
