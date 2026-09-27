package managed

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/peersubnet"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/netif"
)

// PeerRef — правимый пир: его собственные сети (allow-ips на роутере и
// RemoteSubnets в хранилище) занятыми не считаются. Пустой — никого не исключать.
type PeerRef struct{ Iface, PubKey string }

func peerName(desc, pubkey string) string {
	if desc != "" {
		return desc
	}
	return shortKey(pubkey)
}

func peerLabel(desc, pubkey, iface string) string {
	return fmt.Sprintf("пир «%s» сервера %s", peerName(desc, pubkey), iface)
}

// OccupiedSubnets — снимок занятых IPv4-сетей для peersubnet.ValidateRemoteSubnets
// (спека 4.2 шаг 1, решение 11.5): сети всех интерфейсов роутера с адресом
// (подсети WG-серверов и LAN в том числе), allow-ips пиров ВСЕХ WG-серверов
// роутера (кроме их /32 внутри подсети своего сервера — те покрыты ею),
// RemoteSubnets всех пиров из хранилища. Любой отказ чтения — отказ целиком:
// «занятых нет» на лежащем RCI пропустило бы пересечение.
//
// allow-ips читаются по серверу свежо (PeersRCFresh), а не из WGServers.List:
// тот сбой чтения allow-ips молча превращает в «сетей у пира нет».
func (s *Service) OccupiedSubnets(ctx context.Context, exclude PeerRef) ([]peersubnet.Occupied, error) {
	if s.queries == nil || s.queries.Interfaces == nil || s.queries.WGServers == nil {
		return nil, fmt.Errorf("ndms queries not wired")
	}
	var out []peersubnet.Occupied
	used, err := s.listUsedSubnets(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list interface subnets: %w", err)
	}
	for _, u := range used {
		out = append(out, peersubnet.Occupied{Net: u.cidr, Label: "интерфейс " + u.label})
	}
	ifaces, err := s.queries.Interfaces.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}
	for _, iface := range ifaces {
		if !strings.EqualFold(iface.Type, "Wireguard") {
			continue
		}
		peers, err := s.queries.WGServers.PeersRCFresh(ctx, iface.ID)
		if err != nil {
			return nil, err
		}
		srvNet, _ := parseManagedSubnet(iface.Address, iface.Mask)
		for _, p := range peers {
			if iface.ID == exclude.Iface && p.PublicKey == exclude.PubKey {
				continue
			}
			for _, a := range p.AllowedIPs {
				_, n, err := net.ParseCIDR(a)
				if err != nil || n.IP.To4() == nil {
					continue
				}
				if ones, bits := n.Mask.Size(); ones == bits && srvNet != nil && srvNet.Contains(n.IP) {
					continue
				}
				out = append(out, peersubnet.Occupied{Net: n, Label: peerLabel(p.Description, p.PublicKey, iface.ID)})
			}
		}
	}
	appendStored := func(subnets []string, label string) {
		for _, sn := range subnets {
			if _, n, err := net.ParseCIDR(sn); err == nil {
				out = append(out, peersubnet.Occupied{Net: n, Label: label})
			}
		}
	}
	for _, sv := range s.settings.GetManagedServers() {
		for _, p := range sv.Peers {
			if sv.InterfaceName == exclude.Iface && p.PublicKey == exclude.PubKey {
				continue
			}
			appendStored(p.RemoteSubnets, peerLabel(p.Description, p.PublicKey, sv.InterfaceName))
		}
	}
	for serverID, peers := range s.settings.GetServerPeerSecrets() {
		for pub, sec := range peers {
			if serverID == exclude.Iface && pub == exclude.PubKey {
				continue
			}
			appendStored(sec.RemoteSubnets, peerLabel(sec.Description, pub, serverID))
		}
	}
	// Источники — карты: без сортировки текст ошибки о пересечении называл бы
	// то одну, то другую занятую сеть.
	sort.SliceStable(out, func(i, j int) bool {
		if c := bytes.Compare(out[i].Net.IP.To4(), out[j].Net.IP.To4()); c != 0 {
			return c < 0
		}
		return out[i].Label < out[j].Label
	})
	return out, nil
}

// PresetsFor — пресеты AllowedIPs клиента. LAN-сети: бриджи из segments,
// пустой segments — все (11.B/11.4); порядок — по адресу, чтобы строка не
// зависела от обхода карты в InterfaceStore. dns — список IP через запятую,
// уже с подставленным умолчанием вызывающего.
func (s *Service) PresetsFor(ctx context.Context, serverNet *net.IPNet, segments []string, dns string) (peersubnet.Presets, error) {
	if s.queries == nil || s.queries.Interfaces == nil {
		return peersubnet.Presets{}, fmt.Errorf("interface store not wired")
	}
	bridges, err := s.queries.Interfaces.ListLANBridges(ctx)
	if err != nil {
		return peersubnet.Presets{}, fmt.Errorf("list LAN bridges: %w", err)
	}
	want := map[string]bool{}
	for _, seg := range segments {
		want[seg] = true
	}
	var lan []*net.IPNet
	for _, b := range bridges {
		if len(want) > 0 && !want[b.Name] {
			continue
		}
		if n, err := parseManagedSubnet(b.Address, b.Mask); err == nil {
			lan = append(lan, n)
		}
	}
	sort.Slice(lan, func(i, j int) bool { return bytes.Compare(lan[i].IP, lan[j].IP) < 0 })
	var ips []net.IP
	for _, part := range strings.Split(dns, ",") {
		if ip := net.ParseIP(strings.TrimSpace(part)); ip != nil {
			ips = append(ips, ip)
		}
	}
	return peersubnet.BuildPresets(serverNet, lan, ips), nil
}

// PeerPresets — пресеты для пира managed-сервера id. dns — как введён в форме
// (?dns=); пусто → server.DNS → LAN-адрес роутера — цепочка GenerateConf.
func (s *Service) PeerPresets(ctx context.Context, id, dns string) (peersubnet.Presets, error) {
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return peersubnet.Presets{}, fmt.Errorf("managed server not found: %s", id)
	}
	dns, err := ValidatePeerDNS(dns)
	if err != nil {
		return peersubnet.Presets{}, err
	}
	if dns == "" {
		dns = server.DNS
	}
	if dns == "" {
		dns = netif.RouterLANIP(storage.DefaultInterface)
	}
	serverNet, err := parseManagedSubnet(server.Address, server.Mask)
	if err != nil {
		return peersubnet.Presets{}, fmt.Errorf("server subnet: %w", err)
	}
	return s.PresetsFor(ctx, serverNet, server.LANSegments, dns)
}

// peerRouter — адаптер RCI для peersubnet.Apply. Ошибка вместо nil-паники, когда
// Commands не подключены (харнессы без них сети за клиентом не трогают).
func (s *Service) peerRouter() (peersubnet.Router, error) {
	if s.commands == nil || s.commands.Wireguard == nil || s.commands.Routes == nil {
		return nil, fmt.Errorf("ndms commands not wired")
	}
	return command.NewPeerRouter(s.commands), nil
}

// logRollback — отказ отката Apply не должен тонуть: хранилище не тронуто, но
// роутер до следующего сохранения может быть в промежуточном состоянии.
func (s *Service) logRollback(op, subject string, err error) {
	var rb *peersubnet.RollbackError
	if errors.As(err, &rb) {
		s.appLog.Warn(op, subject, "откат сетей за клиентом не завершён: "+rb.Rollback.Error())
	}
}
