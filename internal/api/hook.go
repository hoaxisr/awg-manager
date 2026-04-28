package api

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/events"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// HookDispatcher is the subset of events.Dispatcher that HookHandler
// uses. Interface so tests can inject a fake.
type HookDispatcher interface {
	Enqueue(e events.Event)
}

// HookWANModel is the narrow surface HookHandler needs from the WAN
// model. Kept local so api/hook.go doesn't depend on *wan.Model.
type HookWANModel interface {
	SetUp(kernelName string, up bool)
}

// TunnelHookInvalidator is invoked on ifcreated / ifdestroyed hooks so
// the handler can drop stale NDMS caches and publish a
// `resource:invalidated` hint for the tunnels resource. Every connected
// SSE client then refetches `/api/tunnels/all` and the UI drops/adds
// tunnel cards without a browser refresh.
type TunnelHookInvalidator func(ctx context.Context)

// HookHandler handles NDM hook events.
type HookHandler struct {
	svc            TunnelService
	orch           *orchestrator.Orchestrator
	dispatcher     HookDispatcher // may be nil until SetDispatcher is called
	wanModel       HookWANModel   // may be nil until SetWANModel is called
	refreshTunnels TunnelHookInvalidator
	log            *logging.ScopedLogger
	// selfCreateGate counts in-flight awg-manager-initiated NDMS interface
	// creations. While > 0, ifcreated hook events suppress their automatic
	// snapshot rebroadcast — the caller (importer / Create path) is
	// responsible for publishing a fresh snapshot AFTER it has persisted
	// the tunnel to awg-manager's store. Otherwise the hook-triggered
	// snapshot fires before the Save and the new NDMS interface appears
	// briefly in the "system tunnels" list as a ghost duplicate of the
	// managed tunnel.
	selfCreateGate atomic.Int32
}

// EnterSelfCreate marks the start of an awg-manager-initiated NDMS
// interface creation. Pair with ExitSelfCreate via defer.
func (h *HookHandler) EnterSelfCreate() { h.selfCreateGate.Add(1) }

// ExitSelfCreate marks the end of an awg-manager-initiated NDMS
// interface creation. Callers MUST publish a fresh tunnels invalidation
// hint themselves after this (typically via TunnelsHandler.publishTunnelList)
// so UIs see the finalized state.
func (h *HookHandler) ExitSelfCreate() { h.selfCreateGate.Add(-1) }

// NewHookHandler creates a new hook event handler.
func NewHookHandler(svc TunnelService, orch *orchestrator.Orchestrator, appLogger logging.AppLogger) *HookHandler {
	return &HookHandler{
		svc:  svc,
		orch: orch,
		log:  logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubBoot),
	}
}

// SetDispatcher wires an events.Dispatcher for hook-driven cache
// invalidation. Call after construction (typically from server.New).
func (h *HookHandler) SetDispatcher(d HookDispatcher) {
	h.dispatcher = d
}

// SetWANModel wires the WAN model so iflayerchanged layer=ipv4 hooks
// can update WAN interface up/down state in-memory before dispatching
// EventWANUp/Down to the orchestrator.
func (h *HookHandler) SetWANModel(m HookWANModel) {
	h.wanModel = m
}

// SetTunnelRefresher wires the callback that invalidates NDMS caches
// and publishes a tunnels `resource:invalidated` hint on ifcreated /
// ifdestroyed. Without it, the UI keeps showing cards for tunnels that
// NDMS has already torn down (reported bug).
func (h *HookHandler) SetTunnelRefresher(fn TunnelHookInvalidator) {
	h.refreshTunnels = fn
}

// HandleNDMS is the unified hook endpoint. The shared forwarder script
// installed into /opt/etc/ndm/{iflayerchanged,ifcreated,ifdestroyed,
// ifipchanged}.d/ POSTs here with a `type` discriminator. The handler
// parses the form into a typed events.Event, enqueues it into the
// Dispatcher for cache invalidation, and (for iflayerchanged only) also
// forwards to the orchestrator for tunnel-lifecycle decisions.
//
// POST /api/hook/ndms
//   type=iflayerchanged|ifcreated|ifdestroyed|ifipchanged
//   id=<ndms-interface-id>
//   system_name=<kernel-name>
//   layer=<conf|link|ipv4|ipv6|ctrl>      (layerchanged only)
//   level=<running|disabled|...>          (layerchanged only)
//   address=<ipv4>                        (ipchanged only)
//   up=<0|1>
//   connected=<0|1>
//
//	@Summary		NDMS shell hook
//	@Description	Called from router scripts (public). Form fields: type, id, system_name, layer, etc.
//	@Tags			hook
//	@Accept			x-www-form-urlencoded
//	@Produce		json
//	@Param			type	formData	string	true	"Event type (iflayerchanged, ifcreated, ...)"
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/hook/ndms [post]
func (h *HookHandler) HandleNDMS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if err := r.ParseForm(); err != nil {
		response.BadRequest(w, "parse form: "+err.Error())
		return
	}

	typeStr := r.PostForm.Get("type")
	event := events.Event{
		Type:       events.EventType(typeStr),
		ID:         r.PostForm.Get("id"),
		SystemName: r.PostForm.Get("system_name"),
		Layer:      r.PostForm.Get("layer"),
		Level:      r.PostForm.Get("level"),
		Address:    r.PostForm.Get("address"),
		Up:         r.PostForm.Get("up") == "1" || r.PostForm.Get("up") == "true",
		Connected:  r.PostForm.Get("connected") == "1" || r.PostForm.Get("connected") == "true",
	}

	switch event.Type {
	case events.EventIfLayerChanged, events.EventIfCreated,
		events.EventIfDestroyed, events.EventIfIPChanged:
		// OK
	default:
		response.BadRequest(w, "unknown hook type: "+typeStr)
		return
	}

	// 1) Enqueue into Dispatcher for cache invalidation (async, non-blocking).
	if h.dispatcher != nil {
		h.dispatcher.Enqueue(event)
	}

	// 1b) On interface create/destroy, rebroadcast the tunnel list so
	// every connected UI client drops/adds the card without a browser
	// refresh. Runs in a goroutine so the hook POST acks immediately.
	//
	// Exception: if awg-manager is currently creating an interface itself
	// (EnterSelfCreate was called), the corresponding ifcreated would fire
	// before our code has persisted the tunnel to our store. Publishing a
	// snapshot at that moment would show the new interface in the "system"
	// list (because managedNativeWGNames can't see a tunnel that isn't in
	// the store yet) — a ghost duplicate that vanishes on next refresh.
	// Skip; the creator publishes its own snapshot after Save.
	if event.Type == events.EventIfCreated && h.selfCreateGate.Load() > 0 {
		// Self-initiated creation: skip auto-refresh.
	} else if event.Type == events.EventIfCreated || event.Type == events.EventIfDestroyed {
		if h.refreshTunnels != nil {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				h.refreshTunnels(ctx)
			}()
		}
	}

	// 2) For iflayerchanged, route to the orchestrator:
	//    - layer=conf → NDMS hook path (tunnel lifecycle)
	//    - layer=ipv4 → WAN model update + EventWANUp/Down
	if event.Type == events.EventIfLayerChanged {
		if event.Layer == "ipv4" {
			h.handleWANLayerEvent(event)
		} else if h.orch != nil {
			go func(e events.Event) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := h.orch.HandleEvent(ctx, orchestrator.Event{
					Type:     orchestrator.EventNDMSHook,
					NDMSName: e.ID,
					Layer:    e.Layer,
					Level:    e.Level,
				}); err != nil {
					h.log.Warn("hook", e.ID, "orchestrator HandleEvent failed: "+err.Error())
				}
			}(event)
		}
	}

	h.log.Info("hook", event.ID, fmt.Sprintf("ndms: type=%s layer=%s level=%s", event.Type, event.Layer, event.Level))
	response.Success(w, map[string]interface{}{"ok": true})
}

// handleWANLayerEvent processes an iflayerchanged hook with layer=ipv4.
// Updates the WAN model synchronously so the orchestrator's WAN-up
// decision sees the fresh state, then dispatches EventWANUp/Down in a
// goroutine (same 60s timeout the legacy /api/wan/event handler used).
//
// Skips VPN/tunnel kernel names (nwg*, opkgtun*, awg*, wg*, wireguard*,
// ipsec*, sstp*, openvpn*, proxy*) — they fire ipv4-layer too but aren't
// WAN. If we didn't skip, wanModel.SetUp would trigger a repopulate
// storm and the orchestrator would treat tunnel events as WAN events.
func (h *HookHandler) handleWANLayerEvent(e events.Event) {
	kernelName := e.SystemName
	if kernelName == "" {
		// Can't update WAN model without a kernel name.
		return
	}
	if ndmsquery.IsNonISPInterface(kernelName) {
		return
	}
	if h.wanModel == nil {
		return
	}
	up := e.Level == "running"

	// Sync WAN model update — must happen before the orch decides
	// whether any WAN is up. SetUp handles hot-plug via repopulateFn.
	h.wanModel.SetUp(kernelName, up)

	if h.orch == nil {
		return
	}
	action := "up"
	evType := orchestrator.EventWANUp
	if !up {
		action = "down"
		evType = orchestrator.EventWANDown
	}
	go func(iface, act string, et orchestrator.EventType) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := h.orch.HandleEvent(ctx, orchestrator.Event{
			Type:     et,
			WANIface: iface,
		}); err != nil {
			h.log.Warn("hook", iface, "orchestrator WAN "+act+" failed: "+err.Error())
		}
	}(kernelName, action, evType)
}
