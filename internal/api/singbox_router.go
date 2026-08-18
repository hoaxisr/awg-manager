package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/storage"
	tunnelservice "github.com/hoaxisr/awg-manager/internal/tunnel/service"
)

// ── Response DTOs ────────────────────────────────────────────────

// ── Request DTOs ─────────────────────────────────────────────────

type SingboxRouterHandler struct {
	svc             router.Service
	log             *logging.ScopedLogger
	deviceProxyRefs tunnelservice.DeviceProxyRefChecker
	routerRefs      tunnelservice.RouterRefChecker
}

func NewSingboxRouterHandler(svc router.Service, appLogger logging.AppLogger) *SingboxRouterHandler {
	return &SingboxRouterHandler{
		svc: svc,
		log: logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubSingboxRouter),
	}
}

// SetOutboundRefCheckers wires device-proxy and router reference guards for
// composite-outbound deletion (refuse 409 when the tag is still selected by a
// device-proxy instance).
func (h *SingboxRouterHandler) SetOutboundRefCheckers(dp tunnelservice.DeviceProxyRefChecker, r tunnelservice.RouterRefChecker) {
	h.deviceProxyRefs = dp
	h.routerRefs = r
}

// GetStatus returns the current sing-box router engine status.
//
//	@Summary		Get sing-box router status
//	@Description	Returns the singbox-router status snapshot (running, mode, policy/iptables state, rule/ruleset/outbound counts).
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxRouterStatusResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/status [get]
func (h *SingboxRouterHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	st, err := h.svc.GetStatus(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, st)
}

// Enable starts the singbox-router engine and installs iptables/policy rules.
//
//	@Summary		Enable singbox-router
//	@Description	Starts the singbox-router engine and installs iptables/policy rules. Returns 400 with code POLICY_NOT_CONFIGURED or POLICY_MISSING when the router policy mode is incomplete. Returns 503 SINGBOX_NOT_READY when sing-box did not become ready within the boot-wait window — iptables install is deliberately skipped to avoid orphaning DNS:53 redirects (issue #221).
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	OkResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Failure		503	{object}	APIErrorEnvelope	"sing-box did not come up in time"
//	@Router			/singbox/router/enable [post]
func (h *SingboxRouterHandler) Enable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if err := h.svc.Enable(r.Context()); err != nil {
		if errors.Is(err, router.ErrPolicyNotConfigured) {
			response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "POLICY_NOT_CONFIGURED")
			return
		}
		if errors.Is(err, router.ErrPolicyMissing) {
			response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "POLICY_MISSING")
			return
		}
		if errors.Is(err, router.ErrSingboxNotReady) {
			response.ErrorWithStatus(w, http.StatusServiceUnavailable, err.Error(), "SINGBOX_NOT_READY")
			return
		}
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("enable", "", "Sing-box router enabled")
	response.Success(w, map[string]bool{"ok": true})
}

// Disable stops the singbox-router engine and uninstalls iptables/policy rules.
//
//	@Summary		Disable singbox-router
//	@Description	Stops the singbox-router engine and uninstalls iptables/policy rules. Idempotent.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	OkResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/disable [post]
func (h *SingboxRouterHandler) Disable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if err := h.svc.Disable(r.Context()); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	h.log.Info("disable", "", "Sing-box router disabled")
	response.Success(w, map[string]bool{"ok": true})
}

// SwitchMode orchestrates a routing-mode transition (off↔tproxy↔fakeip-tun↔policy-tun)
// with directional fail-closed rollback. Progress is reported out-of-band as
// "singbox-router:transition" events on the existing events SSE stream
// (GET /events) — no new stream endpoint.
//
//	@Summary		Switch singbox-router routing mode
//	@Description	Orchestrates a routing-mode transition (off↔tproxy↔fakeip-tun↔policy-tun): tears down the old mode then brings up the new one, with directional fail-closed rollback. Per-step progress is published as "singbox-router:transition" events on the existing GET /events SSE stream (see SingboxRouterTransitionData). Returns 400 INVALID_MODE for an unknown mode.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterModeRequest	true	"Target routing mode"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/mode [post]
func (h *SingboxRouterHandler) SwitchMode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body SingboxRouterModeRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.BadRequest(w, "invalid request body: "+err.Error())
		return
	}
	switch body.Mode {
	case "off", "tproxy", "fakeip-tun", "policy-tun":
	default:
		response.ErrorWithStatus(w, http.StatusBadRequest,
			"invalid routing mode (want off|tproxy|fakeip-tun|policy-tun)", "INVALID_MODE")
		return
	}
	mode := body.Mode
	go func() {
		if err := h.svc.SwitchRoutingMode(context.Background(), mode); err != nil {
			// Terminal errors are also emitted as singbox-router:transition SSE
			// events, but the bus silently drops them with zero subscribers —
			// this log line is the only guaranteed trace of a failed switch.
			h.log.Warn("mode-switch", mode, "SwitchRoutingMode failed: "+err.Error())
		}
	}()
	// Keep the documented OkResponse shape ({"ok":true}); 200 here means
	// "transition accepted and started", progress/terminal state arrives via
	// the singbox-router:transition SSE events.
	h.log.Info("mode-switch", mode, "mode switch requested: "+mode)
	response.Success(w, map[string]bool{"ok": true})
}

// GetSettings reads singbox-router settings (policy-mode, defaults, etc.).
//
//	@Summary		Get singbox-router settings
//	@Description	Reads the current singbox-router settings (policy mode, defaults, ...).
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxRouterSettingsResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/settings [get]
func (h *SingboxRouterHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	s, err := h.svc.GetSettings(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, s)
}

// PutSettings persists singbox-router settings.
//
//	@Summary		Update singbox-router settings
//	@Description	Persists singbox-router settings. The router is restarted only when fields that affect the running config change. `routingMode`/`enabled` in the body are ignored — the routing mode is changed only via POST /singbox/router/mode.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterSettingsData	true	"Singbox-router settings payload"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/settings [post]
//	@Router			/singbox/router/settings [put]
func (h *SingboxRouterHandler) PutSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		response.MethodNotAllowed(w)
		return
	}
	// Теневой указатель различает «поле не прислано» и «прислано пустым»:
	// пустой fakeipPool6 ЗНАЧИМ (выключает v6), и Go zero "" от отсутствующего
	// ключа молча выключал бы его (узор дефекта PR #757, routingMode). Правило
	// encoding/json: поле меньшей глубины перехватывает ключ у embedded-
	// структуры — body.FakeIPPool6 видит JSON, embedded-поле остаётся "".
	var body struct {
		storage.SingboxRouterSettings
		FakeIPPool6 *string `json:"fakeipPool6"`
	}
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	sr := body.SingboxRouterSettings
	if body.FakeIPPool6 != nil {
		sr.FakeIPPool6 = *body.FakeIPPool6
	} else {
		// Absent = не менять: подставляем текущее значение (нормализация
		// возвращает pool6 дословно, включая "").
		cur, err := h.svc.GetSettings(r.Context())
		if err != nil {
			h.handleErr(w, "request", err)
			return
		}
		sr.FakeIPPool6 = cur.FakeIPPool6
	}
	if err := h.svc.UpdateSettings(r.Context(), sr); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("settings", "", "Sing-box router settings updated")
	response.Success(w, map[string]bool{"ok": true})
}

// PolicyTunNATPreview lists router segments with their current NAT mode.
//
//	@Summary		Preview policy-tun source-preserve segments
//	@Description	Returns router segments with their current NAT mode (dynamic `ip nat` / static `ip static <seg> <wan>` / none) — the editable "what will change" preview behind the policy-tun source-preserve toggle. Port-forwarding `ip static` entries (no to-interface) and the manager's own OpkgTun interfaces are excluded.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxRouterNATPreviewResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/policy-tun/nat-preview [get]
func (h *SingboxRouterHandler) PolicyTunNATPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	preview, err := h.svc.PolicyTunNATPreview(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if preview.Segments == nil {
		preview.Segments = []router.NATSegmentInfo{}
	}
	response.Success(w, map[string]any{
		"segments": preview.Segments,
		"egresses": preview.Egresses,
	})
}

// ── Staging DTOs ──────────────────────────────────────────────────

func (h *SingboxRouterHandler) handleErr(w http.ResponseWriter, action string, err error) {
	h.log.Warn(action, "", err.Error())
	switch {
	case errors.Is(err, router.ErrNetfilterComponentMissing),
		errors.Is(err, router.ErrIPTablesModTProxyMissing):
		response.Error(w, err.Error(), "NETFILTER_MISSING")
	case errors.Is(err, router.ErrRuleSetReferenced),
		errors.Is(err, router.ErrOutboundReferenced),
		errors.Is(err, router.ErrRuleSetTagConflict),
		errors.Is(err, router.ErrOutboundTagConflict),
		errors.Is(err, router.ErrDNSServerTagConflict),
		errors.Is(err, router.ErrDNSServerReferenced):
		response.Error(w, err.Error(), "CONFLICT")
	case errors.Is(err, router.ErrRuleIndexOutOfRange),
		errors.Is(err, router.ErrDNSRuleIndexOutOfRange),
		errors.Is(err, router.ErrDNSServerNotFound),
		errors.Is(err, router.ErrRuleSetNotFound),
		errors.Is(err, router.ErrOutboundNotFound):
		response.Error(w, err.Error(), "NOT_FOUND")
	case errors.Is(err, router.ErrBulkEmptyIndices),
		errors.Is(err, router.ErrBulkEmptyTags):
		// 400: empty selection for a bulk rule/ruleset mutation — nothing to do.
		response.Error(w, err.Error(), "BULK_EMPTY_SELECTION")
	case errors.Is(err, router.ErrBulkInvalidSelection):
		// 400: non-empty but invalid bulk selection (duplicate index/tag,
		// non-route rule, unknown outbound tag, non-remote rule set).
		response.Error(w, err.Error(), "BULK_INVALID_SELECTION")
	case errors.Is(err, router.ErrTransitionInProgress):
		response.Error(w, err.Error(), "TRANSITION_IN_PROGRESS")
	case errors.Is(err, router.ErrCompositeMemberUnknown):
		// 400: member-тег композита не существует ни в одном каталоге (#567).
		response.Error(w, err.Error(), "COMPOSITE_MEMBER_UNKNOWN")
	case errors.Is(err, router.ErrInvalidMatchers),
		errors.Is(err, router.ErrDNSInvalidServer):
		response.Error(w, err.Error(), "INVALID_MATCHERS")
	case errors.Is(err, router.ErrQoSClassesInvalid):
		// 400 with the detailed Russian message (DSCP range/duplicate/limit/
		// outbound) intact so the settings UI can surface it verbatim.
		response.Error(w, err.Error(), "QOS_CLASSES_INVALID")
	case errors.Is(err, router.ErrDNSChainTagReserved):
		// 400: пространство тегов awgm-dns-* принадлежит оверлею DNS-пресета —
		// занять его пользовательским правилом нельзя.
		response.Error(w, err.Error(), "DNS_TAG_RESERVED")
	case errors.Is(err, router.ErrDNSRuleManaged):
		// 400: правило принадлежит оверлею DNS-пресета — править/двигать его
		// пользователь не может.
		response.Error(w, err.Error(), "DNS_RULE_MANAGED")
	case errors.Is(err, router.ErrReservedInboundTag):
		// 400: user rules must not claim the reserved qos-* inbound namespace
		// (they'd be inert shadow rules — the managed slot merges first).
		response.Error(w, err.Error(), "RESERVED_INBOUND_TAG")
	default:
		response.InternalError(w, err.Error())
	}
}
