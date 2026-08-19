package api

import (
	"errors"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	tunnelservice "github.com/hoaxisr/awg-manager/internal/tunnel/service"
)

// ListOutbounds returns all composite outbounds.
//
//	@Summary		List singbox-router outbounds
//	@Description	Returns all composite outbounds (sing-box selectors/urltests over multiple base outbounds).
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxRouterOutboundsListResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/outbounds/list [get]
func (h *SingboxRouterHandler) ListOutbounds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	o, err := h.svc.ListCompositeOutbounds(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, o)
}

// AddOutbound creates a new composite outbound.
//
//	@Summary		Add singbox-router outbound
//	@Description	Creates a new composite outbound. The base outbounds it references must already exist.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterOutboundDTO	true	"Composite outbound payload"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/outbounds/add [post]
func (h *SingboxRouterHandler) AddOutbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var o router.Outbound
	if err := decodeBody(r, &o); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.AddCompositeOutbound(r.Context(), o); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("outbound-add", o.Tag, "composite outbound added: "+o.Tag)
	response.Success(w, map[string]bool{"ok": true})
}

// UpdateOutbound replaces the composite outbound identified by tag.
//
//	@Summary		Update singbox-router outbound
//	@Description	Replaces the composite outbound identified by tag with the provided one.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterOutboundUpdateRequest	true	"Tag + replacement outbound"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/outbounds/update [post]
func (h *SingboxRouterHandler) UpdateOutbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body struct {
		Tag      string          `json:"tag"`
		Outbound router.Outbound `json:"outbound"`
	}
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.UpdateCompositeOutbound(r.Context(), body.Tag, body.Outbound); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("outbound-update", body.Tag, "composite outbound updated: "+body.Tag)
	response.Success(w, map[string]bool{"ok": true})
}

// DeleteOutbound removes the composite outbound identified by tag.
//
//	@Summary		Delete singbox-router outbound
//	@Description	Removes the composite outbound identified by tag. Refuses if any rule references it; pass force=true to override.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterOutboundDeleteRequest	true	"Tag + optional force flag"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		409		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/outbounds/delete [post]
func (h *SingboxRouterHandler) DeleteOutbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body struct {
		Tag   string `json:"tag"`
		Force bool   `json:"force"`
	}
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := tunnelservice.CheckOutboundTagReferences(body.Tag, body.Tag, h.deviceProxyRefs, h.routerRefs); err != nil {
		var refErr tunnelservice.ErrTunnelReferenced
		if errors.As(err, &refErr) {
			WriteTunnelReferenced(w, refErr)
			return
		}
	}
	if err := h.svc.DeleteCompositeOutbound(r.Context(), body.Tag, body.Force); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("outbound-delete", body.Tag, "composite outbound deleted: "+body.Tag)
	response.Success(w, map[string]bool{"ok": true})
}

// ListPresets returns the catalog of built-in singbox-router presets.
//
//	@Summary		List singbox-router presets
//	@Description	Returns the catalog of built-in presets the user can apply (each preset = a curated bundle of rules + rulesets).
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxRouterPresetsListResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Router			/singbox/router/presets/list [get]
func (h *SingboxRouterHandler) ListPresets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	list, err := h.svc.ListPresets()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, list)
}

// ApplyPreset materialises the named preset against the chosen outbound.
//
//	@Summary		Apply singbox-router preset
//	@Description	Materialises the preset (id) into rules + rulesets, routing matched traffic via the selected outbound. Existing rules with the same tag are overwritten.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterApplyPresetRequest	true	"Preset id + target outbound"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/presets/apply [post]
func (h *SingboxRouterHandler) ApplyPreset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body struct {
		ID       string `json:"id"`
		Outbound string `json:"outbound"`
	}
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.ApplyPreset(r.Context(), body.ID, body.Outbound); err != nil {
		h.handleErr(w, "request", err)
		return
	}
	h.log.Info("preset-apply", body.ID, "preset applied: "+body.ID+" (outbound: "+body.Outbound+")")
	response.Success(w, map[string]bool{"ok": true})
}

// ListWANInterfaces returns all router WAN interfaces for the
// WAN-binding picker. No up/down filtering — the UI shows every
// interface and the user picks.
//
//	@Summary		List WAN interfaces
//	@Description	Returns all router WAN interfaces (no up/down filtering) used by the WAN-binding picker in singbox-router settings. Always a JSON array, never null. The `name` field is the kernel system-name and is the value that should be persisted into `wanInterface`.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxRouterWANInterfacesListResponse
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/wan-interfaces [get]
func (h *SingboxRouterHandler) ListWANInterfaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	ifaces, err := h.svc.ListWANInterfaces(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if ifaces == nil {
		ifaces = []router.WANInterfaceInfo{}
	}
	response.Success(w, ifaces)
}

// ListBindableInterfaces returns interfaces a user can bind a direct outbound to.
//
//	@Summary		List bindable interfaces for direct outbounds
//	@Description	Returns router interfaces (minus our own and AWG/WG auto-covered) that a direct outbound can bind to. Fields id and priority are not populated for this endpoint (only name, label, up are meaningful).
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxRouterWANInterfacesListResponse
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/bindable-interfaces [get]
func (h *SingboxRouterHandler) ListBindableInterfaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	ifaces, err := h.svc.ListBindableInterfaces(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if ifaces == nil {
		ifaces = []router.WANInterfaceInfo{}
	}
	response.Success(w, ifaces)
}

// ListIngressEligibleInterfaces returns interfaces eligible for sing-box ingress-scope.
//
//	@Summary		List ingress-eligible interfaces
//	@Description	Returns router interfaces eligible for sing-box ingress-scope (bindable minus WAN minus LAN bridges). Used by the ingress multiselect in singbox-router settings.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	SingboxRouterWANInterfacesListResponse
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/ingress-eligible-interfaces [get]
func (h *SingboxRouterHandler) ListIngressEligibleInterfaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	ifaces, err := h.svc.ListIngressEligibleInterfaces(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if ifaces == nil {
		ifaces = []router.WANInterfaceInfo{}
	}
	response.Success(w, ifaces)
}
