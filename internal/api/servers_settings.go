package api

import (
	"context"
	"net"
	"net/http"
	"regexp"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

var serverEndpointHostPattern = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`)

// SetServerEndpointRequest is the body for POST /servers/{name}/endpoint.
type SetServerEndpointRequest struct {
	Endpoint string `json:"endpoint" example:"203.0.113.42"`
}

func isValidServerEndpointHost(val string) bool {
	val = strings.TrimSpace(val)
	if val == "" {
		return true
	}
	if net.ParseIP(val) != nil {
		return true
	}
	return serverEndpointHostPattern.MatchString(val)
}

// formatWireguardEndpointHost brackets IPv6 literals so the host:port line
// in generated client configs stays parseable (isValidServerEndpointHost
// accepts IPv6 via net.ParseIP).
func formatWireguardEndpointHost(host string) string {
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		return "[" + host + "]"
	}
	return host
}

// resolveWireguardClientEndpointHost picks the connect host for generated
// client configs before WAN IP fallback: stored override → KeenDNS → empty.
func resolveWireguardClientEndpointHost(storedEndpoint, keenDNSDomain string) string {
	if ep := strings.TrimSpace(storedEndpoint); ep != "" {
		return ep
	}
	if domain := strings.TrimSpace(keenDNSDomain); domain != "" {
		return domain
	}
	return ""
}

func detectSystemServerNATMode(natEnabled, hasStatic bool) string {
	switch {
	case hasStatic && !natEnabled:
		return "internet-only"
	case natEnabled:
		return "full"
	default:
		return "none"
	}
}

func (h *ServersHandler) readSystemServerNATMode(ctx context.Context, iface string) (natEnabled bool, natMode string, err error) {
	if h.queries == nil || h.queries.NAT == nil {
		return false, "none", nil
	}
	natEnabled, err = h.queries.NAT.HasInterface(ctx, iface)
	if err != nil {
		return false, "", err
	}
	hasStatic := false
	if h.queries.StaticNAT != nil {
		hasStatic, _, err = h.queries.StaticNAT.ForInterface(ctx, iface)
		if err != nil {
			return false, "", err
		}
	}
	return natEnabled, detectSystemServerNATMode(natEnabled, hasStatic), nil
}

func (h *ServersHandler) readSystemServerPolicy(ctx context.Context, iface string) (string, error) {
	if h.queries == nil || h.queries.RunningConfig == nil {
		return "none", nil
	}
	policy, err := h.queries.RunningConfig.GetInterfaceHotspotPolicy(ctx, iface)
	if err != nil {
		return "", err
	}
	if policy == "" {
		return "none", nil
	}
	return policy, nil
}

func (h *ServersHandler) invalidateSystemServerCaches(iface string) {
	if h.queries == nil {
		return
	}
	if h.queries.NAT != nil {
		h.queries.NAT.InvalidateAll()
	}
	if h.queries.StaticNAT != nil {
		h.queries.StaticNAT.InvalidateAll()
	}
	if h.queries.RunningConfig != nil {
		h.queries.RunningConfig.InvalidateAll()
	}
	if h.queries.WGServers != nil {
		h.queries.WGServers.Invalidate(iface)
	}
	if h.managedSvc != nil {
		h.managedSvc.InvalidateCache(iface)
	}
}

// SetNAT sets the NAT mode on a built-in/marked WireGuard server.
// POST /api/servers/{name}/nat
//
//	@Summary		Set server NAT mode
//	@Description	Sets the NAT mode (full / internet-only / none) on the named WireGuard server via NDMS. Returns the fresh servers snapshot.
//	@Tags			servers
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			name	path		string				true	"Interface name (e.g. Wireguard0)"
//	@Param			body	body		SetNATModeRequest	true	"NAT mode"
//	@Success		200		{object}	ServersAllResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/servers/{name}/nat [post]
func (h *ServersHandler) SetNAT(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.managedSvc == nil {
		response.Error(w, "managed service not initialized", "INTERNAL_ERROR")
		return
	}
	if _, ok := h.requireListedServer(r.Context(), w, name); !ok {
		return
	}

	req, ok := parseJSON[SetNATModeRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	mode := req.Mode
	if mode == "" && req.Enabled != nil {
		if *req.Enabled {
			mode = "full"
		} else {
			mode = "none"
		}
	}
	if mode == "" {
		response.BadRequest(w, "NAT mode required")
		return
	}
	switch mode {
	case "full", "internet-only", "none":
	default:
		response.BadRequest(w, "invalid NAT mode: "+mode)
		return
	}

	meta, _ := h.settings.GetServerInterfaceMeta(name)
	wan, err := h.managedSvc.ApplyNATModeToInterface(r.Context(), name, mode, meta.NATStaticWAN)
	if err != nil {
		response.Error(w, err.Error(), "NAT_FAILED")
		return
	}
	if err := h.settings.UpdateServerInterfaceMeta(name, func(m *storage.ServerInterfaceMeta) error {
		m.NATMode = mode
		if mode == "internet-only" {
			m.NATStaticWAN = wan
		} else {
			m.NATStaticWAN = ""
		}
		return nil
	}); err != nil {
		response.Error(w, err.Error(), "SAVE_FAILED")
		return
	}

	h.invalidateSystemServerCaches(name)
	publishInvalidated(h.bus, ResourceServers, "server-nat-changed")
	h.writeAll(w, r)
}

// SetPolicy sets the ip hotspot policy on a built-in/marked WireGuard server.
// POST /api/servers/{name}/policy
//
//	@Summary		Set server access policy
//	@Description	Binds the named WireGuard server interface to an NDMS hotspot access policy (or "none"). Returns the fresh servers snapshot.
//	@Tags			servers
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			name	path		string					true	"Interface name (e.g. Wireguard0)"
//	@Param			body	body		SetServerPolicyRequest	true	"Policy id (router-side) or 'none'"
//	@Success		200		{object}	ServersAllResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/servers/{name}/policy [post]
func (h *ServersHandler) SetPolicy(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.managedSvc == nil {
		response.Error(w, "managed service not initialized", "INTERNAL_ERROR")
		return
	}
	if _, ok := h.requireListedServer(r.Context(), w, name); !ok {
		return
	}

	req, ok := parseJSON[SetServerPolicyRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	current, err := h.readSystemServerPolicy(r.Context(), name)
	if err != nil {
		response.Error(w, err.Error(), "POLICY_READ_FAILED")
		return
	}
	if req.Policy == current {
		h.writeAll(w, r)
		return
	}

	if err := h.managedSvc.ApplyPolicyToInterface(r.Context(), name, req.Policy); err != nil {
		response.Error(w, err.Error(), "POLICY_FAILED")
		return
	}

	h.invalidateSystemServerCaches(name)
	publishInvalidated(h.bus, ResourceServers, "server-policy-changed")
	h.writeAll(w, r)
}

// SetEndpoint stores the connect host used in generated client .conf files.
// POST /api/servers/{name}/endpoint
//
//	@Summary		Set server client endpoint host
//	@Description	Stores the IP or domain embedded in generated client configs. Empty clears the override and falls back to WAN IP. Returns the fresh servers snapshot.
//	@Tags			servers
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			name	path		string					true	"Interface name (e.g. Wireguard0)"
//	@Param			body	body		SetServerEndpointRequest	true	"Endpoint host"
//	@Success		200		{object}	ServersAllResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/servers/{name}/endpoint [post]
func (h *ServersHandler) SetEndpoint(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if _, ok := h.requireListedServer(r.Context(), w, name); !ok {
		return
	}

	req, ok := parseJSON[SetServerEndpointRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	endpoint := strings.TrimSpace(req.Endpoint)
	if !isValidServerEndpointHost(endpoint) {
		response.BadRequest(w, "endpoint must be an IP address or domain name")
		return
	}

	meta, _ := h.settings.GetServerInterfaceMeta(name)
	if meta.Endpoint == endpoint {
		h.writeAll(w, r)
		return
	}

	if err := h.settings.UpdateServerInterfaceMeta(name, func(m *storage.ServerInterfaceMeta) error {
		m.Endpoint = endpoint
		return nil
	}); err != nil {
		response.Error(w, err.Error(), "SAVE_FAILED")
		return
	}

	publishInvalidated(h.bus, ResourceServers, "server-endpoint-changed")
	h.writeAll(w, r)
}
