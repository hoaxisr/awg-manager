package managed

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
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
	rules, err := segmentRules(srcs, server.LANSegments, bridges)
	if errors.Is(err, ErrUnknownLANSegment) {
		// Правка пира упёрлась в сегмент сервера — чинится не здесь.
		return nil, fmt.Errorf("%w; пересохраните LAN-сегменты сервера", err)
	}
	return rules, err
}

// peerACLEdit — правка AWGM_<iface> под смену сетей пира: точечная (add,
// remove) или, когда списка на роутере нет либо он не привязан, полная
// пересборка (rebuild) по сетям всех пиров после правки (newNets); oldNets —
// сети всех пиров до неё, для отката.
type peerACLEdit struct {
	add, remove      []permitRule
	rebuild          bool
	newNets, oldNets []string
}

// planPeerSubnetsACL решает, как править список, без мутаций на роутере — до
// первого RCI, чтобы неизвестный сегмент и нечитаемый running-config отказали
// чисто. newNets — сети всех пиров сервера после правки.
//
// Точечная правка годится только для живого привязанного списка: снятие
// правила из несуществующего отказывает `argument parse error` (стенд 12.09),
// а permit молча создал бы НЕпривязанный список с одними правилами пира и без
// auto-delete. Тогда — полная пересборка applyLANSegmentsRaw.
func (s *Service) planPeerSubnetsACL(ctx context.Context, server *storage.ManagedServer, add, remove, newNets []string) (peerACLEdit, error) {
	addRules, err := s.peerSubnetACLRules(ctx, server, add)
	if err != nil {
		return peerACLEdit{}, err
	}
	removeRules, err := s.peerSubnetACLRules(ctx, server, remove)
	if err != nil {
		return peerACLEdit{}, err
	}
	if len(addRules)+len(removeRules) == 0 {
		return peerACLEdit{}, nil
	}
	exists, bound, err := s.lanACLState(ctx, server.InterfaceName)
	if err != nil {
		return peerACLEdit{}, err
	}
	if !exists || !bound {
		return peerACLEdit{rebuild: true, newNets: newNets, oldNets: serverPeerSubnets(server.Peers)}, nil
	}
	return peerACLEdit{add: addRules, remove: removeRules}, nil
}

// lanACLState — есть ли список AWGM_<iface> в running-config и привязан ли
// он к интерфейсу. Чтение свежее (Fetch, мимо кэша): список мог снять кто
// угодно мимо нас, а хук ndm на такую правку к нам не приходит.
func (s *Service) lanACLState(ctx context.Context, iface string) (exists, bound bool, err error) {
	if s.queries == nil || s.queries.RunningConfig == nil {
		return false, false, fmt.Errorf("running-config store not wired")
	}
	lines, err := s.queries.RunningConfig.Fetch(ctx)
	if err != nil {
		return false, false, fmt.Errorf("read running-config: %w", err)
	}
	acl := "AWGM_" + iface
	return slices.Contains(lines, "access-list "+acl), slices.Contains(query.InterfaceAccessGroupsOf(lines, iface, "ip"), acl), nil
}

// applyPeerSubnetsACL применяет правку: permit добавленных сетей в каждый
// сегмент, затем снятие правил убранных (или полную пересборку, см.
// planPeerSubnetsACL). Точечно, а не пересборкой всегда: unbind→bind
// переставил бы наш список за чужой permit-all (порядок джампов = порядок
// привязки) и на миг снял бы доступ.
//
// Отказ точечной правки — сделанное этим вызовом откатывается (на отвязанном
// ctx), ошибка наверх; отказ пересборки — ошибка наверх (списка до неё не
// было или он не работал). Успех — undo для отката при отказе следующего
// шага. Вызывающий держит LockPeerSubnets.
//
// conf — интерфейс сервера, подтверждённый вызывающим (нужен пересборке).
func (s *Service) applyPeerSubnetsACL(ctx context.Context, conf query.Confirmed, server *storage.ManagedServer, e peerACLEdit) (undo func(context.Context), err error) {
	iface := server.InterfaceName
	if e.rebuild {
		if err := s.applyLANSegmentsRaw(ctx, conf, server.Address, server.Mask, server.LANSegments, e.newNets); err != nil {
			return nil, fmt.Errorf("LAN ACL rebuild: %w", err)
		}
		return func(ctx context.Context) {
			if err := s.applyLANSegmentsRaw(ctx, conf, server.Address, server.Mask, server.LANSegments, e.oldNets); err != nil {
				s.appLog.Warn("lan-acl", iface, "список не пересобран при откате: "+err.Error())
			}
		}, nil
	}
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

// peerNetsWith — сети всех пиров, где у пира pubkey список заменён на nets.
func peerNetsWith(peers []storage.ManagedPeer, pubkey string, nets []string) []string {
	var out []string
	for _, p := range peers {
		if p.PublicKey == pubkey {
			out = append(out, nets...)
		} else {
			out = append(out, p.RemoteSubnets...)
		}
	}
	return out
}
