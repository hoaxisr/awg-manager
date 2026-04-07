package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// AccessPolicyHandler handles access policy CRUD and device assignment operations.
type AccessPolicyHandler struct {
	svc accesspolicy.Service
	bus *events.Bus
}

// SetEventBus sets the event bus for SSE publishing.
func (h *AccessPolicyHandler) SetEventBus(bus *events.Bus) { h.bus = bus }

// publishPoliciesUpdated publishes the full policy list via SSE (best-effort).
func (h *AccessPolicyHandler) publishPoliciesUpdated(ctx context.Context) {
	if h.bus == nil {
		return
	}
	list, err := h.svc.List(ctx)
	if err != nil {
		return
	}
	h.bus.Publish("routing:policies-updated", list)
}

// publishDevicesUpdated publishes the full device list via SSE (best-effort).
func (h *AccessPolicyHandler) publishDevicesUpdated(ctx context.Context) {
	if h.bus == nil {
		return
	}
	devices, err := h.svc.ListDevices(ctx)
	if err != nil {
		return
	}
	h.bus.Publish("routing:policy-devices-updated", devices)
}

// NewAccessPolicyHandler creates a new access policy handler.
func NewAccessPolicyHandler(svc accesspolicy.Service) *AccessPolicyHandler {
	return &AccessPolicyHandler{svc: svc}
}

// List returns all access policies.
// GET /api/access-policies
func (h *AccessPolicyHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	ctx := r.Context()
	if r.URL.Query().Get("refresh") == "true" {
		ctx = accesspolicy.ContextWithForceRefresh(ctx)
	}
	policies, err := h.svc.List(ctx)
	if err != nil {
		response.Error(w, err.Error(), "LIST_FAILED")
		return
	}
	response.Success(w, response.MustNotNil(policies))
}

// Create creates a new access policy.
// POST /api/access-policies/create
// Body: {"description":"..."}
func (h *AccessPolicyHandler) Create(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		response.Error(w, "invalid request body", "INVALID_BODY")
		return
	}
	var req struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	policy, err := h.svc.Create(r.Context(), req.Description)
	if err != nil {
		response.Error(w, err.Error(), "CREATE_FAILED")
		return
	}
	response.Success(w, policy)
	h.publishPoliciesUpdated(r.Context())
}

// Delete removes an access policy.
// DELETE /api/access-policies/delete?name=Policy0
func (h *AccessPolicyHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		response.MethodNotAllowed(w)
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		response.Error(w, "missing name parameter", "MISSING_NAME")
		return
	}
	if err := h.svc.Delete(r.Context(), name); err != nil {
		response.Error(w, err.Error(), "DELETE_FAILED")
		return
	}
	response.Success(w, map[string]bool{"ok": true})
	h.publishPoliciesUpdated(r.Context())
}

// SetDescription updates the description of an access policy.
// POST /api/access-policies/description
// Body: {"name":"Policy0","description":"..."}
func (h *AccessPolicyHandler) SetDescription(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		response.Error(w, "invalid request body", "INVALID_BODY")
		return
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if req.Name == "" {
		response.Error(w, "missing name", "MISSING_NAME")
		return
	}
	if err := h.svc.SetDescription(r.Context(), req.Name, req.Description); err != nil {
		response.Error(w, err.Error(), "SET_DESCRIPTION_FAILED")
		return
	}
	response.Success(w, map[string]bool{"ok": true})
	h.publishPoliciesUpdated(r.Context())
}

// SetStandalone enables or disables standalone mode on an access policy.
// POST /api/access-policies/standalone
// Body: {"name":"Policy0","enabled":true}
func (h *AccessPolicyHandler) SetStandalone(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		response.Error(w, "invalid request body", "INVALID_BODY")
		return
	}
	var req struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if req.Name == "" {
		response.Error(w, "missing name", "MISSING_NAME")
		return
	}
	if err := h.svc.SetStandalone(r.Context(), req.Name, req.Enabled); err != nil {
		response.Error(w, err.Error(), "SET_STANDALONE_FAILED")
		return
	}
	response.Success(w, map[string]bool{"ok": true})
	h.publishPoliciesUpdated(r.Context())
}

// PermitInterface handles permit/deny operations for policy interfaces.
// POST /api/access-policies/permit — add interface
// Body: {"name":"Policy0","interface":"Wireguard0","order":0}
// DELETE /api/access-policies/permit?name=Policy0&interface=Wireguard0 — remove interface
func (h *AccessPolicyHandler) PermitInterface(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.permitInterfaceAdd(w, r)
	case http.MethodDelete:
		h.permitInterfaceRemove(w, r)
	default:
		response.MethodNotAllowed(w)
	}
}

func (h *AccessPolicyHandler) permitInterfaceAdd(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		response.Error(w, "invalid request body", "INVALID_BODY")
		return
	}
	var req struct {
		Name      string `json:"name"`
		Interface string `json:"interface"`
		Order     int    `json:"order"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if req.Name == "" {
		response.Error(w, "missing name", "MISSING_NAME")
		return
	}
	if req.Interface == "" {
		response.Error(w, "missing interface", "MISSING_INTERFACE")
		return
	}
	if err := h.svc.PermitInterface(r.Context(), req.Name, req.Interface, req.Order); err != nil {
		response.Error(w, err.Error(), "PERMIT_FAILED")
		return
	}
	response.Success(w, map[string]bool{"ok": true})
	h.publishPoliciesUpdated(r.Context())
}

func (h *AccessPolicyHandler) permitInterfaceRemove(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	iface := r.URL.Query().Get("interface")
	if name == "" {
		response.Error(w, "missing name parameter", "MISSING_NAME")
		return
	}
	if iface == "" {
		response.Error(w, "missing interface parameter", "MISSING_INTERFACE")
		return
	}
	if err := h.svc.DenyInterface(r.Context(), name, iface); err != nil {
		response.Error(w, err.Error(), "DENY_FAILED")
		return
	}
	response.Success(w, map[string]bool{"ok": true})
	h.publishPoliciesUpdated(r.Context())
}

// AssignDevice handles device assignment to policies.
// POST /api/access-policies/assign — assign device
// Body: {"mac":"AA:BB:CC:DD:EE:FF","policy":"Policy0"}
// DELETE /api/access-policies/assign?mac=AA:BB:CC:DD:EE:FF — unassign device
func (h *AccessPolicyHandler) AssignDevice(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.assignDevicePost(w, r)
	case http.MethodDelete:
		h.unassignDeviceDelete(w, r)
	default:
		response.MethodNotAllowed(w)
	}
}

func (h *AccessPolicyHandler) assignDevicePost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		response.Error(w, "invalid request body", "INVALID_BODY")
		return
	}
	var req struct {
		MAC    string `json:"mac"`
		Policy string `json:"policy"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if req.MAC == "" {
		response.Error(w, "missing mac", "MISSING_MAC")
		return
	}
	if req.Policy == "" {
		response.Error(w, "missing policy", "MISSING_POLICY")
		return
	}
	if err := h.svc.AssignDevice(r.Context(), req.MAC, req.Policy); err != nil {
		response.Error(w, err.Error(), "ASSIGN_FAILED")
		return
	}
	response.Success(w, map[string]bool{"ok": true})
	h.publishPoliciesUpdated(r.Context())
	h.publishDevicesUpdated(r.Context())
}

func (h *AccessPolicyHandler) unassignDeviceDelete(w http.ResponseWriter, r *http.Request) {
	mac := r.URL.Query().Get("mac")
	if mac == "" {
		response.Error(w, "missing mac parameter", "MISSING_MAC")
		return
	}
	if err := h.svc.UnassignDevice(r.Context(), mac); err != nil {
		response.Error(w, err.Error(), "UNASSIGN_FAILED")
		return
	}
	response.Success(w, map[string]bool{"ok": true})
	h.publishPoliciesUpdated(r.Context())
	h.publishDevicesUpdated(r.Context())
}

// ListDevices returns all LAN devices with their policy assignments.
// GET /api/access-policies/devices
func (h *AccessPolicyHandler) ListDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	ctx := r.Context()
	if r.URL.Query().Get("refresh") == "true" {
		ctx = accesspolicy.ContextWithForceRefresh(ctx)
	}
	devices, err := h.svc.ListDevices(ctx)
	if err != nil {
		response.Error(w, err.Error(), "LIST_DEVICES_FAILED")
		return
	}
	response.Success(w, response.MustNotNil(devices))
}

// ListGlobalInterfaces returns all router interfaces available for policy routing.
// GET /api/access-policies/interfaces
func (h *AccessPolicyHandler) ListGlobalInterfaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	ifaces, err := h.svc.ListGlobalInterfaces(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "LIST_INTERFACES_FAILED")
		return
	}
	response.Success(w, response.MustNotNil(ifaces))
}

// SetInterfaceUp brings an interface up or down.
// POST /api/access-policies/interface-up
// Body: {"name":"Wireguard0","up":true}
func (h *AccessPolicyHandler) SetInterfaceUp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req struct {
		Name string `json:"name"`
		Up   bool   `json:"up"`
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		response.Error(w, "invalid body", "INVALID_BODY")
		return
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Name == "" {
		response.Error(w, "name required", "INVALID_REQUEST")
		return
	}
	if err := h.svc.SetInterfaceUp(r.Context(), req.Name, req.Up); err != nil {
		response.Error(w, err.Error(), "INTERFACE_UP_FAILED")
		return
	}
	response.Success(w, map[string]bool{"ok": true})
	h.publishPoliciesUpdated(r.Context())
}
