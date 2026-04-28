package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// AddPeerRequestDTO is the swagger-visible body for POST /managed-servers/{id}/peers.
type AddPeerRequestDTO struct {
	Description string `json:"description" example:"My Phone"`
	TunnelIP    string `json:"tunnelIP" example:"10.10.0.2/32"`
	DNS         string `json:"dns,omitempty" example:"8.8.8.8"`
}

// UpdatePeerRequestDTO is the swagger-visible body for PUT /managed-servers/{id}/peers/{pubkey}.
type UpdatePeerRequestDTO struct {
	Description string `json:"description" example:"My Phone"`
	TunnelIP    string `json:"tunnelIP" example:"10.10.0.2/32"`
	DNS         string `json:"dns,omitempty" example:"8.8.8.8"`
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
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/managed-servers/{id}/peers [post]
func (h *ManagedServerHandler) AddPeer(w http.ResponseWriter, r *http.Request, id string) {
	req, ok := parseJSON[managed.AddPeerRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	peer, err := h.svc.AddPeer(r.Context(), id, req)
	if err != nil {
		response.Error(w, err.Error(), "ADD_PEER_FAILED")
		return
	}
	response.Success(w, peer)
	h.publishServerUpdated()
}

// UpdatePeer updates an existing peer of a managed server.
// PUT /api/managed-servers/{id}/peers/{pubkey}
//
//	@Summary		Update managed-server peer
//	@Description	Updates fields (name, allowed-ips, ...) of the peer identified by pubkey on the named managed server.
//	@Tags			managed-servers
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id		path		string						true	"Server id"
//	@Param			pubkey	path		string						true	"Peer public key (URL-encoded)"
//	@Param			body	body		UpdatePeerRequestDTO	true	"Peer update payload"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		404		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/managed-servers/{id}/peers/{pubkey} [put]
func (h *ManagedServerHandler) UpdatePeer(w http.ResponseWriter, r *http.Request, id, pubkey string) {
	req, ok := parseJSON[managed.UpdatePeerRequest](w, r, http.MethodPut)
	if !ok {
		return
	}
	if err := h.svc.UpdatePeer(r.Context(), id, pubkey, req); err != nil {
		response.Error(w, err.Error(), "UPDATE_PEER_FAILED")
		return
	}
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
//	@Success		200		{object}	map[string]interface{}
//	@Failure		404		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
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
	h.publishServerUpdated()
	h.writeServersSnapshot(w, r)
}

// togglePeerRequest is the body for the toggle endpoint. Only carries
// the new enabled state — the pubkey now travels in the URL.
type togglePeerRequest struct {
	Enabled bool `json:"enabled"`
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
//	@Param			body	body		map[string]interface{}	true	"{enabled: bool}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/managed-servers/{id}/peers/{pubkey}/toggle [post]
func (h *ManagedServerHandler) TogglePeer(w http.ResponseWriter, r *http.Request, id, pubkey string) {
	req, ok := parseJSON[togglePeerRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	if err := h.svc.TogglePeer(r.Context(), id, pubkey, req.Enabled); err != nil {
		response.Error(w, err.Error(), "TOGGLE_FAILED")
		return
	}
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
//	@Success		200		{object}	map[string]interface{}
//	@Failure		404		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/managed-servers/{id}/peers/{pubkey}/conf [get]
func (h *ManagedServerHandler) PeerConf(w http.ResponseWriter, r *http.Request, id, pubkey string) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	conf, err := h.svc.GenerateConf(r.Context(), id, pubkey)
	if err != nil {
		response.Error(w, err.Error(), "CONF_FAILED")
		return
	}
	response.Success(w, map[string]string{"conf": conf})
}
