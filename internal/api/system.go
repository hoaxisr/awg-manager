package api

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/kmod"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
)

// SettingsProvider provides access to settings.
type SettingsProvider interface {
	Get() (*storage.Settings, error)
}

// KmodLoader provides kernel module status and download management.
type KmodLoader interface {
	ModuleExists() bool
	IsLoaded() bool
	Model() string
	SoC() kmod.SoC
	OnDiskVersion() string
	DownloadStatus() kmod.DownloadStatus
	DownloadError() string
	TriggerDownload(ctx context.Context) error
	SwapModule(ctx context.Context, version string) error
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
	logger           AppLogger
	restartFn        func()
}

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

// SetLoggingService sets the logging service.
func (h *SystemHandler) SetLoggingService(logger LoggingService) {
	h.logger = logger
}

// SetRestartFunc sets the callback to trigger daemon self-restart.
func (h *SystemHandler) SetRestartFunc(fn func()) {
	h.restartFn = fn
}

// Info returns system information.
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
	var kernelModuleDownloadStatus kmod.DownloadStatus = kmod.StatusNotNeeded
	var kernelModuleDownloadError string
	if h.kmodLoader != nil {
		kernelModuleExists = h.kmodLoader.ModuleExists()
		kernelModuleLoaded = h.kmodLoader.IsLoaded()
		kernelModuleModel = h.kmodLoader.Model()
		kernelModuleVersion = h.kmodLoader.OnDiskVersion()
		isAarch64 = h.kmodLoader.SoC().IsAARCH64()
		kernelModuleDownloadStatus = h.kmodLoader.DownloadStatus()
		kernelModuleDownloadError = h.kmodLoader.DownloadError()
	}
	activeBackendType := "userspace"
	if h.activeBackend != nil {
		activeBackendType = h.activeBackend.Type().String()
	}

	info := map[string]interface{}{
		"version":                     h.version,
		"goVersion":                   runtime.Version(),
		"goArch":                      runtime.GOARCH,
		"goOS":                        runtime.GOOS,
		"keeneticOS":                  string(osdetect.Get()),
		"isOS5":                       osdetect.Is5(),
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
		"kernelModuleDownloadStatus":  string(kernelModuleDownloadStatus),
		"kernelModuleDownloadError":   kernelModuleDownloadError,
		"activeBackend":               activeBackendType,
	}

	response.Success(w, info)
}

// ChangeBackend changes the backend mode and triggers daemon restart.
// POST /api/system/change-backend
func (h *SystemHandler) ChangeBackend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "Invalid request body")
		return
	}

	// Validate mode
	if req.Mode != "auto" && req.Mode != "kernel" && req.Mode != "userspace" {
		response.BadRequest(w, "Invalid mode: must be auto, kernel, or userspace")
		return
	}

	// Save new backend mode to settings
	if h.settingsWriter == nil {
		response.InternalError(w, "Settings store not available")
		return
	}
	settings, err := h.settingsWriter.Get()
	if err != nil {
		response.InternalError(w, "Failed to read settings: "+err.Error())
		return
	}
	settings.BackendMode = req.Mode
	if err := h.settingsWriter.Save(settings); err != nil {
		response.InternalError(w, "Failed to save settings: "+err.Error())
		return
	}

	// Teardown all tunnels: delete OS-side resources (OpkgTun, interface,
	// firewall) so they get recreated cleanly with the new backend.
	if h.tunnelService != nil {
		ctx := context.Background()
		_ = h.tunnelService.TeardownForBackendSwitch(ctx)
	}

	// Stop ping check monitoring
	if h.pingCheckService != nil {
		h.pingCheckService.StopMonitoringAll()
	}

	// Log the change
	if h.logger != nil {
		h.logger.Log(logging.CategorySystem, "change-backend", req.Mode, "Backend mode changed, restarting daemon")
	}

	// Send response before restarting
	response.Success(w, map[string]interface{}{
		"success": true,
		"mode":    req.Mode,
	})

	// Flush the response
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// Schedule restart
	if h.restartFn != nil {
		h.restartFn()
	}
}

// DownloadKmod triggers a manual kernel module download.
// POST /api/system/kmod/download
func (h *SystemHandler) DownloadKmod(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	if h.kmodLoader == nil {
		response.InternalError(w, "Kernel module loader not available")
		return
	}

	if err := h.kmodLoader.TriggerDownload(r.Context()); err != nil {
		response.Error(w, "Download failed: "+err.Error(), "KMOD_DOWNLOAD_FAILED")
		return
	}

	response.Success(w, map[string]interface{}{
		"success": true,
	})
}

// wanInterfaceJSON is the JSON response for a single WAN interface.
type wanInterfaceJSON struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	State string `json:"state"`
}

// KmodVersions returns available kernel module versions.
// GET /api/system/kmod/versions
func (h *SystemHandler) KmodVersions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	current := ""
	if h.kmodLoader != nil {
		current = h.kmodLoader.OnDiskVersion()
	}

	response.Success(w, map[string]interface{}{
		"versions":    kmod.KnownVersions,
		"current":     current,
		"recommended": kmod.RecommendedVersion,
	})
}

// SwapKmod changes the kernel module version and restarts the daemon.
// POST /api/system/kmod/swap
func (h *SystemHandler) SwapKmod(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	var req struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "Invalid request body")
		return
	}

	// Validate version
	if !kmod.IsKnownVersion(req.Version) {
		response.BadRequest(w, "Unknown kernel module version: "+req.Version)
		return
	}

	// Save kmodVersion to settings
	if h.settingsWriter == nil {
		response.InternalError(w, "Settings store not available")
		return
	}
	settings, err := h.settingsWriter.Get()
	if err != nil {
		response.InternalError(w, "Failed to read settings: "+err.Error())
		return
	}
	settings.KmodVersion = req.Version
	if err := h.settingsWriter.Save(settings); err != nil {
		response.InternalError(w, "Failed to save settings: "+err.Error())
		return
	}

	// Teardown all tunnels (same as ChangeBackend)
	if h.tunnelService != nil {
		ctx := context.Background()
		_ = h.tunnelService.TeardownForBackendSwitch(ctx)
	}

	// Stop ping check monitoring
	if h.pingCheckService != nil {
		h.pingCheckService.StopMonitoringAll()
	}

	// Log the change
	if h.logger != nil {
		h.logger.Log(logging.CategorySystem, "swap-kmod", req.Version, "Kernel module version changed, restarting daemon")
	}

	// Send response before restarting
	response.Success(w, map[string]interface{}{
		"success": true,
		"version": req.Version,
	})

	// Flush the response
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// Schedule restart — EnsureModule at startup will download the new version
	if h.restartFn != nil {
		h.restartFn()
	}
}

// WANInterfaces returns available WAN interfaces for routing.
// GET /api/system/wan-interfaces
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
