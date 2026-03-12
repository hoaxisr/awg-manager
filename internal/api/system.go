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
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
)

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
	ndmsClient       ndms.Client
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

// SetNDMSClient sets the NDMS client for querying router interfaces.
func (h *SystemHandler) SetNDMSClient(c ndms.Client) {
	h.ndmsClient = c
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
	if h.kmodLoader != nil {
		kernelModuleExists = h.kmodLoader.ModuleExists()
		kernelModuleLoaded = h.kmodLoader.IsLoaded()
		kernelModuleModel = h.kmodLoader.Model()
		kernelModuleVersion = h.kmodLoader.OnDiskVersion()
		isAarch64 = h.kmodLoader.SoC().IsAARCH64()
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

// wanInterfaceJSON is the JSON response for a single WAN interface.
type wanInterfaceJSON struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	State string `json:"state"`
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

// AllInterfaces returns all router interfaces for routing configuration.
// GET /api/system/all-interfaces
func (h *SystemHandler) AllInterfaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	if h.ndmsClient == nil {
		response.InternalError(w, "NDMS client not available")
		return
	}

	ifaces, err := h.ndmsClient.QueryAllInterfaces(r.Context())
	if err != nil {
		response.InternalError(w, "Failed to query interfaces: "+err.Error())
		return
	}

	response.Success(w, ifaces)
}
