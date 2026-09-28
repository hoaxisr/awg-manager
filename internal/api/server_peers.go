package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/peersubnet"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/signature"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/netif"
	"github.com/hoaxisr/awg-manager/internal/testing"
)

// ServerAddPeerRequestDTO is the body for POST /servers/{name}/peers.
type ServerAddPeerRequestDTO struct {
	Description string `json:"description" example:"My Phone"`
	TunnelIP    string `json:"tunnelIP" example:"10.0.14.2/32"`
	// DNS — резолвер пира для `.conf`: список IP через запятую. Пусто —
	// LAN-адрес роутера (#933).
	DNS string `json:"dns,omitempty" example:"192.168.1.1"`
	// ClientAllowedIPs — строка AllowedIPs в .conf клиента (CIDR через запятую,
	// пусто — весь трафик). RemoteSubnets — сети за клиентом, IPv4 CIDR (#713).
	ClientAllowedIPs string   `json:"clientAllowedIPs,omitempty" example:"10.0.14.0/24, 192.168.1.0/24"`
	RemoteSubnets    []string `json:"remoteSubnets,omitempty" example:"192.168.77.0/24"`
}

// ServerUpdatePeerRequestDTO is the body for PUT /servers/{name}/peers/{pubkey}.
type ServerUpdatePeerRequestDTO struct {
	Description string `json:"description" example:"My Phone"`
	TunnelIP    string `json:"tunnelIP" example:"10.0.14.2/32"`
	// Signature: nil — сигнатуру пира не трогать; объект — заменить все пять
	// полей и профиль целиком (пустые поля объекта стирают старые байты).
	Signature *PeerSignatureDTO `json:"signature,omitempty"`
	// DNS — резолвер пира: «прислали → присвоили», как у Description. Пусто
	// снимает свой резолвер и возвращает пира к LAN-адресу роутера (#933).
	DNS string `json:"dns,omitempty" example:"192.168.1.1"`
	// ClientAllowedIPs — строка AllowedIPs в .conf клиента (CIDR через запятую,
	// пусто — весь трафик). Отсутствие поля или null — не менять.
	ClientAllowedIPs *string `json:"clientAllowedIPs,omitempty" example:"10.0.14.0/24, 192.168.1.0/24"`
	// RemoteSubnets — сети за клиентом, IPv4 CIDR (#713); полная замена списка:
	// отсутствие поля или null — не менять; пустой список — снять все.
	RemoteSubnets *[]string `json:"remoteSubnets,omitempty" example:"192.168.77.0/24"`
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
		if parts[2] == "presets" {
			h.ServerPeerPresets(w, r, name)
			return
		}
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

// genKeyPair и genPSK — швы генерации ключей пира: подменяются в тестах,
// чтобы путь добавления пира не звал /opt/sbin/awg на хосте.
var (
	genKeyPair = managed.GenerateKeyPair
	genPSK     = managed.GeneratePresharedKey
)

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
	// Routes — для NewPeerRouter (сети за клиентом): без него сверка упала бы
	// nil-паникой посреди операции, а не чистым отказом до RCI.
	if h.commands == nil || h.commands.Wireguard == nil || h.commands.Routes == nil {
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
	if peerTunnelIPInUse(server, req.TunnelIP, h.storedPeerHost(name)) {
		response.Error(w, "tunnel IP already in use", "TUNNEL_IP_IN_USE")
		return
	}
	// Правило одно на обе серверные роли — берём его у managed, своей копии не
	// заводим: разойдясь, две копии дали бы пира, которого одна сторона
	// принимает, а другая нет.
	peerDNS, err := managed.ValidatePeerDNS(req.DNS)
	if err != nil {
		response.Error(w, err.Error(), "INVALID_PEER_DNS")
		return
	}
	clientAllowed, err := peersubnet.ValidateClientAllowedIPs(req.ClientAllowedIPs)
	if err != nil {
		response.Error(w, err.Error(), "INVALID_CLIENT_ALLOWED_IPS")
		return
	}
	// F508: занятые → валидация → запись → роутер под одной блокировкой,
	// общей с managed-путём.
	if len(req.RemoteSubnets) > 0 && h.managedSvc != nil {
		defer h.managedSvc.LockPeerSubnets()()
	}
	// Чтение роутера — после дешёвых локальных проверок и до ключей.
	remote, ok := h.validateRemoteSubnets(r.Context(), w, req.RemoteSubnets, managed.PeerRef{Iface: name})
	if !ok {
		return
	}

	// Сигнатура принадлежит пиру: генерируем по дефолтному профилю. Отказ
	// генератора — отказ создания пира (fail closed), молча выдавать пира
	// без имитации нельзя.
	sig, err := signature.Generate(signature.DefaultProfile)
	if err != nil {
		response.Error(w, err.Error(), "SIGNATURE_GENERATE_FAILED")
		return
	}

	privKey, pubKey, err := genKeyPair(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "KEYGEN_FAILED")
		return
	}
	if !isValidWGKey(pubKey) {
		response.Error(w, "generated public key is malformed", "KEYGEN_FAILED")
		return
	}
	psk, err := genPSK(r.Context())
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
		DNS:          peerDNS,

		ClientAllowedIPs: clientAllowed,
		RemoteSubnets:    remote,

		I1:               sig.Packets.I1,
		I2:               sig.Packets.I2,
		I3:               sig.Packets.I3,
		I4:               sig.Packets.I4,
		I5:               sig.Packets.I5,
		SignatureProfile: sig.Profile,
	}); err != nil {
		response.Error(w, err.Error(), "SAVE_FAILED")
		return
	}
	if err := h.commands.Wireguard.AddPeer(r.Context(), name, pubKey, psk, strings.TrimSpace(req.Description), ip.String(), true); err != nil {
		if derr := h.settings.DeleteServerPeerSecret(name, pubKey); derr != nil {
			h.log.Warn("add-peer", name, "rollback of stranded secret failed: "+derr.Error())
		}
		response.Error(w, err.Error(), "ADD_PEER_FAILED")
		return
	}
	// Шаги 3–4 спеки. Секрет записан ДО роутера (ключ не теряется), поэтому
	// откат здесь — снять пира и секрет: запись переживает только полный успех.
	// Свои allow-ips и маршруты Reconcile откатывает сам.
	if len(remote) > 0 {
		if err := peersubnet.Reconcile(r.Context(), ndmscommand.NewPeerRouter(h.commands), name, pubKey, []net.IP{ip}, remote); err != nil {
			h.logRollback("add-peer", name, err)
			h.rollbackAddedServerPeer(r.Context(), name, pubKey)
			response.Error(w, err.Error(), "ADD_PEER_FAILED")
			return
		}
	}
	h.bus.PublishInvalidated(events.ResourceServers, "server-peer-added")
	h.writeAll(w, r)
}

// UpdateServerPeer updates a peer's tunnel IP and/or description.
// PUT /api/servers/{name}/peers/{pubkey}
//
//	@Summary		Update server peer
//	@Description	Changes a peer's allowed-IP and/or description on the named WireGuard server. Returns the fresh servers snapshot.
//	@Description	clientAllowedIPs and remoteSubnets: absent or null keeps the stored value; "" / [] clears it (removes all subnets behind the client).
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
	// F508: занятые → валидация → сверка → запись под одной блокировкой, общей
	// с managed-путём; секрет ниже читается уже под ней, поэтому возврат «к
	// записанному» верен.
	if req.RemoteSubnets != nil && h.managedSvc != nil {
		defer h.managedSvc.LockPeerSubnets()()
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
	// Секрет читаем один раз: он же решает судьбу сигнатуры и он же
	// примиряется с изменением ниже.
	sec, hasSecret := h.settings.GetServerPeerSecret(name, pubkey)
	// Резолвер проверяем ДО обращения к роутеру — по той же причине, что и
	// сигнатуру: отказ обязан быть чистым.
	peerDNS, err := managed.ValidatePeerDNS(req.DNS)
	if err != nil {
		response.Error(w, err.Error(), "INVALID_PEER_DNS")
		return
	}
	// Резолверу негде жить без секрета — ровно как сигнатуре ниже: хранится он
	// в нём. Отказ ЯВНЫЙ, иначе ручка отвечала бы «обновлено», а значение
	// оседало в никуда. Пустое значение пропускаем: им правят описание пира,
	// заведённого вне панели, и снимать нечего.
	if peerDNS != "" && !hasSecret {
		response.Error(w, "ключ клиента недоступен (создан вне AWG Manager или через KeenDNS)", "NO_PEER_SECRET")
		return
	}
	// Поля сетей: nil (отсутствие или null) — значение пира не меняется
	// (решение владельца 28.09, перекрывает спеку 5.4); ""/[] — очистить.
	clientAllowed := ""
	if req.ClientAllowedIPs != nil {
		if clientAllowed, err = peersubnet.ValidateClientAllowedIPs(*req.ClientAllowedIPs); err != nil {
			response.Error(w, err.Error(), "INVALID_CLIENT_ALLOWED_IPS")
			return
		}
	}
	var reqRemote []string
	if req.RemoteSubnets != nil {
		reqRemote = *req.RemoteSubnets
	}
	// Обоим полям негде жить без секрета — как DNS и сигнатуре. Пустые
	// значения пропускаем: фронт шлёт их всегда, в т.ч. правя чужого пира.
	if (clientAllowed != "" || len(reqRemote) > 0) && !hasSecret {
		response.Error(w, "ключ клиента недоступен (создан вне AWG Manager или через KeenDNS)", "NO_PEER_SECRET")
		return
	}
	// Сигнатуру проверяем ДО обращения к роутеру: отказ обязан быть чистым,
	// без наполовину применённых изменений на NDMS.
	sigProfile := ""
	if req.Signature != nil {
		var err error
		if sigProfile, err = signature.ValidateProfileAndSize(req.Signature.Profile, req.Signature.packets()); err != nil {
			switch {
			case errors.Is(err, signature.ErrUnknownProtocol):
				response.Error(w, err.Error(), "INVALID_SIGNATURE_PROFILE")
			case errors.Is(err, signature.ErrPacketsTooLarge):
				response.Error(w, err.Error(), "SIGNATURE_TOO_LARGE")
			case errors.Is(err, signature.ErrInvalidPacketTag):
				response.Error(w, err.Error(), "SIGNATURE_INVALID_TAG")
			default:
				response.Error(w, err.Error(), "UPDATE_PEER_FAILED")
			}
			return
		}
		// Сигнатуре негде жить без секрета: пир создан вне AWG Manager,
		// приватного ключа у нас нет, и заводить огрызок секрета ради
		// имитации нельзя — .conf по нему всё равно не собрать.
		if !hasSecret {
			response.Error(w, "ключ клиента недоступен (создан вне AWG Manager или через KeenDNS)", "NO_PEER_SECRET")
			return
		}
	}

	oldIP := peerTunnelHostIP(peer)
	if hasSecret && sec.TunnelIP != "" {
		// У пира с записью адрес — из неё: с сетями за клиентом в allow-ips
		// «первый /32» может оказаться сетью за клиентом, и снялся бы не тот.
		if ip, _, err := net.ParseCIDR(sec.TunnelIP); err == nil {
			oldIP = ip.String()
		}
	}
	wantIPChange := req.TunnelIP != "" && req.TunnelIP != oldIP+"/32" && req.TunnelIP != oldIP
	newIP := ""
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
		newIP = newHost.String()
		// F512: занятость — как в Add. Правимый пир сам себе не мешает: его
		// адрес здесь считается так же, как oldIP выше, а oldIP != newIP.
		if peerTunnelIPInUse(server, req.TunnelIP, h.storedPeerHost(name)) {
			response.Error(w, "tunnel IP already in use", "TUNNEL_IP_IN_USE")
			return
		}
	}
	// Чтение роутера — после дешёвых локальных проверок. Сверка — только у
	// пира с записью: у чужого сетей за клиентом нет, а его allow-ips сверка
	// сняла бы как «лишние».
	reconcile := req.RemoteSubnets != nil && hasSecret
	var remote []string
	if reconcile {
		if remote, ok = h.validateRemoteSubnets(r.Context(), w, reqRemote, managed.PeerRef{Iface: name, PubKey: pubkey}); !ok {
			return
		}
	}
	prevRemote := sec.RemoteSubnets
	// Туннельные адреса (старый и новый) — не сети за клиентом.
	tunnelHosts := []net.IP{net.ParseIP(oldIP), net.ParseIP(newIP)}

	// revertIP — хранилище не пишется, значит /32 на роутере обязан вернуться
	// к записанному, иначе .conf выдаст адрес, которого у пира нет. Новый
	// снимается всегда (отсутствующий — не отказ): и когда старого не было
	// (чужая форма allow-ips), и когда добавление нового само отказало.
	revertIP := func() {
		if !wantIPChange {
			return
		}
		rbCtx, cancel := detachedCtx(r.Context())
		defer cancel()
		if err := h.commands.Wireguard.RemovePeerAllowIP(rbCtx, name, pubkey, newIP, "255.255.255.255"); err != nil {
			h.log.Warn("update-peer", name, "новый tunnel IP не снят после отказа: "+err.Error())
		}
		if oldIP == "" {
			return
		}
		if err := h.commands.Wireguard.AddPeerAllowIP(rbCtx, name, pubkey, oldIP, "255.255.255.255"); err != nil {
			h.log.Warn("update-peer", name, "tunnel IP не возвращён после отказа: "+err.Error())
		}
	}
	if wantIPChange {
		if err := h.commands.Wireguard.UpdatePeerAllowIPs(r.Context(), name, pubkey, oldIP, newIP); err != nil {
			// Старый /32 уже мог сняться до отказа добавления нового.
			revertIP()
			response.Error(w, err.Error(), "UPDATE_PEER_FAILED")
			return
		}
	}
	if req.Description != peer.Description {
		if err := h.commands.Wireguard.SetPeerComment(r.Context(), name, pubkey, strings.TrimSpace(req.Description)); err != nil {
			revertIP()
			response.Error(w, err.Error(), "UPDATE_PEER_FAILED")
			return
		}
	}
	// Сверка с роутером, не разница с записью (F509): расхождение прошлых
	// сбоев это сохранение снимает.
	if reconcile {
		if err := peersubnet.Reconcile(r.Context(), ndmscommand.NewPeerRouter(h.commands), name, pubkey, tunnelHosts, remote); err != nil {
			h.logRollback("update-peer", name, err)
			revertIP()
			response.Error(w, err.Error(), "UPDATE_PEER_FAILED")
			return
		}
	}
	// Keep the stored secret (source of truth for .conf regen) in sync with
	// the router change. Runs unconditionally so a retry after a prior save
	// failure still reconciles even when the router side is now a no-op. The
	// error is surfaced, not swallowed: a stale stored IP would hand out a
	// wrong .conf after reboot.
	if hasSecret {
		changed := false
		if req.TunnelIP != "" && sec.TunnelIP != req.TunnelIP {
			sec.TunnelIP = req.TunnelIP
			changed = true
		}
		if sec.Description != req.Description {
			sec.Description = req.Description
			changed = true
		}
		if sec.DNS != peerDNS {
			sec.DNS = peerDNS
			changed = true
		}
		if req.ClientAllowedIPs != nil && sec.ClientAllowedIPs != clientAllowed {
			sec.ClientAllowedIPs = clientAllowed
			changed = true
		}
		if req.RemoteSubnets != nil && !slices.Equal(sec.RemoteSubnets, remote) {
			sec.RemoteSubnets = remote
			changed = true
		}
		if req.Signature != nil {
			sec.I1, sec.I2 = req.Signature.I1, req.Signature.I2
			sec.I3, sec.I4, sec.I5 = req.Signature.I3, req.Signature.I4, req.Signature.I5
			// Канонический ключ, не то, что прислали: валидация выше
			// принимает " SIP " — хранить такое нельзя.
			sec.SignatureProfile = sigProfile
			changed = true
		}
		if changed {
			if err := h.settings.SetServerPeerSecret(name, pubkey, sec); err != nil {
				// Роутер ушёл вперёд записи: возвращаем его к ней — карточка
				// и .conf показывают запись.
				if reconcile {
					rbCtx, cancel := detachedCtx(r.Context())
					if rbErr := peersubnet.Reconcile(rbCtx, ndmscommand.NewPeerRouter(h.commands), name, pubkey, tunnelHosts, prevRemote); rbErr != nil {
						h.log.Warn("update-peer", name, "сети за клиентом не возвращены к записи после отказа сохранения: "+rbErr.Error())
					}
					cancel()
				}
				revertIP()
				response.Error(w, err.Error(), "SAVE_FAILED")
				return
			}
		}
	}
	h.bus.PublishInvalidated(events.ResourceServers, "server-peer-updated")
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
	// Свои маршруты — до снятия пира и fail-closed (11.B/11.6): маршрут-сирота
	// без пира никто уже не снимет. Все с меткой пира, найденные на роутере, а
	// не список записи: сирота прошлого сбоя в записи не значится.
	if err := peersubnet.RemoveRoutes(r.Context(), ndmscommand.NewPeerRouter(h.commands), name, pubkey); err != nil {
		response.Error(w, err.Error(), "DELETE_PEER_FAILED")
		return
	}
	if err := h.commands.Wireguard.RemovePeer(r.Context(), name, pubkey); err != nil {
		response.Error(w, err.Error(), "DELETE_PEER_FAILED")
		return
	}
	if err := h.settings.DeleteServerPeerSecret(name, pubkey); err != nil {
		h.log.Warn("delete-peer", name, "peer removed from router but its secret stayed in store: "+err.Error())
	}
	h.bus.PublishInvalidated(events.ResourceServers, "server-peer-deleted")
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
	peer := findServerPeer(server, pubkey)
	if peer == nil {
		response.Error(w, "peer not found", "NOT_FOUND")
		return
	}
	if err := h.commands.Wireguard.SetPeerConnect(r.Context(), name, pubkey, req.Enabled, peer.Description); err != nil {
		response.Error(w, err.Error(), "TOGGLE_FAILED")
		return
	}
	h.bus.PublishInvalidated(events.ResourceServers, "server-peer-toggled")
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
//	@Param			endpoint	query		string	false	"Хост для [Peer] Endpoint вместо WAN/KeenDNS (прокси-обвязки шлют 127.0.0.1)"
//	@Success		200		{object}	PeerConfResponse
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
	conf, err := h.generateServerPeerConf(r.Context(), server, pubkey, sec, r.URL.Query().Get("endpoint"))
	if err != nil {
		response.Error(w, err.Error(), "CONF_FAILED")
		return
	}
	response.Success(w, map[string]string{"conf": conf})
}

// generateServerPeerConf: непустой endpointHost идёт в [Peer] Endpoint без
// обращения к WAN/KeenDNS — так конфиг просят прокси-обвязки (FreeTurn),
// которые всё равно ведут клиента на 127.0.0.1 и без WAN падали бы зря.
func (h *ServersHandler) generateServerPeerConf(ctx context.Context, server *ndms.WireguardServer, pubkey string, sec storage.ServerPeerSecret, endpointHost string) (string, error) {
	endpoint := endpointHost
	if endpoint == "" {
		var err error
		if endpoint, err = h.resolveServerEndpoint(ctx, server.ID); err != nil {
			return "", err
		}
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
	// Резолвер: свой у пира → LAN-адрес роутера. Зашитого `1.1.1.1, 8.8.8.8`
	// здесь больше нет (#933): абонент ходил через туннель, а имена резолвил у
	// Cloudflare с Google — мимо роутера и мимо всех его правил. Роутер не
	// определился — строки нет вовсе, подставлять что-то «на всякий случай»
	// значит вернуть ту же дыру под другим адресом.
	dns := sec.DNS
	if dns == "" {
		dns = netif.RouterLANIP(storage.DefaultInterface)
	}
	if dns != "" {
		b.WriteString(fmt.Sprintf("DNS = %s\n", dns))
	} else {
		// Молчать здесь нельзя: `AllowedIPs` ниже заворачивает в туннель ВЕСЬ
		// трафик, значит и собственный резолвер клиента, — файл без строки
		// `DNS` оставит его без резолва, и узнает об этом клиент, а не
		// владелец. У FreeTurn вдвое хуже: этот `.conf` вшивается в ссылку и
		// перевыпуску не подлежит (F391).
		h.log.Warn("peer-conf", pubkey,
			"LAN-адрес роутера не определился и свой DNS у пира не задан — в конфигурации не будет строки DNS")
	}
	b.WriteString(fmt.Sprintf("MTU = %d\n", mtu))

	if h.queries != nil && h.queries.WGServers != nil {
		if ascRaw, err := h.queries.WGServers.GetASCParams(ctx, server.ID, true); err == nil && ascRaw != nil {
			signature.WriteASCConf(&b, ascRaw, secPackets(sec))
		}
	}

	b.WriteString("\n[Peer]\n")
	b.WriteString(fmt.Sprintf("PublicKey = %s\n", server.PublicKey))
	if sec.PresharedKey != "" {
		b.WriteString(fmt.Sprintf("PresharedKey = %s\n", sec.PresharedKey))
	}
	b.WriteString(fmt.Sprintf("Endpoint = %s:%d\n", formatWireguardEndpointHost(endpoint), server.ListenPort))
	allowed := sec.ClientAllowedIPs
	if allowed == "" {
		allowed = peersubnet.DefaultClientAllowedIPs
	}
	b.WriteString("AllowedIPs = " + allowed + "\n")
	b.WriteString("PersistentKeepalive = 25\n")
	return b.String(), nil
}

func (h *ServersHandler) resolveServerEndpoint(ctx context.Context, serverID string) (string, error) {
	var storedEndpoint, keenDNSDomain string
	if meta, ok := h.settings.GetServerInterfaceMeta(serverID); ok {
		storedEndpoint = meta.Endpoint
	}
	// Имя KeenDNS годится Endpoint'ом ТОЛЬКО при прямом доступе: в остальных
	// режимах оно ведёт на прокси NDMS, который проксирует HTTP, а не порт
	// WireGuard-сервера. Конфигурация с таким Endpoint выглядит правильной и
	// не подключается, а узнаёт об этом клиент, не владелец (F392).
	if h.queries != nil && h.queries.KeenDNS != nil {
		if info, err := h.queries.KeenDNS.Get(ctx); err == nil && info.DirectAccess() {
			keenDNSDomain = info.Domain
		}
	}
	if host := resolveWireguardClientEndpointHost(storedEndpoint, keenDNSDomain); host != "" {
		return host, nil
	}
	h.log.Info("endpoint", serverID, "KeenDNS/CrazeDNS не настроен — endpoint WireGuard-сервера будет указан как WAN IP")
	return testing.GetWANIPWithFallback(ctx, h.queries.WANInterfaceAddress)
}

// secPackets — сигнатура пира из его секрета в виде, который понимает
// signature.WriteASCConf.
func secPackets(sec storage.ServerPeerSecret) signature.GeneratedPackets {
	return signature.GeneratedPackets{I1: sec.I1, I2: sec.I2, I3: sec.I3, I4: sec.I4, I5: sec.I5}
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

// peerTunnelIPInUse: у пира с записью адрес — из неё (storedHost), эвристика
// «первый /32 в allow-ips» только без записи: с сетями за клиентом в allow-ips
// кандидатов больше одного (#713). Сравниваются разобранные IP, не префиксы
// строк: "10.0.0.2" — префикс "10.0.0.20", но другой адрес.
func peerTunnelIPInUse(server *ndms.WireguardServer, tunnelIP string, storedHost func(pubkey string) string) bool {
	host, _, err := net.ParseCIDR(tunnelIP)
	if err != nil {
		return false
	}
	for i := range server.Peers {
		existing := ""
		if storedHost != nil {
			existing = storedHost(server.Peers[i].PublicKey)
		}
		if existing == "" {
			existing = peerTunnelHostIP(&server.Peers[i])
		}
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
	dto.BuiltIn = srv.Description == ndms.BuiltInVPNServerDescription
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
			dto.Peers[i].I1, dto.Peers[i].I2 = sec.I1, sec.I2
			dto.Peers[i].I3, dto.Peers[i].I4, dto.Peers[i].I5 = sec.I3, sec.I4, sec.I5
			dto.Peers[i].SignatureProfile = sec.SignatureProfile
			dto.Peers[i].DNS = sec.DNS
			dto.Peers[i].ClientAllowedIPs = sec.ClientAllowedIPs
			dto.Peers[i].RemoteSubnets = sec.RemoteSubnets
			dto.Peers[i].TunnelIP = sec.TunnelIP
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

// ServerPeerPresets returns AllowedIPs presets for a peer of a system server.
// GET /api/servers/{name}/peers/presets
//
//	@Summary		Peer AllowedIPs presets
//	@Description	routerOnly — server subnet plus all LAN bridges; exceptRouter — everything else plus /32 of the resolver and ::/0.
//	@Tags			servers
//	@Produce		json
//	@Security		CookieAuth
//	@Param			name	path		string	true	"Interface name (e.g. Wireguard0)"
//	@Param			dns		query		string	false	"Peer DNS as typed in the form; empty — router LAN IP"
//	@Success		200		{object}	PeerPresetsResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Router			/servers/{name}/peers/presets [get]
func (h *ServersHandler) ServerPeerPresets(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if h.managedSvc == nil {
		response.Error(w, "managed service not initialized", "INTERNAL_ERROR")
		return
	}
	server, ok := h.requireListedServer(r.Context(), w, name)
	if !ok {
		return
	}
	dns, err := managed.ValidatePeerDNS(r.URL.Query().Get("dns"))
	if err != nil {
		response.Error(w, err.Error(), "INVALID_PEER_DNS")
		return
	}
	if dns == "" {
		dns = netif.RouterLANIP(storage.DefaultInterface)
	}
	serverNet := serverSubnetOf(server)
	if serverNet == nil {
		response.Error(w, "server subnet unknown", "PRESETS_FAILED")
		return
	}
	p, err := h.managedSvc.PresetsFor(r.Context(), serverNet, nil, dns)
	if err != nil {
		response.Error(w, err.Error(), "PRESETS_FAILED")
		return
	}
	response.Success(w, PeerPresetsDTO{RouterOnly: p.RouterOnly, ExceptRouter: p.ExceptRouter})
}

// serverSubnetOf — подсеть системного сервера из Address/Mask (маска точечная).
func serverSubnetOf(server *ndms.WireguardServer) *net.IPNet {
	ip := net.ParseIP(server.Address)
	mask := net.IPMask(net.ParseIP(server.Mask).To4())
	if ip == nil || mask == nil {
		return nil
	}
	return &net.IPNet{IP: ip.Mask(mask), Mask: mask}
}

// storedPeerHost — host tunnel IP пира из его секрета; "" без секрета.
func (h *ServersHandler) storedPeerHost(serverID string) func(pubkey string) string {
	return func(pubkey string) string {
		sec, ok := h.settings.GetServerPeerSecret(serverID, pubkey)
		if !ok || sec.TunnelIP == "" {
			return ""
		}
		ip, _, err := net.ParseCIDR(sec.TunnelIP)
		if err != nil {
			return ""
		}
		return ip.String()
	}
}

// validateRemoteSubnets — шаги 1–2 спеки для системного пути: снимок занятых
// сетей и валидация до единого обращения к роутеру. Отказ уже записан в w.
func (h *ServersHandler) validateRemoteSubnets(ctx context.Context, w http.ResponseWriter, subnets []string, exclude managed.PeerRef) ([]string, bool) {
	if len(subnets) == 0 {
		return nil, true
	}
	if h.managedSvc == nil {
		response.Error(w, "managed service not initialized", "INTERNAL_ERROR")
		return nil, false
	}
	occupied, err := h.managedSvc.OccupiedSubnets(ctx, exclude)
	if err != nil {
		response.Error(w, err.Error(), "GET_FAILED")
		return nil, false
	}
	remote, err := peersubnet.ValidateRemoteSubnets(subnets, occupied)
	if err != nil {
		code, ok := peerSubnetErrorCode(err)
		if !ok {
			code = "INVALID_REMOTE_SUBNETS"
		}
		response.Error(w, err.Error(), code)
		return nil, false
	}
	return remote, true
}

// peerRollbackTimeout — бюджет отката на роутере: ctx запроса к этому моменту
// может быть уже отменён (обрыв клиента — самая вероятная причина сбоя), а
// откат обязан дойти.
const peerRollbackTimeout = 30 * time.Second

func detachedCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), peerRollbackTimeout)
}

// rollbackAddedServerPeer снимает с роутера только что добавленного пира и его
// секрет: сначала все маршруты с его меткой, найденные на роутере (сироты
// незавершённого отката), потом пира — allow-ips уходят с ним. Пир не снялся —
// секрет остаётся: без него пир на роутере — сирота с потерянным ключом, а с
// ним его видно в панели и можно удалить.
func (h *ServersHandler) rollbackAddedServerPeer(ctx context.Context, name, pubKey string) {
	rbCtx, cancel := detachedCtx(ctx)
	defer cancel()
	if err := peersubnet.RemoveRoutes(rbCtx, ndmscommand.NewPeerRouter(h.commands), name, pubKey); err != nil {
		h.log.Warn("add-peer", name, "маршруты сетей за клиентом не сняты при откате: "+err.Error())
	}
	if err := h.commands.Wireguard.RemovePeer(rbCtx, name, pubKey); err != nil {
		h.log.Warn("add-peer", name, "пир не снят после отказа сетей за клиентом, секрет оставлен: "+err.Error())
		// Сетей на роутере запись не держит: их увидит и снимет сверка
		// следующего сохранения, маршруты — удаление пира (оба читают роутер).
		if sec, ok := h.settings.GetServerPeerSecret(name, pubKey); ok && len(sec.RemoteSubnets) > 0 {
			sec.RemoteSubnets = nil
			if err := h.settings.SetServerPeerSecret(name, pubKey, sec); err != nil {
				h.log.Warn("add-peer", name, "сети не убраны из оставленного секрета: "+err.Error())
			}
		}
		return
	}
	if err := h.settings.DeleteServerPeerSecret(name, pubKey); err != nil {
		h.log.Warn("add-peer", name, "rollback of stranded secret failed: "+err.Error())
	}
}

// logRollback — отказ отката Reconcile в журнал приложения (хранилище не тронуто).
func (h *ServersHandler) logRollback(op, name string, err error) {
	var rb *peersubnet.RollbackError
	if errors.As(err, &rb) {
		h.log.Warn(op, name, "откат сетей за клиентом не завершён: "+rb.Rollback.Error())
	}
}
