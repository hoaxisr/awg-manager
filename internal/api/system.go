package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
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
	Version             string                        `json:"version" example:"2.5.0"`
	GoVersion           string                        `json:"goVersion" example:"go1.23.0"`
	GoArch              string                        `json:"goArch" example:"arm64"`
	GoOS                string                        `json:"goOS" example:"linux"`
	KeeneticOS          string                        `json:"keeneticOS" example:"ndms"`
	IsOS5               bool                          `json:"isOS5" example:"true"`
	FirmwareVersion     string                        `json:"firmwareVersion" example:"4.2.1"`
	SupportsExtendedASC bool                          `json:"supportsExtendedASC" example:"true"`
	SupportsHRanges     bool                          `json:"supportsHRanges" example:"true"`
	SupportsPingCheck   bool                          `json:"supportsPingCheck" example:"true"`
	TotalMemoryMB       int                           `json:"totalMemoryMB" example:"512"`
	IsLowMemory         bool                          `json:"isLowMemory" example:"false"`
	GcMemLimit          string                        `json:"gcMemLimit" example:"128MiB"`
	Gogc                string                        `json:"gogc" example:"25"`
	DisableMemorySaving bool                          `json:"disableMemorySaving" example:"false"`
	KernelModuleExists  bool                          `json:"kernelModuleExists" example:"true"`
	KernelModuleLoaded  bool                          `json:"kernelModuleLoaded" example:"false"`
	KernelModuleModel   string                        `json:"kernelModuleModel" example:"MT7981"`
	KernelModuleVersion string                        `json:"kernelModuleVersion" example:""`
	IsAarch64           bool                          `json:"isAarch64" example:"true"`
	ActiveBackend       string                        `json:"activeBackend" example:"nativewg"`
	RouterIP            string                        `json:"routerIP" example:"192.168.1.1"`
	BootInProgress      bool                          `json:"bootInProgress" example:"false"`
	BackendAvailability SystemInfoBackendAvailability `json:"backendAvailability"`
	Singbox             SystemInfoSingbox             `json:"singbox"`
	RouterDetails       *RouterDetails                `json:"routerDetails,omitempty"`
}

// RouterDetails contains extended router metadata derived from NDMS/RCI and local procfs.
type RouterDetails struct {
	Model             string   `json:"model,omitempty" example:"KN-3812"`
	ModelDisplay      string   `json:"modelDisplay,omitempty" example:"CMCC RAX3000M (KN-3812)"`
	PortedBuild       bool     `json:"portedBuild" example:"true"`
	HardwareID        string   `json:"hardwareId,omitempty" example:"KN-3812"`
	Region            string   `json:"region,omitempty" example:"EA"`
	Architecture      string   `json:"architecture,omitempty" example:"aarch64"`
	CPUModel          string   `json:"cpuModel,omitempty" example:"MT7981"`
	CPUTempC          int      `json:"cpuTempC,omitempty" example:"79"`
	WiFi24TempC       int      `json:"wifi24TempC,omitempty" example:"70"`
	WiFi5TempC        int      `json:"wifi5TempC,omitempty" example:"68"`
	MemoryUsedMB      int      `json:"memoryUsedMB,omitempty" example:"303"`
	MemoryTotalMB     int      `json:"memoryTotalMB,omitempty" example:"486"`
	MemoryUsedPercent int      `json:"memoryUsedPercent,omitempty" example:"62"`
	FirmwareTitle     string   `json:"firmwareTitle,omitempty" example:"CMCC RAX3000M (KN-3812) [Port]"`
	FirmwareRelease   string   `json:"firmwareRelease,omitempty" example:"5.0.9 (5.00.C.9.0-1)"`
	FirmwareSandbox   string   `json:"firmwareSandbox,omitempty" example:"preview"`
	FirmwareBuildDate string   `json:"firmwareBuildDate,omitempty" example:"7 Apr 2026"`
	BootSlot          string   `json:"bootSlot,omitempty" example:"1"`
	UptimeHuman       string   `json:"uptimeHuman,omitempty" example:"7d 16h 57m"`
	LoadAverage       string   `json:"loadAverage,omitempty" example:"1.82, 1.58, 1.58"`
	OpkgStorage       string   `json:"opkgStorage,omitempty" example:"451 MB / 205 GB"`
	VPNComponents     []string `json:"vpnComponents,omitempty" example:"WireGuard,OpenVPN,IPsec/IKEv2,L2TP,SSTP,ZeroTier"`
	StorageComponents []string `json:"storageComponents,omitempty" example:"NTFS,ExFAT,EXT4,SMB,FTP"`
	FeatureComponents []string `json:"featureComponents,omitempty" example:"HW-NAT,Wi-Fi 5GHz,WPA3,USB"`
	MeshMembers       []string `json:"meshMembers,omitempty" example:"RAX3000M (KN-3812) | 5.0.9 | 1921 Мбит/с | 13 дн. 06:22:26,SmartBox Giga (KN-1913) | 5.0.9 | 260 Мбит/с | 13 дн. 06:21:52"`
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

	singboxInfoMu                sync.RWMutex
	singboxVersionCached         string
	singboxVersionFetchedAt      time.Time
	singboxVersionRefreshRunning bool
	singboxBinaryFingerprint     string
}

const singboxVersionCacheTTL = 45 * time.Second

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
	singboxInstalled, singboxVersion := h.getSingboxInfoFast()
	routerDetails := collectRouterDetails()

	return map[string]interface{}{
		"version":             h.version,
		"goVersion":           runtime.Version(),
		"goArch":              runtime.GOARCH,
		"goOS":                runtime.GOOS,
		"keeneticOS":          string(osdetect.Get()),
		"isOS5":               osdetect.Is5(),
		"firmwareVersion":     osdetect.ReleaseString(),
		"supportsExtendedASC": osdetect.AtLeast(5, 1),
		"supportsHRanges":     ndmsinfo.SupportsHRanges(),
		"supportsPingCheck":   ndmsinfo.HasPingCheckComponent(),
		"totalMemoryMB":       osdetect.GetTotalMemoryMB(),
		"isLowMemory":         osdetect.IsLowMemoryDevice(),
		"gcMemLimit":          gcMemLimit,
		"gogc":                gogc,
		"disableMemorySaving": disableMemorySaving,
		"kernelModuleExists":  kernelModuleExists,
		"kernelModuleLoaded":  kernelModuleLoaded,
		"kernelModuleModel":   kernelModuleModel,
		"kernelModuleVersion": kernelModuleVersion,
		"isAarch64":           isAarch64,
		"activeBackend":       activeBackendType,
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
		"routerDetails": routerDetails,
	}
}

type rciVersionWire struct {
	Release string `json:"release"`
	Title   string `json:"title"`
	Model   string `json:"model"`
	HwID    string `json:"hw_id"`
	Region  string `json:"region"`
	Arch    string `json:"arch"`
	Sandbox string `json:"sandbox"`
	Ndm     struct {
		CDate string `json:"cdate"`
	} `json:"ndm"`
	NDW struct {
		Components string `json:"components"`
		Features   string `json:"features"`
	} `json:"ndw"`
}

type rciInterfaceTempWire map[string]struct {
	Temperature int `json:"temperature"`
}

type rciOpkgDiskWire struct {
	Disk string `json:"disk"`
}

type rciLSWire map[string]struct {
	Free  int64  `json:"free"`
	Total int64  `json:"total"`
	Label string `json:"label"`
}

type rciLSRootWire struct {
	Entry map[string]json.RawMessage `json:"entry"`
}

func collectRouterDetails() *RouterDetails {
	ver := ndmsinfo.Get()
	if ver == nil {
		return nil
	}
	out := &RouterDetails{
		Model:           strings.TrimSpace(ver.Model),
		HardwareID:      strings.TrimSpace(ver.HardwareID),
		Region:          strings.TrimSpace(ver.Region),
		FirmwareRelease: strings.TrimSpace(ver.Release),
		FirmwareTitle:   strings.TrimSpace(ver.Title),
	}

	rciVer := fetchRCIVersion()
	if rciVer != nil {
		if out.Model == "" {
			out.Model = strings.TrimSpace(rciVer.Model)
		}
		if out.HardwareID == "" {
			out.HardwareID = strings.TrimSpace(rciVer.HwID)
		}
		if out.Region == "" {
			out.Region = strings.TrimSpace(rciVer.Region)
		}
		if out.FirmwareRelease == "" {
			out.FirmwareRelease = strings.TrimSpace(rciVer.Release)
		}
		if out.FirmwareTitle == "" {
			out.FirmwareTitle = strings.TrimSpace(rciVer.Title)
		}
		out.Architecture = detectArchitecture(strings.TrimSpace(rciVer.Arch))
		out.FirmwareSandbox = strings.TrimSpace(rciVer.Sandbox)
		out.FirmwareBuildDate = strings.TrimSpace(rciVer.Ndm.CDate)
		out.VPNComponents = detectLabeledComponents(rciVer.NDW.Components, []componentLabel{
			{key: "wireguard", label: "WireGuard"},
			{key: "openvpn", label: "OpenVPN"},
			{key: "ipsec", label: "IPsec/IKEv2"},
			{key: "l2tp", label: "L2TP"},
			{key: "sstp", label: "SSTP"},
			{key: "zerotier", label: "ZeroTier"},
		})
		out.StorageComponents = detectLabeledComponents(rciVer.NDW.Components, []componentLabel{
			{key: "ntfs", label: "NTFS"},
			{key: "exfat", label: "ExFAT"},
			{key: "ext", label: "EXT4"},
			{key: "tsmb", label: "SMB"},
			{key: "ftp", label: "FTP"},
		})
		out.FeatureComponents = detectLabeledComponents(rciVer.NDW.Features, []componentLabel{
			{key: "hwnat", label: "HW-NAT"},
			{key: "ppe", label: "PPE"},
			{key: "wifi5ghz", label: "Wi-Fi 5GHz"},
			{key: "wpa3", label: "WPA3"},
			{key: "usb", label: "USB"},
		})
	}
	if out.Architecture == "" {
		out.Architecture = runtime.GOARCH
	}

	out.ModelDisplay, out.PortedBuild = buildModelDisplay(out.Model)
	out.CPUModel = detectCPUModel()
	out.CPUTempC = readThermalZoneC("/sys/devices/virtual/thermal/thermal_zone0/temp")
	out.WiFi24TempC, out.WiFi5TempC = fetchWiFiTemps()
	out.MemoryUsedMB, out.MemoryTotalMB, out.MemoryUsedPercent = readMemUsage()
	out.UptimeHuman = formatUptime(readUptimeSeconds())
	out.LoadAverage = readLoadAverage()
	out.BootSlot = strings.TrimSpace(readTextFile("/proc/dual_image/boot_current"))
	out.OpkgStorage = fetchOPKGStorage()
	out.MeshMembers = fetchMeshMembers()

	return out
}

func fetchRCIVersion() *rciVersionWire {
	var v rciVersionWire
	if err := rciGetJSON("/show/version", &v); err != nil {
		return nil
	}
	return &v
}

func rciGetJSON(path string, dst any) error {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	req, err := http.NewRequest(http.MethodGet, "http://localhost:79/rci"+path, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, dst)
}

func rciGetRaw(path string) ([]byte, error) {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	req, err := http.NewRequest(http.MethodGet, "http://localhost:79/rci"+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func buildModelDisplay(model string) (string, bool) {
	m := strings.TrimSpace(model)
	if m == "" {
		return "", false
	}
	vendor := "Keenetic"
	port := false
	rules := []struct {
		patterns []string
		vendor   string
		port     bool
	}{
		{[]string{"Cudy", "WBR3000", "TR3000", "WR3000"}, "Cudy", true},
		{[]string{"CMCC", "RAX3000M"}, "CMCC", true},
		{[]string{"Netis", "NX31", "NX32", "N6"}, "Netis", true},
		{[]string{"Redmi"}, "Redmi", true},
		{[]string{"Xiaomi", "AX3000T", "3G", "3P", "4A", "4C"}, "Xiaomi", true},
		{[]string{"Mercusys"}, "Mercusys", true},
		{[]string{"SmartBox"}, "SmartBox", true},
		{[]string{"TP-Link", "EC330", "Archer"}, "TP-Link", true},
		{[]string{"Linksys"}, "Linksys", true},
		{[]string{"WiFire"}, "WiFire", true},
		{[]string{"Vertell"}, "Vertell", true},
		{[]string{"MTS", "WG430"}, "MTS", true},
		{[]string{"HLK"}, "HLK", true},
	}
	for _, r := range rules {
		for _, p := range r.patterns {
			if strings.Contains(m, p) {
				vendor = r.vendor
				port = r.port
				goto done
			}
		}
	}
done:
	display := m
	if vendor != "" && vendor != "Keenetic" && !strings.Contains(m, vendor) {
		display = vendor + " " + m
	}
	return display, port
}

var cpuModelPattern = regexp.MustCompile(`MT76[0-9A-Za-z]*|MT79[0-9A-Za-z]*|EN75[0-9A-Za-z]*`)

func detectCPUModel() string {
	b, err := os.ReadFile("/lib/libndmMwsController.so")
	if err == nil {
		if m := cpuModelPattern.Find(b); len(m) > 0 {
			return string(m)
		}
	}
	proc := readTextFile("/proc/cpuinfo")
	for _, line := range strings.Split(proc, "\n") {
		trim := strings.TrimSpace(line)
		lower := strings.ToLower(trim)
		if strings.HasPrefix(lower, "system type") || strings.HasPrefix(lower, "hardware") || strings.HasPrefix(lower, "model name") {
			parts := strings.SplitN(trim, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

func detectArchitecture(fallback string) string {
	arch := strings.TrimSpace(fallback)
	if arch != "" {
		return arch
	}
	return runtime.GOARCH
}

func fetchWiFiTemps() (int, int) {
	var payload rciInterfaceTempWire
	if err := rciGetJSON("/show/interface", &payload); err != nil {
		return 0, 0
	}
	return payload["WifiMaster0"].Temperature, payload["WifiMaster1"].Temperature
}

func readThermalZoneC(path string) int {
	raw := strings.TrimSpace(readTextFile(path))
	if raw == "" {
		return 0
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	if v >= 1000 {
		return v / 1000
	}
	return v
}

func readMemUsage() (usedMB int, totalMB int, usedPercent int) {
	meminfo := readTextFile("/proc/meminfo")
	var totalKB, availKB int
	for _, line := range strings.Split(meminfo, "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			totalKB = parseFirstInt(line)
		}
		if strings.HasPrefix(line, "MemAvailable:") {
			availKB = parseFirstInt(line)
		}
	}
	if totalKB <= 0 {
		return 0, 0, 0
	}
	totalMB = totalKB / 1024
	usedMB = (totalKB - availKB) / 1024
	if totalMB > 0 {
		usedPercent = usedMB * 100 / totalMB
	}
	return
}

func parseFirstInt(s string) int {
	fields := strings.Fields(s)
	for _, f := range fields {
		if n, err := strconv.Atoi(f); err == nil {
			return n
		}
	}
	return 0
}

func readUptimeSeconds() int64 {
	raw := strings.TrimSpace(readTextFile("/proc/uptime"))
	if raw == "" {
		return 0
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0
	}
	intPart := strings.SplitN(fields[0], ".", 2)[0]
	v, _ := strconv.ParseInt(intPart, 10, 64)
	return v
}

func formatUptime(seconds int64) string {
	if seconds <= 0 {
		return ""
	}
	d := seconds / 86400
	h := (seconds % 86400) / 3600
	m := (seconds % 3600) / 60
	return fmt.Sprintf("%dd %dh %dm", d, h, m)
}

func readLoadAverage() string {
	raw := strings.TrimSpace(readTextFile("/proc/loadavg"))
	if raw == "" {
		return ""
	}
	f := strings.Fields(raw)
	if len(f) < 3 {
		return ""
	}
	return strings.Join(f[:3], ", ")
}

func fetchOPKGStorage() string {
	var disk rciOpkgDiskWire
	if err := rciGetJSON("/show/sc/opkg/disk", &disk); err != nil {
		return ""
	}
	rawLabel := strings.TrimSpace(disk.Disk)
	if rawLabel == "" {
		return ""
	}

	// kn-info parity: it keeps leading path markers and only strips trailing "/" and ":".
	knLabel := strings.TrimSuffix(strings.TrimSuffix(rawLabel, "/"), ":")
	trimmedLabel := strings.Trim(rawLabel, ":/")
	labels := make([]string, 0, 4)
	for _, v := range []string{rawLabel, knLabel, trimmedLabel} {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		dup := false
		for _, seen := range labels {
			if seen == v {
				dup = true
				break
			}
		}
		if !dup {
			labels = append(labels, v)
		}
	}
	if len(labels) == 0 {
		return ""
	}

	raw, err := rciGetRaw("/ls")
	if err != nil {
		return ""
	}

	// Newer NDMS layout: {"entry": {"<id>:": {...}}}
	var root rciLSRootWire
	if err := json.Unmarshal(raw, &root); err == nil && len(root.Entry) > 0 {
		for _, nodeRaw := range root.Entry {
			var node map[string]any
			if err := json.Unmarshal(nodeRaw, &node); err != nil {
				continue
			}
			nodeLabel := strings.TrimSpace(anyToString(node["label"]))
			for _, label := range labels {
				if nodeLabel != label && strings.Trim(nodeLabel, ":/") != strings.Trim(label, ":/") {
					continue
				}
				free := anyToInt64(node["free"])
				total := anyToInt64(node["total"])
				if total <= 0 {
					continue
				}
				used := total - free
				return formatBytesPair(used, total)
			}
		}
	}

	var ls rciLSWire
	if err := json.Unmarshal(raw, &ls); err == nil {
		for _, label := range labels {
			key := label + ":"
			if v, ok := ls[key]; ok && v.Total > 0 {
				used := v.Total - v.Free
				return formatBytesPair(used, v.Total)
			}
		}
	}
	// kn-info parity fallback: locate matching "label" block in /rci/ls response.
	var any map[string]json.RawMessage
	if err := json.Unmarshal(raw, &any); err != nil {
		return ""
	}
	for _, nodeRaw := range any {
		var node struct {
			Label string `json:"label"`
			Free  int64  `json:"free"`
			Total int64  `json:"total"`
		}
		if err := json.Unmarshal(nodeRaw, &node); err != nil {
			continue
		}
		nodeLabel := strings.TrimSpace(node.Label)
		for _, label := range labels {
			if node.Total > 0 && (nodeLabel == label || strings.Trim(nodeLabel, ":/") == strings.Trim(label, ":/")) {
				used := node.Total - node.Free
				return formatBytesPair(used, node.Total)
			}
		}
	}
	return ""
}

func anyToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatInt(int64(t), 10)
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	default:
		return ""
	}
}

func anyToInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	default:
		return 0
	}
}

func formatBytesPair(used, total int64) string {
	usedMB := used / 1024 / 1024
	totalMB := total / 1024 / 1024
	if totalMB >= 1024 {
		totalGB := total / 1024 / 1024 / 1024
		if usedMB < 1024 {
			return fmt.Sprintf("%d MB / %d GB", usedMB, totalGB)
		}
		return fmt.Sprintf("%d GB / %d GB", used/1024/1024/1024, totalGB)
	}
	return fmt.Sprintf("%d MB / %d MB", usedMB, totalMB)
}

func fetchMeshMembers() []string {
	var members []map[string]any
	if err := rciGetJSON("/show/mws/member", &members); err != nil {
		return nil
	}
	out := make([]string, 0, len(members))
	for _, m := range members {
		model := strings.TrimSpace(anyToString(m["model"]))
		if model == "" {
			continue
		}
		fw := strings.TrimSpace(anyToString(m["fw"]))
		if fw == "" {
			out = append(out, model+" | Не в сети")
			continue
		}

		var uptime int64
		if sys, ok := m["system"].(map[string]any); ok {
			uptime = anyToInt64(sys["uptime"])
		}

		var speed int64
		if backhaul, ok := m["backhaul"].(map[string]any); ok {
			speed = anyToInt64(backhaul["txrate"])
			if speed <= 0 {
				speed = anyToInt64(backhaul["speed"])
			}
		}
		if speed <= 0 {
			speed = 0
		}
		out = append(out, fmt.Sprintf("%s | %s | %d Мбит/с | %s", model, fw, speed, formatUptimeRU(uptime)))
	}
	return out
}

func formatUptimeRU(seconds int64) string {
	if seconds <= 0 {
		return "00:00:00"
	}
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	mins := (seconds % 3600) / 60
	secs := seconds % 60
	if days > 0 {
		return fmt.Sprintf("%d дн. %02d:%02d:%02d", days, hours, mins, secs)
	}
	return fmt.Sprintf("%02d:%02d:%02d", hours, mins, secs)
}

type componentLabel struct {
	key   string
	label string
}

func detectLabeledComponents(raw string, labels []componentLabel) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	lower := strings.ToLower(raw)
	out := make([]string, 0, len(labels))
	for _, item := range labels {
		if strings.Contains(lower, item.key) {
			out = append(out, item.label)
		}
	}
	return out
}

func readTextFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(bytes.TrimSpace(b)))
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

// nativewgAvailable returns true if NativeWG backend can work:
// (1) the firmware has the 'wireguard' component installed, AND
// (2) either firmware supports WireGuard ASC natively (>= 5.01.A.4)
//
//	or awg_proxy.ko is loaded (provides obfuscation proxy for older firmware).
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
