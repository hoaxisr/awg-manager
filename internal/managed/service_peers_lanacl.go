package managed

import (
	"context"
	"fmt"
	"slices"

	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// peerSubnetACLRules — правила «сеть за клиентом → сегмент» списка
// AWGM_<iface>. nil без запроса к роутеру, когда у сервера нет LAN-сегментов
// или сетей нет: такой сервер ACL не держит (#713).
func (s *Service) peerSubnetACLRules(ctx context.Context, server *storage.ManagedServer, nets []string) ([]permitRule, error) {
	if len(server.LANSegments) == 0 || len(nets) == 0 {
		return nil, nil
	}
	if s.commands == nil || s.commands.Interfaces == nil || s.queries == nil || s.queries.Interfaces == nil {
		return nil, fmt.Errorf("ndms commands not wired")
	}
	srcs, err := parseCIDRs(nets)
	if err != nil {
		return nil, err
	}
	bridges, err := s.queries.Interfaces.ListLANBridges(ctx)
	if err != nil {
		return nil, fmt.Errorf("list LAN bridges: %w", err)
	}
	return segmentRules(srcs, server.LANSegments, bridges)
}

// peerACLEdit — точечная правка AWGM_<iface> под смену сетей пира.
type peerACLEdit struct {
	add, remove []permitRule
}

// planPeerSubnetsACL резолвит правила правки без мутаций на роутере — до
// первого RCI, чтобы неизвестный сегмент отказал чисто.
func (s *Service) planPeerSubnetsACL(ctx context.Context, server *storage.ManagedServer, add, remove []string) (peerACLEdit, error) {
	addRules, err := s.peerSubnetACLRules(ctx, server, add)
	if err != nil {
		return peerACLEdit{}, err
	}
	removeRules, err := s.peerSubnetACLRules(ctx, server, remove)
	if err != nil {
		return peerACLEdit{}, err
	}
	return peerACLEdit{add: addRules, remove: removeRules}, nil
}

// applyPeerSubnetsACL применяет правку: permit добавленных сетей в каждый
// сегмент, затем снятие правил убранных.
// Не пересборка: unbind→bind переставил бы наш список за чужой permit-all
// (порядок джампов = порядок привязки) и на миг снял бы доступ.
//
// Отказ — сделанное этим вызовом откатывается (на отвязанном ctx), ошибка
// наверх. Успех — undo для отката при отказе следующего шага: снять
// добавленное, вернуть снятое. Вызывающий держит LockPeerSubnets.
func (s *Service) applyPeerSubnetsACL(ctx context.Context, iface string, e peerACLEdit) (undo func(context.Context), err error) {
	if len(e.add)+len(e.remove) == 0 {
		return func(context.Context) {}, nil
	}
	acl := "AWGM_" + iface
	cmd := s.commands.Interfaces
	var added, removed []permitRule
	undo = func(ctx context.Context) {
		for _, r := range added {
			if err := cmd.ACLRemovePermitIP(ctx, acl, r.srcSub, r.srcMask, r.dstSub, r.dstMask); err != nil {
				s.appLog.Warn("lan-acl", iface, fmt.Sprintf("правило %s/%s → %s не снято при откате: %v", r.srcSub, r.srcMask, r.seg, err))
			}
		}
		for _, r := range removed {
			if err := cmd.ACLPermitIP(ctx, acl, r.srcSub, r.srcMask, r.dstSub, r.dstMask); err != nil && !command.IsACLDuplicate(err) {
				s.appLog.Warn("lan-acl", iface, fmt.Sprintf("правило %s/%s → %s не возвращено при откате: %v", r.srcSub, r.srcMask, r.seg, err))
			}
		}
	}
	fail := func(r permitRule, err error) (func(context.Context), error) {
		rbCtx, cancel := detachedCtx(ctx)
		undo(rbCtx)
		cancel()
		return nil, fmt.Errorf("LAN ACL %s/%s → %s: %w", r.srcSub, r.srcMask, r.seg, err)
	}
	for _, r := range e.add {
		err := cmd.ACLPermitIP(ctx, acl, r.srcSub, r.srcMask, r.dstSub, r.dstMask)
		if command.IsACLDuplicate(err) {
			continue // стояло до нас — откатом не снимать
		}
		if err != nil {
			return fail(r, err)
		}
		added = append(added, r)
	}
	for _, r := range e.remove {
		if err := cmd.ACLRemovePermitIP(ctx, acl, r.srcSub, r.srcMask, r.dstSub, r.dstMask); err != nil {
			return fail(r, err)
		}
		removed = append(removed, r)
	}
	return undo, nil
}

// removePeerSubnetsACL снимает правила сетей удалённого пира — best-effort:
// правило без маршрута и allow-ips безвредно (пропускает источник, которого
// за туннелем больше нет), поэтому отказ только в журнал.
func (s *Service) removePeerSubnetsACL(ctx context.Context, server *storage.ManagedServer, nets []string) {
	rules, err := s.peerSubnetACLRules(ctx, server, nets)
	if err != nil {
		s.appLog.Warn("lan-acl", server.InterfaceName, "правила сетей удалённого пира не сняты: "+err.Error())
		return
	}
	acl := "AWGM_" + server.InterfaceName
	for _, r := range rules {
		if err := s.commands.Interfaces.ACLRemovePermitIP(ctx, acl, r.srcSub, r.srcMask, r.dstSub, r.dstMask); err != nil {
			s.appLog.Warn("lan-acl", server.InterfaceName, fmt.Sprintf("правило %s/%s → %s не снято: %v", r.srcSub, r.srcMask, r.seg, err))
		}
	}
}

// subnetDiff — сети из a, которых нет в b.
func subnetDiff(a, b []string) []string {
	var out []string
	for _, n := range a {
		if !slices.Contains(b, n) {
			out = append(out, n)
		}
	}
	return out
}
