package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/testing"
)

// ServerAddPeerRequestDTO is the body for POST /servers/{name}/peers.
type ServerAddPeerRequestDTO struct {
	Description string `json:"description" example:"My Phone"`
	TunnelIP    string `json:"tunnelIP" example:"10.0.14.2/32"`
}

// ServerUpdatePeerRequestDTO is the body for PUT /servers/{name}/peers/{pubkey}.
type ServerUpdatePeerRequestDTO struct {
	Description string `json:"description" example:"My Phone"`
	TunnelIP    string `json:"tunnelIP" example:"10.0.14.2/32"`
}

// Subtree dispatches /api/servers/{name}/... operations.
func (h *ServersHandler) Subtree(w http.ResponseWriter, r *http.Request) {
	parts, ok := splitPath(r.URL.EscapedPath(), "/api/servers/")
	if !ok || len(parts) < 2 {
		response.Error(w, "unknown path", "UNKNOWN_PATH")
		return
	}
	name := parts[0]
	if !h.validateName(w, name) {
		return
	}
	switch parts[1] {
	case "nat":
		if len(parts) != 2 {
			response.Error(w, "unknown path", "UNKNOWN_PATH")
			return
		}
		h.SetNAT(w, r, name)
		return
	case "policy":
		if len(parts) != 2 {
			response.Error(w, "unknown path", "UNKNOWN_PATH")
			return
		}
		h.SetPolicy(w, r, name)
		return
	case "endpoint":
		if len(parts) != 2 {
			response.Error(w, "unknown path", "UNKNOWN_PATH")
			return
		}
		h.SetEndpoint(w, r, name)
		return
	case "peers":
	default:
		response.Error(w, "unknown path", "UNKNOWN_PATH")
		return
	}
	switch len(parts) {
	case 2:
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		h.AddServerPeer(w, r, name)
	case 3:
		pubkey, err := url.PathUnescape(parts[2])
		if err != nil || !validateWireguardPubkey(pubkey) {
			response.Error(w, "invalid public key", "INVALID_PUBKEY")
			return
		}
		switch r.Method {
		case http.MethodPut:
			h.UpdateServerPeer(w, r, name, pubkey)
		case http.MethodDelete:
			h.DeleteServerPeer(w, r, name, pubkey)
		default:
			response.MethodNotAllowed(w)
		}
	case 4:
		pubkey, err := url.PathUnescape(parts[2])
		if err != nil || !validateWireguardPubkey(pubkey) {
			response.Error(w, "invalid public key", "INVALID_PUBKEY")
			return
		}
		switch parts[3] {
		case "toggle":
			h.ToggleServerPeer(w, r, name, pubkey)
		case "conf":
			h.ServerPeerConf(w, r, name, pubkey)
		default:
			response.Error(w, "unknown path", "UNKNOWN_PATH")
		}
	default:
		response.Error(w, "unknown path", "UNKNOWN_PATH")
	}
}

func validateWireguardPubkey(pubkey string) bool {
	return len(pubkey) == 44 && strings.HasSuffix(pubkey, "=")
}

func (h *ServersHandler) requireListedServer(ctx context.Context, w http.ResponseWriter, name string) (*ndms.WireguardServer, bool) {
	server, err := h.getListedServer(ctx, name)
	if err != nil {
		response.Error(w, err.Error(), "GET_FAILED")
		return nil, false
	}
	if server == nil {
		response.Error(w, "server not found", "NOT_FOUND")
		return nil, false
	}
	return server, true
}

func (h *ServersHandler) requireWGCommands(w http.ResponseWriter) bool {
	if h.commands == nil || h.commands.Wireguard == nil {
		response.Error(w, "ndms commands not initialized", "INTERNAL_ERROR")
		return false
	}
	return true
}

// AddServerPeer adds a peer to a built-in/marked WireGuard server.
// POST /api/servers/{name}/peers
//
//	@Summary		Add server peer
//	@Description	Generates a keypair and registers a new peer on the named WireGuard server. Returns the fresh servers snapshot.
//	@Tags			servers
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			name	path		string						true	"Interface name (e.g. Wireguard0)"
//	@Param			body	body		ServerAddPeerRequestDTO	true	"Peer description and tunnel IP"
//	@Success		200		{object}	ServersAllResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/servers/{name}/peers [post]
func (h *ServersHandler) AddServerPeer(w http.ResponseWriter, r *http.Request, name string) {
	req, ok := parseJSON[ServerAddPeerRequestDTO](w, r, http.MethodPost)
	if !ok {
		return
	}
	if !h.requireWGCommands(w) {
		return
	}
	server, ok := h.requireListedServer(r.Context(), w, name)
	if !ok {
		return
	}
	if err := h.validateServerPeerTunnelIP(server, req.TunnelIP); err != nil {
		response.Error(w, err.Error(), "INVALID_TUNNEL_IP")
		return
	}
	if peerTunnelIPInUse(server, req.TunnelIP) {
		response.Error(w, "tunnel IP already in use", "TUNNEL_IP_IN_USE")
		return
	}

	privKey, pubKey, err := managed.GenerateKeyPair(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "KEYGEN_FAILED")
		return
	}
	psk, err := managed.GeneratePresharedKey(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "KEYGEN_FAILED")
		return
	}
	ip, _, err := net.ParseCIDR(req.TunnelIP)
	if err != nil {
		response.Error(w, "invalid tunnel IP", "INVALID_TUNNEL_IP")
		return
	}

	// Persist the secret BEFORE the router add. If the order were reversed and
	// the save failed, the peer would exist on the router with its private key
	// lost forever — .conf could never be regenerated and the IP would stay
	// occupied. A stranded secret (save ok, router add fails) is harmless: it
	// is keyed by a pubkey that never reaches the router peer list, and we roll
	// it back below anyway.
	if err := h.settings.SetServerPeerSecret(name, pubKey, storage.ServerPeerSecret{
		PrivateKey:   privKey,
		PresharedKey: psk,
		Description:  req.Description,
		TunnelIP:     req.TunnelIP,
	}); err != nil {
		response.Error(w, err.Error(), "SAVE_FAILED")
		return
	}
	if err := h.commands.Wireguard.AddPeer(r.Context(), name, pubKey, psk, strings.TrimSpace(req.Description), ip.String(), true); err != nil {
		_ = h.settings.DeleteServerPeerSecret(name, pubKey)
		response.Error(w, err.Error(), "ADD_PEER_FAILED")
		return
	}
	publishInvalidated(h.bus, ResourceServers, "server-peer-added")
	h.writeAll(w, r)
}

// UpdateServerPeer updates a peer's tunnel IP and/or description.
// PUT /api/servers/{name}/peers/{pubkey}
//
//	@Summary		Update server peer
//	@Description	Changes a peer's allowed-IP and/or description on the named WireGuard server. Returns the fresh servers snapshot.
//	@Tags			servers
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			name	path		string							true	"Interface name (e.g. Wireguard0)"
//	@Param			pubkey	path		string							true	"Peer public key"
//	@Param			body	body		ServerUpdatePeerRequestDTO	true	"New description and tunnel IP"
//	@Success		200		{object}	ServersAllResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		404		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/servers/{name}/peers/{pubkey} [put]
func (h *ServersHandler) UpdateServerPeer(w http.ResponseWriter, r *http.Request, name, pubkey string) {
	req, ok := parseJSON[ServerUpdatePeerRequestDTO](w, r, http.MethodPut)
	if !ok {
		return
	}
	if !h.requireWGCommands(w) {
		return
	}
	server, ok := h.requireListedServer(r.Context(), w, name)
	if !ok {
		return
	}
	peer := findServerPeer(server, pubkey)
	if peer == nil {
		response.Error(w, "peer not found", "NOT_FOUND")
		return
	}

	oldIP := peerTunnelHostIP(peer)
	wantIPChange := req.TunnelIP != "" && req.TunnelIP != oldIP+"/32" && req.TunnelIP != oldIP
	if wantIPChange {
		if err := h.validateServerPeerTunnelIP(server, req.TunnelIP); err != nil {
			response.Error(w, err.Error(), "INVALID_TUNNEL_IP")
			return
		}
		newHost, _, _ := net.ParseCIDR(req.TunnelIP)
		if newHost == nil {
			response.Error(w, "invalid tunnel IP", "INVALID_TUNNEL_IP")
			return
		}
		if err := h.commands.Wireguard.UpdatePeerAllowIPs(r.Context(), name, pubkey, oldIP, newHost.String()); err != nil {
			response.Error(w, err.Error(), "UPDATE_PEER_FAILED")
			return
		}
	}
	if req.Description != peer.Description {
		if err := h.commands.Wireguard.SetPeerComment(r.Context(), name, pubkey, strings.TrimSpace(req.Description)); err != nil {
			response.Error(w, err.Error(), "UPDATE_PEER_FAILED")
			return
		}
	}
	// Keep the stored secret (source of truth for .conf regen) in sync with
	// the router change. Runs unconditionally so a retry after a prior save
	// failure still reconciles even when the router side is now a no-op. The
	// error is surfaced, not swallowed: a stale stored IP would hand out a
	// wrong .conf after reboot.
	if sec, ok := h.settings.GetServerPeerSecret(name, pubkey); ok {
		changed := false
		if req.TunnelIP != "" && sec.TunnelIP != req.TunnelIP {
			sec.TunnelIP = req.TunnelIP
			changed = true
		}
		if sec.Description != req.Description {
			sec.Description = req.Description
			changed = true
		}
		if changed {
			if err := h.settings.SetServerPeerSecret(name, pubkey, sec); err != nil {
				response.Error(w, err.Error(), "SAVE_FAILED")
				return
			}
		}
	}
	publishInvalidated(h.bus, ResourceServers, "server-peer-updated")
	h.writeAll(w, r)
}

// DeleteServerPeer removes a peer from a WireGuard server.
// DELETE /api/servers/{name}/peers/{pubkey}
//
//	@Summary		Delete server peer
//	@Description	Removes the peer with the given public key from the named WireGuard server. Returns the fresh servers snapshot.
//	@Tags			servers
//	@Produce		json
//	@Security		CookieAuth
//	@Param			name	path		string	true	"Interface name (e.g. Wireguard0)"
//	@Param			pubkey	path		string	true	"Peer public key"
//	@Success		200		{object}	ServersAllResponse
//	@Failure		404		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/servers/{name}/peers/{pubkey} [delete]
func (h *ServersHandler) DeleteServerPeer(w http.ResponseWriter, r *http.Request, name, pubkey string) {
	if !h.requireWGCommands(w) {
		return
	}
	server, ok := h.requireListedServer(r.Context(), w, name)
	if !ok {
		return
	}
	if findServerPeer(server, pubkey) == nil {
		response.Error(w, "peer not found", "NOT_FOUND")
		return
	}
	if err := h.commands.Wireguard.RemovePeer(r.Context(), name, pubkey); err != nil {
		response.Error(w, err.Error(), "DELETE_PEER_FAILED")
		return
	}
	_ = h.settings.DeleteServerPeerSecret(name, pubkey)
	publishInvalidated(h.bus, ResourceServers, "server-peer-deleted")
	h.writeAll(w, r)
}

// ToggleServerPeer enables or disables a peer.
// POST /api/servers/{name}/peers/{pubkey}/toggle
//
//	@Summary		Toggle server peer
//	@Description	Enables or disables (connect on/off) the peer on the named WireGuard server. Returns the fresh servers snapshot.
//	@Tags			servers
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			name	path		string					true	"Interface name (e.g. Wireguard0)"
//	@Param			pubkey	path		string					true	"Peer public key"
//	@Param			body	body		EnabledToggleRequest	true	"Enabled flag"
//	@Success		200		{object}	ServersAllResponse
//	@Failure		404		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/servers/{name}/peers/{pubkey}/toggle [post]
func (h *ServersHandler) ToggleServerPeer(w http.ResponseWriter, r *http.Request, name, pubkey string) {
	req, ok := parseJSON[EnabledToggleRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	if !h.requireWGCommands(w) {
		return
	}
	server, ok := h.requireListedServer(r.Context(), w, name)
	if !ok {
		return
	}
	if findServerPeer(server, pubkey) == nil {
		response.Error(w, "peer not found", "NOT_FOUND")
		return
	}
	if err := h.commands.Wireguard.SetPeerConnect(r.Context(), name, pubkey, req.Enabled); err != nil {
		response.Error(w, err.Error(), "TOGGLE_FAILED")
		return
	}
	publishInvalidated(h.bus, ResourceServers, "server-peer-toggled")
	h.writeAll(w, r)
}

// ServerPeerConf returns the downloadable WireGuard .conf for a peer.
// GET /api/servers/{name}/peers/{pubkey}/conf
//
//	@Summary		Get server peer config
//	@Description	Generates the WireGuard client .conf for the peer. Only available when the peer's private key was created via AWG Manager and is stored locally.
//	@Tags			servers
//	@Produce		json
//	@Security		CookieAuth
//	@Param			name	path		string	true	"Interface name (e.g. Wireguard0)"
//	@Param			pubkey	path		string	true	"Peer public key"
//	@Success		200		{object}	map[string]string
//	@Failure		404		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/servers/{name}/peers/{pubkey}/conf [get]
func (h *ServersHandler) ServerPeerConf(w http.ResponseWriter, r *http.Request, name, pubkey string) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	server, ok := h.requireListedServer(r.Context(), w, name)
	if !ok {
		return
	}
	if findServerPeer(server, pubkey) == nil {
		response.Error(w, "peer not found", "NOT_FOUND")
		return
	}
	sec, ok := h.settings.GetServerPeerSecret(name, pubkey)
	if !ok || sec.PrivateKey == "" {
		response.Error(w, "ключ клиента недоступен (создан вне AWG Manager или через KeenDNS)", "CONF_UNAVAILABLE")
		return
	}
	conf, err := h.generateServerPeerConf(r.Context(), server, pubkey, sec)
	if err != nil {
		response.Error(w, err.Error(), "CONF_FAILED")
		return
	}
	response.Success(w, map[string]string{"conf": conf})
}

func (h *ServersHandler) generateServerPeerConf(ctx context.Context, server *ndms.WireguardServer, pubkey string, sec storage.ServerPeerSecret) (string, error) {
	endpoint, err := h.resolveServerEndpoint(ctx, server.ID)
	if err != nil {
		return "", err
	}
	tunnelIP := sec.TunnelIP
	if tunnelIP == "" {
		if peer := findServerPeer(server, pubkey); peer != nil {
			host := peerTunnelHostIP(peer)
			if host != "" {
				tunnelIP = host + "/32"
			}
		}
	}
	if tunnelIP == "" {
		return "", fmt.Errorf("peer tunnel IP unknown")
	}
	mtu := server.MTU
	if mtu == 0 {
		mtu = 1420
	}

	var b strings.Builder
	b.WriteString("[Interface]\n")
	b.WriteString(fmt.Sprintf("PrivateKey = %s\n", sec.PrivateKey))
	b.WriteString(fmt.Sprintf("Address = %s\n", tunnelIP))
	b.WriteString("DNS = 1.1.1.1, 8.8.8.8\n")
	b.WriteString(fmt.Sprintf("MTU = %d\n", mtu))

	if h.queries != nil && h.queries.WGServers != nil {
		if ascRaw, err := h.queries.WGServers.GetASCParams(ctx, server.ID, true); err == nil && ascRaw != nil {
			writeServerASCParams(&b, ascRaw)
		}
	}

	b.WriteString("\n[Peer]\n")
	b.WriteString(fmt.Sprintf("PublicKey = %s\n", server.PublicKey))
	if sec.PresharedKey != "" {
		b.WriteString(fmt.Sprintf("PresharedKey = %s\n", sec.PresharedKey))
	}
	b.WriteString(fmt.Sprintf("Endpoint = %s:%d\n", formatWireguardEndpointHost(endpoint), server.ListenPort))
	b.WriteString("AllowedIPs = 0.0.0.0/0, ::/0\n")
	b.WriteString("PersistentKeepalive = 25\n")
	return b.String(), nil
}

func (h *ServersHandler) resolveServerEndpoint(ctx context.Context, serverID string) (string, error) {
	var storedEndpoint, keenDNSDomain string
	if meta, ok := h.settings.GetServerInterfaceMeta(serverID); ok {
		storedEndpoint = meta.Endpoint
	}
	if h.queries != nil && h.queries.KeenDNS != nil {
		if info, err := h.queries.KeenDNS.Get(ctx); err == nil && info != nil {
			keenDNSDomain = info.Domain
		}
	}
	if host := resolveWireguardClientEndpointHost(storedEndpoint, keenDNSDomain); host != "" {
		return host, nil
	}
	return testing.GetWANIPWithFallback(ctx, h.queries.WANInterfaceAddress)
}

func writeServerASCParams(b *strings.Builder, raw json.RawMessage) {
	var ext ndms.ASCParamsExtended
	if err := json.Unmarshal(raw, &ext); err != nil || ext.Jc == 0 {
		return
	}
	b.WriteString(fmt.Sprintf("Jc = %d\n", ext.Jc))
	b.WriteString(fmt.Sprintf("Jmin = %d\n", ext.Jmin))
	b.WriteString(fmt.Sprintf("Jmax = %d\n", ext.Jmax))
	b.WriteString(fmt.Sprintf("S1 = %d\n", ext.S1))
	b.WriteString(fmt.Sprintf("S2 = %d\n", ext.S2))
	b.WriteString(fmt.Sprintf("H1 = %s\n", ext.H1))
	b.WriteString(fmt.Sprintf("H2 = %s\n", ext.H2))
	b.WriteString(fmt.Sprintf("H3 = %s\n", ext.H3))
	b.WriteString(fmt.Sprintf("H4 = %s\n", ext.H4))
	if ext.S3 > 0 || ext.S4 > 0 {
		b.WriteString(fmt.Sprintf("S3 = %d\n", ext.S3))
		b.WriteString(fmt.Sprintf("S4 = %d\n", ext.S4))
	}
}

func findServerPeer(server *ndms.WireguardServer, pubkey string) *ndms.WireguardServerPeer {
	for i := range server.Peers {
		if server.Peers[i].PublicKey == pubkey {
			return &server.Peers[i]
		}
	}
	return nil
}

func peerTunnelHostIP(peer *ndms.WireguardServerPeer) string {
	for _, allowed := range peer.AllowedIPs {
		if strings.Contains(allowed, "/32") {
			host, _, err := net.ParseCIDR(allowed)
			if err == nil && host != nil {
				return host.String()
			}
		}
	}
	if len(peer.AllowedIPs) > 0 {
		host, _, err := net.ParseCIDR(peer.AllowedIPs[0])
		if err == nil && host != nil {
			return host.String()
		}
	}
	return ""
}

// peerTunnelIPInUse reports whether tunnelIP's host address is already
// assigned to an existing peer. Compares parsed host IPs for equality —
// a string prefix check (e.g. "10.0.0.2" vs "10.0.0.20") gives false
// positives and must not be used here.
func peerTunnelIPInUse(server *ndms.WireguardServer, tunnelIP string) bool {
	host, _, err := net.ParseCIDR(tunnelIP)
	if err != nil {
		return false
	}
	for i := range server.Peers {
		existing := peerTunnelHostIP(&server.Peers[i])
		if existing != "" && net.ParseIP(existing).Equal(host) {
			return true
		}
	}
	return false
}

func (h *ServersHandler) validateServerPeerTunnelIP(server *ndms.WireguardServer, tunnelIP string) error {
	ip, ipNet, err := net.ParseCIDR(tunnelIP)
	if err != nil {
		return fmt.Errorf("invalid tunnel IP (must be CIDR, e.g. 10.0.0.2/32): %w", err)
	}
	serverIP := net.ParseIP(server.Address)
	serverMask := net.IPMask(net.ParseIP(server.Mask).To4())
	if serverIP == nil || serverMask == nil {
		return nil
	}
	serverNet := &net.IPNet{IP: serverIP.Mask(serverMask), Mask: serverMask}
	if !serverNet.Contains(ip) {
		return fmt.Errorf("tunnel IP %s is not in server subnet %s", ip, serverNet)
	}
	if ip.Equal(serverIP) {
		return fmt.Errorf("tunnel IP %s is the server's own address", ip)
	}
	ones, bits := serverNet.Mask.Size()
	if ones < bits-1 {
		if ip.Equal(serverNet.IP) {
			return fmt.Errorf("tunnel IP %s is the network address", ip)
		}
		broadcast := make(net.IP, len(serverNet.IP))
		for i := range serverNet.IP {
			broadcast[i] = serverNet.IP[i] | ^serverNet.Mask[i]
		}
		if ip.Equal(broadcast) {
			return fmt.Errorf("tunnel IP %s is the broadcast address", ip)
		}
	}
	_ = ipNet
	return nil
}

const builtInVPNServerDescription = "Wireguard VPN Server"

func (h *ServersHandler) readSystemServerEnabled(ctx context.Context, iface string) (enabled bool, known bool) {
	if h.queries == nil || h.queries.Interfaces == nil {
		return false, false
	}
	details, err := h.queries.Interfaces.GetDetails(ctx, iface)
	if err != nil || details == nil {
		return false, false
	}
	return details.ConfLayer == "running", true
}

func (h *ServersHandler) enrichServerDTO(ctx context.Context, srv ndms.WireguardServer) WireguardServerDTO {
	dto := toWireguardServerDTO(srv)
	dto.BuiltIn = srv.Description == builtInVPNServerDescription
	if enabled, known := h.readSystemServerEnabled(ctx, srv.ID); known {
		dto.Enabled = enabled
		dto.EnabledKnown = true
	}
	if _, mode, err := h.readSystemServerNATMode(ctx, srv.ID); err == nil {
		dto.NATEnabled = mode == "full"
		dto.NATMode = mode
		dto.NATModeKnown = true
	}
	if policy, err := h.readSystemServerPolicy(ctx, srv.ID); err == nil {
		dto.Policy = policy
		dto.PolicyKnown = true
	}
	if h.queries != nil && h.queries.KeenDNS != nil {
		if info, err := h.queries.KeenDNS.Get(ctx); err == nil && info != nil {
			dto.KeenDNSDomain = info.Domain
		}
	}
	if meta, ok := h.settings.GetServerInterfaceMeta(srv.ID); ok {
		dto.Endpoint = meta.Endpoint
	}
	for i := range dto.Peers {
		sec, ok := h.settings.GetServerPeerSecret(srv.ID, dto.Peers[i].PublicKey)
		if ok {
			dto.Peers[i].ConfAvailable = true
			if dto.Peers[i].Description == "" && sec.Description != "" {
				dto.Peers[i].Description = sec.Description
			}
		}
	}
	return dto
}

func toWireguardServerDTO(srv ndms.WireguardServer) WireguardServerDTO {
	peers := make([]WireguardServerPeerDTO, len(srv.Peers))
	for i, p := range srv.Peers {
		peers[i] = WireguardServerPeerDTO{
			PublicKey:     p.PublicKey,
			Description:   p.Description,
			Endpoint:      p.Endpoint,
			AllowedIPs:    p.AllowedIPs,
			RxBytes:       p.RxBytes,
			TxBytes:       p.TxBytes,
			LastHandshake: p.LastHandshake,
			Online:        p.Online,
			Enabled:       p.Enabled,
		}
	}
	return WireguardServerDTO{
		ID:            srv.ID,
		InterfaceName: srv.InterfaceName,
		Description:   srv.Description,
		Status:        srv.Status,
		Connected:     srv.Connected,
		MTU:           srv.MTU,
		Address:       srv.Address,
		Mask:          srv.Mask,
		PublicKey:     srv.PublicKey,
		ListenPort:    srv.ListenPort,
		Peers:         peers,
	}
}
