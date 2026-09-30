package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/mihomo"
	"github.com/hoaxisr/awg-manager/internal/mihomo/installer"
	"github.com/hoaxisr/awg-manager/internal/mihomonative"
	"github.com/hoaxisr/awg-manager/internal/proxyengine"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// MihomoHandler provides REST API endpoints for managing the Mihomo engine.
type MihomoHandler struct {
	op            proxyengine.Engine
	installer     *installer.Installer
	bus           *events.Bus
	settingsStore *storage.SettingsStore
	routerSvc     router.Service
	reloadFn      func() error
	restartFn     func() error
	nativeStore   *mihomonative.Store
	nativeBridge  *mihomonative.BridgeManager
	bridgePrepare func(context.Context, []mihomonative.BridgeRef) error
	bridgeReady   func(context.Context) error
	bridgeDown    func(context.Context) error
	// reloadPublishesBridge is true for production DynamicEngine, whose
	// prepare/readiness hooks already own the bridge commit. Unit/simple engine
	// wiring leaves it false and the handler performs that boundary itself.
	reloadPublishesBridge bool
	mutationTx            func(func() error) error
	mutationReloadFn      func() error
	mutationApplier       NativeMutationApplier
	providerRefresher     mihomo.MihomoProviderRefresher
	nativeMu              sync.Mutex
	orderIdempotencyMu    sync.Mutex
	orderIdempotency      map[string]cachedOrderResult
}

type cachedOrderResult struct {
	response    NativeRuleOrderResponse
	headers     map[string]string
	fingerprint string
	cachedAt    time.Time
}

// MihomoStatusSnapshot is a secret-free internal status view shared by the
// REST handler and read-only diagnostics such as the AI assistant.
type MihomoStatusSnapshot struct {
	Running          bool   `json:"running"`
	Degraded         bool   `json:"degraded"`
	PID              int    `json:"pid,omitempty"`
	Binary           string `json:"binary,omitempty"`
	Error            string `json:"error,omitempty"`
	Selected         bool   `json:"selected"`
	Enabled          bool   `json:"enabled"`
	Active           bool   `json:"active"`
	Installed        bool   `json:"installed"`
	InstallAvailable bool   `json:"installAvailable"`
	UpdateAvailable  bool   `json:"updateAvailable"`
	CurrentVersion   string `json:"currentVersion,omitempty"`
	Version          string `json:"version,omitempty"`
	RequiredVersion  string `json:"requiredVersion,omitempty"`
	InstallState     string `json:"installState,omitempty"`
	RequiredBytes    int64  `json:"requiredBytes,omitempty"`
	FreeBytes        int64  `json:"freeBytes,omitempty"`
}

func (h *MihomoHandler) StatusSnapshot() (MihomoStatusSnapshot, error) {
	if h == nil || h.op == nil {
		return MihomoStatusSnapshot{}, errors.New("mihomo engine is unavailable")
	}
	running, pid := h.op.IsRunning()
	binPath := h.op.Binary()
	status := MihomoStatusSnapshot{Running: running, PID: pid, Binary: binPath, Error: h.op.LastError()}

	if h.installer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		status.Installed = h.installer.IsInstalled()
		status.InstallAvailable = h.installer.IsInstallAvailable()
		status.CurrentVersion = h.installer.CurrentVersion(ctx)
		status.Version = status.CurrentVersion
		status.RequiredVersion = h.installer.RequiredVersion()
		status.UpdateAvailable = h.installer.UpdateAvailable(ctx)
		status.InstallState = string(h.installer.EvaluateInstallState(ctx))
		status.RequiredBytes = h.installer.RequiredSize()
		if free, ok := h.installer.FreeBytes(); ok {
			status.FreeBytes = free
		}
	} else {
		info, err := os.Stat(binPath)
		status.Installed = err == nil && !info.IsDir() && (info.Mode()&0111 != 0)
	}

	if h.settingsStore == nil {
		return status, nil
	}
	settings, err := h.settingsStore.Load()
	if err != nil {
		return status, err
	}
	status.Selected = settings.SingboxRouter.RoutingEngine == "mihomo"
	status.Enabled = settings.SingboxRouter.Enabled
	hasNativeListeners := h.nativeStore != nil && len(h.nativeStore.ConfigBridgeListeners()) > 0
	status.Active = (status.Selected && status.Enabled && running) || (running && hasNativeListeners)
	return status, nil
}

func NewMihomoHandler(op proxyengine.Engine) *MihomoHandler {
	h := &MihomoHandler{
		op:               op,
		orderIdempotency: make(map[string]cachedOrderResult),
	}
	if refresher, ok := op.(mihomo.MihomoProviderRefresher); ok {
		h.providerRefresher = refresher
	}
	return h
}

func (h *MihomoHandler) SetProviderRefresher(refresher mihomo.MihomoProviderRefresher) {
	h.nativeMu.Lock()
	defer h.nativeMu.Unlock()
	h.providerRefresher = refresher
}

func (h *MihomoHandler) checkMutationAllowed() error {
	h.nativeMu.Lock()
	applier := h.mutationApplier
	h.nativeMu.Unlock()
	if applier == nil {
		return nil
	}
	if applier.IsDegraded() {
		return ErrRecoveryRequired
	}
	if err := applier.CheckMutationAllowed(); err != nil {
		return ErrRecoveryRequired
	}
	return nil
}

// CheckMutationAllowed returns ErrRecoveryRequired if the engine is degraded
// or if mutations are otherwise blocked.
func (h *MihomoHandler) CheckMutationAllowed() error {
	return h.checkMutationAllowed()
}

// MutationApplier returns the configured mutation applier, if any.
func (h *MihomoHandler) MutationApplier() NativeMutationApplier {
	h.nativeMu.Lock()
	defer h.nativeMu.Unlock()
	return h.mutationApplier
}

func (h *MihomoHandler) SetInstaller(inst *installer.Installer) {
	h.installer = inst
}

func (h *MihomoHandler) SetEventBus(bus *events.Bus) {
	h.bus = bus
}

func (h *MihomoHandler) SetSettingsStore(store *storage.SettingsStore) {
	h.settingsStore = store
}

func (h *MihomoHandler) SetRouterService(svc router.Service) {
	h.routerSvc = svc
}

func (h *MihomoHandler) Restart() error {
	if err := h.checkMutationAllowed(); err != nil {
		return err
	}
	if h.restartFn != nil {
		return h.restartFn()
	}
	if h.op == nil {
		return errors.New("mihomo engine is unavailable")
	}
	if err := h.op.Stop(); err != nil {
		return err
	}
	time.Sleep(500 * time.Millisecond)
	return h.op.Start()
}

func (h *MihomoHandler) SetRestartFunc(fn func() error) {
	h.restartFn = fn
}

func (h *MihomoHandler) Reload() error {
	if err := h.checkMutationAllowed(); err != nil {
		return err
	}
	if h.reloadFn != nil {
		return h.reloadFn()
	}
	if h.op != nil {
		return h.op.Reload()
	}
	return errors.New("mihomo engine is unavailable")
}

func (h *MihomoHandler) SetReloadFunc(fn func() error) {
	h.reloadFn = fn
}

func (h *MihomoHandler) SetMutationTransaction(tx func(func() error) error, reloadInsideTx func() error) {
	h.mutationTx = tx
	h.mutationReloadFn = reloadInsideTx
}

func (h *MihomoHandler) SetReloadPublishesNativeBridges(enabled bool) {
	h.reloadPublishesBridge = enabled
}

func (h *MihomoHandler) SetNativeStore(store *mihomonative.Store) {
	h.nativeStore = store
}

func (h *MihomoHandler) NativeStore() *mihomonative.Store {
	return h.nativeStore
}

func (h *MihomoHandler) RouterService() router.Service {
	return h.routerSvc
}

func (h *MihomoHandler) SetNativeBridgeManager(manager *mihomonative.BridgeManager) {
	h.nativeBridge = manager
}

func (h *MihomoHandler) BatchSaveRules(ctx context.Context, rules []mihomonative.Rule) error {
	if err := h.checkMutationAllowed(); err != nil {
		return err
	}
	_, err := h.withNativeMutation(ctx, true, func() (interface{}, error) {
		return h.nativeStore.SaveRulesBatch(rules)
	})
	return err
}

// SetNativeBridgeLifecycle installs the process-aware publication boundary for
// native ProxyN exports. prepare may allocate listener identities but must not
// advertise them; ready runs only after Mihomo has accepted the new config;
// down withdraws exports when apply/readiness fails.
func (h *MihomoHandler) SetNativeBridgeLifecycle(
	prepare func(context.Context, []mihomonative.BridgeRef) error,
	ready func(context.Context) error,
	down func(context.Context) error,
) {
	h.bridgePrepare = prepare
	h.bridgeReady = ready
	h.bridgeDown = down
}

func (h *MihomoHandler) SetMutationApplier(applier NativeMutationApplier) {
	h.nativeMu.Lock()
	defer h.nativeMu.Unlock()
	h.mutationApplier = applier
}

// MihomoRecoveryReconcileRequest specifies recovery parameters for degraded mode.
type MihomoRecoveryReconcileRequest struct {
	Action string `json:"action"` // "rollback_to_lkg" | "regenerate_from_desired"
	Force  bool   `json:"force"`
}

// @Summary		Export redacted diagnostic recovery evidence
// @Description	Returns safely redacted coordinator and runtime state for degraded diagnostics
// @Tags			mihomo
// @Produce		json
// @Success		200		{object}	APIEnvelope{data=mihomo.RecoveryEvidenceDTO}
// @Failure		500		{object}	APIErrorEnvelope
// @Failure		503		{object}	APIErrorEnvelope
// @Router			/mihomo/recovery/evidence [get]
func (h *MihomoHandler) HandleRecoveryEvidence(w http.ResponseWriter, r *http.Request) {
	if h.mutationApplier == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "recovery applier unavailable", "UNAVAILABLE")
		return
	}
	evidence, err := h.mutationApplier.ExportEvidence(r.Context())
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "EVIDENCE_EXPORT_FAILED")
		return
	}
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Content-Disposition", "attachment; filename=\"mihomo-recovery-evidence.json\"")
	response.Success(w, evidence)
}

// @Summary		Reconcile degraded Mihomo coordinator state
// @Description	Administratively triggers reconciliation or clears recovery marker for degraded Mihomo state
// @Tags			mihomo
// @Accept			json
// @Produce		json
// @Param			body	body		MihomoRecoveryReconcileRequest	true	"Reconciliation action and force flag"
// @Success		200		{object}	APIEnvelope
// @Failure		400		{object}	APIErrorEnvelope
// @Failure		503		{object}	APIErrorEnvelope
// @Router			/mihomo/recovery/reconcile [post]
func (h *MihomoHandler) HandleRecoveryReconcile(w http.ResponseWriter, r *http.Request) {
	if h.mutationApplier == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "recovery applier unavailable", "UNAVAILABLE")
		return
	}
	var req MihomoRecoveryReconcileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body")
		return
	}
	if req.Action != "rollback_to_lkg" && req.Action != "regenerate_from_desired" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "unsupported recovery action: "+req.Action, "INVALID_ACTION")
		return
	}
	if err := h.mutationApplier.Reconcile(r.Context(), req.Action, req.Force); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "RECONCILE_FAILED")
		return
	}
	response.Success(w, map[string]string{"status": "ok"})
}

// RouteRegistrar represents an HTTP route registration target such as *http.ServeMux.
type RouteRegistrar interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

func (h *MihomoHandler) RegisterRoutes(mux *http.ServeMux, guarded func(http.HandlerFunc) http.HandlerFunc) {
	h.RegisterRoutesTo(mux, guarded)
}

func (h *MihomoHandler) RegisterRoutesTo(mux RouteRegistrar, guarded func(http.HandlerFunc) http.HandlerFunc) {
	if guarded == nil {
		guarded = func(next http.HandlerFunc) http.HandlerFunc { return next }
	}
	mux.HandleFunc("GET /api/mihomo/status", guarded(h.HandleStatus))
	mux.HandleFunc("POST /api/mihomo/install", guarded(h.handleInstall))
	mux.HandleFunc("POST /api/mihomo/update", guarded(h.handleUpdate))
	mux.HandleFunc("POST /api/mihomo/uninstall", guarded(h.handleUninstall))
	mux.HandleFunc("GET /api/mihomo/config", guarded(h.handleConfig))
	mux.HandleFunc("POST /api/mihomo/reload", guarded(h.handleReload))
	mux.HandleFunc("POST /api/mihomo/restart", guarded(h.handleRestart))
	mux.HandleFunc("GET /api/mihomo/recovery/evidence", guarded(h.HandleRecoveryEvidence))
	mux.HandleFunc("POST /api/mihomo/recovery/reconcile", guarded(h.HandleRecoveryReconcile))
	if h.nativeStore != nil {
		mux.HandleFunc("GET /api/mihomo/native/proxies", guarded(h.handleNativeProxyList))
		mux.HandleFunc("POST /api/mihomo/native/proxies", guarded(h.handleNativeProxyCreate))
		mux.HandleFunc("GET /api/mihomo/native/proxies/{id}", guarded(h.handleNativeProxyGet))
		mux.HandleFunc("PUT /api/mihomo/native/proxies/{id}", guarded(h.handleNativeProxyUpdate))
		mux.HandleFunc("DELETE /api/mihomo/native/proxies/{id}", guarded(h.handleNativeProxyDelete))
		mux.HandleFunc("GET /api/mihomo/native/subscriptions", guarded(h.handleNativeSubscriptionList))
		mux.HandleFunc("POST /api/mihomo/native/subscriptions", guarded(h.handleNativeSubscriptionCreate))
		mux.HandleFunc("GET /api/mihomo/native/subscriptions/{id}", guarded(h.handleNativeSubscriptionGet))
		mux.HandleFunc("PUT /api/mihomo/native/subscriptions/{id}", guarded(h.handleNativeSubscriptionUpdate))
		mux.HandleFunc("DELETE /api/mihomo/native/subscriptions/{id}", guarded(h.handleNativeSubscriptionDelete))
		mux.HandleFunc("POST /api/mihomo/native/subscriptions/{id}/refresh", guarded(h.handleNativeSubscriptionRefresh))
		mux.HandleFunc("GET /api/mihomo/native/groups", guarded(h.handleNativeGroupList))
		mux.HandleFunc("GET /api/mihomo/native/groups/{id}/references", guarded(h.handleNativeGroupReferences))
		mux.HandleFunc("POST /api/mihomo/native/groups", guarded(h.handleNativeGroupSave))
		mux.HandleFunc("PUT /api/mihomo/native/groups/{id}", guarded(h.handleNativeGroupSave))
		mux.HandleFunc("DELETE /api/mihomo/native/groups/{id}", guarded(h.handleNativeGroupDelete))
		mux.HandleFunc("GET /api/mihomo/native/rules", guarded(h.handleNativeRuleList))
		mux.HandleFunc("GET /api/mihomo/native/rules/unsupported", guarded(h.HandleNativeUnsupportedRulesList))
		mux.HandleFunc("POST /api/mihomo/native/rules/unsupported/delete", guarded(h.HandleNativeUnsupportedRulesDelete))
		mux.HandleFunc("GET /api/router/mihomo/rules/unsupported", guarded(h.HandleNativeUnsupportedRulesList))
		mux.HandleFunc("POST /api/router/mihomo/rules/unsupported/delete", guarded(h.HandleNativeUnsupportedRulesDelete))
		mux.HandleFunc("POST /api/mihomo/native/rules", guarded(h.handleNativeRuleCreate))
		mux.HandleFunc("PUT /api/mihomo/native/rules/{id}", guarded(h.handleNativeRuleUpdate))
		mux.HandleFunc("DELETE /api/mihomo/native/rules/{id}", guarded(h.handleNativeRuleDelete))
		mux.HandleFunc("PUT /api/mihomo/native/rules/order", guarded(h.handleNativeRuleOrder))
		mux.HandleFunc("GET /api/mihomo/native/rule-providers", guarded(h.handleNativeRuleProviderList))
		mux.HandleFunc("POST /api/mihomo/native/rule-providers", guarded(h.handleNativeRuleProviderSave))
		mux.HandleFunc("PUT /api/mihomo/native/rule-providers/{id}", guarded(h.handleNativeRuleProviderSave))
		mux.HandleFunc("DELETE /api/mihomo/native/rule-providers/{id}", guarded(h.handleNativeRuleProviderDelete))
		mux.HandleFunc("POST /api/mihomo/router/inspect", guarded(h.handleMihomoInspect))
		mux.HandleFunc("GET /api/mihomo/router/inspect/stream", guarded(h.handleMihomoInspectStream))
		mux.HandleFunc("POST /api/mihomo/native/reset", guarded(h.handleNativeReset))
	}

	// Mount the clash proxy under /api/mihomo/clash/
	clashProxy := NewMihomoClashProxy(h.op, h)
	mux.HandleFunc("/api/mihomo/clash", guarded(clashProxy.ServeHTTP))
	mux.HandleFunc("/api/mihomo/clash/", guarded(clashProxy.ServeHTTP))
}

func (h *MihomoHandler) handleConfig(w http.ResponseWriter, _ *http.Request) {
	data, err := os.ReadFile(filepath.Join(h.op.ConfigDir(), "config.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		response.ErrorWithStatus(w, http.StatusNotFound, "mihomo config has not been generated yet", "CONFIG_NOT_FOUND")
		return
	}
	if err != nil {
		response.InternalError(w, fmt.Sprintf("read mihomo config: %v", err))
		return
	}
	response.Success(w, map[string]string{"yaml": string(data)})
}

func (h *MihomoHandler) handleNativeGroupList(w http.ResponseWriter, _ *http.Request) {
	response.Success(w, map[string]interface{}{"items": h.nativeStore.ListGroups()})
}

func (h *MihomoHandler) handleNativeGroupReferences(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "group id is required", "INVALID_REQUEST")
		return
	}
	refs := h.nativeStore.GetGroupReferences(id)
	if refs == nil {
		refs = []mihomonative.GroupReference{}
	}
	response.Success(w, map[string]interface{}{"references": refs})
}
func (h *MihomoHandler) handleNativeGroupSave(w http.ResponseWriter, r *http.Request) {
	if err := h.checkMutationAllowed(); err != nil {
		writeNativeMutationError(w, err, "")
		return
	}
	var group mihomonative.ProxyGroup
	if err := json.NewDecoder(r.Body).Decode(&group); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid JSON body", "INVALID_REQUEST")
		return
	}
	if id := r.PathValue("id"); id != "" {
		group.ID = id
	}
	apply := r.URL.Query().Get("apply") != "false"
	result, err := h.withNativeMutation(r.Context(), apply, func() (interface{}, error) {
		saved, saveErr := h.nativeStore.SaveGroup(group)
		if saveErr != nil {
			return nil, nativeInputError{saveErr}
		}
		return saved, nil
	})
	if err != nil {
		writeNativeMutationError(w, err, "INVALID_GROUP")
		return
	}
	response.Success(w, result)
}
func (h *MihomoHandler) handleNativeGroupDelete(w http.ResponseWriter, r *http.Request) {
	if err := h.checkMutationAllowed(); err != nil {
		writeNativeMutationError(w, err, "")
		return
	}
	apply := r.URL.Query().Get("apply") != "false"
	_, err := h.withNativeMutation(r.Context(), apply, func() (interface{}, error) {
		if deleteErr := h.nativeStore.DeleteGroup(r.PathValue("id")); deleteErr != nil {
			return nil, deleteErr
		}
		return map[string]bool{"deleted": true}, nil
	})
	if err != nil {
		writeNativeMutationError(w, err, "INVALID_GROUP")
		return
	}
	response.Success(w, map[string]bool{"deleted": true})
}
func (h *MihomoHandler) handleNativeRuleList(w http.ResponseWriter, _ *http.Request) {
	var rules []mihomonative.Rule
	var rev uint64
	if h.nativeStore != nil {
		rules = h.nativeStore.ListRules()
		rev = h.nativeStore.SnapshotRevision()
	}
	if rules == nil {
		rules = []mihomonative.Rule{}
	}
	response.Success(w, map[string]interface{}{
		"items":    rules,
		"revision": rev,
	})
}
func (h *MihomoHandler) handleNativeRuleCreate(w http.ResponseWriter, r *http.Request) {
	if err := h.checkMutationAllowed(); err != nil {
		writeNativeMutationError(w, err, "")
		return
	}
	var input mihomonative.RuleInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid JSON body", "INVALID_REQUEST")
		return
	}
	if input.ID != "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "rule id must not be specified when creating a rule", "ID_NOT_ALLOWED")
		return
	}
	apply := r.URL.Query().Get("apply") != "false"
	result, outcome, err := h.withNativeMutationDetailed(r.Context(), apply, func() (interface{}, error) {
		saved, saveErr := h.nativeStore.CreateRule(input)
		if saveErr != nil {
			if errors.Is(saveErr, mihomonative.ErrIDNotAllowed) {
				return nil, saveErr
			}
			return nil, nativeInputError{saveErr}
		}
		return saved, nil
	})
	if err != nil {
		writeNativeMutationError(w, err, "INVALID_RULE")
		return
	}
	setOutcomeHeaders(w, outcome)
	saved, _ := result.(mihomonative.Rule)
	response.Success(w, h.nativeRuleMutationResponse(&saved, false, outcome))
}

func (h *MihomoHandler) handleNativeRuleUpdate(w http.ResponseWriter, r *http.Request) {
	if err := h.checkMutationAllowed(); err != nil {
		writeNativeMutationError(w, err, "")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		response.ErrorWithStatus(w, http.StatusNotFound, "rule id required", "NOT_FOUND")
		return
	}
	var input mihomonative.RuleInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid JSON body", "INVALID_REQUEST")
		return
	}
	if input.ID != "" && input.ID != id {
		response.ErrorWithStatus(w, http.StatusBadRequest, "payload id does not match route id", "ID_MISMATCH")
		return
	}
	apply := r.URL.Query().Get("apply") != "false"
	result, outcome, err := h.withNativeMutationDetailed(r.Context(), apply, func() (interface{}, error) {
		updated, updateErr := h.nativeStore.UpdateRule(id, input)
		if updateErr != nil {
			if errors.Is(updateErr, mihomonative.ErrRuleNotFound) || errors.Is(updateErr, mihomonative.ErrIDMismatch) {
				return nil, updateErr
			}
			return nil, nativeInputError{updateErr}
		}
		return updated, nil
	})
	if err != nil {
		writeNativeMutationError(w, err, "INVALID_RULE")
		return
	}
	setOutcomeHeaders(w, outcome)
	updated, _ := result.(mihomonative.Rule)
	response.Success(w, h.nativeRuleMutationResponse(&updated, false, outcome))
}

func (h *MihomoHandler) handleNativeRuleDelete(w http.ResponseWriter, r *http.Request) {
	if err := h.checkMutationAllowed(); err != nil {
		writeNativeMutationError(w, err, "")
		return
	}
	apply := r.URL.Query().Get("apply") != "false"
	_, outcome, err := h.withNativeMutationDetailed(r.Context(), apply, func() (interface{}, error) {
		if deleteErr := h.nativeStore.DeleteRule(r.PathValue("id")); deleteErr != nil {
			return nil, deleteErr
		}
		return map[string]bool{"deleted": true}, nil
	})
	if err != nil {
		writeNativeMutationError(w, err, "INVALID_RULE")
		return
	}
	setOutcomeHeaders(w, outcome)
	response.Success(w, h.nativeRuleMutationResponse(nil, true, outcome))
}

// NativeUnsupportedRulesResponse represents the response containing unsupported rules and snapshot revision.
type NativeUnsupportedRulesResponse struct {
	Items    []mihomonative.Rule `json:"items"`
	Revision string              `json:"revision"`
}

// NativeDeleteUnsupportedRulesRequest represents the payload required to delete unsupported rules.
type NativeDeleteUnsupportedRulesRequest struct {
	IDs      []string `json:"ids" binding:"required,min=1,dive,min=1" validate:"min=1,unique,dive,min=1" extensions:"x-nullable=false"`
	Revision string   `json:"revision" binding:"required,min=1" validate:"min=1"`
}

// NativeDeleteUnsupportedRulesResponse represents the result of deleting unsupported rules.
type NativeDeleteUnsupportedRulesResponse struct {
	Deleted      bool `json:"deleted"`
	DeletedCount int  `json:"deletedCount"`
}

// @Summary		List unsupported native Mihomo rules
// @Description	Returns a snapshot of rules with unsupported types and a revision token
// @Tags			mihomo
// @Produce		json
// @Success		200	{object}	NativeUnsupportedRulesResponse
// @Router			/mihomo/native/rules/unsupported [get]
func (h *MihomoHandler) HandleNativeUnsupportedRulesList(w http.ResponseWriter, _ *http.Request) {
	items, revision := h.nativeStore.ComputeUnsupportedRulesSnapshot()
	if items == nil {
		items = []mihomonative.Rule{}
	}
	response.Success(w, NativeUnsupportedRulesResponse{
		Items:    items,
		Revision: revision,
	})
}

// @Summary		Delete unsupported native Mihomo rules
// @Description	Deletes the specified unsupported rules if the full set and revision match
// @Tags			mihomo
// @Accept			json
// @Produce		json
// @Param			apply	query		bool								false	"Apply config immediately"
// @Param			body	body		NativeDeleteUnsupportedRulesRequest	true	"Rule IDs and revision token"
// @Success		200		{object}	NativeDeleteUnsupportedRulesResponse
// @Failure		400		{object}	APIErrorEnvelope
// @Failure		409		{object}	APIErrorEnvelope
// @Failure		500		{object}	APIErrorEnvelope
// @Router			/mihomo/native/rules/unsupported/delete [post]
func (h *MihomoHandler) HandleNativeUnsupportedRulesDelete(w http.ResponseWriter, r *http.Request) {
	if err := h.checkMutationAllowed(); err != nil {
		writeNativeMutationError(w, err, "")
		return
	}
	var body NativeDeleteUnsupportedRulesRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid JSON body", "INVALID_REQUEST")
		return
	}
	if body.Revision == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "revision is required", "INVALID_REQUEST")
		return
	}
	if len(body.IDs) == 0 {
		response.ErrorWithStatus(w, http.StatusBadRequest, "ids array must not be empty", "INVALID_REQUEST")
		return
	}
	seenIDs := make(map[string]struct{}, len(body.IDs))
	for _, id := range body.IDs {
		if id == "" {
			response.ErrorWithStatus(w, http.StatusBadRequest, "rule id must not be empty", "INVALID_REQUEST")
			return
		}
		if _, seen := seenIDs[id]; seen {
			response.ErrorWithStatus(w, http.StatusBadRequest, "duplicate rule id in selection", "SELECTION_MISMATCH")
			return
		}
		seenIDs[id] = struct{}{}
	}
	apply := r.URL.Query().Get("apply") != "false"
	result, err := h.withNativeMutation(r.Context(), apply, func() (interface{}, error) {
		deletedCount, deleteErr := h.nativeStore.DeleteUnsupportedRules(body.IDs, body.Revision)
		if deleteErr != nil {
			return nil, deleteErr
		}
		return NativeDeleteUnsupportedRulesResponse{Deleted: true, DeletedCount: deletedCount}, nil
	})
	if err != nil {
		if errors.Is(err, mihomonative.ErrRulesStale) {
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "MIHOMO_RULES_STALE")
			return
		}
		if errors.Is(err, mihomonative.ErrSelectionMismatch) {
			response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "SELECTION_MISMATCH")
			return
		}
		writeNativeMutationError(w, err, "DELETE_UNSUPPORTED_RULES_FAILED")
		return
	}
	response.Success(w, result)
}

// NativeRuleOrderRequest represents the payload for reordering rules.
type NativeRuleOrderRequest struct {
	IDs          []string `json:"ids"`
	Order        []string `json:"order"`
	BaseRevision *uint64  `json:"baseRevision"`
	OperationID  string   `json:"operationId"`
}

// NativeRuleOrderResponse represents the response after reordering rules.
type NativeRuleOrderResponse struct {
	Reordered     bool                `json:"reordered"`
	Items         []mihomonative.Rule `json:"items"`
	Revision      uint64              `json:"revision"`
	Generation    uint64              `json:"generation,omitempty"`
	ApplyPath     string              `json:"applyPath,omitempty"`
	TransactionID string              `json:"transactionId,omitempty"`
}

// NativeRuleMutationResponse is returned by every rule mutation so clients can
// advance from the server's authoritative revision instead of guessing it.
type NativeRuleMutationResponse struct {
	Item          *mihomonative.Rule  `json:"item,omitempty"`
	Deleted       bool                `json:"deleted,omitempty"`
	Items         []mihomonative.Rule `json:"items"`
	Revision      uint64              `json:"revision"`
	Generation    uint64              `json:"generation,omitempty"`
	ApplyPath     string              `json:"applyPath,omitempty"`
	TransactionID string              `json:"transactionId,omitempty"`
}

func (h *MihomoHandler) nativeRuleMutationResponse(item *mihomonative.Rule, deleted bool, outcome *mihomo.MutationOutcome) NativeRuleMutationResponse {
	resp := NativeRuleMutationResponse{Item: item, Deleted: deleted, Items: []mihomonative.Rule{}}
	if h.nativeStore != nil {
		resp.Items = h.nativeStore.ListRules()
		resp.Revision = h.nativeStore.SnapshotRevision()
	}
	if resp.Items == nil {
		resp.Items = []mihomonative.Rule{}
	}
	if outcome != nil {
		resp.Generation = outcome.Generation
		resp.ApplyPath = string(outcome.ApplyPath)
		resp.TransactionID = outcome.TxID
	}
	return resp
}

func (h *MihomoHandler) handleNativeRuleOrder(w http.ResponseWriter, r *http.Request) {
	if err := h.checkMutationAllowed(); err != nil {
		writeNativeMutationError(w, err, "")
		return
	}
	var req NativeRuleOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid JSON body", "INVALID_REQUEST")
		return
	}

	ids := req.IDs
	if len(ids) == 0 && len(req.Order) > 0 {
		ids = req.Order
	}
	if len(ids) == 0 {
		response.ErrorWithStatus(w, http.StatusBadRequest, "rule ids required", "INVALID_REQUEST")
		return
	}
	fingerprintBytes, _ := json.Marshal(struct {
		IDs          []string `json:"ids"`
		BaseRevision *uint64  `json:"baseRevision"`
		Apply        bool     `json:"apply"`
	}{IDs: ids, BaseRevision: req.BaseRevision, Apply: r.URL.Query().Get("apply") != "false"})
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(fingerprintBytes))

	// Idempotent retry check
	if req.OperationID != "" {
		h.orderIdempotencyMu.Lock()
		if entry, ok := h.orderIdempotency[req.OperationID]; ok && time.Since(entry.cachedAt) < 5*time.Minute {
			if entry.fingerprint != fingerprint {
				h.orderIdempotencyMu.Unlock()
				response.ErrorWithStatus(w, http.StatusConflict, "operationId was already used for a different rule order request", "MIHOMO_OPERATION_ID_CONFLICT")
				return
			}
			h.orderIdempotencyMu.Unlock()
			for k, v := range entry.headers {
				w.Header().Set(k, v)
			}
			response.Success(w, entry.response)
			return
		}
		h.orderIdempotencyMu.Unlock()
	}

	// CAS check against baseRevision before mutation
	if req.BaseRevision != nil && h.nativeStore != nil {
		currentRev := h.nativeStore.SnapshotRevision()
		if *req.BaseRevision != currentRev {
			var currentRules []mihomonative.Rule
			if h.nativeStore != nil {
				currentRules = h.nativeStore.ListRules()
			}
			if currentRules == nil {
				currentRules = []mihomonative.Rule{}
			}
			response.ErrorWithData(w, http.StatusConflict, "rules have been modified concurrently", "MIHOMO_RULES_STALE", map[string]interface{}{
				"items":    currentRules,
				"revision": currentRev,
			})
			return
		}
	}

	apply := r.URL.Query().Get("apply") != "false"
	_, outcome, err := h.withNativeMutationDetailed(r.Context(), apply, func() (interface{}, error) {
		if reorderErr := h.nativeStore.ReorderRulesWithRevision(ids, req.BaseRevision); reorderErr != nil {
			return nil, reorderErr
		}
		return map[string]bool{"reordered": true}, nil
	})
	if err != nil {
		if errors.Is(err, mihomonative.ErrRulesStale) {
			var currentRules []mihomonative.Rule
			var currentRev uint64
			if h.nativeStore != nil {
				currentRules = h.nativeStore.ListRules()
				currentRev = h.nativeStore.SnapshotRevision()
			}
			if currentRules == nil {
				currentRules = []mihomonative.Rule{}
			}
			response.ErrorWithData(w, http.StatusConflict, err.Error(), "MIHOMO_RULES_STALE", map[string]interface{}{
				"items":    currentRules,
				"revision": currentRev,
			})
			return
		}
		writeNativeMutationError(w, err, "INVALID_RULE_ORDER")
		return
	}

	var currentRules []mihomonative.Rule
	var currentRev uint64
	if h.nativeStore != nil {
		currentRules = h.nativeStore.ListRules()
		currentRev = h.nativeStore.SnapshotRevision()
	}
	if currentRules == nil {
		currentRules = []mihomonative.Rule{}
	}

	resp := NativeRuleOrderResponse{
		Reordered: true,
		Items:     currentRules,
		Revision:  currentRev,
	}
	if outcome != nil {
		resp.ApplyPath = string(outcome.ApplyPath)
		resp.Generation = outcome.Generation
		resp.TransactionID = outcome.TxID
	} else if !apply {
		resp.ApplyPath = "draft_only"
	}

	setOutcomeHeaders(w, outcome)

	// Record in idempotency cache
	if req.OperationID != "" {
		headers := make(map[string]string)
		if outcome != nil {
			if outcome.ServerTiming != "" {
				headers["Server-Timing"] = outcome.ServerTiming
			}
			if outcome.TxID != "" {
				headers["X-Transaction-ID"] = outcome.TxID
			}
			if outcome.ApplyPath != "" {
				headers["X-Apply-Path"] = string(outcome.ApplyPath)
			}
		}
		h.orderIdempotencyMu.Lock()
		if h.orderIdempotency == nil {
			h.orderIdempotency = make(map[string]cachedOrderResult)
		}
		if len(h.orderIdempotency) > 100 {
			now := time.Now()
			for k, v := range h.orderIdempotency {
				if now.Sub(v.cachedAt) > 5*time.Minute {
					delete(h.orderIdempotency, k)
				}
			}
		}
		h.orderIdempotency[req.OperationID] = cachedOrderResult{
			response:    resp,
			headers:     headers,
			fingerprint: fingerprint,
			cachedAt:    time.Now(),
		}
		h.orderIdempotencyMu.Unlock()
	}

	response.Success(w, resp)
}
func (h *MihomoHandler) handleNativeRuleProviderList(w http.ResponseWriter, _ *http.Request) {
	response.Success(w, map[string]interface{}{"items": h.nativeStore.ListRuleProviders()})
}
func (h *MihomoHandler) handleNativeRuleProviderSave(w http.ResponseWriter, r *http.Request) {
	if err := h.checkMutationAllowed(); err != nil {
		writeNativeMutationError(w, err, "")
		return
	}
	var provider mihomonative.RuleProvider
	if err := json.NewDecoder(r.Body).Decode(&provider); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid JSON body", "INVALID_REQUEST")
		return
	}
	if id := r.PathValue("id"); id != "" {
		provider.ID = id
	}
	apply := r.URL.Query().Get("apply") != "false"
	result, err := h.withNativeMutation(r.Context(), apply, func() (interface{}, error) {
		saved, saveErr := h.nativeStore.SaveRuleProvider(provider)
		if saveErr != nil {
			return nil, nativeInputError{saveErr}
		}
		return saved, nil
	})
	if err != nil {
		writeNativeMutationError(w, err, "INVALID_RULE_PROVIDER")
		return
	}
	response.Success(w, result)
}
func (h *MihomoHandler) handleNativeRuleProviderDelete(w http.ResponseWriter, r *http.Request) {
	if err := h.checkMutationAllowed(); err != nil {
		writeNativeMutationError(w, err, "")
		return
	}
	apply := r.URL.Query().Get("apply") != "false"
	_, err := h.withNativeMutation(r.Context(), apply, func() (interface{}, error) {
		if deleteErr := h.nativeStore.DeleteRuleProvider(r.PathValue("id")); deleteErr != nil {
			return nil, deleteErr
		}
		return map[string]bool{"deleted": true}, nil
	})
	if err != nil {
		writeNativeMutationError(w, err, "INVALID_RULE_PROVIDER")
		return
	}
	response.Success(w, map[string]bool{"deleted": true})
}

type nativeProxyCreateRequest struct {
	URI              string                         `json:"uri"`
	EnginePreference mihomonative.EnginePreference  `json:"enginePreference"`
	Manual           *mihomonative.ManualProxyInput `json:"manual,omitempty"`
	Enabled          *bool                          `json:"enabled,omitempty"`
}

type nativeInputError struct{ err error }

func (e nativeInputError) Error() string { return e.err.Error() }
func (e nativeInputError) Unwrap() error { return e.err }

func writeNativeMutationError(w http.ResponseWriter, err error, invalidCode string) {
	if errors.Is(err, ErrRecoveryRequired) || errors.Is(err, mihomo.ErrRecoveryRequired) {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "Mihomo engine is in degraded recovery mode; mutation rejected", "RECOVERY_REQUIRED")
		return
	}
	if errors.Is(err, mihomonative.ErrNotFound) || errors.Is(err, mihomonative.ErrRuleNotFound) {
		response.ErrorWithStatus(w, http.StatusNotFound, "Mihomo resource not found", "NOT_FOUND")
		return
	}
	if errors.Is(err, mihomonative.ErrIDNotAllowed) {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "ID_NOT_ALLOWED")
		return
	}
	if errors.Is(err, mihomonative.ErrIDMismatch) {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "ID_MISMATCH")
		return
	}
	if errors.Is(err, mihomonative.ErrRulesStale) {
		response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "MIHOMO_RULES_STALE")
		return
	}
	if errors.Is(err, mihomonative.ErrSelectionMismatch) {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "SELECTION_MISMATCH")
		return
	}
	var inputErr nativeInputError
	if errors.As(err, &inputErr) {
		response.ErrorWithStatus(w, http.StatusBadRequest, inputErr.Error(), invalidCode)
		return
	}
	response.InternalError(w, err.Error())
}

func (h *MihomoHandler) routingEngine() mihomonative.EnginePreference {
	if h.settingsStore != nil {
		if settings, err := h.settingsStore.Load(); err == nil && settings.SingboxRouter.RoutingEngine == "mihomo" {
			return mihomonative.EngineMihomo
		}
	}
	return mihomonative.EngineSingbox
}

func (h *MihomoHandler) applyNativeMutation(insideTx bool) error {
	if h.settingsStore == nil {
		return nil
	}
	settings, err := h.settingsStore.Load()
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	hasNativeListeners := h.nativeStore != nil && len(h.nativeStore.ConfigBridgeListeners()) > 0
	sidecar := hasNativeListeners
	if settings.SingboxRouter.RoutingEngine == "mihomo" && !settings.SingboxRouter.Enabled && !sidecar {
		return h.op.Stop()
	}
	if settings.SingboxRouter.RoutingEngine != "mihomo" && h.reloadFn == nil {
		if sidecar {
			return h.op.Reload()
		}
		return h.op.Stop()
	}
	reload := h.reloadFn
	if insideTx && h.mutationReloadFn != nil {
		reload = h.mutationReloadFn
	} else if reload == nil {
		reload = h.op.Reload
	}
	return reload()
}

func setOutcomeHeaders(w http.ResponseWriter, outcome *mihomo.MutationOutcome) {
	if outcome == nil {
		return
	}
	if outcome.ServerTiming != "" {
		w.Header().Set("Server-Timing", outcome.ServerTiming)
	}
	if outcome.TxID != "" {
		w.Header().Set("X-Transaction-ID", outcome.TxID)
	}
	if outcome.ApplyPath != "" {
		w.Header().Set("X-Apply-Path", string(outcome.ApplyPath))
	}
}

func (h *MihomoHandler) withNativeMutationDetailed(
	ctx context.Context,
	apply bool,
	mutate func() (interface{}, error),
) (interface{}, *mihomo.MutationOutcome, error) {
	mutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()

	if h.mutationApplier != nil {
		if h.mutationApplier.IsDegraded() {
			return nil, nil, ErrRecoveryRequired
		}
		if err := h.mutationApplier.CheckMutationAllowed(); err != nil {
			return nil, nil, ErrRecoveryRequired
		}
		var result interface{}
		var mutErr error
		var beforeBridges []mihomonative.BridgeRef
		if h.nativeStore != nil {
			beforeBridges = h.nativeStore.ListBridges()
		}
		fn := func() error {
			if mutate != nil {
				result, mutErr = mutate()
				if mutErr != nil {
					return mutErr
				}
			}
			if h.bridgePrepare != nil {
				if bridgeErr := h.bridgePrepare(mutCtx, beforeBridges); bridgeErr != nil {
					return fmt.Errorf("prepare bridges: %w", bridgeErr)
				}
			} else if h.nativeBridge != nil {
				if bridgeErr := h.nativeBridge.Reconcile(mutCtx, beforeBridges); bridgeErr != nil {
					return fmt.Errorf("reconcile bridges: %w", bridgeErr)
				}
			}
			return nil
		}
		if apply {
			outcome, err := h.mutationApplier.ApplyNativeMutationWithOutcome(mutCtx, fn)
			if err != nil {
				return nil, outcome, err
			}
			return result, outcome, nil
		} else {
			if err := h.mutationApplier.ApplyDraftOnly(mutCtx, fn); err != nil {
				return nil, nil, err
			}
			return result, &mihomo.MutationOutcome{ApplyPath: mihomo.ApplyPathDraftOnly}, nil
		}
	}

	h.nativeMu.Lock()
	defer h.nativeMu.Unlock()

	body := func() (interface{}, error) {
		snapshot, err := h.nativeStore.Snapshot()
		if err != nil {
			return nil, fmt.Errorf("snapshot Mihomo native store: %w", err)
		}
		beforeBridges := h.nativeStore.ListBridges()
		result, err := mutate()
		if err != nil {
			if restoreErr := h.nativeStore.RestoreSnapshot(snapshot); restoreErr != nil {
				return nil, fmt.Errorf("mutation failed: %v; rollback error: %w", err, restoreErr)
			}
			return nil, err
		}
		// Always pass the pre-mutation bridge set here. DynamicEngine's own prepare
		// sees only the current store and cannot remove a ProxyN belonging to a
		// just-deleted/disabled resource.
		if h.bridgePrepare != nil {
			if err = h.bridgePrepare(ctx, beforeBridges); err != nil {
				return nil, h.rollbackNativeMutation(ctx, snapshot, err)
			}
		} else if h.nativeBridge != nil {
			if err = h.nativeBridge.Reconcile(ctx, beforeBridges); err != nil {
				return nil, h.rollbackNativeMutation(ctx, snapshot, err)
			}
		}
		afterBridges := h.nativeStore.ListBridges()
		if apply {
			err = h.applyNativeMutation(h.mutationTx != nil)
			if err == nil && !h.reloadPublishesBridge && h.bridgeReady != nil {
				err = h.bridgeReady(ctx)
			}
		}
		if err != nil {
			if !h.reloadPublishesBridge && h.bridgeDown != nil {
				_ = h.bridgeDown(ctx)
			}
			return nil, h.rollbackNativeMutationWithBridges(ctx, snapshot, afterBridges, err, apply)
		}
		return result, nil
	}

	if h.mutationTx != nil {
		var res interface{}
		var txErr error
		err := h.mutationTx(func() error {
			res, txErr = body()
			return txErr
		})
		if err != nil {
			return nil, nil, err
		}
		return res, nil, nil
	}
	res, err := body()
	return res, nil, err
}

func (h *MihomoHandler) withNativeMutation(
	ctx context.Context,
	apply bool,
	mutate func() (interface{}, error),
) (interface{}, error) {
	res, _, err := h.withNativeMutationDetailed(ctx, apply, mutate)
	return res, err
}

func (h *MihomoHandler) rollbackNativeMutation(ctx context.Context, snapshot mihomonative.StoreSnapshot, cause error) error {
	// A process-aware prepare may have already withdrawn obsolete/current
	// ProxyN interfaces before failing. Reapply the restored config so
	// DynamicEngine can run readiness and republish the old bridge set.
	return h.rollbackNativeMutationWithBridges(ctx, snapshot, h.nativeStore.ListBridges(), cause, h.reloadPublishesBridge)
}

func (h *MihomoHandler) rollbackNativeMutationWithBridges(
	ctx context.Context,
	snapshot mihomonative.StoreSnapshot,
	mutatedBridges []mihomonative.BridgeRef,
	cause error,
	reapply bool,
) error {
	if restoreErr := h.nativeStore.RestoreSnapshot(snapshot); restoreErr != nil {
		return fmt.Errorf("%v; restore native store failed: %v", cause, restoreErr)
	}
	var rollbackErrors []string
	if h.bridgePrepare != nil {
		if bridgeErr := h.bridgePrepare(ctx, mutatedBridges); bridgeErr != nil {
			rollbackErrors = append(rollbackErrors, "restore bridges: "+bridgeErr.Error())
		}
	} else if h.nativeBridge != nil {
		if bridgeErr := h.nativeBridge.Reconcile(ctx, mutatedBridges); bridgeErr != nil {
			rollbackErrors = append(rollbackErrors, "restore bridges: "+bridgeErr.Error())
		}
	}
	if reapply {
		if applyErr := h.applyNativeMutation(h.mutationTx != nil); applyErr != nil {
			rollbackErrors = append(rollbackErrors, "reapply previous config: "+applyErr.Error())
		}
	}
	if len(rollbackErrors) == 0 && !h.reloadPublishesBridge && h.bridgeReady != nil {
		if bridgeErr := h.bridgeReady(ctx); bridgeErr != nil {
			rollbackErrors = append(rollbackErrors, "restore bridge exports: "+bridgeErr.Error())
		}
	}
	if len(rollbackErrors) > 0 {
		return fmt.Errorf("%v; rollback failed: %s", cause, strings.Join(rollbackErrors, "; "))
	}
	return fmt.Errorf("%v; change was rolled back", cause)
}

func (h *MihomoHandler) handleNativeProxyList(w http.ResponseWriter, _ *http.Request) {
	response.Success(w, map[string]interface{}{"items": h.nativeStore.ListProxies()})
}

func (h *MihomoHandler) handleNativeProxyCreate(w http.ResponseWriter, r *http.Request) {
	if err := h.checkMutationAllowed(); err != nil {
		writeNativeMutationError(w, err, "")
		return
	}
	var req nativeProxyCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid JSON body", "INVALID_REQUEST")
		return
	}
	result, err := h.withNativeMutation(r.Context(), true, func() (interface{}, error) {
		if req.Manual != nil {
			req.Manual.EnginePreference = req.EnginePreference
			node, createErr := h.nativeStore.CreateManual(*req.Manual, h.routingEngine())
			if createErr != nil {
				return nil, nativeInputError{createErr}
			}
			return map[string]interface{}{"items": []mihomonative.ProxyNode{node}}, nil
		}
		nodes, createErr := h.nativeStore.CreateProxy(req.URI, req.EnginePreference, h.routingEngine())
		if createErr != nil {
			return nil, nativeInputError{createErr}
		}
		return map[string]interface{}{"items": nodes}, nil
	})
	if err != nil {
		writeNativeMutationError(w, err, "INVALID_PROXY")
		return
	}
	response.Success(w, result)
}

func (h *MihomoHandler) handleNativeProxyGet(w http.ResponseWriter, r *http.Request) {
	node, err := h.nativeStore.GetProxy(r.PathValue("id"))
	if err != nil {
		writeNativeMutationError(w, err, "INVALID_PROXY")
		return
	}
	response.Success(w, node)
}

func (h *MihomoHandler) handleNativeProxyUpdate(w http.ResponseWriter, r *http.Request) {
	if err := h.checkMutationAllowed(); err != nil {
		writeNativeMutationError(w, err, "")
		return
	}
	var req nativeProxyCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid JSON body", "INVALID_REQUEST")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	result, err := h.withNativeMutation(r.Context(), true, func() (interface{}, error) {
		node, updateErr := h.nativeStore.UpdateProxy(r.PathValue("id"), mihomonative.UpdateProxyInput{
			URI: req.URI, Manual: req.Manual, EnginePreference: req.EnginePreference,
			Enabled: enabled, RoutingEngine: h.routingEngine(),
		})
		if updateErr != nil {
			if errors.Is(updateErr, mihomonative.ErrNotFound) {
				return nil, updateErr
			}
			return nil, nativeInputError{updateErr}
		}
		return node, nil
	})
	if err != nil {
		writeNativeMutationError(w, err, "INVALID_PROXY")
		return
	}
	response.Success(w, result)
}

func (h *MihomoHandler) handleNativeProxyDelete(w http.ResponseWriter, r *http.Request) {
	if err := h.checkMutationAllowed(); err != nil {
		writeNativeMutationError(w, err, "")
		return
	}
	id := r.PathValue("id")
	_, err := h.withNativeMutation(r.Context(), r.URL.Query().Get("apply") != "false", func() (interface{}, error) {
		if deleteErr := h.nativeStore.DeleteProxy(id); deleteErr != nil {
			return nil, deleteErr
		}
		return map[string]bool{"deleted": true}, nil
	})
	if err != nil {
		writeNativeMutationError(w, err, "INVALID_PROXY")
		return
	}
	response.Success(w, map[string]bool{"deleted": true})
}

type nativeSubscriptionCreateRequest struct {
	Name             string                          `json:"name"`
	URL              string                          `json:"url"`
	Inline           string                          `json:"inline"`
	Format           mihomonative.SubscriptionFormat `json:"format"`
	EnginePreference mihomonative.EnginePreference   `json:"enginePreference"`
	RefreshHours     int                             `json:"refreshHours"`
	Enabled          bool                            `json:"enabled"`
	Mode             string                          `json:"mode,omitempty"`
	TestURL          string                          `json:"testUrl,omitempty"`
	TestInterval     int                             `json:"testInterval,omitempty"`
	TestTolerance    int                             `json:"testTolerance,omitempty"`
	FilterInclude    string                          `json:"filterInclude,omitempty"`
	FilterExclude    string                          `json:"filterExclude,omitempty"`
	BindInterface    string                          `json:"bindInterface,omitempty"`
	Headers          []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"headers"`
}

func (h *MihomoHandler) handleNativeSubscriptionList(w http.ResponseWriter, _ *http.Request) {
	response.Success(w, map[string]interface{}{"items": h.nativeStore.ListSubscriptions()})
}

func (h *MihomoHandler) handleNativeSubscriptionCreate(w http.ResponseWriter, r *http.Request) {
	if err := h.checkMutationAllowed(); err != nil {
		writeNativeMutationError(w, err, "")
		return
	}
	var req nativeSubscriptionCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid JSON body", "INVALID_REQUEST")
		return
	}
	result, err := h.withNativeMutation(r.Context(), true, func() (interface{}, error) {
		sub, createErr := h.nativeStore.CreateSubscription(mihomonative.CreateSubscriptionInput{
			Name: req.Name, URL: req.URL, Inline: req.Inline, Format: req.Format,
			EnginePreference: req.EnginePreference, RefreshHours: req.RefreshHours, Enabled: req.Enabled,
			Mode: req.Mode, TestURL: req.TestURL, TestInterval: req.TestInterval, TestTolerance: req.TestTolerance,
			FilterInclude: req.FilterInclude, FilterExclude: req.FilterExclude, BindInterface: req.BindInterface,
			RoutingEngine: h.routingEngine(), Headers: nativeSubscriptionHeaders(req),
		})
		if createErr != nil {
			return nil, nativeInputError{createErr}
		}
		return sub, nil
	})
	if err != nil {
		writeNativeMutationError(w, err, "INVALID_SUBSCRIPTION")
		return
	}
	response.Success(w, result)
}

func nativeSubscriptionHeaders(req nativeSubscriptionCreateRequest) map[string][]string {
	headers := make(map[string][]string)
	for _, header := range req.Headers {
		name, value := strings.TrimSpace(header.Name), strings.TrimSpace(header.Value)
		if name != "" && value != "" {
			headers[name] = append(headers[name], value)
		}
	}
	return headers
}

func (h *MihomoHandler) handleNativeSubscriptionGet(w http.ResponseWriter, r *http.Request) {
	sub, err := h.nativeStore.GetSubscription(r.PathValue("id"))
	if err != nil {
		writeNativeMutationError(w, err, "INVALID_SUBSCRIPTION")
		return
	}
	response.Success(w, sub)
}

func (h *MihomoHandler) handleNativeSubscriptionUpdate(w http.ResponseWriter, r *http.Request) {
	if err := h.checkMutationAllowed(); err != nil {
		writeNativeMutationError(w, err, "")
		return
	}
	var req nativeSubscriptionCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid JSON body", "INVALID_REQUEST")
		return
	}
	result, err := h.withNativeMutation(r.Context(), true, func() (interface{}, error) {
		sub, updateErr := h.nativeStore.UpdateSubscription(r.PathValue("id"), mihomonative.UpdateSubscriptionInput{
			Name: req.Name, URL: req.URL, Inline: req.Inline, Format: req.Format,
			EnginePreference: req.EnginePreference, RefreshHours: req.RefreshHours, Enabled: req.Enabled,
			Mode: req.Mode, TestURL: req.TestURL, TestInterval: req.TestInterval, TestTolerance: req.TestTolerance,
			FilterInclude: req.FilterInclude, FilterExclude: req.FilterExclude, BindInterface: req.BindInterface,
			RoutingEngine: h.routingEngine(), Headers: nativeSubscriptionHeaders(req),
		})
		if updateErr != nil {
			if errors.Is(updateErr, mihomonative.ErrNotFound) {
				return nil, updateErr
			}
			return nil, nativeInputError{updateErr}
		}
		return sub, nil
	})
	if err != nil {
		writeNativeMutationError(w, err, "INVALID_SUBSCRIPTION")
		return
	}
	response.Success(w, result)
}

func (h *MihomoHandler) handleNativeSubscriptionDelete(w http.ResponseWriter, r *http.Request) {
	if err := h.checkMutationAllowed(); err != nil {
		writeNativeMutationError(w, err, "")
		return
	}
	id := r.PathValue("id")
	_, err := h.withNativeMutation(r.Context(), r.URL.Query().Get("apply") != "false", func() (interface{}, error) {
		if deleteErr := h.nativeStore.DeleteSubscription(id); deleteErr != nil {
			return nil, deleteErr
		}
		return map[string]bool{"deleted": true}, nil
	})
	if err != nil {
		writeNativeMutationError(w, err, "INVALID_SUBSCRIPTION")
		return
	}
	response.Success(w, map[string]bool{"deleted": true})
}

func (h *MihomoHandler) handleNativeSubscriptionRefresh(w http.ResponseWriter, r *http.Request) {
	if err := h.checkMutationAllowed(); err != nil {
		writeNativeMutationError(w, err, "")
		return
	}
	id := r.PathValue("id")
	if err := h.RefreshNativeSubscription(r.Context(), id); err != nil {
		if errors.Is(err, ErrRecoveryRequired) || errors.Is(err, mihomo.ErrRecoveryRequired) {
			writeNativeMutationError(w, err, "")
			return
		}
		status, code := http.StatusBadGateway, "PROVIDER_REFRESH_FAILED"
		if errors.Is(err, mihomonative.ErrNotFound) {
			status, code = http.StatusNotFound, "NOT_FOUND"
		} else if errors.Is(err, errNativeSubscriptionNotRuntimeProvider) {
			status, code = http.StatusConflict, "NOT_RUNTIME_PROVIDER"
		}
		response.ErrorWithStatus(w, status, err.Error(), code)
		return
	}
	response.Success(w, map[string]bool{"refreshed": true})
}

var errNativeSubscriptionNotRuntimeProvider = errors.New("only Mihomo provider subscriptions can be refreshed at runtime")

// RefreshNativeSubscription refreshes one Mihomo provider through its local
// controller. The same method is shared by the HTTP API and confirmed AI
// remediation actions.
func (h *MihomoHandler) RefreshNativeSubscription(ctx context.Context, id string) error {
	if err := h.checkMutationAllowed(); err != nil {
		return err
	}
	if h == nil || h.nativeStore == nil {
		return errors.New("mihomo native subscription store is unavailable")
	}
	sub, err := h.nativeStore.GetSubscription(id)
	if err != nil {
		return err
	}
	if sub.Format != mihomonative.FormatMihomoProvider || sub.ProviderName == "" {
		return errNativeSubscriptionNotRuntimeProvider
	}
	refresher := func() mihomo.MihomoProviderRefresher {
		h.nativeMu.Lock()
		defer h.nativeMu.Unlock()
		if h.providerRefresher != nil {
			return h.providerRefresher
		}
		if opRefresher, ok := h.op.(mihomo.MihomoProviderRefresher); ok {
			return opRefresher
		}
		return nil
	}()
	if refresher == nil {
		return errors.New("mihomo provider refresher is unavailable")
	}
	refreshErr := refresher.RefreshProvider(ctx, sub.ProviderName)
	recordErr := func() error {
		h.nativeMu.Lock()
		defer h.nativeMu.Unlock()
		return h.nativeStore.RecordSubscriptionRefresh(id, refreshErr)
	}()
	if recordErr != nil && refreshErr == nil {
		return recordErr
	}
	return refreshErr
}

// @Summary		Get Mihomo engine status
// @Description	Returns runtime and installation status of Mihomo engine including degraded state
// @Tags			mihomo
// @Produce		json
// @Success		200	{object}	APIEnvelope
// @Router			/mihomo/status [get]
func (h *MihomoHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	status, err := h.StatusSnapshot()
	degraded := h.mutationApplier != nil && h.mutationApplier.IsDegraded()
	payload := map[string]interface{}{
		"running":          status.Running,
		"degraded":         degraded,
		"pid":              status.PID,
		"binary":           status.Binary,
		"error":            status.Error,
		"engine":           "mihomo",
		"selected":         status.Selected,
		"enabled":          status.Enabled,
		"active":           status.Active,
		"installed":        status.Installed,
		"installAvailable": status.InstallAvailable,
		"updateAvailable":  status.UpdateAvailable,
		"currentVersion":   status.CurrentVersion,
		"version":          status.CurrentVersion,
		"requiredVersion":  status.RequiredVersion,
		"installState":     status.InstallState,
		"requiredBytes":    status.RequiredBytes,
		"freeBytes":        status.FreeBytes,
	}
	if err != nil {
		payload["settingsError"] = err.Error()
	}
	response.Success(w, payload)
}

func (h *MihomoHandler) handleInstall(w http.ResponseWriter, r *http.Request) {
	if err := h.checkMutationAllowed(); err != nil {
		writeNativeMutationError(w, err, "")
		return
	}
	if h.installer == nil {
		response.ErrorWithStatus(w, http.StatusNotImplemented, "mihomo installer not configured", "NOT_CONFIGURED")
		return
	}
	if err := h.installer.Install(r.Context()); err != nil {
		response.InternalError(w, fmt.Sprintf("install mihomo: %v", err))
		return
	}
	if h.bus != nil {
		h.bus.PublishInvalidated(events.ResourceSysInfo, "mihomo-installed")
	}
	status, _ := h.StatusSnapshot()
	response.Success(w, status)
}

func (h *MihomoHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if err := h.checkMutationAllowed(); err != nil {
		writeNativeMutationError(w, err, "")
		return
	}
	if h.installer == nil {
		response.ErrorWithStatus(w, http.StatusNotImplemented, "mihomo installer not configured", "NOT_CONFIGURED")
		return
	}
	wasRunning, _ := h.op.IsRunning()
	if wasRunning {
		_ = h.op.Stop()
	}
	if err := h.installer.Install(r.Context()); err != nil {
		if wasRunning {
			_ = h.op.Start()
		}
		response.InternalError(w, fmt.Sprintf("update mihomo: %v", err))
		return
	}
	if wasRunning {
		_ = h.op.Start()
	}
	if h.bus != nil {
		h.bus.PublishInvalidated(events.ResourceSysInfo, "mihomo-updated")
	}
	status, _ := h.StatusSnapshot()
	response.Success(w, status)
}

func (h *MihomoHandler) handleUninstall(w http.ResponseWriter, r *http.Request) {
	if err := h.checkMutationAllowed(); err != nil {
		writeNativeMutationError(w, err, "")
		return
	}
	if h.installer == nil {
		response.ErrorWithStatus(w, http.StatusNotImplemented, "mihomo installer not configured", "NOT_CONFIGURED")
		return
	}
	if running, _ := h.op.IsRunning(); running {
		_ = h.op.Stop()
	}
	if err := h.installer.Remove(r.Context()); err != nil {
		response.InternalError(w, fmt.Sprintf("remove mihomo: %v", err))
		return
	}
	if h.bus != nil {
		h.bus.PublishInvalidated(events.ResourceSysInfo, "mihomo-uninstalled")
	}
	status, _ := h.StatusSnapshot()
	response.Success(w, status)
}

func (h *MihomoHandler) handleReload(w http.ResponseWriter, r *http.Request) {
	if err := h.checkMutationAllowed(); err != nil {
		writeNativeMutationError(w, err, "")
		return
	}
	if h.settingsStore != nil {
		settings, err := h.settingsStore.Load()
		if err != nil {
			response.InternalError(w, "load settings: "+err.Error())
			return
		}
		selected := settings.SingboxRouter.RoutingEngine == "mihomo"
		enabled := settings.SingboxRouter.Enabled
		hasNativeListeners := h.nativeStore != nil && len(h.nativeStore.ConfigBridgeListeners()) > 0
		if !((selected && enabled) || hasNativeListeners) {
			response.ErrorWithStatus(w, http.StatusConflict, "mihomo is not active", "MIHOMO_INACTIVE")
			return
		}
	}
	reload := h.reloadFn
	if reload == nil {
		reload = h.op.Reload
	}
	if err := reload(); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]bool{"reloaded": true})
}

func (h *MihomoHandler) handleRestart(w http.ResponseWriter, r *http.Request) {
	if err := h.checkMutationAllowed(); err != nil {
		writeNativeMutationError(w, err, "")
		return
	}
	if err := h.Restart(); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]bool{"restarted": true})
}

// MihomoClashProxy forwards /api/mihomo/clash/* to the Mihomo API.
type MihomoClashProxy struct {
	op proxyengine.Engine
	h  *MihomoHandler
}

func NewMihomoClashProxy(op proxyengine.Engine, handlers ...*MihomoHandler) *MihomoClashProxy {
	p := &MihomoClashProxy{op: op}
	if len(handlers) > 0 {
		p.h = handlers[0]
	}
	return p
}

const mihomoClashPrefix = "/api/mihomo/clash"

func (p *MihomoClashProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	isMutating := r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete
	if isMutating && p.h != nil {
		if err := p.h.checkMutationAllowed(); err != nil {
			writeNativeMutationError(w, err, "")
			return
		}
	}

	upstreamPath := strings.TrimPrefix(r.URL.Path, mihomoClashPrefix)
	if upstreamPath == "" {
		upstreamPath = "/"
	}

	addr := "127.0.0.1:9090"
	var secret string
	if resolver, ok := p.op.(interface{ ControllerTarget() (string, string) }); ok && resolver != nil {
		a, s := resolver.ControllerTarget()
		if a != "" {
			addr = a
		}
		secret = s
	}

	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		p.proxyWebSocket(w, r, addr, secret, upstreamPath)
		return
	}
	p.proxyHTTP(w, r, addr, secret, upstreamPath)
}

var mihomoHTTPClient = &http.Client{Timeout: 30 * time.Second}

func (p *MihomoClashProxy) proxyHTTP(w http.ResponseWriter, r *http.Request, addr, secret, path string) {
	target := &url.URL{Scheme: "http", Host: addr, Path: path, RawQuery: r.URL.RawQuery}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for k, vv := range r.Header {
		if strings.EqualFold(k, "Authorization") {
			continue
		}
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := mihomoHTTPClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *MihomoClashProxy) proxyWebSocket(w http.ResponseWriter, r *http.Request, addr, secret, path string) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	clientConn, bufrw, err := hj.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer clientConn.Close()

	upstream, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	defer upstream.Close()

	target := &url.URL{Scheme: "http", Host: addr, Path: path, RawQuery: r.URL.RawQuery}
	req, _ := http.NewRequest(r.Method, target.String(), nil)
	for k, vv := range r.Header {
		if strings.EqualFold(k, "Authorization") {
			continue
		}
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}

	if err := req.Write(upstream); err != nil {
		return
	}

	errc := make(chan error, 2)
	go func() {
		_, err := io.Copy(upstream, bufrw)
		errc <- err
	}()
	go func() {
		_, err := io.Copy(clientConn, upstream)
		errc <- err
	}()
	<-errc
}

func (h *MihomoHandler) handleNativeReset(w http.ResponseWriter, r *http.Request) {
	if err := h.checkMutationAllowed(); err != nil {
		writeNativeMutationError(w, err, "")
		return
	}
	if h.nativeStore == nil {
		response.Error(w, "native store not initialized", "NATIVE_STORE_UNAVAILABLE")
		return
	}
	_, err := h.withNativeMutation(r.Context(), true, func() (interface{}, error) {
		return nil, h.nativeStore.Reset()
	})
	if err != nil {
		writeNativeMutationError(w, err, "RESET_FAILED")
		return
	}
	response.Success(w, map[string]interface{}{"success": true, "message": "Конфигурация Mihomo сброшена к заводским настройкам"})
}
