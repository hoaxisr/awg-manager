package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/httpprobe"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

// OtherEngineIntegrationHooks supplies read/purge/reload callbacks for the sibling
// routing engine (Sing-box) and AWGM stores without introducing circular imports.
type OtherEngineIntegrationHooks struct {
	IsSingboxRunning   func() bool
	IsSingboxInstalled func() bool
	GetSingboxCounts   func() (tunnels, subs int)
	PurgeSingbox       func(ctx context.Context) (tunnelsRemoved, subsRemoved int, err error)
	PostRestoreSync    func(ctx context.Context, restoredSingbox, restoredSettings bool) error
	OnBeforePurgeFirc  func(ctx context.Context)
	ListOwnedProxies   func(ctx context.Context) ([]string, error)
}

// FircHandler exposes HTTP API endpoints for managing fircd and tunvless.
type FircHandler struct {
	op                  *firc.Operator
	settingsStore       *storage.SettingsStore
	bus                 *events.Bus
	singboxConfigDir    string
	onSelectEngine      func(selected bool)
	onExtChanged        func(ext firc.ExtConfig)
	startOtherEngine    func(ctx context.Context, engine string) error
	stopOtherEngine     func(ctx context.Context, engine string) error
	standbyOtherEngine  func(ctx context.Context, engine string) error
	activateOtherEngine func(ctx context.Context, engine string) error
	captureOtherEngine  func(ctx context.Context) (func(ctx context.Context) error, error)
	integrationHooks    OtherEngineIntegrationHooks
	v1TransportMu       sync.Mutex
	v1TransportSocket   string
	v1Transport         *http.Transport
	installer           *firc.OnDemandInstaller
	installMu           sync.Mutex
	probeInterfaceFn    func(ctx context.Context, ifaceName, checkURL string) fircInterfaceProbeResult
}

func (h *FircHandler) getV1ProxyTransport(socketPath string) *http.Transport {
	h.v1TransportMu.Lock()
	defer h.v1TransportMu.Unlock()
	if h.v1Transport != nil && h.v1TransportSocket == socketPath {
		return h.v1Transport
	}
	if h.v1Transport != nil {
		h.v1Transport.CloseIdleConnections()
	}
	sp := socketPath
	h.v1TransportSocket = sp
	h.v1Transport = &http.Transport{
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     30 * time.Second,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sp)
		},
	}
	return h.v1Transport
}

// NewFircHandler creates a new FircHandler.
func NewFircHandler(op *firc.Operator) *FircHandler {
	return &FircHandler{op: op}
}

// SetSettingsStore wires the settings store so FircHandler can inspect/switch RoutingEngine.
func (h *FircHandler) SetSettingsStore(store *storage.SettingsStore) {
	h.settingsStore = store
}

// SetEventBus wires the SSE event bus.
func (h *FircHandler) SetEventBus(bus *events.Bus) {
	h.bus = bus
}

// SetSingboxConfigDir sets the source directory for one-click migration from sing-box.
func (h *FircHandler) SetSingboxConfigDir(dir string) {
	h.singboxConfigDir = dir
}

// SetOnSelectEngine sets a callback invoked when RoutingEngine is switched to or from "firc".
func (h *FircHandler) SetOnSelectEngine(fn func(selected bool)) {
	h.onSelectEngine = fn
}

// SetOnExtChanged sets a callback invoked when the AWGM FIRC extension config is updated.
func (h *FircHandler) SetOnExtChanged(fn func(ext firc.ExtConfig)) {
	h.onExtChanged = fn
}

// SetOtherEngineLifecycle wires optional callbacks to start/stop non-firc routing engines (sing-box).
func (h *FircHandler) SetOtherEngineLifecycle(startFn, stopFn func(ctx context.Context, engine string) error) {
	h.startOtherEngine = startFn
	h.stopOtherEngine = stopFn
}

// SetOtherEngineRoutingHooks wires state-capture, standby-parking, and target-activation hooks
// for transactional sing-box <-> firc engine switches.
func (h *FircHandler) SetOtherEngineRoutingHooks(
	captureFn func(ctx context.Context) (func(ctx context.Context) error, error),
	standbyFn, activateFn func(ctx context.Context, engine string) error,
) {
	h.captureOtherEngine = captureFn
	h.standbyOtherEngine = standbyFn
	h.activateOtherEngine = activateFn
}

// SetInstaller wires the on-demand FIRC installer for install/uninstall endpoints.
func (h *FircHandler) SetInstaller(inst *firc.OnDemandInstaller) {
	h.installer = inst
}

// SetOtherEngineIntegrationHooks wires sing-box status, purge, and post-restore reload hooks.
func (h *FircHandler) SetOtherEngineIntegrationHooks(hooks OtherEngineIntegrationHooks) {
	h.integrationHooks = hooks
}

func (h *FircHandler) runPostRestoreSync(ctx context.Context, restoredSingbox, restoredSettings bool) {
	if h.integrationHooks.PostRestoreSync != nil {
		_ = h.integrationHooks.PostRestoreSync(ctx, restoredSingbox, restoredSettings)
	}
}

func (h *FircHandler) publishEngineInvalidations(reason string) {
	if h.bus == nil {
		return
	}
	h.bus.PublishInvalidated(events.ResourceSingboxStatus, reason)
	h.bus.PublishInvalidated(events.ResourceSingboxTunnels, reason)
	h.bus.PublishInvalidated(events.ResourceSingboxSubscriptions, reason)
	h.bus.PublishInvalidated(events.ResourceSingboxRouterRules, reason)
	h.bus.PublishInvalidated(events.ResourceAwg3, reason)
	h.bus.PublishInvalidated(events.ResourceSettings, reason)
	h.bus.PublishInvalidated(events.ResourceSysInfo, reason)
	h.bus.PublishInvalidated(events.ResourceFirc, reason)
}

// publishFirc — подсказка ResourceFirc после мутации состояния FIRC.
// Безопасна без шины: (*events.Bus)(nil).PublishInvalidated — no-op.
func (h *FircHandler) publishFirc(reason string) {
	h.bus.PublishInvalidated(events.ResourceFirc, reason)
}

// HasInstaller reports whether an OnDemandInstaller is configured on this handler.
func (h *FircHandler) HasInstaller() bool {
	return h != nil && h.installer != nil
}

// Installer returns the wired OnDemandInstaller.
func (h *FircHandler) Installer() *firc.OnDemandInstaller {
	if h == nil {
		return nil
	}
	return h.installer
}

// Operator returns the underlying firc Operator.
func (h *FircHandler) Operator() *firc.Operator {
	return h.op
}

func (h *FircHandler) buildEngineSwitchHooks() firc.EngineSwitchHooks {
	var override firc.EngineSwitchHooks
	if h.settingsStore != nil {
		override.GetCurrentEngine = func(_ context.Context) (string, error) {
			s, err := h.settingsStore.Get()
			if err != nil {
				return "", err
			}
			if s == nil || strings.TrimSpace(s.SingboxRouter.RoutingEngine) == "" {
				return "sing-box", nil
			}
			return s.SingboxRouter.RoutingEngine, nil
		}
		override.CommitSettings = func(_ context.Context, engine string) error {
			return h.settingsStore.Update(func(s *storage.Settings) error {
				s.SingboxRouter.RoutingEngine = engine
				return nil
			})
		}
	}
	if h.captureOtherEngine != nil {
		override.CaptureState = h.captureOtherEngine
	}
	if h.startOtherEngine != nil {
		override.StartTarget = h.startOtherEngine
	}
	if h.stopOtherEngine != nil {
		override.StopEngine = h.stopOtherEngine
	}
	if h.standbyOtherEngine != nil {
		override.ParkEngineForStandby = h.standbyOtherEngine
	}
	if h.activateOtherEngine != nil {
		override.ActivateTarget = h.activateOtherEngine
	}
	if h.onSelectEngine != nil {
		override.OnSwitched = func(fircSelected bool) {
			h.onSelectEngine(fircSelected)
		}
	}
	return override
}

// SwitchEngine executes a transactional routing engine switch via Operator.SwitchEngine.
func (h *FircHandler) SwitchEngine(ctx context.Context, targetEngine string) error {
	return h.SwitchEngineWithOptions(ctx, targetEngine, firc.EngineSwitchOptions{})
}

// SwitchEngineWithOptions executes a transactional routing engine switch with explicit
// stop-vs-standby semantics.
func (h *FircHandler) SwitchEngineWithOptions(ctx context.Context, targetEngine string, opts firc.EngineSwitchOptions) error {
	if h.op == nil {
		return http.ErrNotSupported
	}
	// Публикуем и на ошибке: сорванная смена движка оставляет метку
	// восстановления, и её страница должна увидеть сразу.
	defer h.publishFirc("engine-switch")
	return h.op.SwitchEngineWithOptions(ctx, targetEngine, opts, h.buildEngineSwitchHooks())
}

// ReconcileRecovery executes a verified recovery reconciliation workflow via Operator.ReconcileRecovery.
func (h *FircHandler) ReconcileRecovery(ctx context.Context, preferredEngine string) (*firc.ReconcileReport, error) {
	if h.op == nil {
		return nil, http.ErrNotSupported
	}
	// Как и смена движка: исход любой, состояние восстановления сменилось.
	defer h.publishFirc("recovery-reconcile")
	return h.op.ReconcileRecovery(ctx, preferredEngine, h.buildEngineSwitchHooks())
}

// fircEngineExitPaths — ручки, которыми при стороннем firc с движка FIRC уходят:
// сам уход разрешён, остальное в них отказывает обработчик.
var fircEngineExitPaths = map[string]bool{
	"/api/firc/engine/switch": true,
	"/api/firc/control":       true,
	"/api/firc/settings":      true,
}

// writeForeignFirc отвечает отказом при стороннем firc или неизвестном владельце
// его файлов. false — err о другом.
func writeForeignFirc(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, firc.ErrForeignFirc):
		response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "FIRC_FOREIGN")
	case errors.Is(err, firc.ErrForeignUnknown):
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "FIRC_OWNER_UNKNOWN")
	default:
		return false
	}
	return true
}

// rejectForeign отвечает отказом, если FIRC на роутере не наш.
func (h *FircHandler) rejectForeign(w http.ResponseWriter) bool {
	return h.op != nil && writeForeignFirc(w, h.op.CheckNotForeign())
}

// refuseForeign закрывает все мутации /api/firc/*, включая прокси /v1. Чтения
// проходят: оператор при стороннем firc отдаёт пустое состояние.
func (h *FircHandler) refuseForeign(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		read := r.Method == http.MethodGet || r.Method == http.MethodHead
		if !read && !fircEngineExitPaths[r.URL.Path] && h.rejectForeign(w) {
			return
		}
		next(w, r)
	}
}

// RegisterRoutes registers all /api/firc/* endpoints on mux.
func (h *FircHandler) RegisterRoutes(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	guarded := func(next http.HandlerFunc) http.HandlerFunc { return auth(h.refuseForeign(next)) }
	mux.HandleFunc("GET /api/firc/status", guarded(h.handleStatus))
	mux.HandleFunc("POST /api/firc/control", guarded(h.handleControl))
	mux.HandleFunc("GET /api/firc/tunnels", guarded(h.handleGetTunnels))
	mux.HandleFunc("PUT /api/firc/tunnels", guarded(h.handlePutTunnels))
	mux.HandleFunc("GET /api/firc/tunnels/state", guarded(h.handleGetTunnelsState))
	mux.HandleFunc("GET /api/firc/tunnels/probe", guarded(h.handleGetTunnelProbeProgress))
	mux.HandleFunc("POST /api/firc/tunnels/probe", guarded(h.handleProbeTunnel))
	mux.HandleFunc("POST /api/firc/interfaces/probe", guarded(h.handleProbeInterfaces))
	mux.HandleFunc("POST /api/firc/tunnels/preview", guarded(h.handlePreviewTunnel))
	mux.HandleFunc("POST /api/firc/tunnels/normalize-source", guarded(h.handleNormalizeSource))
	mux.HandleFunc("GET /api/firc/groups", guarded(h.handleGetGroups))
	mux.HandleFunc("PUT /api/firc/groups", guarded(h.handlePutGroups))
	mux.HandleFunc("POST /api/firc/migrate", guarded(h.handleMigrate))
	mux.HandleFunc("GET /api/firc/migrate/state", guarded(h.handleGetMigrateState))
	mux.HandleFunc("PUT /api/firc/migrate/state", guarded(h.handlePutMigrateState))
	mux.HandleFunc("GET /api/firc/snapshots", guarded(h.handleListSnapshots))
	mux.HandleFunc("POST /api/firc/snapshots", guarded(h.handleCreateSnapshot))
	mux.HandleFunc("POST /api/firc/snapshots/restore", guarded(h.handleRestoreSnapshot))
	mux.HandleFunc("DELETE /api/firc/snapshots", guarded(h.handleDeleteSnapshot))
	mux.HandleFunc("GET /api/firc/events", guarded(h.handleGetEvents))
	mux.HandleFunc("GET /api/firc/settings", guarded(h.handleGetSettings))
	mux.HandleFunc("PUT /api/firc/settings", guarded(h.handlePutSettings))
	mux.HandleFunc("GET /api/firc/ext", guarded(h.handleGetExt))
	mux.HandleFunc("PUT /api/firc/ext", guarded(h.handlePutExt))
	mux.HandleFunc("GET /api/firc/monitor", guarded(h.handleGetMonitor))
	mux.HandleFunc("POST /api/firc/monitor/check", guarded(h.handleCheckDomain))
	mux.HandleFunc("GET /api/firc/autolist", guarded(h.handleGetAutolist))
	mux.HandleFunc("PUT /api/firc/autolist/config", guarded(h.handleConfigureAutolist))
	mux.HandleFunc("POST /api/firc/autolist/check", guarded(h.handleRecheckAutolist))
	mux.HandleFunc("POST /api/firc/autolist/probe", guarded(h.handleProbeAutolist))
	mux.HandleFunc("POST /api/firc/autolist/candidate", guarded(h.handleAutolistCandidate))
	mux.HandleFunc("POST /api/firc/autolist/address/dismiss", guarded(h.handleDismissAutolistAddress))
	mux.HandleFunc("POST /api/firc/engine/switch", guarded(h.handleSwitchEngine))
	mux.HandleFunc("POST /api/firc/engine/purge", guarded(h.handlePurgeEngine))
	mux.HandleFunc("POST /api/firc/recovery/reconcile", guarded(h.handleReconcileRecovery))
	mux.HandleFunc("POST /api/firc/recovery/clear", guarded(h.handleClearRecovery))
	mux.HandleFunc("/api/firc/v1/", guarded(h.handleV1Proxy))
	mux.HandleFunc("GET /api/firc/install-status", guarded(h.handleInstallStatus))
	mux.HandleFunc("POST /api/firc/install", guarded(h.handleInstall))
	mux.HandleFunc("POST /api/firc/upgrade", guarded(h.handleUpgrade))
	mux.HandleFunc("POST /api/firc/uninstall", guarded(h.handleUninstall))
}

func (h *FircHandler) isSelected() bool {
	if h.settingsStore == nil {
		return false
	}
	s, err := h.settingsStore.Get()
	if err != nil || s == nil {
		return false
	}
	return strings.EqualFold(s.SingboxRouter.RoutingEngine, "firc")
}

// isExplicitlyUnselected reports whether a settingsStore is wired and the active
// routing engine is not "firc". Unit tests constructed with NewFircHandler(op)
// without a settingsStore remain unrestricted.
func (h *FircHandler) isExplicitlyUnselected() bool {
	return h.settingsStore != nil && !h.isSelected()
}

func (h *FircHandler) rejectNotSelected(w http.ResponseWriter) {
	response.ErrorWithStatus(w, http.StatusConflict, "AWGM Route engine is not selected", "FIRC_NOT_SELECTED")
}

// rejectFircRuntimeMissing — смена ядра на AWGM Route без рантайма. Без проверки
// смена доходила до запуска кандидата и падала на «fircd binary missing»; как и
// миграция с восстановлением снимка, отказываем до любых изменений.
func (h *FircHandler) rejectFircRuntimeMissing(w http.ResponseWriter, target string) bool {
	if !strings.EqualFold(strings.TrimSpace(target), "firc") {
		return false
	}
	if writeForeignFirc(w, h.op.CheckNotForeign()) {
		return true
	}
	if err := h.op.CheckRuntimeInstalledForSwitch(); err != nil {
		response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "FIRC_RUNTIME_NOT_INSTALLED")
		return true
	}
	return false
}

func offlineAutolistStatus() *firc.AutolistStatus {
	return &firc.AutolistStatus{
		Enabled:    false,
		Running:    false,
		Mode:       "candidates",
		Addresses:  []firc.AutolistAddress{},
		Candidates: []firc.AutolistCandidate{},
		Probes: firc.AutolistProbes{
			Results: []firc.AutolistProbeResult{},
		},
	}
}

func (h *FircHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	if h.isExplicitlyUnselected() {
		response.Success(w, h.op.GetOfflineStatus(false))
		return
	}
	st := h.op.GetStatus(r.Context(), h.isSelected())
	response.Success(w, st)
}

// @Summary Get FIRC runtime installation status
// @Tags firc
// @Produce json
// @Security CookieAuth
// @Success 200 {object} APIEnvelope
// @Failure 503 {object} APIErrorEnvelope
// @Router /firc/install-status [get]
func (h *FircHandler) handleInstallStatus(w http.ResponseWriter, r *http.Request) {
	if h.installer == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc installer not configured", "UNAVAILABLE")
		return
	}
	st := h.installer.GetInstallStatus(r.Context(), h.op, h.isSelected())
	response.Success(w, st)
}

type fircUninstallRequest struct {
	PurgeConfigs bool `json:"purgeConfigs"`
}

func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil || r.Body == http.NoBody {
		return true
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		response.ErrorWithStatus(w, http.StatusBadRequest, "unexpected trailing request data", "BAD_REQUEST")
		return false
	}
	return true
}

// @Summary Install the verified FIRC runtime package
// @Description Accepts an asynchronous operation. Poll install-status for completion.
// @Tags firc
// @Produce json
// @Security CookieAuth
// @Success 202 {object} APIEnvelope
// @Failure 409 {object} APIErrorEnvelope
// @Failure 503 {object} APIErrorEnvelope
// @Router /firc/install [post]
func (h *FircHandler) handleInstall(w http.ResponseWriter, r *http.Request) {
	if h.installer == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc installer not configured", "UNAVAILABLE")
		return
	}

	h.installMu.Lock()
	st := h.installer.GetInstallStatus(r.Context(), h.op, h.isSelected())
	if !st.Available {
		h.installMu.Unlock()
		reason := st.UnavailableReason
		if reason == "" {
			reason = firc.ErrReleaseUnavailable.Error()
		}
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, reason, "RELEASE_UNAVAILABLE")
		return
	}
	if st.Busy {
		h.installMu.Unlock()
		response.ErrorWithStatus(w, http.StatusConflict, firc.ErrInstallBusy.Error(), "INSTALL_BUSY")
		return
	}
	if st.State == firc.InstallStateRunning || (h.op != nil && h.op.IsRunning(r.Context())) {
		h.installMu.Unlock()
		response.ErrorWithStatus(w, http.StatusConflict, firc.ErrFircRunning.Error(), "FIRC_RUNNING")
		return
	}
	canRepairWhileActive := st.State == firc.InstallStateNotInstalled || st.State == firc.InstallStateRecoveryNeeded
	if h.isSelected() && !canRepairWhileActive {
		h.installMu.Unlock()
		response.ErrorWithStatus(w, http.StatusConflict, firc.ErrInstallActiveCore.Error(), "FIRC_ACTIVE")
		return
	}
	h.installMu.Unlock()

	if err := h.installer.StartInstallWithOperator(h.op); err != nil {
		response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "INSTALL_BUSY")
		return
	}

	h.publishFirc("install-started")
	response.Accepted(w, map[string]string{"status": "install_started"})
}

// @Summary Upgrade the installed FIRC runtime package
// @Description Downloads and verifies the target runtime IPK before restarting an active FIRC daemon and rolling back automatically on failure.
// @Tags firc
// @Produce json
// @Security CookieAuth
// @Success 202 {object} APIEnvelope
// @Failure 409 {object} APIErrorEnvelope
// @Failure 503 {object} APIErrorEnvelope
// @Router /firc/upgrade [post]
func (h *FircHandler) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	if h.installer == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc installer not configured", "UNAVAILABLE")
		return
	}

	h.installMu.Lock()
	st := h.installer.GetInstallStatus(r.Context(), h.op, h.isSelected())
	if !st.Available {
		h.installMu.Unlock()
		reason := st.UnavailableReason
		if reason == "" {
			reason = firc.ErrReleaseUnavailable.Error()
		}
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, reason, "RELEASE_UNAVAILABLE")
		return
	}
	if st.Busy {
		h.installMu.Unlock()
		response.ErrorWithStatus(w, http.StatusConflict, firc.ErrInstallBusy.Error(), "INSTALL_BUSY")
		return
	}
	if st.State == firc.InstallStateNotInstalled || st.InstalledVersion == "" {
		h.installMu.Unlock()
		response.ErrorWithStatus(w, http.StatusConflict, firc.ErrRuntimeNotInstalled.Error(), "RUNTIME_NOT_INSTALLED")
		return
	}
	if st.State == firc.InstallStateRecoveryNeeded {
		h.installMu.Unlock()
		response.ErrorWithStatus(w, http.StatusConflict, firc.ErrUpgradeRequiresRepair.Error(), "RECOVERY_REQUIRED")
		return
	}
	if !st.UpdateAvailable {
		h.installMu.Unlock()
		response.ErrorWithStatus(w, http.StatusConflict, firc.ErrAlreadyUpToDate.Error(), "ALREADY_UP_TO_DATE")
		return
	}
	h.installMu.Unlock()

	if err := h.installer.StartUpgrade(h.op); err != nil {
		response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "INSTALL_BUSY")
		return
	}

	h.publishFirc("upgrade-started")
	response.Accepted(w, map[string]string{"status": "upgrade_started"})
}

// @Summary Remove the FIRC runtime while preserving configuration
// @Description Accepts an asynchronous operation. Configuration purge is not supported.
// @Tags firc
// @Accept json
// @Produce json
// @Security CookieAuth
// @Param request body fircUninstallRequest false "Removal options; purgeConfigs must be false"
// @Success 202 {object} APIEnvelope
// @Failure 400 {object} APIErrorEnvelope
// @Failure 409 {object} APIErrorEnvelope
// @Failure 503 {object} APIErrorEnvelope
// @Router /firc/uninstall [post]
func (h *FircHandler) handleUninstall(w http.ResponseWriter, r *http.Request) {
	if h.installer == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc installer not configured", "UNAVAILABLE")
		return
	}

	var req fircUninstallRequest
	if !decodeOptionalJSON(w, r, &req) {
		return
	}
	if req.PurgeConfigs {
		response.ErrorWithStatus(w, http.StatusBadRequest, "Runtime removal preserves configuration; full data purge is not supported", "PURGE_UNSUPPORTED")
		return
	}

	h.installMu.Lock()
	st := h.installer.GetInstallStatus(r.Context(), h.op, h.isSelected())
	if st.Busy {
		h.installMu.Unlock()
		response.ErrorWithStatus(w, http.StatusConflict, firc.ErrInstallBusy.Error(), "INSTALL_BUSY")
		return
	}

	// Validate before launching goroutine
	if h.isSelected() {
		h.installMu.Unlock()
		response.ErrorWithStatus(w, http.StatusConflict, firc.ErrFircActive.Error(), "FIRC_ACTIVE")
		return
	}
	if h.op != nil && h.op.IsRunning(r.Context()) {
		h.installMu.Unlock()
		response.ErrorWithStatus(w, http.StatusConflict, firc.ErrFircRunning.Error(), "FIRC_RUNNING")
		return
	}
	if len(st.DependentServices) > 0 {
		h.installMu.Unlock()
		response.ErrorWithStatus(w, http.StatusConflict, fmt.Sprintf("cannot uninstall: services depend on AWGM Route: %s", strings.Join(st.DependentServices, ", ")), "FIRC_DEPENDENCIES")
		return
	}
	h.installMu.Unlock()

	purge := req.PurgeConfigs
	if err := h.installer.StartUninstall(h.op, h.isSelected(), purge); err != nil {
		response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "INSTALL_BUSY")
		return
	}

	h.publishFirc("uninstall-started")
	response.Accepted(w, map[string]string{"status": "uninstall_started"})
}

type fircControlRequest struct {
	Action string `json:"action"` // "start" | "stop" | "reload" | "select"
	Engine string `json:"engine,omitempty"`
}

func (h *FircHandler) handleControl(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	var req fircControlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	// Остановку выбранного движка проверяет оператор: при неизвестном владельце
	// свой рантайм останавливается как обычно.
	stopOwn := action == "stop" && !h.isExplicitlyUnselected()
	if action != "select" && !stopOwn && h.rejectForeign(w) {
		return
	}
	switch action {
	case "start":
		if h.isExplicitlyUnselected() {
			h.rejectNotSelected(w)
			return
		}
		if err := h.op.StartByUser(r.Context()); err != nil {
			response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "START_FAILED")
			return
		}
		h.publishFirc("control-start")
	case "stop":
		if err := h.op.StopByUser(r.Context()); err != nil {
			if writeForeignFirc(w, err) {
				return
			}
			response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "STOP_FAILED")
			return
		}
		if h.isExplicitlyUnselected() {
			h.op.CleanupPoolRejectBarrier(r.Context())
		}
		h.publishFirc("control-stop")
	case "reload":
		if h.op.IsRecoveryRequired() {
			if _, err := h.ReconcileRecovery(r.Context(), "firc"); err != nil {
				response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "RECONCILE_RECOVERY_FAILED")
				return
			}
		} else {
			if h.isExplicitlyUnselected() {
				h.rejectNotSelected(w)
				return
			}
			if err := h.op.Reload(); err != nil {
				response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "RELOAD_FAILED")
				return
			}
			h.publishFirc("control-reload")
		}
	case "clear_recovery", "reconcile_recovery":
		if _, err := h.ReconcileRecovery(r.Context(), strings.TrimSpace(req.Engine)); err != nil {
			response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "RECONCILE_RECOVERY_FAILED")
			return
		}
	case "select":
		target := strings.TrimSpace(req.Engine)
		if target == "" {
			target = "firc"
		}
		if err := h.SwitchEngineWithOptions(firc.WithTurnOff(r.Context()), target, firc.EngineSwitchOptions{
			AuthorizeDisabledStart: strings.EqualFold(target, "firc"),
		}); err != nil {
			if writeForeignFirc(w, err) {
				return
			}
			response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "SWITCH_ENGINE_FAILED")
			return
		}
	default:
		response.ErrorWithStatus(w, http.StatusBadRequest, "unsupported action: "+action, "BAD_REQUEST")
		return
	}
	// select/reconcile публикуют сами (SwitchEngineWithOptions,
	// ReconcileRecovery) — второй подсказки здесь не шлём.
	if h.isExplicitlyUnselected() {
		response.Success(w, h.op.GetOfflineStatus(false))
		return
	}
	response.Success(w, h.op.GetStatus(r.Context(), h.isSelected()))
}

func (h *FircHandler) handleGetTunnels(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	if !h.isExplicitlyUnselected() && h.op.IsRunning(r.Context()) {
		tunnels, err := h.op.Client().GetTunnels(r.Context())
		if err == nil {
			response.Success(w, tunnels)
			return
		}
	}
	tunnels, err := h.op.LoadTunnelsFile()
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "LOAD_TUNNELS_FAILED")
		return
	}
	if tunnels == nil {
		tunnels = []firc.Tunnel{}
	}
	response.Success(w, tunnels)
}

func (h *FircHandler) handlePutTunnels(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	var payload struct {
		Tunnels []firc.Tunnel `json:"tunnels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	var (
		putResp *firc.PutTunnelsResponse
		err     error
	)
	if h.isExplicitlyUnselected() {
		putResp, err = h.op.StageTunnelsOffline(payload.Tunnels, true)
	} else {
		putResp, err = h.op.ApplyTunnels(r.Context(), payload.Tunnels, true)
	}
	if err != nil {
		if errors.Is(err, firc.ErrRecoveryRequired) {
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "RECOVERY_REQUIRED")
			return
		}
		var filterErr *subscription.FilterError
		if errors.As(err, &filterErr) {
			response.ErrorWithStatus(w, http.StatusBadRequest, filterErr.Error(), "INVALID_FILTER")
			return
		}
		var valErr *firc.TunnelValidationError
		if errors.As(err, &valErr) {
			response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "NORMALIZE_FAILED")
			return
		}
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "APPLY_TUNNELS_FAILED")
		return
	}
	response.Success(w, putResp)
}

func (h *FircHandler) handleGetTunnelsState(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	if h.isExplicitlyUnselected() || !h.op.IsRunning(r.Context()) {
		response.Success(w, []firc.TunnelState{})
		return
	}
	states, err := h.op.Client().GetTunnelsState(r.Context())
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "TUNNELS_STATE_FAILED")
		return
	}
	response.Success(w, states)
}

func (h *FircHandler) happKeysPath() string {
	var cfgDir string
	if h.op != nil {
		cfgDir = h.op.ConfigDir()
	}
	return firc.ResolveHappKeysPath("", h.singboxConfigDir, cfgDir)
}

func (h *FircHandler) handleProbeTunnel(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	var wrapped struct {
		ID     string       `json:"id,omitempty"`
		Tunnel *firc.Tunnel `json:"tunnel,omitempty"`
		Keys   []string     `json:"keys,omitempty"`
	}
	if err := json.Unmarshal(rawBody, &wrapped); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	probeID := wrapped.ID
	var draft *firc.Tunnel
	if wrapped.Tunnel != nil {
		if err := firc.NormalizeTunnelSources(wrapped.Tunnel, h.happKeysPath()); err != nil {
			response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "NORMALIZE_FAILED")
			return
		}
		draft = wrapped.Tunnel
	} else {
		var tun firc.Tunnel
		if err := json.Unmarshal(rawBody, &tun); err != nil {
			response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
			return
		}
		if err := firc.NormalizeTunnelSources(&tun, h.happKeysPath()); err != nil {
			response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "NORMALIZE_FAILED")
			return
		}
		if probeID == "" {
			probeID = tun.ID
		}
		draft = &tun
	}
	if err := firc.ValidateTunnelFilters(draft); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_FILTER")
		return
	}
	if h.isExplicitlyUnselected() {
		h.rejectNotSelected(w)
		return
	}
	if !h.op.IsRunning(r.Context()) {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "fircd is not running", "NOT_RUNNING")
		return
	}
	res, err := h.op.Client().ProbeTunnelKeys(r.Context(), probeID, draft, wrapped.Keys)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "PROBE_FAILED")
		return
	}
	response.Success(w, res)
}

func (h *FircHandler) handleGetTunnelProbeProgress(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	if h.isExplicitlyUnselected() || !h.op.IsRunning(r.Context()) {
		response.Success(w, &firc.ProbeProgressResponse{
			Total: 0,
			Nodes: []firc.ProbeNodeResult{},
		})
		return
	}
	res, err := h.op.Client().GetTunnelProbeProgress(r.Context())
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "PROBE_PROGRESS_FAILED")
		return
	}
	if res.Nodes == nil {
		res.Nodes = []firc.ProbeNodeResult{}
	}
	response.Success(w, res)
}

var fircProbeIfaceNameRe = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,32}$`)

type fircInterfacesProbeRequest struct {
	Interfaces []string `json:"interfaces"`
	URL        string   `json:"url,omitempty"`
}

type fircInterfaceProbeResult struct {
	Interface string `json:"interface"`
	Ok        bool   `json:"ok"`
	LatencyMs int    `json:"latencyMs"`
	HTTPCode  int    `json:"httpCode,omitempty"`
	Error     string `json:"error,omitempty"`
}

type fircInterfacesProbeResponse struct {
	URL       string                              `json:"url"`
	CheckedAt int64                               `json:"checkedAt"`
	Results   map[string]fircInterfaceProbeResult `json:"results"`
}

func computeFircInterfaceProbeLatencyMs(metrics httpclient.Metrics) int {
	totalMs := httpclient.SecToMs(metrics.TimeTotal)
	if metrics.TimeNameLookup > 0 && metrics.TimeTotal > metrics.TimeNameLookup {
		dnsMs := httpclient.SecToMs(metrics.TimeNameLookup)
		if totalMs > dnsMs {
			totalMs -= dnsMs
		}
	}
	if httpprobe.SuccessCode(metrics.HTTPCode) && totalMs <= 0 {
		totalMs = 1
	}
	return totalMs
}

func probeFircInterfaceHTTP(ctx context.Context, ifaceName, checkURL string) fircInterfaceProbeResult {
	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	res, err := httpclient.DefaultClient.Do(probeCtx, httpclient.CallConfig{
		URL:            checkURL,
		Interface:      ifaceName,
		ConnectTimeout: 3 * time.Second,
		MaxTime:        4 * time.Second,
		DiscardBody:    true,
	})
	if err != nil {
		return fircInterfaceProbeResult{
			Interface: ifaceName,
			Ok:        false,
			LatencyMs: 0,
			Error:     err.Error(),
		}
	}
	if !httpprobe.SuccessCode(res.Metrics.HTTPCode) {
		return fircInterfaceProbeResult{
			Interface: ifaceName,
			Ok:        false,
			LatencyMs: 0,
			HTTPCode:  res.Metrics.HTTPCode,
			Error:     fmt.Sprintf("HTTP %d", res.Metrics.HTTPCode),
		}
	}
	return fircInterfaceProbeResult{
		Interface: ifaceName,
		Ok:        true,
		LatencyMs: computeFircInterfaceProbeLatencyMs(res.Metrics),
		HTTPCode:  res.Metrics.HTTPCode,
	}
}

func isValidProbeHTTPURL(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	u, err := url.Parse(trimmed)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", false
	}
	return u.String(), true
}

// resolveProbeCheckURL выбирает адрес пробы: из запроса, app.check.url работающего
// fircd (им же он проверяет интерфейсы), общая настройка, умолчание.
func (h *FircHandler) resolveProbeCheckURL(ctx context.Context, reqURL string) (string, error) {
	if strings.TrimSpace(reqURL) != "" {
		normalized, ok := isValidProbeHTTPURL(reqURL)
		if !ok {
			return "", errors.New("invalid probe url")
		}
		return normalized, nil
	}

	if h.op != nil {
		sCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
		defer cancel()
		if h.op.IsRunning(sCtx) {
			if st, err := h.op.Client().GetSettings(sCtx); err == nil && st != nil {
				appURL, _ := st.Settings["app.check.url"].(string)
				if normalized, ok := isValidProbeHTTPURL(appURL); ok {
					return normalized, nil
				}
			}
		}
	}
	if h.settingsStore != nil {
		if s, err := h.settingsStore.Get(); err == nil && s != nil {
			if settingsURL := strings.TrimSpace(s.ConnectivityCheckURL); settingsURL != "" {
				return settingsURL, nil
			}
		}
	}
	return storage.DefaultConnectivityCheckURL, nil
}

func (h *FircHandler) handleProbeInterfaces(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	if h.isExplicitlyUnselected() {
		h.rejectNotSelected(w)
		return
	}

	var req fircInterfacesProbeRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		response.ErrorWithStatus(w, http.StatusBadRequest, "unexpected trailing request data", "BAD_REQUEST")
		return
	}

	seen := make(map[string]struct{}, len(req.Interfaces))
	uniqueIfaces := make([]string, 0, len(req.Interfaces))
	for _, raw := range req.Interfaces {
		iface := strings.TrimSpace(raw)
		if iface == "" {
			continue
		}
		if iface == "blackhole" || iface == "block" || iface == firc.DNSOnlyInterface || !fircProbeIfaceNameRe.MatchString(iface) {
			response.ErrorWithStatus(w, http.StatusBadRequest, fmt.Sprintf("invalid interface %q", iface), "BAD_REQUEST")
			return
		}
		if _, exists := seen[iface]; exists {
			continue
		}
		seen[iface] = struct{}{}
		uniqueIfaces = append(uniqueIfaces, iface)
	}
	if len(uniqueIfaces) == 0 || len(uniqueIfaces) > 16 {
		response.ErrorWithStatus(w, http.StatusBadRequest, "interfaces must contain 1 to 16 unique interface names", "BAD_REQUEST")
		return
	}

	checkURL, err := h.resolveProbeCheckURL(r.Context(), req.URL)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "BAD_REQUEST")
		return
	}

	probeFn := h.probeInterfaceFn
	if probeFn == nil {
		probeFn = probeFircInterfaceHTTP
	}

	probeCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	const maxConcurrent = 4
	sem := make(chan struct{}, maxConcurrent)
	results := make(map[string]fircInterfaceProbeResult, len(uniqueIfaces))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, iface := range uniqueIfaces {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-probeCtx.Done():
				mu.Lock()
				results[name] = fircInterfaceProbeResult{
					Interface: name,
					Ok:        false,
					LatencyMs: 0,
					Error:     probeCtx.Err().Error(),
				}
				mu.Unlock()
				return
			}
			res := probeFn(probeCtx, name, checkURL)
			if res.Interface == "" {
				res.Interface = name
			}
			mu.Lock()
			results[name] = res
			mu.Unlock()
		}(iface)
	}
	wg.Wait()

	response.Success(w, fircInterfacesProbeResponse{
		URL:       checkURL,
		CheckedAt: time.Now().Unix(),
		Results:   results,
	})
}

func (h *FircHandler) handlePreviewTunnel(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	var tun firc.Tunnel
	if err := json.NewDecoder(r.Body).Decode(&tun); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	if err := firc.NormalizeTunnelSources(&tun, h.happKeysPath()); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "NORMALIZE_FAILED")
		return
	}
	if err := firc.ValidateTunnelFilters(&tun); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_FILTER")
		return
	}
	if h.isExplicitlyUnselected() || !h.op.IsRunning(r.Context()) {
		response.Success(w, &firc.PreviewResponse{})
		return
	}
	res, err := h.op.Client().PreviewTunnel(r.Context(), tun)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "PREVIEW_FAILED")
		return
	}
	response.Success(w, res)
}

func (h *FircHandler) handleNormalizeSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind     string            `json:"kind"`
		Name     string            `json:"name"`
		Input    string            `json:"input"`
		Interval *int              `json:"interval"`
		Headers  map[string]string `json:"headers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	sources, err := firc.ParseInlineSourcesForFirc(req.Input, req.Name, h.happKeysPath())
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "NORMALIZE_FAILED")
		return
	}
	for i := range sources {
		if sources[i].Kind == "subscription" {
			if req.Interval != nil {
				sources[i].Interval = firc.NormalizeSubscriptionInterval(*req.Interval)
			}
			if len(req.Headers) > 0 {
				sources[i].Headers = req.Headers
			}
		}
	}
	response.Success(w, struct {
		Sources []firc.TunnelSource `json:"sources"`
	}{
		Sources: sources,
	})
}

func (h *FircHandler) handleGetGroups(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	if !h.isExplicitlyUnselected() && h.op.IsRunning(r.Context()) {
		groups, err := h.op.Client().GetGroups(r.Context(), true)
		if err == nil {
			response.Success(w, groups)
			return
		}
	}
	groups, err := h.op.LoadGroupsFile()
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "LOAD_GROUPS_FAILED")
		return
	}
	if groups == nil {
		groups = []firc.Group{}
	}
	response.Success(w, groups)
}

func (h *FircHandler) handlePutGroups(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	var payload struct {
		Groups     []firc.Group      `json:"groups"`
		GroupLinks map[string]string `json:"groupLinks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	var (
		updated []firc.Group
		err     error
	)
	if h.isExplicitlyUnselected() {
		updated, err = h.op.StageGroupsOfflineWithLinks(payload.Groups, payload.GroupLinks)
	} else {
		updated, err = h.op.ApplyGroupsWithLinks(r.Context(), payload.Groups, payload.GroupLinks)
	}
	if err != nil {
		if errors.Is(err, firc.ErrRecoveryRequired) {
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "RECOVERY_REQUIRED")
			return
		}
		var invalid *firc.GroupValidationError
		if errors.As(err, &invalid) {
			response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_GROUPS")
			return
		}
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "APPLY_GROUPS_FAILED")
		return
	}
	response.Success(w, updated)
}

func (h *FircHandler) handleMigrate(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	if h.singboxConfigDir == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "sing-box config directory not configured", "BAD_REQUEST")
		return
	}
	dryRun := r.URL.Query().Get("dry_run") == "1" || strings.EqualFold(r.URL.Query().Get("dry_run"), "true")
	var reqBody struct {
		DryRun      bool `json:"dry_run"`
		DryRunCamel bool `json:"dryRun"`
		firc.MigrationOptions
	}
	if rs := strings.TrimSpace(r.URL.Query().Get("router_source")); rs != "" {
		reqBody.RouterSource = rs
	}
	var opts *firc.MigrationOptions
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil && err != io.EOF {
			response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
			return
		}
		if reqBody.DryRun || reqBody.DryRunCamel {
			dryRun = true
		}
	}
	if reqBody.RouterSource != "" ||
		reqBody.IncludeTunnels != nil || reqBody.IncludeSubscriptions != nil ||
		reqBody.IncludeGroups != nil || reqBody.IncludeClientRoutes != nil ||
		reqBody.IncludeFakeIPConfig != nil || reqBody.SelectedTunnelIDs != nil ||
		reqBody.SelectedGroupIDs != nil || reqBody.PostSingboxAction != "" {
		opts = &reqBody.MigrationOptions
	}
	if opts != nil {
		postAction := strings.ToLower(strings.TrimSpace(opts.PostSingboxAction))
		if !dryRun && (postAction == "switch_standby" || postAction == "switch_and_stop" || postAction == "switch_and_uninstall") {
			if rerr := h.op.CheckRuntimeInstalledForSwitch(); rerr != nil {
				response.ErrorWithStatus(w, http.StatusConflict, rerr.Error(), "FIRC_RUNTIME_NOT_INSTALLED")
				return
			}
		}
	}
	var (
		res *firc.MigrationResult
		err error
	)
	if dryRun {
		res, err = h.op.PreviewFromSingboxWithOptions(h.singboxConfigDir, opts)
	} else {
		res, err = h.op.MigrateFromSingboxWithOptions(r.Context(), h.singboxConfigDir, opts)
		if err == nil && res != nil {
			res.ConfigMigrated = true
			res.FircStarted = h.op.IsRunning(r.Context())
			if opts != nil {
				switch strings.ToLower(strings.TrimSpace(opts.PostSingboxAction)) {
				case "switch_standby":
					if serr := h.SwitchEngineWithOptions(r.Context(), "firc", firc.EngineSwitchOptions{
						SingboxStandby:         true,
						AuthorizeDisabledStart: true,
					}); serr != nil {
						res.PostActionError = serr.Error()
						res.SwitchError = serr.Error()
						res.FircStarted = h.op.IsRunning(r.Context())
						if !res.FircStarted || strings.Contains(serr.Error(), "firc candidate start failed") {
							res.StartError = serr.Error()
						}
						res.EngineSwitched = false
						res.RecoveryRequired = h.op.IsRecoveryRequired()
					} else {
						res.FircStarted = true
						res.EngineSwitched = true
						res.PostActionApplied = "switch_standby"
						h.op.RecordMigrationPostAction("switch_standby")
					}
				case "switch_and_stop":
					if serr := h.SwitchEngineWithOptions(r.Context(), "firc", firc.EngineSwitchOptions{
						SingboxStandby:         false,
						AuthorizeDisabledStart: true,
					}); serr != nil {
						res.PostActionError = serr.Error()
						res.SwitchError = serr.Error()
						res.FircStarted = h.op.IsRunning(r.Context())
						if !res.FircStarted || strings.Contains(serr.Error(), "firc candidate start failed") {
							res.StartError = serr.Error()
						}
						res.EngineSwitched = false
						res.RecoveryRequired = h.op.IsRecoveryRequired()
					} else {
						res.FircStarted = true
						res.EngineSwitched = true
						res.PostActionApplied = "switch_and_stop"
						h.op.RecordMigrationPostAction("switch_and_stop")
					}
				case "switch_and_uninstall":
					if serr := h.SwitchEngineWithOptions(r.Context(), "firc", firc.EngineSwitchOptions{
						SingboxStandby:         false,
						AuthorizeDisabledStart: true,
					}); serr != nil {
						res.PostActionError = serr.Error()
						res.SwitchError = serr.Error()
						res.FircStarted = h.op.IsRunning(r.Context())
						if !res.FircStarted || strings.Contains(serr.Error(), "firc candidate start failed") {
							res.StartError = serr.Error()
						}
						res.EngineSwitched = false
						res.RecoveryRequired = h.op.IsRecoveryRequired()
					} else {
						res.FircStarted = true
						res.EngineSwitched = true
						purgeRes, perr := h.op.PurgeSingboxAndTunnels(
							r.Context(),
							h.singboxConfigDir,
							"firc",
							res.SnapshotCreated,
							firc.EnginePurgeHooks{PurgeSingbox: h.integrationHooks.PurgeSingbox},
						)
						if perr != nil {
							res.PostActionError = perr.Error()
							res.RecoveryRequired = h.op.IsRecoveryRequired()
						} else {
							res.RemovedEngine = purgeRes
							res.PostActionApplied = "switch_and_uninstall"
							h.op.RecordMigrationPostAction("switch_and_uninstall")
							h.publishEngineInvalidations("purge-singbox")
						}
					}
				case "keep_current":
					res.PostActionApplied = "keep_current"
					res.EngineSwitched = false
					h.op.RecordMigrationPostAction("keep_current")
				}
			}
		}
	}
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "MIGRATE_FAILED")
		return
	}
	response.Success(w, res)
}

func (h *FircHandler) handleGetEvents(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	q := r.URL.Query()
	since, _ := strconv.ParseUint(q.Get("since"), 10, 64)
	if h.isExplicitlyUnselected() || !h.op.IsRunning(r.Context()) {
		response.Success(w, &firc.EventsResponse{})
		return
	}
	res, err := h.op.Client().GetAllEvents(r.Context(), since)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "EVENTS_FAILED")
		return
	}
	if len(res.Events) > 1024 {
		res.Events = res.Events[len(res.Events)-1024:]
	}
	response.Success(w, res)
}

func (h *FircHandler) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	if h.isExplicitlyUnselected() || !h.op.IsRunning(r.Context()) {
		response.Success(w, map[string]interface{}{
			"selected": h.isSelected(),
		})
		return
	}
	res, err := h.op.Client().GetSettings(r.Context())
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "SETTINGS_FAILED")
		return
	}
	response.Success(w, res)
}

func (h *FircHandler) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	var payload map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	if eng, ok := payload["routingEngine"].(string); ok && strings.TrimSpace(eng) != "" {
		delete(payload, "routingEngine")
		if h.rejectFircRuntimeMissing(w, eng) {
			return
		}
		if err := h.SwitchEngineWithOptions(firc.WithTurnOff(r.Context()), eng, firc.EngineSwitchOptions{
			AuthorizeDisabledStart: strings.EqualFold(strings.TrimSpace(eng), "firc"),
		}); err != nil {
			if writeForeignFirc(w, err) {
				return
			}
			response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "SWITCH_ENGINE_FAILED")
			return
		}
	} else if sel, ok := payload["selected"].(bool); ok {
		delete(payload, "selected")
		targetEngine := "sing-box"
		if sel {
			targetEngine = "firc"
		}
		if h.rejectFircRuntimeMissing(w, targetEngine) {
			return
		}
		if err := h.SwitchEngineWithOptions(firc.WithTurnOff(r.Context()), targetEngine, firc.EngineSwitchOptions{
			AuthorizeDisabledStart: sel,
		}); err != nil {
			if writeForeignFirc(w, err) {
				return
			}
			response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "SWITCH_ENGINE_FAILED")
			return
		}
	}
	if len(payload) == 0 {
		response.Success(w, map[string]interface{}{
			"selected": h.isSelected(),
		})
		return
	}
	if h.rejectForeign(w) {
		return
	}
	if h.isExplicitlyUnselected() {
		h.rejectNotSelected(w)
		return
	}
	if !h.op.IsRunning(r.Context()) {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "fircd is not running", "NOT_RUNNING")
		return
	}
	res, err := h.op.Client().PutSettings(r.Context(), payload)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "SETTINGS_UPDATE_FAILED")
		return
	}
	h.publishFirc("settings-updated")
	response.Success(w, res)
}

func (h *FircHandler) handleSwitchEngine(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	var req struct {
		Engine         string `json:"engine"`
		RoutingEngine  string `json:"routingEngine"`
		SingboxStandby bool   `json:"singboxStandby,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	target := strings.TrimSpace(req.Engine)
	if target == "" {
		target = strings.TrimSpace(req.RoutingEngine)
	}
	if target == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "engine is required", "BAD_REQUEST")
		return
	}
	if h.rejectFircRuntimeMissing(w, target) {
		return
	}
	if err := h.SwitchEngineWithOptions(firc.WithTurnOff(r.Context()), target, firc.EngineSwitchOptions{
		SingboxStandby:         req.SingboxStandby,
		AuthorizeDisabledStart: strings.EqualFold(target, "firc"),
	}); err != nil {
		if errors.Is(err, uplink.ErrConflict) {
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "UPLINK_POLICY_CONFLICT")
			return
		}
		if writeForeignFirc(w, err) {
			return
		}
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "SWITCH_ENGINE_FAILED")
		return
	}
	response.Success(w, map[string]interface{}{
		"routingEngine": target,
		"selected":      h.isSelected(),
	})
}

type fircRecoveryRequest struct {
	Engine          string `json:"engine,omitempty"`
	TargetEngine    string `json:"targetEngine,omitempty"`
	ForceAdminClear bool   `json:"forceAdminClear,omitempty"`
	Confirm         string `json:"confirm,omitempty"`
	TransitionID    string `json:"transitionId,omitempty"`
	Actor           string `json:"actor,omitempty"`
}

func isLoopbackBreakGlassRequest(r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("X-Forwarded-For")) != "" || strings.TrimSpace(r.Header.Get("X-Real-IP")) != "" {
		return false
	}
	host := strings.TrimSpace(r.RemoteAddr)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (h *FircHandler) handleReconcileRecovery(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	var req fircRecoveryRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
			return
		}
	}
	target := strings.TrimSpace(req.TargetEngine)
	if target == "" {
		target = strings.TrimSpace(req.Engine)
	}
	report, err := h.ReconcileRecovery(r.Context(), target)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "RECONCILE_RECOVERY_FAILED")
		return
	}
	response.Success(w, map[string]interface{}{
		"recoveryRequired": false,
		"reconcile":        report,
	})
}

func (h *FircHandler) handleClearRecovery(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	var req fircRecoveryRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
			return
		}
	}
	if req.ForceAdminClear {
		if !isLoopbackBreakGlassRequest(r) {
			response.ErrorWithStatus(w, http.StatusForbidden, "break-glass forceAdminClear is restricted to localhost/CLI loopback requests", "BREAK_GLASS_LOCALHOST_ONLY")
			return
		}
		actor := strings.TrimSpace(req.Actor)
		if actor == "" {
			actor = strings.TrimSpace(r.Header.Get("X-Firc-Break-Glass-Actor"))
		}
		transitionID := strings.TrimSpace(req.TransitionID)
		if transitionID == "" {
			transitionID = strings.TrimSpace(r.Header.Get("X-Firc-Transition-Id"))
		}
		if err := h.op.ForceClearRecoveryAdmin(req.Confirm, firc.BreakGlassOptions{
			TransitionID: transitionID,
			Actor:        actor,
			Source:       r.RemoteAddr,
		}); err != nil {
			if errors.Is(err, firc.ErrRecoveryPersistFailed) {
				response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "RECOVERY_PERSIST_FAILED")
				return
			}
			response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "ADMIN_CONFIRMATION_REQUIRED")
			return
		}
		response.Success(w, map[string]interface{}{
			"recoveryRequired": false,
			"forcedAdminClear": true,
			"actor":            actor,
			"transitionId":     transitionID,
			"warning":          "UNVERIFIED BREAK-GLASS CLEAR: runtime/disk consistency was bypassed; run /api/firc/recovery/reconcile as soon as possible",
		})
		return
	}
	target := strings.TrimSpace(req.TargetEngine)
	if target == "" {
		target = strings.TrimSpace(req.Engine)
	}
	report, err := h.ReconcileRecovery(r.Context(), target)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "RECONCILE_RECOVERY_FAILED")
		return
	}
	response.Success(w, map[string]interface{}{
		"recoveryRequired": false,
		"reconcile":        report,
	})
}

func canonicalizeFircV1SubPath(r *http.Request) (string, bool) {
	if r == nil || r.URL == nil {
		return "", false
	}
	if r.URL.RawPath != "" {
		lowerRaw := strings.ToLower(r.URL.RawPath)
		if strings.Contains(lowerRaw, "%2f") ||
			strings.Contains(lowerRaw, "%5c") ||
			strings.Contains(lowerRaw, "%00") ||
			strings.ContainsRune(r.URL.RawPath, '\\') ||
			strings.ContainsRune(r.URL.RawPath, 0) {
			return "", false
		}
	}
	if strings.ContainsRune(r.URL.Path, '\\') || strings.ContainsRune(r.URL.Path, 0) {
		return "", false
	}
	if !strings.HasPrefix(r.URL.Path, "/api/firc/v1") {
		return "", false
	}
	rawSub := strings.TrimPrefix(r.URL.Path, "/api/firc/v1")
	if rawSub != "" && !strings.HasPrefix(rawSub, "/") {
		return "", false
	}
	for _, seg := range strings.Split(rawSub, "/") {
		if seg == "." || seg == ".." {
			return "", false
		}
	}
	return path.Clean("/" + strings.TrimPrefix(rawSub, "/")), true
}

func hasSingleSegmentBetween(cleanPath, prefix, suffix string) bool {
	if !strings.HasPrefix(cleanPath, prefix) || !strings.HasSuffix(cleanPath, suffix) {
		return false
	}
	if len(cleanPath) <= len(prefix)+len(suffix) {
		return false
	}
	mid := cleanPath[len(prefix) : len(cleanPath)-len(suffix)]
	return mid != "" && !strings.ContainsRune(mid, '/')
}

func isAllowedFircV1Route(method, cleanSubPath string) bool {
	m := method
	if m == http.MethodHead {
		m = http.MethodGet
	}
	switch cleanSubPath {
	case "/auth",
		"/tunnels/state",
		"/groups/list/catalog",
		"/groups/list/preview",
		"/system/interfaces",
		"/system/hosts",
		"/system/policies",
		"/system/resolvers",
		"/system/netfilter",
		"/system/events",
		"/system/dnsstats",
		"/system/autolist":
		return m == http.MethodGet
	case "/tunnels", "/groups", "/system/settings":
		return m == http.MethodGet || m == http.MethodPut
	case "/tunnels/probe":
		return m == http.MethodGet || m == http.MethodPost
	case "/tunnels/preview", "/system/restart":
		return m == http.MethodPost
	case "/system/capture":
		return m == http.MethodGet || m == http.MethodPost || m == http.MethodDelete
	}
	switch {
	case hasSingleSegmentBetween(cleanSubPath, "/tunnels/", "/refresh"):
		return m == http.MethodPost
	case hasSingleSegmentBetween(cleanSubPath, "/tunnels/", "/restart"):
		return m == http.MethodPost
	case hasSingleSegmentBetween(cleanSubPath, "/groups/", "/move-up"):
		return m == http.MethodPost
	case hasSingleSegmentBetween(cleanSubPath, "/groups/", "/list/sync"):
		return m == http.MethodPost
	case hasSingleSegmentBetween(cleanSubPath, "/groups/", "/list/sync/events"):
		return m == http.MethodGet
	case hasSingleSegmentBetween(cleanSubPath, "/groups/", "/list/rules"):
		return m == http.MethodGet || m == http.MethodPatch
	case hasSingleSegmentBetween(cleanSubPath, "/system/autolist/candidates/", "/add"):
		return m == http.MethodPost
	default:
		return false
	}
}

// isMutatingFircV1Request — не-GET запрос /v1, меняющий состояние. POST
// предпросмотра и пробы туннеля только читают — их не считаем.
func isMutatingFircV1Request(method, cleanSubPath string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return cleanSubPath != "/tunnels/preview" && cleanSubPath != "/tunnels/probe"
}

// fircV1StatusWriter запоминает код ответа. Unwrap/Flush — чтобы
// ReverseProxy (FlushInterval -1) по-прежнему сбрасывал поток.
type fircV1StatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *fircV1StatusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *fircV1StatusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *fircV1StatusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *fircV1StatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (h *FircHandler) handleV1Proxy(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"firc operator not initialized"}`))
		return
	}
	cleanSubPath, ok := canonicalizeFircV1SubPath(r)
	if !ok {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid firc api path", "INVALID_PATH")
		return
	}
	if !isAllowedFircV1Route(r.Method, cleanSubPath) {
		response.ErrorWithStatus(w, http.StatusForbidden, "firc v1 route is not allowed", "ROUTE_NOT_ALLOWED")
		return
	}
	// Успешная мутация через /v1 (группы, туннели, настройки демона,
	// перезапуск, правила списков) — подсказка firc. Выходов из обработчика
	// с успехом много (офлайн-стейджинг, прокси к демону), поэтому статус
	// снимаем с ответа, а не расставляем публикацию по веткам.
	if isMutatingFircV1Request(r.Method, cleanSubPath) {
		sw := &fircV1StatusWriter{ResponseWriter: w}
		w = sw
		defer func() {
			if sw.status >= 200 && sw.status < 300 {
				h.publishFirc("v1" + strings.ReplaceAll(cleanSubPath, "/", "-"))
			}
		}()
	}
	if cleanSubPath == "/auth" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"authenticated":true}`))
		return
	}
	if strings.HasPrefix(cleanSubPath, "/system/autolist/candidates/") && strings.HasSuffix(cleanSubPath, "/add") {
		if r.Method != http.MethodPost {
			response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
			return
		}
		rawDomain := strings.TrimSuffix(strings.TrimPrefix(cleanSubPath, "/system/autolist/candidates/"), "/add")
		if unescaped, uErr := url.PathUnescape(rawDomain); uErr == nil {
			rawDomain = unescaped
		}
		domain := strings.ToLower(strings.TrimSpace(rawDomain))
		if domain == "" || strings.Contains(domain, "/") {
			response.ErrorWithStatus(w, http.StatusBadRequest, "domain is required", "BAD_REQUEST")
			return
		}
		var req struct {
			GroupID   string `json:"groupId"`
			Confirmed bool   `json:"confirmed"`
		}
		if r.Body != nil {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
				response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
				return
			}
		}
		h.execConfirmedAutolistCandidateAdd(w, r, domain, req.GroupID, req.Confirmed)
		return
	}
	isMutating := r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete
	if isMutating {
		if req, reason := h.op.RecoveryInfo(); req {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":            "firc recovery required: " + reason,
				"recoveryRequired": true,
			})
			return
		}
	}
	if r.Method == http.MethodPut && cleanSubPath == "/tunnels" {
		var payload struct {
			Tunnels []firc.Tunnel `json:"tunnels"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid request body: " + err.Error()})
			return
		}
		var (
			putResp *firc.PutTunnelsResponse
			err     error
		)
		if h.isExplicitlyUnselected() {
			putResp, err = h.op.StageTunnelsOffline(payload.Tunnels, true)
		} else {
			putResp, err = h.op.ApplyTunnels(r.Context(), payload.Tunnels, true)
		}
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			status := http.StatusBadRequest
			if errors.Is(err, firc.ErrRecoveryRequired) {
				status = http.StatusConflict
			}
			errMsg := err.Error()
			var filterErr *subscription.FilterError
			if errors.As(err, &filterErr) {
				errMsg = filterErr.Error()
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":            errMsg,
				"recoveryRequired": h.op.IsRecoveryRequired(),
			})
			return
		}
		if len(putResp.Tunnels) == 0 {
			if savedTunnels, err := h.op.LoadTunnelsFile(); err == nil && savedTunnels != nil {
				putResp.Tunnels = savedTunnels
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(putResp)
		return
	}
	if r.Method == http.MethodPut && cleanSubPath == "/groups" {
		var payload struct {
			Groups     []firc.Group      `json:"groups"`
			GroupLinks map[string]string `json:"groupLinks"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid request body: " + err.Error()})
			return
		}
		var (
			updated []firc.Group
			err     error
		)
		if h.isExplicitlyUnselected() {
			updated, err = h.op.StageGroupsOfflineWithLinks(payload.Groups, payload.GroupLinks)
		} else {
			updated, err = h.op.ApplyGroupsWithLinks(r.Context(), payload.Groups, payload.GroupLinks)
		}
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			status := http.StatusInternalServerError
			var invalid *firc.GroupValidationError
			if errors.Is(err, firc.ErrRecoveryRequired) {
				status = http.StatusConflict
			} else if errors.As(err, &invalid) {
				status = http.StatusBadRequest
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":            err.Error(),
				"recoveryRequired": h.op.IsRecoveryRequired(),
			})
			return
		}
		if updated == nil {
			updated = []firc.Group{}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"groups": updated})
		return
	}
	if r.Method == http.MethodPost && (cleanSubPath == "/tunnels/preview" || cleanSubPath == "/tunnels/probe") && r.Body != nil {
		rawBody, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err == nil && len(rawBody) > 0 {
			var wrapped struct {
				ID     string       `json:"id,omitempty"`
				Tunnel *firc.Tunnel `json:"tunnel,omitempty"`
				Keys   []string     `json:"keys,omitempty"`
			}
			if json.Unmarshal(rawBody, &wrapped) == nil && wrapped.Tunnel != nil {
				if err := firc.NormalizeTunnelSources(wrapped.Tunnel, h.happKeysPath()); err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
					return
				}
				if cleanSubPath == "/tunnels/preview" || cleanSubPath == "/tunnels/probe" {
					if err := firc.ValidateTunnelFilters(wrapped.Tunnel); err != nil {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusBadRequest)
						_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
						return
					}
				}
				if normBytes, err := json.Marshal(wrapped); err == nil {
					rawBody = normBytes
				}
			} else {
				var tun firc.Tunnel
				if json.Unmarshal(rawBody, &tun) == nil {
					changed := false
					if len(tun.Sources) > 0 {
						if err := firc.NormalizeTunnelSources(&tun, h.happKeysPath()); err != nil {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(http.StatusBadRequest)
							_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
							return
						}
						changed = true
					}
					if cleanSubPath == "/tunnels/preview" || cleanSubPath == "/tunnels/probe" {
						if err := firc.ValidateTunnelFilters(&tun); err != nil {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(http.StatusBadRequest)
							_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
							return
						}
						changed = true
					}
					if changed {
						if normBytes, err := json.Marshal(tun); err == nil {
							rawBody = normBytes
						}
					}
				}
			}
			r.Body = io.NopCloser(bytes.NewReader(rawBody))
			r.ContentLength = int64(len(rawBody))
			r.Header.Set("Content-Length", strconv.Itoa(len(rawBody)))
		}
	}
	if r.Method == http.MethodPost && cleanSubPath == "/tunnels/preview" {
		if h.isExplicitlyUnselected() || !h.op.IsRunning(r.Context()) {
			writeOfflineJSON(w, r, &firc.PreviewResponse{})
			return
		}
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		if h.isExplicitlyUnselected() || !h.op.IsRunning(r.Context()) {
			h.serveOfflineV1Read(w, r, cleanSubPath)
			return
		}
	} else {
		if h.isExplicitlyUnselected() {
			h.rejectNotSelected(w)
			return
		}
		if !h.op.IsRunning(r.Context()) {
			response.ErrorWithStatus(w, http.StatusServiceUnavailable, "fircd is not running", "NOT_RUNNING")
			return
		}
	}
	client := h.op.Client()
	socketPath := ""
	if client != nil {
		socketPath = client.SocketPath()
	}
	if socketPath == "" {
		socketPath = firc.DefaultSocketPath
	}
	targetURL, _ := url.Parse("http://unix")
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.FlushInterval = -1
	proxy.Transport = h.getV1ProxyTransport(socketPath)
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = "http"
		req.URL.Host = "unix"
		req.Host = "unix"
		req.URL.Path = "/api/v1" + cleanSubPath
		req.URL.RawPath = ""
	}
	if r.Method == http.MethodGet && cleanSubPath == "/system/interfaces" {
		proxy.ModifyResponse = func(resp *http.Response) error {
			if resp.StatusCode != http.StatusOK || resp.Body == nil {
				return nil
			}
			raw, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				resp.Body = io.NopCloser(bytes.NewReader(raw))
				return nil
			}
			var payload struct {
				Interfaces []map[string]interface{} `json:"interfaces"`
			}
			if err := json.Unmarshal(raw, &payload); err != nil || payload.Interfaces == nil {
				resp.Body = io.NopCloser(bytes.NewReader(raw))
				return nil
			}
			tunStatus := make(map[string]string)
			if client != nil {
				if states, err := client.GetTunnelsState(r.Context()); err == nil {
					for _, st := range states {
						if st.Device != "" {
							tunStatus[st.Device] = st.Status
						}
					}
				}
			}
			hasT2S := false
			for _, item := range payload.Interfaces {
				if id, _ := item["id"].(string); strings.HasPrefix(strings.ToLower(strings.TrimSpace(id)), "t2s") {
					hasT2S = true
					break
				}
			}
			// Владение не прочитано — прячем все t2s*, как до разбора: прокси
			// sing-box в выборе выхода FIRC хуже, чем на время скрытый чужой.
			var ownedProxies map[string]bool
			if hasT2S && h.integrationHooks.ListOwnedProxies != nil {
				ownedProxies = ownedProxySet(h.integrationHooks.ListOwnedProxies(r.Context()))
			}
			for _, item := range payload.Interfaces {
				id, _ := item["id"].(string)
				idTrim := strings.TrimSpace(id)
				if idTrim == "" {
					continue
				}
				if role, _ := item["role"].(string); strings.EqualFold(strings.TrimSpace(role), "wan") && ndmsquery.IsNonISPInterface(idTrim) {
					item["role"] = "tunnel"
				}
				if strings.HasPrefix(strings.ToLower(idTrim), "t2s") {
					if ownedProxies == nil || ownedProxies[strings.ToLower(idTrim)] {
						item["awgmSingboxProxy"] = true
					}
				}
				if _, hasUp := item["up"].(bool); hasUp {
					continue
				}
				up := false
				if st, ok := tunStatus[id]; ok {
					up = st == "up"
				} else if nif, err := net.InterfaceByName(id); err == nil {
					up = (nif.Flags & net.FlagUp) != 0
				}
				item["up"] = up
			}
			modified, err := json.Marshal(payload)
			if err != nil {
				resp.Body = io.NopCloser(bytes.NewReader(raw))
				return nil
			}
			resp.Body = io.NopCloser(bytes.NewReader(modified))
			resp.ContentLength = int64(len(modified))
			resp.Header.Set("Content-Length", strconv.Itoa(len(modified)))
			return nil
		}
	}
	proxy.ErrorHandler = func(rw http.ResponseWriter, _ *http.Request, err error) {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(rw).Encode(map[string]string{
			"error": "fircd socket unavailable: " + err.Error(),
		})
	}
	proxy.ServeHTTP(w, r)
}

// ownedProxySet — имена ядра наших ProxyN в нижнем регистре; nil — владение
// не прочитано.
func ownedProxySet(names []string, err error) map[string]bool {
	if err != nil {
		return nil
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		if k := strings.ToLower(strings.TrimSpace(n)); k != "" {
			out[k] = true
		}
	}
	return out
}

// OwnedProxySet — ownedProxySet для пакета server (PageScan).
func OwnedProxySet(names []string, err error) map[string]bool { return ownedProxySet(names, err) }

func writeOfflineJSON(w http.ResponseWriter, r *http.Request, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "ENCODE_FAILED")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func (h *FircHandler) serveOfflineV1Read(w http.ResponseWriter, r *http.Request, cleanSubPath string) {
	var payload any
	switch {
	case cleanSubPath == "/tunnels":
		tunnels, err := h.op.LoadTunnelsFile()
		if err != nil {
			response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "LOAD_TUNNELS_FAILED")
			return
		}
		if tunnels == nil {
			tunnels = []firc.Tunnel{}
		}
		payload = map[string]any{"tunnels": tunnels}
	case cleanSubPath == "/tunnels/state":
		payload = map[string]any{"tunnels": []firc.TunnelState{}}
	case cleanSubPath == "/tunnels/probe":
		payload = map[string]any{"total": 0, "nodes": []firc.ProbeNodeResult{}}
	case cleanSubPath == "/groups":
		groups, err := h.op.LoadGroupsFile()
		if err != nil {
			response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "LOAD_GROUPS_FAILED")
			return
		}
		if groups == nil {
			groups = []firc.Group{}
		}
		payload = map[string]any{"groups": groups}
	case cleanSubPath == "/groups/list/catalog":
		payload = map[string]any{
			"generated": "",
			"fetchedAt": 0,
			"stale":     false,
			"sources":   []any{},
		}
	case cleanSubPath == "/groups/list/preview":
		payload = map[string]any{
			"rules":         []any{},
			"total":         0,
			"byType":        map[string]int{},
			"dropped":       0,
			"unconstrained": 0,
		}
	case strings.HasPrefix(cleanSubPath, "/groups/") && strings.HasSuffix(cleanSubPath, "/list/rules"):
		payload = map[string]any{
			"total":   0,
			"matched": 0,
			"offset":  0,
			"rules":   []any{},
		}
	case cleanSubPath == "/system/netfilter":
		payload = map[string]any{
			"ok":                true,
			"running":           false,
			"selected":          h.isSelected(),
			"firstWritePending": false,
		}
	case cleanSubPath == "/system/interfaces":
		payload = map[string]any{"interfaces": []any{}}
	case cleanSubPath == "/system/hosts":
		payload = map[string]any{"hosts": []any{}}
	case cleanSubPath == "/system/policies":
		payload = map[string]any{"policies": []any{}}
	case cleanSubPath == "/system/resolvers":
		payload = map[string]any{"resolvers": []any{}}
	case cleanSubPath == "/system/settings":
		payload = map[string]any{
			"settings":       map[string]any{},
			"classes":        map[string]string{},
			"boot":           "",
			"pendingRestart": []string{},
		}
	case cleanSubPath == "/system/events":
		payload = map[string]any{
			"events":  []any{},
			"next":    0,
			"dropped": 0,
			"boot":    "",
		}
	case cleanSubPath == "/system/dnsstats":
		payload = map[string]any{
			"enabled": false,
			"window":  0,
			"now":     time.Now().Unix(),
			"since":   0,
			"queries": 0,
			"blocked": 0,
			"domains": 0,
			"rows":    []any{},
			"matched": 0,
			"offset":  0,
		}
	case cleanSubPath == "/system/capture":
		payload = map[string]any{"running": false}
	case cleanSubPath == "/system/autolist":
		st := offlineAutolistStatus()
		h.enrichAutolistStatus(r.Context(), st, false)
		payload = st
	default:
		payload = map[string]any{
			"running":  false,
			"selected": h.isSelected(),
		}
	}
	writeOfflineJSON(w, r, payload)
}

func (h *FircHandler) handleGetExt(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	response.Success(w, h.op.LoadExtConfigWithStatus(r.Context()))
}

func (h *FircHandler) handlePutExt(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	var req firc.ExtConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	// Проверка прокси-группы уходит в группы: с негодным URL fircd отвергнет и
	// правку групп, и groups.yaml на старте.
	for _, pg := range req.ProxyGroups {
		if pg.Check == nil || strings.TrimSpace(pg.Check.URL) == "" {
			continue
		}
		if _, ok := isValidProbeHTTPURL(pg.Check.URL); !ok {
			response.ErrorWithStatus(w, http.StatusBadRequest, fmt.Sprintf("invalid check url in proxy group %q", pg.Name), "BAD_REQUEST")
			return
		}
	}
	var (
		ext firc.ExtConfig
		err error
	)
	if h.isExplicitlyUnselected() {
		ext, err = h.op.StageExtConfigOffline(r.Context(), req)
	} else {
		ext, err = h.op.ApplyExtConfig(r.Context(), req)
	}
	if err != nil {
		if errors.Is(err, firc.ErrRecoveryRequired) {
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "RECOVERY_REQUIRED")
			return
		}
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "APPLY_EXT_FAILED")
		return
	}
	if h.onExtChanged != nil {
		h.onExtChanged(ext)
	}
	h.publishFirc("ext-updated")
	response.Success(w, ext)
}

func (h *FircHandler) handleGetMonitor(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	if h.isExplicitlyUnselected() {
		response.Success(w, h.op.GetOfflineMonitorSnapshot(r.Context()))
		return
	}
	// Списки клиентов растут с числом устройств, поэтому уходят только по запросу.
	withClients := r.URL.Query().Get("clients") == "1"
	response.Success(w, h.op.GetMonitorSnapshot(r.Context(), withClients))
}

func (h *FircHandler) handleCheckDomain(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	var req struct {
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	if h.isExplicitlyUnselected() {
		h.rejectNotSelected(w)
		return
	}
	if !h.op.IsRunning(r.Context()) {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "fircd is not running", "NOT_RUNNING")
		return
	}
	res, err := h.op.CheckDomainRouting(r.Context(), req.Domain)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "CHECK_DOMAIN_FAILED")
		return
	}
	response.Success(w, res)
}

var autolistGenericSLDs = map[string]bool{
	"ac": true, "biz": true, "co": true, "com": true, "edu": true, "gen": true,
	"go": true, "gob": true, "gov": true, "info": true, "ltd": true, "me": true,
	"mil": true, "ne": true, "net": true, "nic": true, "nom": true, "or": true,
	"org": true, "plc": true, "sch": true,
}

var autolistTwoLabelSuffixes = map[string]bool{
	"msk.ru": true, "spb.ru": true, "nov.ru": true, "msk.su": true, "spb.su": true,
	"kiev.ua": true, "kharkov.ua": true, "appspot.com": true, "azurewebsites.net": true,
	"blogspot.com": true, "cloudfront.net": true, "firebaseapp.com": true, "fly.dev": true,
	"github.io": true, "githubusercontent.com": true, "gitlab.io": true, "herokuapp.com": true,
	"netlify.app": true, "onrender.com": true, "pages.dev": true, "r2.dev": true,
	"vercel.app": true, "web.app": true, "workers.dev": true, "amazonaws.com": true,
	"cloudapp.net": true, "azureedge.net": true, "trafficmanager.net": true, "repl.co": true,
	"myshopify.com": true, "wixsite.com": true, "tumblr.com": true, "wordpress.com": true,
	"livejournal.com": true, "narod.ru": true, "ucoz.ru": true, "tilda.ws": true,
	"googleusercontent.com": true, "akamaized.net": true, "akamaihd.net": true,
	"edgekey.net": true, "edgesuite.net": true, "fastly.net": true, "b-cdn.net": true,
	"cloudflarestorage.com": true,
}

func extractAutolistRegDomain(host string) string {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if h == "" || net.ParseIP(h) != nil {
		return ""
	}
	parts := strings.Split(h, ".")
	if len(parts) < 2 {
		return ""
	}
	for _, p := range parts {
		if p == "" {
			return ""
		}
	}
	if len(parts) == 2 {
		return h
	}
	last2 := parts[len(parts)-2] + "." + parts[len(parts)-1]
	if autolistTwoLabelSuffixes[last2] {
		return parts[len(parts)-3] + "." + last2
	}
	tld := parts[len(parts)-1]
	sld := parts[len(parts)-2]
	if len(tld) == 2 && autolistGenericSLDs[sld] {
		return parts[len(parts)-3] + "." + last2
	}
	return last2
}

func isAutolistHostRouted(host string, groups []firc.Group) bool {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if h == "" {
		return false
	}
	for _, g := range groups {
		if !g.Enable {
			continue
		}
		for _, r := range g.Rules {
			if !r.Enable {
				continue
			}
			rv := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(r.Rule)), ".")
			if rv == "" {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(r.Type)) {
			case "domain":
				if h == rv {
					return true
				}
			case "namespace", "":
				if h == rv || strings.HasSuffix(h, "."+rv) {
					return true
				}
			case "wildcard":
				if strings.HasPrefix(rv, "*.") {
					suf := strings.TrimPrefix(rv, "*.")
					if h == suf || strings.HasSuffix(h, "."+suf) {
						return true
					}
				}
			}
		}
	}
	return false
}

func (h *FircHandler) enrichAutolistStatus(ctx context.Context, st *firc.AutolistStatus, allowLive bool) {
	if st == nil || h.op == nil {
		return
	}
	live := allowLive && h.op.IsRunning(ctx)
	var groups []firc.Group
	if live {
		groups, _ = h.op.Client().GetGroups(ctx, false)
	}
	if len(groups) == 0 {
		groups, _ = h.op.LoadGroupsFile()
	}
	target := strings.TrimSpace(st.Group)
	if live {
		if sr, err := h.op.Client().GetSettings(ctx); err == nil && sr != nil && sr.Settings != nil {
			if target == "" {
				if rawG, ok := sr.Settings["app.autolist.group"].(string); ok && strings.TrimSpace(rawG) != "" {
					target = strings.TrimSpace(rawG)
					st.Group = target
				}
			}
		}
	}
	matchedGroup := false
	for _, g := range groups {
		if (target != "" && (g.ID == target || strings.EqualFold(g.Name, target))) || (target == "" && st.GroupID != "" && g.ID == st.GroupID) {
			st.GroupID = g.ID
			st.Group = g.ID
			st.GroupName = g.Name
			if g.Active != "" {
				st.Interface = g.Active
			} else if len(g.Interface) > 0 {
				st.Interface = g.Interface[0]
			}
			matchedGroup = true
			break
		}
	}
	if !matchedGroup && len(groups) > 0 {
		for i := len(groups) - 1; i >= 0; i-- {
			g := groups[i]
			if g.Enable && g.Block == "" && !g.IsDNSOnly() {
				st.GroupID = g.ID
				st.Group = g.ID
				st.GroupName = g.Name
				if g.Active != "" {
					st.Interface = g.Active
				} else if len(g.Interface) > 0 {
					st.Interface = g.Interface[0]
				}
				matchedGroup = true
				break
			}
		}
		if !matchedGroup {
			g := groups[len(groups)-1]
			st.GroupID = g.ID
			st.Group = g.ID
			st.GroupName = g.Name
		}
	} else if st.GroupID == "" && target != "" {
		st.GroupID = target
	}

	// Promote failed address entries that have a known SNI domain into Candidates
	// if they are not already routed or listed in Candidates.
	candIndex := make(map[string]int, len(st.Candidates))
	for i, c := range st.Candidates {
		candIndex[strings.ToLower(strings.TrimSpace(c.Domain))] = i
	}
	for _, addr := range st.Addresses {
		if !strings.EqualFold(strings.TrimSpace(addr.Outcome), "failed") {
			continue
		}
		host := strings.ToLower(strings.TrimSpace(addr.Name))
		if host == "" {
			continue
		}
		regDomain := extractAutolistRegDomain(host)
		if regDomain == "" {
			continue
		}
		if isAutolistHostRouted(host, groups) || isAutolistHostRouted(regDomain, groups) {
			continue
		}
		if idx, exists := candIndex[regDomain]; exists {
			if st.Candidates[idx].Notes == "SNI / conntrack" {
				st.Candidates[idx].Fails += addr.Fails
				if addr.LastAt > st.Candidates[idx].LastAt {
					st.Candidates[idx].LastAt = addr.LastAt
				}
				if len(addr.Clients) > len(st.Candidates[idx].Clients) {
					st.Candidates[idx].Clients = addr.Clients
				}
				if st.Candidates[idx].Host == "" {
					st.Candidates[idx].Host = host
				}
			}
			continue
		}
		candIndex[regDomain] = len(st.Candidates)
		st.Candidates = append(st.Candidates, firc.AutolistCandidate{
			Domain:  regDomain,
			Host:    host,
			State:   "candidate",
			Why:     addr.Why,
			Notes:   "SNI / conntrack",
			Fails:   addr.Fails,
			Clients: addr.Clients,
			LastAt:  addr.LastAt,
		})
	}
}

func (h *FircHandler) handleGetAutolist(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	if h.isExplicitlyUnselected() || !h.op.IsRunning(r.Context()) {
		st := offlineAutolistStatus()
		h.enrichAutolistStatus(r.Context(), st, false)
		response.Success(w, st)
		return
	}
	st, err := h.op.Client().GetAutolist(r.Context())
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "AUTOLIST_FAILED")
		return
	}
	h.enrichAutolistStatus(r.Context(), st, true)
	response.Success(w, st)
}

func (h *FircHandler) handleConfigureAutolist(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	var req struct {
		Enabled bool   `json:"enabled"`
		GroupID string `json:"groupId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	if h.isExplicitlyUnselected() {
		h.rejectNotSelected(w)
		return
	}
	if reqRec, reason := h.op.RecoveryInfo(); reqRec {
		response.ErrorWithStatus(w, http.StatusConflict, "firc recovery required: "+reason, "RECOVERY_REQUIRED")
		return
	}
	if !h.op.IsRunning(r.Context()) {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "fircd is not running", "NOT_RUNNING")
		return
	}
	groups, err := h.op.Client().GetGroups(r.Context(), false)
	if err != nil {
		groups, _ = h.op.LoadGroupsFile()
	}
	rawTarget := strings.TrimSpace(req.GroupID)
	resolvedGroupID := ""
	for _, g := range groups {
		if rawTarget != "" && (g.ID == rawTarget || strings.EqualFold(g.Name, rawTarget)) {
			if req.Enabled && g.IsDNSOnly() {
				response.ErrorWithStatus(w, http.StatusBadRequest, firc.ErrCandidateGroupDNSOnly.Error(), "GROUP_DNS_ONLY")
				return
			}
			resolvedGroupID = g.ID
			break
		}
	}
	if resolvedGroupID == "" && req.Enabled && len(groups) > 0 {
		for i := len(groups) - 1; i >= 0; i-- {
			g := groups[i]
			if g.Enable && g.Block == "" && !g.IsDNSOnly() {
				resolvedGroupID = g.ID
				break
			}
		}
		if last := groups[len(groups)-1]; resolvedGroupID == "" && !last.IsDNSOnly() {
			resolvedGroupID = last.ID
		}
	}
	if req.Enabled && resolvedGroupID == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "Выберите целевую группу маршрутизации для включения монитора обхода", "GROUP_REQUIRED")
		return
	}
	changes := map[string]interface{}{
		"app.autolist.enabled": req.Enabled,
		"app.autolist.mode":    "candidates",
	}
	if resolvedGroupID != "" {
		changes["app.autolist.group"] = resolvedGroupID
	}
	if req.Enabled {
		changes["app.autolist.failThreshold"] = 2
		changes["app.autolist.failTime"] = 300
		changes["app.autolist.retransThreshold"] = 2
		if sr, sErr := h.op.Client().GetSettings(r.Context()); sErr == nil && sr != nil && sr.Settings != nil {
			switch v := sr.Settings["app.dnsProxy.unmatchedTtl"].(type) {
			case float64:
				if v <= 0 {
					changes["app.dnsProxy.unmatchedTtl"] = 60
				}
			case int:
				if v <= 0 {
					changes["app.dnsProxy.unmatchedTtl"] = 60
				}
			default:
				if v == nil {
					changes["app.dnsProxy.unmatchedTtl"] = 60
				}
			}
		} else {
			changes["app.dnsProxy.unmatchedTtl"] = 60
		}
	}
	if _, err := h.op.Client().PutSettings(r.Context(), changes); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "AUTOLIST_CONFIG_FAILED")
		return
	}
	st, err := h.op.Client().GetAutolist(r.Context())
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "AUTOLIST_FAILED")
		return
	}
	h.enrichAutolistStatus(r.Context(), st, true)
	response.Success(w, st)
}

func (h *FircHandler) handleRecheckAutolist(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	if h.isExplicitlyUnselected() {
		h.rejectNotSelected(w)
		return
	}
	if !h.op.IsRunning(r.Context()) {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "fircd is not running", "NOT_RUNNING")
		return
	}
	if err := h.op.Client().RecheckAutolist(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "AUTOLIST_RECHECK_FAILED")
		return
	}
	response.Success(w, map[string]bool{"ok": true})
}

func (h *FircHandler) handleProbeAutolist(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	var req struct {
		Host string `json:"host"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Host) == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "host is required", "BAD_REQUEST")
		return
	}
	if h.isExplicitlyUnselected() {
		h.rejectNotSelected(w)
		return
	}
	if !h.op.IsRunning(r.Context()) {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "fircd is not running", "NOT_RUNNING")
		return
	}
	res, err := h.op.Client().ProbeAutolistHost(r.Context(), req.Host)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "AUTOLIST_PROBE_FAILED")
		return
	}
	response.Success(w, res)
}

func (h *FircHandler) execConfirmedAutolistCandidateAdd(w http.ResponseWriter, r *http.Request, domain, rawGroupID string, confirmed bool) {
	if !confirmed {
		response.ErrorWithStatus(w, http.StatusBadRequest, "explicit confirmation (confirmed=true) is required to add a bypass candidate domain to a routing group", "CONFIRMATION_REQUIRED")
		return
	}
	groupID := strings.TrimSpace(rawGroupID)
	if groupID == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "groupId is required to add a confirmed bypass candidate domain", "GROUP_REQUIRED")
		return
	}
	if h.isExplicitlyUnselected() {
		h.rejectNotSelected(w)
		return
	}
	updated, resolvedGroupID, err := h.op.AddConfirmedCandidate(r.Context(), domain, groupID)
	if err != nil {
		if errors.Is(err, firc.ErrRecoveryRequired) {
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "RECOVERY_REQUIRED")
			return
		}
		if errors.Is(err, firc.ErrCandidateGroupNotFound) {
			response.ErrorWithStatus(w, http.StatusNotFound, "routing group not found: "+groupID, "GROUP_NOT_FOUND")
			return
		}
		if errors.Is(err, firc.ErrCandidateDomainRequired) {
			response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "BAD_REQUEST")
			return
		}
		if errors.Is(err, firc.ErrCandidateGroupDNSOnly) {
			response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "GROUP_DNS_ONLY")
			return
		}
		if errors.Is(err, firc.ErrCandidateGroupRequired) {
			response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "GROUP_REQUIRED")
			return
		}
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "APPLY_GROUPS_FAILED")
		return
	}
	response.Success(w, map[string]any{
		"ok":      true,
		"action":  "add",
		"domain":  domain,
		"groupId": resolvedGroupID,
		"groups":  updated,
	})
}

func (h *FircHandler) handleAutolistCandidate(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	var req struct {
		Domain    string `json:"domain"`
		Action    string `json:"action"` // "add" | "dismiss"
		GroupID   string `json:"groupId,omitempty"`
		Confirmed bool   `json:"confirmed,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	domain := strings.ToLower(strings.TrimSpace(req.Domain))
	if domain == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "domain is required", "BAD_REQUEST")
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	switch action {
	case "dismiss":
		if h.isExplicitlyUnselected() {
			h.rejectNotSelected(w)
			return
		}
		if reqRec, reason := h.op.RecoveryInfo(); reqRec {
			response.ErrorWithStatus(w, http.StatusConflict, "firc recovery required: "+reason, "RECOVERY_REQUIRED")
			return
		}
		if !h.op.IsRunning(r.Context()) {
			response.ErrorWithStatus(w, http.StatusServiceUnavailable, "fircd is not running", "NOT_RUNNING")
			return
		}
		dismissedAddr := false
		if st, aErr := h.op.Client().GetAutolist(r.Context()); aErr == nil && st != nil {
			for _, addr := range st.Addresses {
				addrName := strings.ToLower(strings.TrimSpace(addr.Name))
				if (addrName != "" && (addrName == domain || strings.HasSuffix(addrName, "."+domain))) || strings.EqualFold(strings.TrimSpace(addr.Address), domain) {
					if dErr := h.op.Client().DismissAutolistAddress(r.Context(), addr.Address, addr.Proto, addr.Port); dErr == nil {
						dismissedAddr = true
					}
				}
			}
		}
		if err := h.op.Client().DismissAutolistCandidate(r.Context(), domain); err != nil && !dismissedAddr {
			response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "AUTOLIST_DISMISS_FAILED")
			return
		}
		response.Success(w, map[string]any{"ok": true, "domain": domain, "action": "dismiss"})
	case "add":
		h.execConfirmedAutolistCandidateAdd(w, r, domain, req.GroupID, req.Confirmed)
	default:
		response.ErrorWithStatus(w, http.StatusBadRequest, "unsupported action: "+action, "BAD_REQUEST")
	}
}

func (h *FircHandler) handleDismissAutolistAddress(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	var req struct {
		Address string `json:"address"`
		Proto   string `json:"proto"`
		Port    uint16 `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Address) == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "address is required", "BAD_REQUEST")
		return
	}
	if h.isExplicitlyUnselected() {
		h.rejectNotSelected(w)
		return
	}
	if !h.op.IsRunning(r.Context()) {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "fircd is not running", "NOT_RUNNING")
		return
	}
	if err := h.op.Client().DismissAutolistAddress(r.Context(), req.Address, req.Proto, req.Port); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "AUTOLIST_ADDRESS_DISMISS_FAILED")
		return
	}
	response.Success(w, map[string]bool{"ok": true})
}

func (h *FircHandler) currentEngine() string {
	if h.settingsStore == nil {
		return "sing-box"
	}
	s, err := h.settingsStore.Get()
	if err != nil || s == nil || strings.TrimSpace(s.SingboxRouter.RoutingEngine) == "" {
		return "sing-box"
	}
	return strings.TrimSpace(s.SingboxRouter.RoutingEngine)
}

func (h *FircHandler) handleGetMigrateState(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	var st firc.Status
	if h.isExplicitlyUnselected() {
		st = h.op.GetOfflineStatus(false)
	} else {
		st = h.op.GetStatus(r.Context(), h.isSelected())
	}
	fircInstalled := h.op.ValidateBinaries() == nil
	wiz := h.op.LoadMigrationWizardState()
	snapshots, _ := h.op.ListSystemSnapshots()
	var fwdPreview *firc.MigrationResult
	if h.singboxConfigDir != "" {
		fwdPreview, _ = h.op.PreviewFromSingboxWithOptions(h.singboxConfigDir, nil)
	}

	sbRunning := false
	if h.integrationHooks.IsSingboxRunning != nil {
		sbRunning = h.integrationHooks.IsSingboxRunning()
	}
	sbInstalled := false
	if h.integrationHooks.IsSingboxInstalled != nil {
		sbInstalled = h.integrationHooks.IsSingboxInstalled()
	}
	var sbTunnelsCount, sbSubsCount int
	if h.integrationHooks.GetSingboxCounts != nil {
		sbTunnelsCount, sbSubsCount = h.integrationHooks.GetSingboxCounts()
	} else if fwdPreview != nil {
		sbTunnelsCount = len(fwdPreview.Tunnels)
		sbSubsCount = fwdPreview.SubscriptionsMigrated
	}
	fircTunnelsList, _ := h.op.LoadTunnelsFile()
	fircGroupsList, _ := h.op.LoadGroupsFile()

	hasMigratableSingbox := fwdPreview != nil && (len(fwdPreview.Tunnels) > 0 || len(fwdPreview.Groups) > 0)
	shouldShowPopup := st.FircdInstalled && !wiz.DismissedAutoPopup && wiz.LastMigratedAt == 0 && hasMigratableSingbox

	response.Success(w, firc.MigrationOverviewResponse{
		ShouldShowPopup:           shouldShowPopup,
		WizardState:               wiz,
		ActiveEngine:              h.currentEngine(),
		FircRunning:               st.Running,
		FircInstalled:             fircInstalled,
		SingboxRunning:            sbRunning,
		SingboxInstalled:          sbInstalled,
		SingboxTunnelsCount:       sbTunnelsCount,
		SingboxSubscriptionsCount: sbSubsCount,
		FircTunnelsCount:          len(fircTunnelsList),
		FircGroupsCount:           len(fircGroupsList),
		Snapshots:                 snapshots,
		ForwardPreview:            fwdPreview,
	})
}

func (h *FircHandler) handlePutMigrateState(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	var req struct {
		DismissedAutoPopup *bool `json:"dismissedAutoPopup"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	wiz := h.op.LoadMigrationWizardState()
	if req.DismissedAutoPopup != nil {
		wiz.DismissedAutoPopup = *req.DismissedAutoPopup
		if wiz.DismissedAutoPopup {
			wiz.DismissedAt = 1
		} else {
			wiz.DismissedAt = 0
		}
	}
	if err := h.op.SaveMigrationWizardState(wiz); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "SAVE_WIZARD_STATE_FAILED")
		return
	}
	response.Success(w, wiz)
}

func (h *FircHandler) handlePurgeEngine(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	var req struct {
		Engine string `json:"engine"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	var trailing any
	if dec.Decode(&trailing) != io.EOF {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "BAD_REQUEST")
		return
	}
	target := strings.ToLower(strings.TrimSpace(req.Engine))
	if target == "singbox" {
		target = "sing-box"
	}
	var (
		res *firc.EnginePurgeResult
		err error
	)
	curEngine := h.currentEngine()
	switch target {
	case "sing-box":
		res, err = h.op.PurgeSingboxAndTunnels(
			r.Context(),
			h.singboxConfigDir,
			curEngine,
			nil,
			firc.EnginePurgeHooks{PurgeSingbox: h.integrationHooks.PurgeSingbox},
		)
	case "firc":
		res, err = h.op.PurgeFircAndTunnels(
			r.Context(),
			h.singboxConfigDir,
			curEngine,
			h.installer,
			nil,
			firc.EnginePurgeHooks{OnBeforeRemoveFircRuntime: h.integrationHooks.OnBeforePurgeFirc},
		)
	default:
		response.ErrorWithStatus(w, http.StatusBadRequest, "engine must be sing-box or firc", "BAD_REQUEST")
		return
	}
	if err != nil {
		switch {
		case errors.Is(err, firc.ErrCannotPurgeActiveEngine):
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "CANNOT_PURGE_ACTIVE_ENGINE")
		case errors.Is(err, firc.ErrRecoveryRequired):
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "RECOVERY_REQUIRED")
		case errors.Is(err, firc.ErrFircRunning), errors.Is(err, firc.ErrTunvlessRunning):
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "FIRC_RUNNING")
		default:
			response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "PURGE_ENGINE_FAILED")
		}
		return
	}
	h.publishEngineInvalidations("purge-" + target)
	response.Success(w, res)
}

func (h *FircHandler) handleListSnapshots(w http.ResponseWriter, _ *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	list, err := h.op.ListSystemSnapshots()
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "LIST_SNAPSHOTS_FAILED")
		return
	}
	response.Success(w, map[string]interface{}{
		"snapshots": list,
	})
}

func (h *FircHandler) handleCreateSnapshot(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	var req struct {
		Label string `json:"label"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	meta, err := h.op.CreateSystemSnapshot(h.singboxConfigDir, strings.TrimSpace(req.Label), "manual", h.currentEngine())
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "CREATE_SNAPSHOT_FAILED")
		return
	}
	response.Success(w, meta)
}

func (h *FircHandler) handleRestoreSnapshot(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	var req struct {
		ID           string `json:"id"`
		SwitchEngine *bool  `json:"switchEngine"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.ID) == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "snapshot id is required", "BAD_REQUEST")
		return
	}
	wantSwitch := req.SwitchEngine == nil || *req.SwitchEngine
	if wantSwitch {
		if snapMeta, mErr := h.op.GetSystemSnapshotMeta(req.ID); mErr == nil && snapMeta != nil {
			targetEng := strings.ToLower(strings.TrimSpace(snapMeta.ActiveEngine))
			if targetEng == "firc" {
				if rerr := h.op.CheckRuntimeInstalledForSwitch(); rerr != nil {
					response.ErrorWithStatus(w, http.StatusConflict, rerr.Error(), "FIRC_RUNTIME_NOT_INSTALLED")
					return
				}
			} else if targetEng == "sing-box" && h.integrationHooks.IsSingboxInstalled != nil && !h.integrationHooks.IsSingboxInstalled() {
				response.ErrorWithStatus(w, http.StatusConflict, "Бинарный файл Sing-box не установлен. Установите Sing-box перед восстановлением этого снимка.", "SINGBOX_NOT_INSTALLED")
				return
			}
		}
	}
	preRestoreEngine := h.currentEngine()
	meta, rollbackFiles, err := h.op.RestoreSystemSnapshotWithRollback(r.Context(), req.ID, h.singboxConfigDir, wantSwitch)
	if err != nil {
		if errors.Is(err, firc.ErrSingboxValidatorUnavailable) {
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "SINGBOX_NOT_INSTALLED")
			return
		}
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "RESTORE_SNAPSHOT_FAILED")
		return
	}
	h.runPostRestoreSync(r.Context(), true, true)
	if wantSwitch && meta != nil && strings.TrimSpace(meta.ActiveEngine) != "" {
		targetEng := strings.TrimSpace(meta.ActiveEngine)
		hooks := h.buildEngineSwitchHooks()
		if preRestoreEngine != "" && !strings.EqualFold(preRestoreEngine, targetEng) {
			// RestoreSystemSnapshotWithRollback wrote settings.json from the snapshot before
			// validating/starting targetEng. Keep stored engine aligned with the still-running
			// preRestoreEngine until SwitchEngineWithOptions commits targetEng so Phase R3
			// (e.g. ResumeFromExternalEngine) never fails with ErrExternalRoutingEngineActive.
			if hooks.CommitSettings != nil {
				_ = hooks.CommitSettings(r.Context(), preRestoreEngine)
			}
			hooks.GetCurrentEngine = func(context.Context) (string, error) {
				return preRestoreEngine, nil
			}
		} else if preRestoreEngine != "" {
			hooks.GetCurrentEngine = func(context.Context) (string, error) {
				return preRestoreEngine, nil
			}
		}
		if serr := h.op.SwitchEngineWithOptions(r.Context(), targetEng, firc.EngineSwitchOptions{
			AuthorizeDisabledStart: strings.EqualFold(targetEng, "firc"),
		}, hooks); serr != nil {
			meta.PostActionError = serr.Error()
			if !h.op.IsRecoveryRequired() && rollbackFiles != nil {
				if rbErr := rollbackFiles(r.Context()); rbErr != nil {
					meta.PostActionError = fmt.Sprintf("%s; rollback pre-restore files failed: %v", serr.Error(), rbErr)
				} else {
					h.runPostRestoreSync(r.Context(), true, true)
					if hooks.ActivateTarget != nil {
						_ = hooks.ActivateTarget(r.Context(), preRestoreEngine)
					}
				}
			}
			meta.RecoveryRequired = h.op.IsRecoveryRequired()
		} else {
			meta.PostActionApplied = "switch_to_" + targetEng
			h.op.RecordMigrationPostAction(meta.PostActionApplied)
		}
	}
	h.publishEngineInvalidations("snapshot-restore")
	response.Success(w, meta)
}

func (h *FircHandler) handleDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	if h.op == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "firc operator not initialized", "UNAVAILABLE")
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" && r.Body != nil {
		var req struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		id = strings.TrimSpace(req.ID)
	}
	if id == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "snapshot id is required", "BAD_REQUEST")
		return
	}
	if err := h.op.DeleteSystemSnapshot(id); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "DELETE_SNAPSHOT_FAILED")
		return
	}
	response.Success(w, map[string]bool{"deleted": true})
}
