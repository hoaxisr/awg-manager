package api

import (
	"errors"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/peersubnet"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/signature"
)

// AddPeerRequestDTO is the swagger-visible body for POST /managed-servers/{id}/peers.
type AddPeerRequestDTO struct {
	Description string `json:"description" example:"My Phone"`
	// TunnelIP is optional: when empty, the first free host address in the
	// server's subnet is allocated.
	TunnelIP string `json:"tunnelIP,omitempty" example:"10.10.0.2/32"`
	DNS      string `json:"dns,omitempty" example:"8.8.8.8"`
	// ClientAllowedIPs — строка AllowedIPs в .conf клиента (CIDR через запятую,
	// пусто — весь трафик). RemoteSubnets — сети за клиентом, IPv4 CIDR (#713).
	ClientAllowedIPs string   `json:"clientAllowedIPs,omitempty" example:"10.10.0.0/24, 192.168.1.0/24"`
	RemoteSubnets    []string `json:"remoteSubnets,omitempty" example:"192.168.77.0/24"`
}

// UpdatePeerRequestDTO is the swagger-visible body for PUT /managed-servers/{id}/peers/{pubkey}.
type UpdatePeerRequestDTO struct {
	Description string `json:"description" example:"My Phone"`
	TunnelIP    string `json:"tunnelIP" example:"10.10.0.2/32"`
	DNS         string `json:"dns,omitempty" example:"8.8.8.8"`
	// Signature: nil — сигнатуру пира не трогать; объект — заменить все пять
	// полей и профиль целиком (пустые поля объекта стирают старые байты).
	Signature *PeerSignatureDTO `json:"signature,omitempty"`
	// ClientAllowedIPs — строка AllowedIPs в .conf клиента (CIDR через запятую,
	// пусто — весь трафик). Отсутствие поля или null — не менять.
	ClientAllowedIPs *string `json:"clientAllowedIPs,omitempty" example:"10.10.0.0/24, 192.168.1.0/24"`
	// RemoteSubnets — сети за клиентом, IPv4 CIDR (#713); полная замена списка:
	// отсутствие поля или null — не менять; пустой список — снять все.
	RemoteSubnets *[]string `json:"remoteSubnets,omitempty" example:"192.168.77.0/24"`
}

// peerSubnetErrorCode — коды отказов валидации сетей пира (#713), общие для
// managed и системного путей: фронт различает их по коду и показывает текст
// пересечения у поля.
func peerSubnetErrorCode(err error) (string, bool) {
	switch {
	case errors.Is(err, peersubnet.ErrInvalidClientAllowedIPs):
		return "INVALID_CLIENT_ALLOWED_IPS", true
	case errors.Is(err, peersubnet.ErrRemoteSubnetOverlap):
		return "REMOTE_SUBNET_OVERLAP", true
	case errors.Is(err, peersubnet.ErrInvalidRemoteSubnets):
		return "INVALID_REMOTE_SUBNETS", true
	}
	return "", false
}

// PeerPresetsDTO — пресеты поля «AllowedIPs клиента» в формате поля (#713).
type PeerPresetsDTO struct {
	RouterOnly   string `json:"routerOnly" example:"10.10.0.0/24, 192.168.1.0/24"`
	ExceptRouter string `json:"exceptRouter" example:"0.0.0.0/5, 8.0.0.0/7, 192.168.1.1/32, ::/0"`
}

// PeerPresetsResponse is the envelope for GET …/peers/presets.
type PeerPresetsResponse struct {
	Success bool           `json:"success" example:"true"`
	Data    PeerPresetsDTO `json:"data"`
}

// PeerPresets returns AllowedIPs presets for a managed-server peer.
// GET /api/managed-servers/{id}/peers/presets
//
//	@Summary		Peer AllowedIPs presets
//	@Description	routerOnly — server subnet plus LAN bridges (LANSegments or all); exceptRouter — everything else plus /32 of the resolver and ::/0.
//	@Tags			managed-servers
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	path		string	true	"Server id"
//	@Param			dns	query		string	false	"Peer DNS as typed in the form; empty — server DNS, then router LAN IP"
//	@Success		200	{object}	PeerPresetsResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Router			/managed-servers/{id}/peers/presets [get]
func (h *ManagedServerHandler) PeerPresets(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	p, err := h.svc.PeerPresets(r.Context(), id, r.URL.Query().Get("dns"))
	if err != nil {
		if errors.Is(err, managed.ErrInvalidPeerDNS) {
			response.Error(w, err.Error(), "INVALID_PEER_DNS")
			return
		}
		response.Error(w, err.Error(), "PRESETS_FAILED")
		return
	}
	response.Success(w, PeerPresetsDTO{RouterOnly: p.RouterOnly, ExceptRouter: p.ExceptRouter})
}

// PeerSignatureDTO is the swagger-visible peer signature: five packets plus the
// profile they were generated from ("" — набраны руками).
type PeerSignatureDTO struct {
	Profile string `json:"profile" example:"quic_initial"`
	I1      string `json:"i1" example:"<b 0xc0>"`
	I2      string `json:"i2"`
	I3      string `json:"i3"`
	I4      string `json:"i4"`
	I5      string `json:"i5"`
}

func (s *PeerSignatureDTO) packets() signature.GeneratedPackets {
	return signature.GeneratedPackets{I1: s.I1, I2: s.I2, I3: s.I3, I4: s.I4, I5: s.I5}
}

// AddPeer adds a new peer to a managed server.
// POST /api/managed-servers/{id}/peers
//
//	@Summary		Add managed-server peer
//	@Description	Adds a new peer to the named managed server. The pubkey is generated server-side if absent.
//	@Tags			managed-servers
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id		path		string					true	"Server id"
//	@Param			body	body		AddPeerRequestDTO	true	"Peer payload"
//	@Success		200		{object}	ManagedPeerResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/managed-servers/{id}/peers [post]
func (h *ManagedServerHandler) AddPeer(w http.ResponseWriter, r *http.Request, id string) {
	req, ok := parseJSON[managed.AddPeerRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	peer, err := h.svc.AddPeer(r.Context(), id, req)
	if err != nil {
		if errors.Is(err, managed.ErrSignatureGenerate) {
			response.Error(w, err.Error(), "SIGNATURE_GENERATE_FAILED")
			return
		}
		if code, ok := peerSubnetErrorCode(err); ok {
			response.Error(w, err.Error(), code)
			return
		}
		if errors.Is(err, managed.ErrUnknownLANSegment) {
			response.Error(w, err.Error(), "LAN_SEGMENTS_FAILED")
			return
		}
		response.Error(w, err.Error(), "ADD_PEER_FAILED")
		return
	}
	h.svc.InvalidateCache(id)
	response.Success(w, peer)
	h.publishServerUpdated()
}

// UpdatePeer updates an existing peer of a managed server.
// PUT /api/managed-servers/{id}/peers/{pubkey}
//
//	@Summary		Update managed-server peer
//	@Description	Updates fields (name, allowed-ips, ...) of the peer identified by pubkey on the named managed server.
//	@Description	The signature field: absent — the peer signature is left untouched; present — it replaces all five packets and the profile.
//	@Description	clientAllowedIPs and remoteSubnets: absent or null keeps the stored value; "" / [] clears it (removes all subnets behind the client). With LAN segments set, subnets behind the client are permitted into those segments; a segment missing on the router fails with LAN_SEGMENTS_FAILED.
//	@Tags			managed-servers
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id		path		string						true	"Server id"
//	@Param			pubkey	path		string						true	"Peer public key (URL-encoded)"
//	@Param			body	body		UpdatePeerRequestDTO	true	"Peer update payload"
//	@Success		200		{object}	ServersAllResponse
//	@Failure		400		{object}	APIErrorEnvelope	"Any failure, including a peer missing on the router (code NOT_FOUND)"
//	@Router			/managed-servers/{id}/peers/{pubkey} [put]
func (h *ManagedServerHandler) UpdatePeer(w http.ResponseWriter, r *http.Request, id, pubkey string) {
	req, ok := parseJSON[managed.UpdatePeerRequest](w, r, http.MethodPut)
	if !ok {
		return
	}
	if err := h.svc.UpdatePeer(r.Context(), id, pubkey, req); err != nil {
		if code, ok := peerSubnetErrorCode(err); ok {
			response.Error(w, err.Error(), code)
			return
		}
		switch {
		case errors.Is(err, managed.ErrUnknownSignatureProfile):
			response.Error(w, err.Error(), "INVALID_SIGNATURE_PROFILE")
		case errors.Is(err, managed.ErrSignatureTooLarge):
			response.Error(w, err.Error(), "SIGNATURE_TOO_LARGE")
		case errors.Is(err, managed.ErrInvalidSignatureTag):
			response.Error(w, err.Error(), "SIGNATURE_INVALID_TAG")
		case errors.Is(err, peersubnet.ErrPeerNotFound):
			// Пир есть в записи, но снят с роутера мимо панели.
			response.Error(w, err.Error(), "NOT_FOUND")
		case errors.Is(err, managed.ErrUnknownLANSegment):
			// Сегмент сервера пропал с роутера — чинится пересохранением сегментов.
			response.Error(w, err.Error(), "LAN_SEGMENTS_FAILED")
		default:
			response.Error(w, err.Error(), "UPDATE_PEER_FAILED")
		}
		return
	}
	h.svc.InvalidateCache(id)
	h.publishServerUpdated()
	h.writeServersSnapshot(w, r)
}

// DeletePeer removes a peer from a managed server.
// DELETE /api/managed-servers/{id}/peers/{pubkey}
//
//	@Summary		Delete managed-server peer
//	@Description	Removes the peer identified by pubkey from the named managed server.
//	@Tags			managed-servers
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id		path		string	true	"Server id"
//	@Param			pubkey	path		string	true	"Peer public key (URL-encoded)"
//	@Param			endpoint	query		string	false	"Хост для [Peer] Endpoint вместо WAN/KeenDNS (прокси-обвязки шлют 127.0.0.1)"
//	@Success		200		{object}	ServersAllResponse
//	@Failure		400		{object}	APIErrorEnvelope	"Any failure (code DELETE_PEER_FAILED)"
//	@Router			/managed-servers/{id}/peers/{pubkey} [delete]
func (h *ManagedServerHandler) DeletePeer(w http.ResponseWriter, r *http.Request, id, pubkey string) {
	if r.Method != http.MethodDelete {
		response.MethodNotAllowed(w)
		return
	}
	if err := h.svc.DeletePeer(r.Context(), id, pubkey); err != nil {
		response.Error(w, err.Error(), "DELETE_PEER_FAILED")
		return
	}
	h.svc.InvalidateCache(id)
	h.publishServerUpdated()
	h.writeServersSnapshot(w, r)
}

// TogglePeer enables or disables a peer.
// POST /api/managed-servers/{id}/peers/{pubkey}/toggle
//
//	@Summary		Toggle managed-server peer enabled
//	@Description	Enables or disables the peer identified by pubkey on the named managed server.
//	@Tags			managed-servers
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id		path		string					true	"Server id"
//	@Param			pubkey	path		string					true	"Peer public key (URL-encoded)"
//	@Param			body	body		EnabledToggleRequest	true	"Enabled flag"
//	@Success		200		{object}	ServersAllResponse
//	@Failure		400		{object}	APIErrorEnvelope	"Any failure, including a peer missing on the router (code NOT_FOUND)"
//	@Router			/managed-servers/{id}/peers/{pubkey}/toggle [post]
func (h *ManagedServerHandler) TogglePeer(w http.ResponseWriter, r *http.Request, id, pubkey string) {
	req, ok := parseJSON[EnabledToggleRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	if err := h.svc.TogglePeer(r.Context(), id, pubkey, req.Enabled); err != nil {
		if errors.Is(err, peersubnet.ErrPeerNotFound) {
			// Пир есть в записи, но снят с роутера мимо панели.
			response.Error(w, err.Error(), "NOT_FOUND")
			return
		}
		response.Error(w, err.Error(), "TOGGLE_FAILED")
		return
	}
	h.svc.InvalidateCache(id)
	h.publishServerUpdated()
	h.writeServersSnapshot(w, r)
}

// PeerConf returns the WireGuard client .conf file for a peer.
// GET /api/managed-servers/{id}/peers/{pubkey}/conf
//
//	@Summary		Generate peer .conf
//	@Description	Returns the WireGuard client .conf for the peer identified by pubkey on the named managed server.
//	@Tags			managed-servers
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id		path		string	true	"Server id"
//	@Param			pubkey	path		string	true	"Peer public key (URL-encoded)"
//	@Success		200		{object}	PeerConfResponse
//	@Failure		404		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/managed-servers/{id}/peers/{pubkey}/conf [get]
func (h *ManagedServerHandler) PeerConf(w http.ResponseWriter, r *http.Request, id, pubkey string) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	conf, err := h.svc.GenerateConf(r.Context(), id, pubkey, r.URL.Query().Get("endpoint"))
	if err != nil {
		response.Error(w, err.Error(), "CONF_FAILED")
		return
	}
	response.Success(w, map[string]string{"conf": conf})
}
