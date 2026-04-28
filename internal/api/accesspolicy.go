package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// ── Response DTOs ────────────────────────────────────────────────

// AccessPolicyInterfaceDTO mirrors frontend AccessPolicyInterface.
type AccessPolicyInterfaceDTO struct {
	Name   string `json:"name" example:"nwg0"`
	Label  string `json:"label,omitempty" example:"My VPN"`
	Order  int    `json:"order" example:"1"`
	Denied bool   `json:"denied,omitempty" example:"false"`
}

// AccessPolicyDTO mirrors frontend AccessPolicy.
type AccessPolicyDTO struct {
	Name        string                     `json:"name" example:"default"`
	Description string                     `json:"description" example:"Default policy"`
	Standalone  bool                       `json:"standalone" example:"false"`
	Interfaces  []AccessPolicyInterfaceDTO `json:"interfaces"`
	DeviceCount int                        `json:"deviceCount" example:"5"`
}

// AccessPoliciesListResponse is the envelope for GET /access-policies.
type AccessPoliciesListResponse struct {
	Success bool              `json:"success" example:"true"`
	Data    []AccessPolicyDTO `json:"data"`
}

// PolicyDeviceDTO mirrors frontend PolicyDevice.
type PolicyDeviceDTO struct {
	MAC      string `json:"mac" example:"aa:bb:cc:dd:ee:ff"`
	IP       string `json:"ip" example:"192.168.1.100"`
	Name     string `json:"name" example:"My Phone"`
	Hostname string `json:"hostname" example:"my-phone"`
	Active   bool   `json:"active" example:"true"`
	Link     string `json:"link" example:"WiFi"`
	Policy   string `json:"policy" example:"default"`
}

// PolicyDevicesListResponse is the envelope for GET /access-policies/devices.
type PolicyDevicesListResponse struct {
	Success bool              `json:"success" example:"true"`
	Data    []PolicyDeviceDTO `json:"data"`
}

// PolicyGlobalInterfaceDTO mirrors frontend PolicyGlobalInterface.
type PolicyGlobalInterfaceDTO struct {
	Name  string `json:"name" example:"nwg0"`
	Label string `json:"label" example:"My VPN"`
	Up    bool   `json:"up" example:"true"`
}

// PolicyInterfacesListResponse is the envelope for GET /access-policies/interfaces.
type PolicyInterfacesListResponse struct {
	Success bool                       `json:"success" example:"true"`
	Data    []PolicyGlobalInterfaceDTO `json:"data"`
}

// AccessPolicyHandler handles access policy CRUD and device assignment operations.
type AccessPolicyHandler struct {
	svc accesspolicy.Service
	bus *events.Bus
}

// SetEventBus sets the event bus for SSE publishing.
func (h *AccessPolicyHandler) SetEventBus(bus *events.Bus) { h.bus = bus }

// publishPoliciesUpdated posts a resource:invalidated hint for the access
// policy list so clients refetch.
func (h *AccessPolicyHandler) publishPoliciesUpdated(reason string) {
	publishInvalidated(h.bus, ResourceRoutingAccessPolicies, reason)
}

// publishDevicesUpdated posts a resource:invalidated hint for the device list.
func (h *AccessPolicyHandler) publishDevicesUpdated(reason string) {
	publishInvalidated(h.bus, ResourceRoutingPolicyDevices, reason)
}

// NewAccessPolicyHandler creates a new access policy handler.
func NewAccessPolicyHandler(svc accesspolicy.Service) *AccessPolicyHandler {
	return &AccessPolicyHandler{svc: svc}
}

// List returns all access policies.
// GET /api/access-policies
//
//	@Summary		List access policies
//	@Description	KeeneticOS 5 only when route is registered.
//	@Tags			access-policy
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{array}	map[string]interface{}
//	@Router			/access-policies [get]
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
//
//	@Summary		Create access policy
//	@Tags			access-policy
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Router			/access-policies/create [post]
func (h *AccessPolicyHandler) Create(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[struct {
		Description string `json:"description"`
	}](w, r, http.MethodPost)
	if !ok {
		return
	}
	policy, err := h.svc.Create(r.Context(), req.Description)
	if err != nil {
		response.Error(w, err.Error(), "CREATE_FAILED")
		return
	}
	response.Success(w, policy)
	h.publishPoliciesUpdated("create")
}

// Delete removes an access policy.
// DELETE /api/access-policies/delete?name=Policy0
//
//	@Summary		Delete access policy
//	@Description	Removes the named access policy. Bound LAN devices revert to the default policy.
//	@Tags			access-policy
//	@Produce		json
//	@Security		CookieAuth
//	@Param			name	query		string	true	"Policy name (e.g. Policy0)"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/access-policies/delete [delete]
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
	h.publishPoliciesUpdated("delete")
}

// SetDescription updates the description of an access policy.
// POST /api/access-policies/description
// Body: {"name":"Policy0","description":"..."}
//
//	@Summary		Set access policy description
//	@Tags			access-policy
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Router			/access-policies/description [post]
func (h *AccessPolicyHandler) SetDescription(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}](w, r, http.MethodPost)
	if !ok {
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
	h.publishPoliciesUpdated("set-description")
}

// SetStandalone enables or disables standalone mode on an access policy.
// POST /api/access-policies/standalone
// Body: {"name":"Policy0","enabled":true}
//
//	@Summary		Set access policy standalone mode
//	@Tags			access-policy
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Router			/access-policies/standalone [post]
func (h *AccessPolicyHandler) SetStandalone(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}](w, r, http.MethodPost)
	if !ok {
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
	h.publishPoliciesUpdated("set-standalone")
}

// PermitInterface handles permit/deny operations for policy interfaces.
// POST /api/access-policies/permit — add interface
// DELETE /api/access-policies/permit?name=...&interface=... — remove interface
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

// permitInterfaceAdd adds an interface to a policy at the given priority.
//
//	@Summary		Permit interface for policy
//	@Description	Adds the named interface to the policy at the given order (lower = higher priority).
//	@Tags			access-policy
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"{name, interface, order}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/access-policies/permit [post]
func (h *AccessPolicyHandler) permitInterfaceAdd(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[struct {
		Name      string `json:"name"`
		Interface string `json:"interface"`
		Order     int    `json:"order"`
	}](w, r, http.MethodPost)
	if !ok {
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
	h.publishPoliciesUpdated("permit-interface")
}

// permitInterfaceRemove removes an interface from a policy.
//
//	@Summary		Deny interface for policy
//	@Description	Removes the named interface from the policy.
//	@Tags			access-policy
//	@Produce		json
//	@Security		CookieAuth
//	@Param			name		query		string	true	"Policy name"
//	@Param			interface	query		string	true	"Interface name"
//	@Success		200			{object}	map[string]interface{}
//	@Failure		400			{object}	map[string]interface{}
//	@Failure		500			{object}	map[string]interface{}
//	@Router			/access-policies/permit [delete]
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
	h.publishPoliciesUpdated("deny-interface")
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

// assignDevicePost binds a LAN device to an access policy.
//
//	@Summary		Assign device to policy
//	@Description	Binds the LAN device identified by MAC to the named policy. Replaces any existing assignment.
//	@Tags			access-policy
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"{mac, policy}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/access-policies/assign [post]
func (h *AccessPolicyHandler) assignDevicePost(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[struct {
		MAC    string `json:"mac"`
		Policy string `json:"policy"`
	}](w, r, http.MethodPost)
	if !ok {
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
	h.publishPoliciesUpdated("assign-device")
	h.publishDevicesUpdated("assign-device")
}

// unassignDeviceDelete removes a LAN device from any access policy.
//
//	@Summary		Unassign device from policy
//	@Description	Removes the policy binding for the LAN device identified by MAC. The device falls back to the default policy.
//	@Tags			access-policy
//	@Produce		json
//	@Security		CookieAuth
//	@Param			mac	query		string	true	"Device MAC address"
//	@Success		200	{object}	map[string]interface{}
//	@Failure		400	{object}	map[string]interface{}
//	@Failure		500	{object}	map[string]interface{}
//	@Router			/access-policies/assign [delete]
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
	h.publishPoliciesUpdated("unassign-device")
	h.publishDevicesUpdated("unassign-device")
}

// ListDevices returns all LAN devices with their policy assignments.
// GET /api/access-policies/devices
//
//	@Summary		List policy devices
//	@Tags			access-policy
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{array}	map[string]interface{}
//	@Router			/access-policies/devices [get]
//	@Router			/routing/policy-devices [get]
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
//
//	@Summary		List global policy interfaces
//	@Tags			access-policy
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{array}	map[string]interface{}
//	@Router			/access-policies/interfaces [get]
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
//
//	@Summary		Set interface admin up
//	@Tags			access-policy
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Router			/access-policies/interface-up [post]
func (h *AccessPolicyHandler) SetInterfaceUp(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[struct {
		Name string `json:"name"`
		Up   bool   `json:"up"`
	}](w, r, http.MethodPost)
	if !ok {
		return
	}
	if req.Name == "" {
		response.Error(w, "name required", "INVALID_REQUEST")
		return
	}
	if err := h.svc.SetInterfaceUp(r.Context(), req.Name, req.Up); err != nil {
		response.Error(w, err.Error(), "INTERFACE_UP_FAILED")
		return
	}
	response.Success(w, map[string]bool{"ok": true})
	publishInvalidated(h.bus, ResourceRoutingPolicyInterfaces, "set-interface-up")
}
