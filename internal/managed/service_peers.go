package managed

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/managed/peerip"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/peersubnet"
	"github.com/hoaxisr/awg-manager/internal/signature"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ErrInvalidPeerDNS is returned when a peer's DNS is not a plain list of
// IP addresses. The value is rendered verbatim into the client .conf as
// "DNS = …", so anything else — a hostname, a second line, a "PostUp ="
// smuggled after a newline — is a config injection that wg-quick would
// execute on the user's machine when they import the file.
var ErrInvalidPeerDNS = errors.New("peer DNS must be a comma-separated list of IP addresses")

// ValidatePeerDNS checks and canonicalises a peer DNS list. Empty is
// allowed (the server default applies). Each entry must parse as an IP;
// the result is re-joined with ", " so no original bytes survive.
func ValidatePeerDNS(dns string) (string, error) {
	if strings.TrimSpace(dns) == "" {
		return "", nil
	}
	parts := strings.Split(dns, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		ip := net.ParseIP(strings.TrimSpace(p))
		if ip == nil {
			return "", fmt.Errorf("%w: %q", ErrInvalidPeerDNS, p)
		}
		out = append(out, ip.String())
	}
	return strings.Join(out, ", "), nil
}

// detachedCtx — ctx отката: запрос к этому моменту может быть уже отменён,
// а откат обязан дойти.
func detachedCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), peersubnet.RollbackTimeout)
}

// rollbackAddedPeer снимает с роутера только что добавленного пира, которого
// не будет в хранилище: сначала все маршруты с его меткой, найденные на
// роутере (router != nil — у пира были сети), потом сам пир; allow-ips уходят
// вместе с ним. Ошибки — в журнал под op вызывающего: он уже возвращает
// первичную.
func (s *Service) rollbackAddedPeer(ctx context.Context, op, iface, pubKey, name string, router peersubnet.Router) {
	rbCtx, cancel := detachedCtx(ctx)
	defer cancel()
	if router != nil {
		if err := peersubnet.RemoveRoutes(rbCtx, router, iface, pubKey); err != nil {
			s.appLog.Warn(op, name, "маршруты сетей за клиентом не сняты при откате: "+err.Error())
		}
	}
	if err := s.rciRemovePeer(rbCtx, iface, pubKey); err != nil {
		if errors.Is(err, command.ErrPeerPresenceUnknown) {
			// Ключ мог и не встать (отказ самого добавления): «не снят» было бы ложью.
			s.appLog.Warn(op, name, "снятие пира при откате отказало, есть ли он на роутере — неизвестно: "+err.Error())
			return
		}
		s.appLog.Warn(op, name, "пир не снят при откате: "+err.Error())
	}
}

// AddPeer adds a new client peer to the managed server identified by id.
// Returns the created peer (including private key for .conf generation).
func (s *Service) AddPeer(ctx context.Context, id string, req AddPeerRequest) (*storage.ManagedPeer, error) {
	// F508: занятые → валидация → роутер → запись под одной блокировкой.
	if len(req.RemoteSubnets) > 0 {
		defer s.LockPeerSubnets()()
	}
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return nil, fmt.Errorf("managed server not found: %s", id)
	}

	// An empty TunnelIP means "allocate": the first free host address in
	// the server's subnet. The MCP tools rely on this — an address invented
	// by a model either collides or lands outside the subnet.
	if strings.TrimSpace(req.TunnelIP) == "" {
		used := make([]string, 0, len(server.Peers))
		for _, p := range server.Peers {
			used = append(used, p.TunnelIP)
		}
		req.TunnelIP = peerip.NextFree(server.Address, used)
		if req.TunnelIP == "" {
			return nil, peerip.ErrNoFree
		}
	}

	// Validate tunnel IP
	if err := s.validateTunnelIP(server, req.TunnelIP); err != nil {
		return nil, err
	}
	dns, err := ValidatePeerDNS(req.DNS)
	if err != nil {
		return nil, err
	}
	req.DNS = dns
	clientAllowed, err := peersubnet.ValidateClientAllowedIPs(req.ClientAllowedIPs)
	if err != nil {
		return nil, err
	}
	// Check tunnel IP not already used
	for _, p := range server.Peers {
		if p.TunnelIP == req.TunnelIP {
			return nil, fmt.Errorf("tunnel IP %s already in use", req.TunnelIP)
		}
	}

	// Generate keys
	privKey, pubKey, err := s.keyGen.GenerateKeyPair(ctx)
	if err != nil {
		return nil, fmt.Errorf("generate keypair: %w", err)
	}

	psk, err := s.keyGen.GeneratePresharedKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("generate PSK: %w", err)
	}

	// Сигнатура принадлежит пиру: генерируем по дефолтному профилю. Отказ
	// генерации — отказ создания пира (fail closed), молча выдавать пира
	// без имитации нельзя.
	sig, err := signature.Generate(signature.DefaultProfile)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSignatureGenerate, err)
	}

	// Parse tunnel IP
	ip, _, err := net.ParseCIDR(req.TunnelIP)
	if err != nil {
		return nil, fmt.Errorf("invalid tunnel IP: %w", err)
	}

	iface := server.InterfaceName

	// Сети за клиентом: снимок занятых и валидация ДО RCI, после дешёвых локальных проверок — отказ чистый.
	var remote []string
	var router peersubnet.Router
	var aclEdit peerACLEdit
	if len(req.RemoteSubnets) > 0 {
		occupied, err := s.OccupiedSubnets(ctx, PeerRef{})
		if err != nil {
			return nil, fmt.Errorf("occupied subnets: %w", err)
		}
		if remote, err = peersubnet.ValidateRemoteSubnets(req.RemoteSubnets, occupied); err != nil {
			return nil, err
		}
		if aclEdit, err = s.planPeerSubnetsACL(ctx, server, remote, nil, append(serverPeerSubnets(server.Peers), remote...)); err != nil {
			return nil, err
		}
		if router, err = s.peerRouter(); err != nil {
			return nil, err
		}
	}

	// Add peer with all parameters in a single RCI call:
	// key, preshared-key, comment, allow-ips (peer /32 only), connect
	if err := s.rciAddPeer(ctx, iface, pubKey, psk, strings.TrimSpace(req.Description), ip.String(), true); err != nil {
		// NDMS применяет payload поэлементно: при вложенном отказе ключ/PSK
		// могли встать — без отката это невидимый сирота. Ключ свежий, чужого
		// пира снятие не заденет.
		s.rollbackAddedPeer(ctx, "add-peer", iface, pubKey, req.Description, nil)
		return nil, fmt.Errorf("add peer: %w", err)
	}

	// Шаги 3–4 спеки: allow-ips и маршруты на сети за клиентом, затем их
	// permit в LAN-сегменты. Отказ — снять только что созданного пира: запись
	// переживает только полный успех.
	undoACL := func(context.Context) {}
	if len(remote) > 0 {
		if err := peersubnet.Reconcile(ctx, router, iface, pubKey, []net.IP{ip}, remote); err != nil {
			s.logRollback("add-peer", req.Description, err)
			s.rollbackAddedPeer(ctx, "add-peer", iface, pubKey, req.Description, router)
			return nil, fmt.Errorf("apply remote subnets: %w", err)
		}
		if undoACL, err = s.applyPeerSubnetsACL(ctx, server, aclEdit); err != nil {
			s.rollbackAddedPeer(ctx, "add-peer", iface, pubKey, req.Description, router)
			return nil, fmt.Errorf("apply remote subnets: %w", err)
		}
	}

	// Save to storage
	peer := storage.ManagedPeer{
		PublicKey:    pubKey,
		PrivateKey:   privKey,
		PresharedKey: psk,
		Description:  req.Description,
		TunnelIP:     req.TunnelIP,
		DNS:          req.DNS,
		Enabled:      true,

		ClientAllowedIPs: clientAllowed,
		RemoteSubnets:    remote,

		I1:               sig.Packets.I1,
		I2:               sig.Packets.I2,
		I3:               sig.Packets.I3,
		I4:               sig.Packets.I4,
		I5:               sig.Packets.I5,
		SignatureProfile: sig.Profile,
	}
	if err := s.settings.UpdateManagedServer(id, func(sv *storage.ManagedServer) error {
		// The "already in use" check above ran on a snapshot. Two
		// concurrent adds without an explicit address both allocate the
		// same one; re-checking under the store lock is what stops the
		// second from persisting a duplicate.
		for _, p := range sv.Peers {
			if p.TunnelIP == req.TunnelIP {
				return fmt.Errorf("tunnel IP %s already in use", req.TunnelIP)
			}
		}
		sv.Peers = append(sv.Peers, peer)
		return nil
	}); err != nil {
		// Без записи пир на роутере — сирота, которого никто не снимет.
		rbCtx, cancel := detachedCtx(ctx)
		undoACL(rbCtx)
		cancel()
		s.rollbackAddedPeer(ctx, "add-peer", iface, pubKey, req.Description, router)
		return nil, fmt.Errorf("save to storage: %w", err)
	}

	s.log.Info("peer added", "interface", iface, "description", req.Description, "tunnelIP", req.TunnelIP)
	s.appLog.Info("add-peer", req.Description, fmt.Sprintf("Peer %s added", req.Description))
	return &peer, nil
}

// UpdatePeer updates an existing peer's description and/or tunnel IP.
func (s *Service) UpdatePeer(ctx context.Context, id, pubkey string, req UpdatePeerRequest) error {
	load := func() (*storage.ManagedServer, int, error) {
		server, ok := s.settings.GetManagedServerByID(id)
		if !ok {
			return nil, -1, fmt.Errorf("managed server not found: %s", id)
		}
		idx := s.findPeerIndex(server, pubkey)
		if idx < 0 {
			return nil, -1, fmt.Errorf("peer not found: %s", pubkey)
		}
		return server, idx, nil
	}
	server, idx, err := load()
	if err != nil {
		return err
	}
	// Сверка — только когда сети есть хоть с одной стороны: фронт шлёт [] и у
	// пира без сетей, такой правке ни блокировка, ни чтения роутера не нужны.
	// Решение — по снимку; F508: занятые → валидация → сверка → запись под
	// одной блокировкой, и запись перечитывается уже под ней — возврат «к
	// записанному» при сбое записи верен.
	needsReconcile := func() bool {
		return req.RemoteSubnets != nil && (len(*req.RemoteSubnets) > 0 || len(server.Peers[idx].RemoteSubnets) > 0)
	}
	reconcile := needsReconcile()
	// Смена адреса и имени — под той же блокировкой: удаление пира её берёт,
	// и проверка наличия пира перед постом по ключу не устареет посреди правки.
	tunnelChange := req.TunnelIP != "" && req.TunnelIP != server.Peers[idx].TunnelIP
	descChange := req.Description != server.Peers[idx].Description
	if reconcile || tunnelChange || descChange {
		defer s.LockPeerSubnets()()
		if server, idx, err = load(); err != nil {
			return err
		}
		// Снимок мог устареть, пока ждали блокировку: соседняя правка
		// записала пиру сети — тогда присланный [] обязан их снять.
		reconcile = needsReconcile()
	}
	peer := &server.Peers[idx]
	iface := server.InterfaceName

	// Validate inputs BEFORE touching RCI or storage so we can fail clean.
	dns, err := ValidatePeerDNS(req.DNS)
	if err != nil {
		return err
	}
	req.DNS = dns
	// Поля сетей: nil — не присланы, значение пира не меняется (решение
	// владельца 28.09, перекрывает спеку 5.4). Проверки — только присланного.
	clientAllowed := ""
	if req.ClientAllowedIPs != nil {
		if clientAllowed, err = peersubnet.ValidateClientAllowedIPs(*req.ClientAllowedIPs); err != nil {
			return err
		}
	}
	sigProfile := ""
	if req.Signature != nil {
		var err error
		if sigProfile, err = signature.ValidateProfileAndSize(req.Signature.Profile, req.Signature.packets()); err != nil {
			switch {
			case errors.Is(err, signature.ErrUnknownProtocol):
				return fmt.Errorf("%w: %s", ErrUnknownSignatureProfile, req.Signature.Profile)
			case errors.Is(err, signature.ErrPacketsTooLarge):
				return ErrSignatureTooLarge
			case errors.Is(err, signature.ErrInvalidPacketTag):
				return ErrInvalidSignatureTag
			default:
				// Чужую ошибку не переклеиваем в «слишком большая»: вызывающий
				// не должен показывать пользователю неверную причину.
				return fmt.Errorf("validate signature: %w", err)
			}
		}
	}
	wantTunnelChange := req.TunnelIP != "" && req.TunnelIP != peer.TunnelIP
	if wantTunnelChange {
		if err := s.validateTunnelIP(server, req.TunnelIP); err != nil {
			return err
		}
		// Check not used by another peer
		for i, p := range server.Peers {
			if i != idx && p.TunnelIP == req.TunnelIP {
				return fmt.Errorf("tunnel IP %s already in use", req.TunnelIP)
			}
		}
	}
	// Чтение роутера — после дешёвых локальных проверок.
	var remote []string
	var router peersubnet.Router
	var aclEdit peerACLEdit
	if reconcile {
		if len(*req.RemoteSubnets) > 0 {
			occupied, err := s.OccupiedSubnets(ctx, PeerRef{Iface: iface, PubKey: pubkey})
			if err != nil {
				return fmt.Errorf("occupied subnets: %w", err)
			}
			if remote, err = peersubnet.ValidateRemoteSubnets(*req.RemoteSubnets, occupied); err != nil {
				return err
			}
		}
		// «Было» для ACL — запись: её правила ставил этот же путь.
		if aclEdit, err = s.planPeerSubnetsACL(ctx, server, subnetDiff(remote, peer.RemoteSubnets), subnetDiff(peer.RemoteSubnets, remote), peerNetsWith(server.Peers, pubkey, remote)); err != nil {
			return err
		}
		if router, err = s.peerRouter(); err != nil {
			return err
		}
	}
	// Туннельные адреса (старый и новый) — не сети за клиентом: сверка их не
	// снимает, в каком бы состоянии смена адреса ни застала allow-ips.
	var tunnelHosts []net.IP
	for _, t := range []string{peer.TunnelIP, req.TunnelIP} {
		if ip, _, err := net.ParseCIDR(t); err == nil {
			tunnelHosts = append(tunnelHosts, ip)
		}
	}

	// Apply RCI changes (tunnel IP, description) before persisting.
	oldIPStr, newIPStr := "", ""

	// Хранилище не пишется — /32 на роутере обязан вернуться к записанному,
	// иначе .conf выдаст адрес, которого у пира нет. Новый снимается всегда
	// (отсутствующий — не отказ), в т.ч. когда старого в записи не было —
	// паритет с revertIP системного пути (api/server_peers.go).
	revertTunnelIP := func(rbCtx context.Context, after string) {
		if !wantTunnelChange {
			return
		}
		warn := func(msg string) {
			s.appLog.Warn("update-peer", req.Description, "tunnel IP не возвращён после "+after+": "+msg)
		}
		if s.commands == nil || s.commands.Wireguard == nil {
			warn("ndms commands not wired")
			return
		}
		// Любая операция allow-ips (и снятие тоже) на отсутствующем ключе NDMS
		// СОЗДАЁТ пира (стенд 5.02.A.11, 28.09): пира, удалённого посреди
		// правки, откат адреса воскресил бы. Поэтому наличие — до первого поста.
		if present, err := s.peerPresent(rbCtx, iface, pubkey); err != nil {
			warn("наличие пира не прочитано: " + err.Error())
			return
		} else if !present {
			s.appLog.Info("update-peer", req.Description, "пир удалён, откат адреса не нужен")
			return
		}
		if err := s.commands.Wireguard.RemovePeerAllowIP(rbCtx, iface, pubkey, newIPStr, "255.255.255.255"); err != nil {
			warn(err.Error())
			return
		}
		if oldIPStr == "" {
			return
		}
		if err := s.commands.Wireguard.AddPeerAllowIP(rbCtx, iface, pubkey, oldIPStr, "255.255.255.255"); err != nil {
			warn(err.Error())
		}
	}

	if wantTunnelChange {
		oldIP, _, _ := net.ParseCIDR(peer.TunnelIP)
		newIP, _, err := net.ParseCIDR(req.TunnelIP)
		if err != nil {
			return fmt.Errorf("invalid tunnel IP: %w", err)
		}
		if oldIP != nil {
			oldIPStr = oldIP.String()
		}
		newIPStr = newIP.String()
		// Пир в записи ещё не значит пир на роутере (удалён в веб-морде):
		// allow-ips на отсутствующий ключ создали бы призрака. Проверка — под
		// блокировкой, которую берёт и удаление.
		if present, err := s.peerPresent(ctx, iface, pubkey); err != nil {
			return fmt.Errorf("check peer on router: %w", err)
		} else if !present {
			return fmt.Errorf("peer %s: %w", shortKey(pubkey), peersubnet.ErrPeerNotFound)
		}
		if err := s.rciUpdatePeerAllowIPs(ctx, iface, pubkey, oldIPStr, newIPStr); err != nil {
			// Старый /32 уже мог сняться до отказа добавления нового.
			rbCtx, cancel := detachedCtx(ctx)
			revertTunnelIP(rbCtx, "отказа смены адреса")
			cancel()
			return fmt.Errorf("update allow-ips: %w", err)
		}
	}

	if req.Description != peer.Description {
		if err := s.rciSetPeerComment(ctx, iface, pubkey, strings.TrimSpace(req.Description)); err != nil {
			// Пира нет на роутере — не «косметика»: запись о нём не пишется,
			// откатывать адрес не у кого.
			if errors.Is(err, peersubnet.ErrPeerNotFound) {
				return fmt.Errorf("set peer comment: %w", err)
			}
			s.log.Warn("failed to set peer comment", "error", err)
		}
	}

	// Сверка с роутером, не разница с хранилищем (F509): «было» читается с
	// роутера, и расхождение прошлых сбоев это сохранение снимает.
	undoACL := func(context.Context) {}
	if reconcile {
		if err := peersubnet.Reconcile(ctx, router, iface, pubkey, tunnelHosts, remote); err != nil {
			s.logRollback("update-peer", req.Description, err)
			rbCtx, cancel := detachedCtx(ctx)
			revertTunnelIP(rbCtx, "отказа сетей за клиентом")
			cancel()
			return fmt.Errorf("apply remote subnets: %w", err)
		}
		if undoACL, err = s.applyPeerSubnetsACL(ctx, server, aclEdit); err != nil {
			rbCtx, cancel := detachedCtx(ctx)
			if rbErr := peersubnet.Reconcile(rbCtx, router, iface, pubkey, tunnelHosts, peer.RemoteSubnets); rbErr != nil {
				s.appLog.Warn("update-peer", req.Description, "сети за клиентом не возвращены после отказа ACL: "+rbErr.Error())
			}
			revertTunnelIP(rbCtx, "отказа ACL LAN-сегментов")
			cancel()
			return fmt.Errorf("apply remote subnets: %w", err)
		}
	}

	// Persist mutations atomically.
	if err := s.settings.UpdateManagedServer(id, func(sv *storage.ManagedServer) error {
		// Re-resolve under the storage lock — the index from the pre-lock copy
		// may be stale if another goroutine added/removed peers in between.
		i := s.findPeerIndex(sv, pubkey)
		if i < 0 {
			return fmt.Errorf("peer not found: %s", pubkey)
		}
		if wantTunnelChange {
			sv.Peers[i].TunnelIP = req.TunnelIP
		}
		sv.Peers[i].Description = req.Description
		sv.Peers[i].DNS = req.DNS
		// Новый слайс, не append по месту: UpdateManagedServer клонирует только
		// Peers, внутренние слайсы элементов делятся с кэшем.
		if req.ClientAllowedIPs != nil {
			sv.Peers[i].ClientAllowedIPs = clientAllowed
		}
		if reconcile {
			sv.Peers[i].RemoteSubnets = remote
		}
		if req.Signature != nil {
			sv.Peers[i].I1 = req.Signature.I1
			sv.Peers[i].I2 = req.Signature.I2
			sv.Peers[i].I3 = req.Signature.I3
			sv.Peers[i].I4 = req.Signature.I4
			sv.Peers[i].I5 = req.Signature.I5
			// Канонический ключ, не то, что прислали: валидация выше
			// принимает " SIP " — хранить такое нельзя.
			sv.Peers[i].SignatureProfile = sigProfile
		}
		return nil
	}); err != nil {
		// Роутер ушёл вперёд записи: вернуть сети и /32 к записанному (паритет с
		// системным путём) — карточка и .conf показывают запись.
		rbCtx, cancel := detachedCtx(ctx)
		undoACL(rbCtx)
		if reconcile {
			if rbErr := peersubnet.Reconcile(rbCtx, router, iface, pubkey, tunnelHosts, peer.RemoteSubnets); rbErr != nil {
				s.appLog.Warn("update-peer", req.Description, "сети за клиентом не возвращены после отказа записи: "+rbErr.Error())
			}
		}
		revertTunnelIP(rbCtx, "отказа записи")
		cancel()
		return fmt.Errorf("save to storage: %w", err)
	}

	s.log.Info("peer updated", "interface", iface, "pubkey", shortKey(pubkey))
	s.appLog.Info("update-peer", req.Description, fmt.Sprintf("Peer %s updated", req.Description))
	return nil
}

// DeletePeer removes a peer from the managed server.
func (s *Service) DeletePeer(ctx context.Context, id, pubkey string) error {
	// I1: снятие маршрутов → пира → записи под той же блокировкой, что правка
	// сетей: иначе сверка параллельной правки легла бы на уходящего пира.
	defer s.LockPeerSubnets()()
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return fmt.Errorf("managed server not found: %s", id)
	}

	idx := s.findPeerIndex(server, pubkey)
	if idx < 0 {
		return fmt.Errorf("peer not found: %s", pubkey)
	}

	peerName := server.Peers[idx].Description
	peerNets := server.Peers[idx].RemoteSubnets
	iface := server.InterfaceName

	// Свои маршруты — до снятия пира и fail-closed (11.B/11.6): маршрут-сирота
	// без записи никто уже не снимет. Все с меткой пира, найденные на роутере,
	// а не список записи: сирота прошлого сбоя в записи не значится.
	router, err := s.peerRouter()
	if err != nil {
		return err
	}
	if err := peersubnet.RemoveRoutes(ctx, router, iface, pubkey); err != nil {
		return fmt.Errorf("remove peer routes: %w", err)
	}

	// Remove via RCI — fail-closed: a peer that stayed on the router while the
	// card says "revoked" keeps the client connected. «Уже снят» распознаётся
	// не по фразе отказа (`no input […]` — общая), а свежим чтением rc в
	// rciRemovePeer.
	if err := s.rciRemovePeer(ctx, iface, pubkey); err != nil {
		return fmt.Errorf("remove peer via RCI: %w", err)
	}

	// Remove from storage
	if err := s.settings.UpdateManagedServer(id, func(sv *storage.ManagedServer) error {
		// Re-resolve under the storage lock — the index from the pre-lock copy
		// may be stale if another goroutine added/removed peers in between.
		i := s.findPeerIndex(sv, pubkey)
		if i < 0 {
			return fmt.Errorf("peer not found: %s", pubkey)
		}
		sv.Peers = append(sv.Peers[:i], sv.Peers[i+1:]...)
		return nil
	}); err != nil {
		return fmt.Errorf("save to storage: %w", err)
	}
	// Правила ACL сетей пира — после того, как пира не стало: best-effort, на
	// отвязанном ctx, как остальная работа после коммита записи.
	rbCtx, cancel := detachedCtx(ctx)
	s.removePeerSubnetsACL(rbCtx, server, peerNets)
	cancel()

	s.log.Info("peer deleted", "interface", iface, "pubkey", shortKey(pubkey))
	s.appLog.Info("delete-peer", peerName, fmt.Sprintf("Peer %s deleted", peerName))
	return nil
}

// TogglePeer enables or disables a peer.
func (s *Service) TogglePeer(ctx context.Context, id, pubkey string, enabled bool) error {
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return fmt.Errorf("managed server not found: %s", id)
	}

	idx := s.findPeerIndex(server, pubkey)
	if idx < 0 {
		return fmt.Errorf("peer not found: %s", pubkey)
	}

	iface := server.InterfaceName

	// Под блокировкой удаления: проверка наличия пира в rciSetPeerConnect не
	// устареет до поста (connect на отсутствующий ключ NDMS создаёт пира).
	defer s.LockPeerSubnets()()
	peerName := server.Peers[idx].Description
	if err := s.rciSetPeerConnect(ctx, iface, pubkey, enabled, peerName); err != nil {
		return fmt.Errorf("toggle peer: %w", err)
	}

	if err := s.settings.UpdateManagedServer(id, func(sv *storage.ManagedServer) error {
		// Re-resolve under the storage lock — the index from the pre-lock copy
		// may be stale if another goroutine added/removed peers in between.
		i := s.findPeerIndex(sv, pubkey)
		if i < 0 {
			return fmt.Errorf("peer not found: %s", pubkey)
		}
		sv.Peers[i].Enabled = enabled
		return nil
	}); err != nil {
		return fmt.Errorf("save to storage: %w", err)
	}

	s.log.Info("peer toggled", "interface", iface, "pubkey", shortKey(pubkey), "enabled", enabled)
	state := "disabled"
	if enabled {
		state = "enabled"
	}
	s.appLog.Info("toggle-peer", peerName, fmt.Sprintf("Peer %s %s", peerName, state))
	return nil
}

// shortKey — префикс ключа для журнала. Импортированный ключ может быть
// короче 8 символов, и pubkey[:8] на нём паниковал уже после записи в NDMS.
func shortKey(k string) string {
	if len(k) > 8 {
		return k[:8] + "..."
	}
	return k
}

func (s *Service) findPeerIndex(server *storage.ManagedServer, pubkey string) int {
	for i, p := range server.Peers {
		if p.PublicKey == pubkey {
			return i
		}
	}
	return -1
}

func (s *Service) validateTunnelIP(server *storage.ManagedServer, tunnelIP string) error {
	ip, _, err := net.ParseCIDR(tunnelIP)
	if err != nil {
		return fmt.Errorf("invalid tunnel IP (must be CIDR, e.g. 10.0.0.2/32): %w", err)
	}

	// Check it's in the server's subnet
	serverIP := net.ParseIP(server.Address)
	serverMask := net.IPMask(net.ParseIP(server.Mask).To4())
	if serverIP == nil || serverMask == nil {
		return nil // Skip subnet check if server address is unparseable
	}
	serverNet := &net.IPNet{IP: serverIP.Mask(serverMask), Mask: serverMask}

	return validatePeerTunnelIP(serverNet, serverIP, ip)
}
