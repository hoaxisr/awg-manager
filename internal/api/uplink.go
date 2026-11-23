package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

// UplinkHandler is registered behind the same authentication as tunnel CRUD.
type UplinkHandler struct{ Controller *uplink.Controller }

// UplinkSnapshotDTO describes the Multi-WAN policy snapshot returned by GET /uplink/policies.
type UplinkSnapshotDTO struct {
	TargetError       string                         `json:"targetError,omitempty"`
	Targets           []uplink.TargetInfo            `json:"targets"`
	Entries           []uplink.Entry                 `json:"entries"`
	Providers         []uplink.Provider              `json:"providers"`
	AutomaticFailback bool                           `json:"automaticFailback"`
	Capabilities      map[string]uplink.Capabilities `json:"capabilities"`
}

// UplinkSnapshotResponse is the envelope for GET /uplink/policies.
type UplinkSnapshotResponse struct {
	Success bool              `json:"success" example:"true"`
	Data    UplinkSnapshotDTO `json:"data"`
}

// UplinkPolicyResponse is the envelope for PUT /uplink/policies.
type UplinkPolicyResponse struct {
	Success bool          `json:"success" example:"true"`
	Data    uplink.Policy `json:"data"`
}

// UplinkEntriesResponse is the envelope for POST /uplink/switch.
type UplinkEntriesResponse struct {
	Success bool           `json:"success" example:"true"`
	Data    []uplink.Entry `json:"data"`
}

// UplinkWANDNSResponse is the envelope for GET and PUT /uplink/dns.
type UplinkWANDNSResponse struct {
	Success bool                `json:"success" example:"true"`
	Data    uplink.WANDNSConfig `json:"data"`
}

// UplinkDeleteRequest is the body for DELETE /uplink/policies.
type UplinkDeleteRequest struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
}

// UplinkSwitchRequest is the body for POST /uplink/switch.
type UplinkSwitchRequest struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Revision uint64 `json:"revision"`
}

func (h *UplinkHandler) DNS(w http.ResponseWriter, r *http.Request) {
	if h.Controller.WANDNS == nil {
		uplinkError(w, uplink.ErrUnsupported)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.handleGetDNS(w, r)
	case http.MethodPut:
		h.handlePutDNS(w, r)
	default:
		response.MethodNotAllowed(w)
	}
}

// handleGetDNS returns per-WAN manager DNS overrides.
//
//	@Summary		Get Multi-WAN provider DNS overrides
//	@Tags			uplink
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	UplinkWANDNSResponse
//	@Failure		401	{object}	APIErrorEnvelope
//	@Failure		422	{object}	APIErrorEnvelope
//	@Router			/uplink/dns [get]
func (h *UplinkHandler) handleGetDNS(w http.ResponseWriter, _ *http.Request) {
	response.Success(w, h.Controller.WANDNS.Snapshot())
}

// handlePutDNS updates per-WAN manager DNS overrides.
//
//	@Summary		Update Multi-WAN provider DNS override
//	@Tags			uplink
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			request	body		uplink.WANDNSUpdate	true	"WAN provider DNS override"
//	@Success		200		{object}	UplinkWANDNSResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		401		{object}	APIErrorEnvelope
//	@Failure		409		{object}	APIErrorEnvelope
//	@Failure		422		{object}	APIErrorEnvelope
//	@Router			/uplink/dns [put]
func (h *UplinkHandler) handlePutDNS(w http.ResponseWriter, r *http.Request) {
	var request uplink.WANDNSUpdate
	if !uplinkDecode(w, r, &request) {
		return
	}
	saved, err := h.Controller.UpdateWANDNS(r.Context(), request)
	if err != nil {
		uplinkError(w, err)
		return
	}
	response.Success(w, saved)
}

func uplinkDecode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		response.BadRequest(w, "Invalid uplink request")
		return false
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		response.BadRequest(w, "Expected one JSON object")
		return false
	}
	return true
}

func uplinkError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, uplink.ErrConflict) {
		status = http.StatusConflict
	}
	if errors.Is(err, uplink.ErrNotFound) {
		status = http.StatusNotFound
	}
	if errors.Is(err, uplink.ErrUnsupported) {
		status = http.StatusUnprocessableEntity
	}
	response.ErrorWithStatus(w, status, err.Error(), "UPLINK_ERROR")
}

// Policies serves desired policies, provider IDs, and observed runtime status.
func (h *UplinkHandler) Policies(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleGetPolicies(w, r)
	case http.MethodPut:
		h.handlePutPolicy(w, r)
	case http.MethodDelete:
		h.handleDeletePolicy(w, r)
	default:
		response.MethodNotAllowed(w)
	}
}

// handleGetPolicies returns Multi-WAN targets, policies, providers, and capabilities.
//
//	@Summary		List Multi-WAN uplink policies and targets
//	@Tags			uplink
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	UplinkSnapshotResponse
//	@Failure		401	{object}	APIErrorEnvelope
//	@Router			/uplink/policies [get]
func (h *UplinkHandler) handleGetPolicies(w http.ResponseWriter, r *http.Request) {
	targets, err := h.Controller.Targets(r.Context())
	targetError := ""
	if err != nil {
		targetError = err.Error()
	}
	capabilities := h.Controller.EngineCapabilities()
	automatic := false
	for _, cap := range capabilities {
		automatic = automatic || cap.AutomaticFailback
	}
	response.Success(w, UplinkSnapshotDTO{
		TargetError:       targetError,
		Targets:           targets,
		Entries:           h.Controller.List(),
		Providers:         h.Controller.Providers(),
		AutomaticFailback: automatic,
		Capabilities:      capabilities,
	})
}

// handlePutPolicy creates or updates a Multi-WAN uplink policy.
//
//	@Summary		Save a Multi-WAN uplink policy
//	@Tags			uplink
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			request	body		uplink.Policy	true	"Multi-WAN policy"
//	@Success		200		{object}	UplinkPolicyResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		401		{object}	APIErrorEnvelope
//	@Failure		404		{object}	APIErrorEnvelope
//	@Failure		409		{object}	APIErrorEnvelope
//	@Failure		422		{object}	APIErrorEnvelope
//	@Router			/uplink/policies [put]
func (h *UplinkHandler) handlePutPolicy(w http.ResponseWriter, r *http.Request) {
	var p uplink.Policy
	if !uplinkDecode(w, r, &p) {
		return
	}
	saved, err := h.Controller.Put(r.Context(), p)
	if err != nil {
		uplinkError(w, err)
		return
	}
	response.Success(w, saved)
}

// handleDeletePolicy removes a Multi-WAN uplink policy.
//
//	@Summary		Delete a Multi-WAN uplink policy
//	@Tags			uplink
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			request	body		UplinkDeleteRequest	true	"Policy ID and revision"
//	@Success		200		{object}	APIEnvelope
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		401		{object}	APIErrorEnvelope
//	@Failure		404		{object}	APIErrorEnvelope
//	@Failure		409		{object}	APIErrorEnvelope
//	@Router			/uplink/policies [delete]
func (h *UplinkHandler) handleDeletePolicy(w http.ResponseWriter, r *http.Request) {
	var req UplinkDeleteRequest
	if !uplinkDecode(w, r, &req) {
		return
	}
	if err := h.Controller.Delete(r.Context(), req.ID, req.Revision); err != nil {
		uplinkError(w, err)
		return
	}
	response.Success(w, nil)
}

// Switch manually moves a Multi-WAN policy onto the requested WAN provider.
//
//	@Summary		Manually switch Multi-WAN policy provider
//	@Tags			uplink
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			request	body		UplinkSwitchRequest	true	"Target provider switch"
//	@Success		200		{object}	UplinkEntriesResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		401		{object}	APIErrorEnvelope
//	@Failure		404		{object}	APIErrorEnvelope
//	@Failure		409		{object}	APIErrorEnvelope
//	@Failure		422		{object}	APIErrorEnvelope
//	@Router			/uplink/switch [post]
func (h *UplinkHandler) Switch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req UplinkSwitchRequest
	if !uplinkDecode(w, r, &req) {
		return
	}
	if err := h.Controller.Switch(r.Context(), req.ID, req.Provider, req.Revision); err != nil {
		uplinkError(w, err)
		return
	}
	response.Success(w, h.Controller.List())
}
