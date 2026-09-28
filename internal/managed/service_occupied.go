package managed

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/ndms"
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
// (подсети WG-серверов и LAN в том числе), allow-ips пиров всех WG-серверов
// (кроме их /32 внутри подсети своего сервера — те покрыты ею), статические
// маршруты через другие интерфейсы, RemoteSubnets пиров из хранилища, которые
// есть на роутере.
// Любой отказ чтения — отказ целиком: «занятых нет» на лежащем RCI
// пропустило бы пересечение.
//
// Сервер = системный, помеченный в панели (ServerInterfaces), встроенный
// (по описанию, как в списке серверов), или managed.
// Прочие WG-интерфейсы — клиентские туннели: их пир держит 0.0.0.0/0 и
// занял бы всё. allow-ips читаются по серверу свежо (PeersRCFresh), а не из
// WGServers.List: тот сбой чтения молча превращает в «сетей у пира нет».
func (s *Service) OccupiedSubnets(ctx context.Context, exclude PeerRef) ([]peersubnet.Occupied, error) {
	if s.queries == nil || s.queries.Interfaces == nil || s.queries.WGServers == nil || s.queries.StaticRoutes == nil {
		return nil, fmt.Errorf("ndms queries not wired")
	}
	// Записи читаются геттерами, которые отказ чтения настроек глотают
	// (GetServerInterfaces → nil): «серверов и сетей в записях нет» пропустило
	// бы пересечение. Отказ — здесь, до них (T6).
	if _, err := s.settings.Get(); err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	var out []peersubnet.Occupied
	// Свежий список, не карта событий: пропущенный хук выкинул бы
	// существующий сервер из проверки (T6).
	ifaces, err := s.queries.Interfaces.ListFresh(ctx)
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}
	exists := make(map[string]bool, len(ifaces))
	for _, iface := range ifaces {
		exists[iface.ID] = true
	}
	subnetByID := map[string]*net.IPNet{}
	for _, u := range usedSubnetsOf(ifaces, "") {
		out = append(out, peersubnet.Occupied{Net: u.cidr, Label: "интерфейс " + u.label})
		subnetByID[u.id] = u.cidr
	}
	// Тот же набор, что показывает список серверов (listServers): помеченные,
	// встроенный «Wireguard VPN Server» (обычно не помечен) и managed.
	servers := map[string]bool{}
	for _, iface := range ifaces {
		if strings.EqualFold(iface.Type, "Wireguard") && iface.Description == ndms.BuiltInVPNServerDescription {
			servers[iface.ID] = true
		}
	}
	for _, id := range s.settings.GetServerInterfaces() {
		servers[id] = true
	}
	for _, sv := range s.settings.GetManagedServers() {
		servers[sv.InterfaceName] = true
	}
	// onRouter — ключи пиров прочитанных серверов (тот же свежий снимок):
	// сети записи пира, снятого с роутера, на роутере не стоят.
	onRouter := map[string]map[string]bool{}
	for id := range servers {
		// Пометка пережила интерфейс (удалён в веб-морде): роутер ответил
		// списком без него — пиров, занимающих сети, нет. Не fail-open.
		if !exists[id] {
			continue
		}
		peers, err := s.queries.WGServers.PeersRCFresh(ctx, id)
		if err != nil {
			return nil, err
		}
		srvNet := subnetByID[id]
		onRouter[id] = make(map[string]bool, len(peers))
		for _, p := range peers {
			onRouter[id][p.PublicKey] = true
			if id == exclude.Iface && p.PublicKey == exclude.PubKey {
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
				out = append(out, peersubnet.Occupied{Net: n, Label: peerLabel(p.Description, p.PublicKey, id)})
			}
		}
	}
	// Статические маршруты через другие интерфейсы: сеть за клиентом поверх
	// такой записи молча дала бы второй маршрут на ту же сеть. Интерфейс
	// правимого пира не в счёт — чужую запись (N, I) на нём сверка и так не
	// перекрывает и своей не считает. Свои (метка awgm-peer:) учтены ниже,
	// через RemoteSubnets пиров, — второй раз не считаются. Маршрут по
	// умолчанию пересекался бы с любой сетью — пропускается.
	routes, err := s.queries.StaticRoutes.Fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("read static routes: %w", err)
	}
	for _, e := range routes {
		if e.Interface == exclude.Iface || strings.HasPrefix(e.Comment, peersubnet.RouteCommentPrefix) {
			continue
		}
		n := e.IPv4Net()
		if n == nil {
			continue
		}
		if ones, _ := n.Mask.Size(); ones == 0 {
			continue
		}
		label := "маршрут через " + e.Interface
		if e.Interface == "" {
			// Запись без интерфейса (через шлюз) — допущение, на стенде не видели.
			label = "статический маршрут через шлюз"
		}
		out = append(out, peersubnet.Occupied{Net: n, Label: label})
	}
	appendStored := func(subnets []string, label string) {
		for _, sn := range subnets {
			if _, n, err := net.ParseCIDR(sn); err == nil {
				out = append(out, peersubnet.Occupied{Net: n, Label: label})
			}
		}
	}
	// Пир записи, которого нет на роутере (снят в веб-морде), сетей не
	// занимает: его RemoteSubnets на роутере не стоят, а запись остаётся —
	// удалить её вправе только владелец. Решение — по снимку выше: интерфейса
	// нет — пиров нет; сервер прочитан и ключа в нём нет — пира нет; сервер
	// не читался (интерфейс есть, но не в наборе серверов) — считаем занятым.
	stored := func(iface, pub string) bool {
		if !exists[iface] {
			return false
		}
		keys, read := onRouter[iface]
		return !read || keys[pub]
	}
	for _, sv := range s.settings.GetManagedServers() {
		for _, p := range sv.Peers {
			if sv.InterfaceName == exclude.Iface && p.PublicKey == exclude.PubKey || !stored(sv.InterfaceName, p.PublicKey) {
				continue
			}
			appendStored(p.RemoteSubnets, peerLabel(p.Description, p.PublicKey, sv.InterfaceName))
		}
	}
	for serverID, peers := range s.settings.GetServerPeerSecrets() {
		for pub, sec := range peers {
			if serverID == exclude.Iface && pub == exclude.PubKey || !stored(serverID, pub) {
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

// LockPeerSubnets — F508: правки сетей за клиентом идут по одной на оба пути
// (системный сервер берёт её через managedSvc). Иначе два параллельных
// сохранения собирают занятые сети до записи друг друга и оба проходят с
// пересекающимися сетями. Держится на всё «занятые → валидация → сверка с
// роутером → запись»; правки без сетей её не берут. Возвращает unlock.
func (s *Service) LockPeerSubnets() (unlock func()) {
	s.peerSubnetsMu.Lock()
	return s.peerSubnetsMu.Unlock
}

// peerRouter — адаптер RCI для peersubnet.Reconcile. Ошибка вместо nil-паники, когда
// Commands не подключены (харнессы без них сети за клиентом не трогают).
func (s *Service) peerRouter() (peersubnet.Router, error) {
	if s.commands == nil || s.commands.Wireguard == nil || s.commands.Routes == nil {
		return nil, fmt.Errorf("ndms commands not wired")
	}
	return command.NewPeerRouter(s.commands), nil
}

// peerPresent — есть ли пир на интерфейсе по свежему rc. Перед allow-ips в
// откатах: на отсутствующий ключ NDMS их принимает, создавая пира.
func (s *Service) peerPresent(ctx context.Context, iface, pubkey string) (bool, error) {
	wg, err := s.peerCommands()
	if err != nil {
		return false, err
	}
	return wg.PeerPresent(ctx, iface, pubkey)
}

// logRollback — отказ отката Reconcile не должен тонуть: хранилище не тронуто, но
// роутер до следующего сохранения может быть в промежуточном состоянии.
func (s *Service) logRollback(op, subject string, err error) {
	var rb *peersubnet.RollbackError
	if errors.As(err, &rb) {
		s.appLog.Warn(op, subject, "откат сетей за клиентом не завершён: "+rb.Rollback.Error())
	}
}
