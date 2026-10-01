package query

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
)

// snapshotDetail разбирает в dst запись name из снимка полного списка (не
// старше SnapshotRecent) — те же поля, что у `show interface <name>`, без
// чтения по имени (F546). false — записи в снимке нет; dst не тронут.
func (s *WGServerStore) snapshotDetail(ctx context.Context, name string, dst any) (bool, error) {
	snap, err := s.interfaces.Snapshot(ctx, SnapshotRecent)
	if err != nil {
		return false, err
	}
	raw, ok := snap.Raw(name)
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, dst)
}

const (
	// wgServerListTTL — safety-net TTL for the full list; hooks invalidate
	// proactively so this rarely matters. Reduced from 30 min to 5 min so
	// the safety-net never serves badly stale data.
	wgServerListTTL = 5 * time.Minute
	// wgServerItemTTL — per-name runtime snapshot TTL. Tight to keep the
	// live UI fresh; mutations explicitly Invalidate(id) so this bound
	// only matters for background traffic / handshake delta detection.
	wgServerItemTTL = 30 * time.Second
	// noHandshakeMarker: RCI sentinel for "no handshake ever".
	noHandshakeMarker = int64(math.MaxInt32) // 2147483647

)

// --- wire types (private) ----------------------------------------------------

// rciInterfaceInfo mirrors the subset of /show/interface/<name> fields we need.
type rciInterfaceInfo struct {
	State         string `json:"state"`
	Link          string `json:"link"`
	Connected     string `json:"connected"`
	InterfaceName string `json:"interface-name"`
	Type          string `json:"type"`
	Description   string `json:"description"`
	Address       string `json:"address"`
	Mask          string `json:"mask"`
}

// rciWireguardDetail is the runtime shape of /show/interface/<name> for a
// WireGuard interface, adding the nested "wireguard" object.
type rciWireguardDetail struct {
	rciInterfaceInfo
	MTU       int   `json:"mtu"`
	Uptime    int64 `json:"uptime"`
	Wireguard *struct {
		PublicKey  string             `json:"public-key"`
		ListenPort int                `json:"listen-port"`
		Peer       []rciWireguardPeer `json:"peer"`
	} `json:"wireguard"`
}

type rciWireguardPeer struct {
	PublicKey             string `json:"public-key"`
	Description           string `json:"description"`
	Comment               string `json:"comment"`
	RemoteEndpointAddress string `json:"remote-endpoint-address"`
	RemotePort            int    `json:"remote-port"`
	Via                   string `json:"via"`
	RxBytes               int64  `json:"rxbytes"`
	TxBytes               int64  `json:"txbytes"`
	LastHandshake         int64  `json:"last-handshake"`
	Online                bool   `json:"online"`
	Enabled               bool   `json:"enabled"`
}

// rciRCInterface — конфигурация интерфейса: запись дерева /show/rc/interface/.
type rciRCInterface struct {
	Description string `json:"description"`
	IP          *struct {
		Address *struct {
			Address string `json:"address"`
			Mask    string `json:"mask"`
		} `json:"address"`
		MTU string `json:"mtu"`
	} `json:"ip"`
	Wireguard *struct {
		ListenPort *struct {
			Port int `json:"port"`
		} `json:"listen-port"`
		Peer []rciRCPeer `json:"peer"`
	} `json:"wireguard"`
}

type rciRCPeer struct {
	Key          string `json:"key"`
	Comment      string `json:"comment"`
	PresharedKey string `json:"preshared-key"`
	AllowIPs     []struct {
		Address string `json:"address"`
		Mask    string `json:"mask"`
	} `json:"allow-ips"`
}

// --- store -------------------------------------------------------------------

// WGServerStore caches WG-server views derived from /show/interface/ and
// the /show/rc/interface/ tree. Invalidation comes from NDMS hooks and
// command-after-write callers.
type WGServerStore struct {
	*cache.ListStore[[]ndms.WireguardServer]

	getter     Getter
	log        Logger
	interfaces *InterfaceStore // снимок полного списка + ResolveSystemName

	// per-name server snapshot (runtime only).
	items *cache.KeyedStore[string, *ndms.WireguardServer]
	// Дерево rc всех интерфейсов (конфигурация: пиры, allow-ips, ASC).
	rcTree *rcInterfaceStore
	// Список СИСТЕМНЫХ (не наших) WG-туннелей. Кэш тут не украшение:
	// поллер метрик спрашивает состав на КАЖДОМ тике (F364), а выборка —
	// `/show/interface/` целиком (см. wireguardInterfaces).
	sysList *cache.ListStore[[]ndms.SystemWireguardTunnel]
}

// NewWGServerStore constructs the store with production TTLs. Takes
// InterfaceStore so kernel-name resolution shares a single memo across
// the query layer.
func NewWGServerStore(g Getter, log Logger, ifaces *InterfaceStore) *WGServerStore {
	return NewWGServerStoreWithTTL(g, log, ifaces, wgServerListTTL, wgServerItemTTL, rcInterfaceTTL)
}

// NewWGServerStoreWithTTL is the test-friendly constructor. ifaces обязателен:
// runtime серверов — из его снимка списка (F546). rcTTL — возраст дерева rc.
func NewWGServerStoreWithTTL(g Getter, log Logger, ifaces *InterfaceStore, listTTL, itemTTL, rcTTL time.Duration) *WGServerStore {
	if ifaces == nil {
		panic("query.NewWGServerStore: ifaces обязателен — runtime читается из снимка InterfaceStore (F546)")
	}
	if log == nil {
		log = NopLogger()
	}
	s := &WGServerStore{
		getter:     g,
		log:        log,
		interfaces: ifaces,
	}
	s.items = cache.NewKeyedStore(itemTTL, log, "wg server", s.fetchItem)
	s.rcTree = newRCInterfaceStore(g, log, rcTTL)
	s.ListStore = cache.NewListStore(listTTL, log, "wg server list", s.fetchAll)
	s.sysList = cache.NewListStore(listTTL, log, "system wg list", s.fetchSystemTunnels)
	return s
}

// Get returns a single WG server's runtime snapshot.
func (s *WGServerStore) Get(ctx context.Context, name string) (*ndms.WireguardServer, error) {
	return s.items.Get(ctx, name)
}

// GetConfig returns the merged (runtime + RC) WG server config.
func (s *WGServerStore) GetConfig(ctx context.Context, name string) (*ndms.WireguardServerConfig, error) {
	return s.fetchConfig(ctx, name)
}

// PeersRCFresh — пиры сервера name из дерева rc, прочитанного сейчас, мимо
// кэша и без stale-on-error: для проверки пересечения сетей перед
// записью (#713). List на сбое обогащения тоже ошибка (F510), но отдаёт
// прежний список из кэша (stale-on-error) — для проверки пересечений мало.
func (s *WGServerStore) PeersRCFresh(ctx context.Context, name string) ([]ndms.WireguardServerPeerConfig, error) {
	// Свежее чтение — и присутствие проверяется свежим списком, не кэшем:
	// сервер, появившийся без хука, обязан попасть в проверку пересечений.
	// Confirm читает только список — E не пишет (F546).
	c, _, ok, err := s.interfaces.Confirm(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("get wireguard server config %s: %w", name, err)
	}
	if !ok {
		return nil, fmt.Errorf("get wireguard server config %s: interface %s: нет в NDMS: %w", name, name, ErrGone)
	}
	return s.PeersRC(ctx, c)
}

// PeersRC — PeersRCFresh по уже подтверждённому интерфейсу: список не
// перечитывается (одно чтение списка на всю сверку вызывающего), дерево rc —
// свежее. Интерфейса нет в дереве — ErrGone.
func (s *WGServerStore) PeersRC(ctx context.Context, c Confirmed) ([]ndms.WireguardServerPeerConfig, error) {
	out, err := s.PeersRCEach(ctx, map[string]Confirmed{c.name: c})
	if err != nil {
		return nil, err
	}
	return out[c.name], nil
}

// PeersRCEach — пиры нескольких подтверждённых серверов по ОДНОМУ свежему
// дереву rc (проверка занятости сетей, миграция). Любого нет в дереве —
// ErrGone: снят между подтверждением и чтением, решение перечитывается.
func (s *WGServerStore) PeersRCEach(ctx context.Context, cs map[string]Confirmed) (map[string][]ndms.WireguardServerPeerConfig, error) {
	if len(cs) == 0 {
		return map[string][]ndms.WireguardServerPeerConfig{}, nil
	}
	tree, err := s.rcTree.Fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("get wireguard server config: %w", err)
	}
	out := make(map[string][]ndms.WireguardServerPeerConfig, len(cs))
	for name := range cs {
		raw, ok := tree[name]
		if !ok {
			return nil, fmt.Errorf("get wireguard server config %s: нет в NDMS: %w", name, ErrGone)
		}
		var rc rciRCInterface
		if err := json.Unmarshal(raw, &rc); err != nil {
			return nil, fmt.Errorf("get wireguard server config %s: %w", name, err)
		}
		out[name] = rciRCToServerConfig(rc, "").Peers
	}
	return out, nil
}

// FindFreeIndex returns the next free WireguardN slot in [0,99]
// (InterfaceStore.FreeIndex: свой свежий список, F574).
// Scan from 0: on a fresh device the first server must be Wireguard0 (#308).
func (s *WGServerStore) FindFreeIndex(ctx context.Context) (int, error) {
	idx, ok, err := s.interfaces.FreeIndex(ctx, "Wireguard", 100, nil)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("no free Wireguard index found")
	}
	return idx, nil
}

// GetASCParams returns the AWG obfuscation params for name. If extended is
// true, fields are encoded as the 16-field ASCParamsExtended shape, else as
// the 9-field ASCParams shape. The caller is responsible for the firmware
// gate (e.g. osdetect.AtLeast(5, 1)).
func (s *WGServerStore) GetASCParams(ctx context.Context, name string, extended bool) (json.RawMessage, error) {
	return s.fetchASC(ctx, name, extended)
}

// ListSystemTunnels returns all system WG tunnels (excluding the built-in VPN
// server). Читает из кэша: состав меняется по хукам NDMS, и они его сбрасывают
// (InvalidateAll в диспетчере на ifcreated/ifdestroyed/iflayerchanged), а TTL —
// та же подстраховка, что у списка серверов рядом.
func (s *WGServerStore) ListSystemTunnels(ctx context.Context) ([]ndms.SystemWireguardTunnel, error) {
	return s.sysList.List(ctx)
}

// ListSystemTunnelsFresh — список, прочитанный с роутера сейчас, для показа.
// Кэш выше годится только тем, кому нужен СОСТАВ (поллер метрик, мониторинг):
// rx/tx, рукопожатие и uptime в нём стоят до 5 минут (F467, #950). Выборка
// заодно освежает кэш для них, а при сбое RCI отдаёт прежний список.
func (s *WGServerStore) ListSystemTunnelsFresh(ctx context.Context) ([]ndms.SystemWireguardTunnel, error) {
	return s.sysList.Refresh(ctx)
}

func (s *WGServerStore) fetchSystemTunnels(ctx context.Context) ([]ndms.SystemWireguardTunnel, error) {
	raw, err := s.wireguardInterfaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("list system wireguard: %w", err)
	}
	var tunnels []ndms.SystemWireguardTunnel
	for id, data := range raw {
		var typeCheck struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		}
		if err := json.Unmarshal(data, &typeCheck); err != nil {
			continue
		}
		if !strings.EqualFold(typeCheck.Type, "Wireguard") {
			continue
		}
		if typeCheck.Description == ndms.BuiltInVPNServerDescription {
			continue
		}
		var detail rciWireguardDetail
		if err := json.Unmarshal(data, &detail); err != nil {
			continue
		}
		if detail.ID() == "" {
			detail.InterfaceName = id
		}
		t := rciToSystemTunnel(detail)
		t.ID = id
		t.InterfaceName = s.resolveSystemName(ctx, id)
		tunnels = append(tunnels, t)
	}
	sort.Slice(tunnels, func(i, j int) bool { return tunnels[i].ID < tunnels[j].ID })
	return tunnels, nil
}

// GetSystemTunnel returns a single system-tunnel view.
func (s *WGServerStore) GetSystemTunnel(ctx context.Context, name string) (*ndms.SystemWireguardTunnel, error) {
	// Интерфейса нет в снимке списка — пустой снимок без ошибки, как прежде
	// давал конверт «unable to find». По имени NDMS не спрашивают (F546).
	var detail rciWireguardDetail
	if _, err := s.snapshotDetail(ctx, name, &detail); err != nil {
		return nil, fmt.Errorf("get system wireguard %s: %w", name, err)
	}
	t := rciToSystemTunnel(detail)
	t.ID = name
	t.InterfaceName = s.resolveSystemName(ctx, name)
	return &t, nil
}

// Invalidate drops caches for a single server name (runtime), the rc tree
// AND the aggregate list cache — otherwise GetAll would keep returning
// a stale peer list after a per-server mutation until the list TTL
// expires. Дерево rc одно на все интерфейсы — сбрасывается целиком.
func (s *WGServerStore) Invalidate(name string) {
	s.items.Invalidate(name)
	s.rcTree.InvalidateAll()
	s.ListStore.InvalidateAll()
}

// InvalidateAll drops every cached entry across all keyspaces. Kernel
// system-name memo is owned by InterfaceStore — callers that need a
// full hot-plug reset should invalidate both stores. Shadows the
// promoted ListStore.InvalidateAll so the per-name keyed caches are
// reset alongside the list cache.
func (s *WGServerStore) InvalidateAll() {
	s.InvalidateRuntime()
	s.rcTree.InvalidateAll()
}

// InvalidateRuntime — сброс всего, что зависит от состояния интерфейсов
// (список серверов, снимки по имени, состав системных туннелей), без дерева
// rc: iflayerchanged конфигурацию не меняет, а дерево стоит ~90 тиков ndm.
func (s *WGServerStore) InvalidateRuntime() {
	s.ListStore.InvalidateAll()
	s.items.InvalidateAll()
	s.sysList.InvalidateAll()
}

// --- fetchers ---------------------------------------------------------------

// wireguardInterfaces — WG-интерфейсы роутера из снимка полного списка не
// старше SnapshotRecent (общий с остальными читателями показа): записи
// списка несут те же поля, что и точечное `show interface <name>` (пиры,
// public-key, listen-port, summary.layer; ответы побайтно совпадают, стенд
// 5.02.A.11). Снимок может держать снятый без хука сервер — обогащение идёт
// по дереву rc, не по имени, так что E это не даёт.
//
// Точечных чтений по составу из InterfaceStore здесь больше нет (F546 S1):
// при внешнем сносе NDMS шлёт iflayerchanged РАНЬШЕ ifdestroyed, хук слоя
// освежает список серверов, а кэш ещё держит имя — точечное чтение доходило
// до NDMS после сноса и писало E «unable to find» (стенд, 3 из 3). Список
// дороже (стенд KN-1810: 30 интерфейсов, 34 КБ, ~15 тиков ndm), но по
// отсутствующему имени не спрашивает никогда.
//
// Ошибка чтения возвращается целиком: ListStore на ошибке отдаёт прежний
// полный список (stale-on-error) — иначе живой сервер пропадал бы из
// /servers и из опроса метрик.
func (s *WGServerStore) wireguardInterfaces(ctx context.Context) (map[string]json.RawMessage, error) {
	snap, err := s.interfaces.Snapshot(ctx, SnapshotRecent)
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}
	out := make(map[string]json.RawMessage)
	for _, rec := range snap.Records() {
		if !strings.EqualFold(rec.Type, "Wireguard") {
			continue
		}
		if data, ok := snap.Raw(rec.ID); ok {
			out[rec.ID] = data
		}
	}
	return out, nil
}

func (s *WGServerStore) fetchAll(ctx context.Context) ([]ndms.WireguardServer, error) {
	raw, err := s.wireguardInterfaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("list wireguard servers: %w", err)
	}
	var servers []ndms.WireguardServer
	for id, data := range raw {
		var typeCheck struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &typeCheck); err != nil {
			continue
		}
		if !strings.EqualFold(typeCheck.Type, "Wireguard") {
			continue
		}
		var detail rciWireguardDetail
		if err := json.Unmarshal(data, &detail); err != nil {
			continue
		}
		srv := rciToWireguardServer(detail)
		srv.ID = id
		srv.InterfaceName = s.resolveSystemName(ctx, id)
		servers = append(servers, srv)
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].ID < servers[j].ID })

	// Обогащение пиров полями rc (allow-ips, comment) — из одного дерева rc
	// на все серверы. Сбой чтения дерева — ошибка всего списка: пиры без
	// allow-ips неотличимы от «сетей нет», а неполный список лёг бы в кэш на
	// TTL. Дерево само отдаёт прежнее при сбое (stale-on-error), если было.
	if len(servers) > 0 {
		tree, err := s.rcTree.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("enrich wireguard servers: %w", err)
		}
		// Сервер из списка, которого нет в дереве из кэша (ifcreated потерян,
		// R35), — одно свежее чтение дерева на пересбор.
		for i := range servers {
			if _, ok := tree[servers[i].ID]; !ok {
				if tree, err = s.rcTree.Fetch(ctx); err != nil {
					return nil, fmt.Errorf("enrich wireguard servers: %w", err)
				}
				break
			}
		}
		for i := range servers {
			byKey, err := s.peerRCByKey(servers[i].ID, tree[servers[i].ID])
			if err != nil {
				return nil, fmt.Errorf("enrich wireguard server %s: %w", servers[i].ID, err)
			}
			for j := range servers[i].Peers {
				if rc, ok := byKey[servers[i].Peers[j].PublicKey]; ok {
					applyPeerRCFields(&servers[i].Peers[j], rc)
				}
			}
		}
	}
	return servers, nil
}

func (s *WGServerStore) fetchItem(ctx context.Context, name string) (*ndms.WireguardServer, error) {
	var detail rciWireguardDetail
	ok, err := s.snapshotDetail(ctx, name, &detail)
	if err != nil {
		return nil, fmt.Errorf("get wireguard server %s: %w", name, err)
	}
	if !ok {
		return nil, fmt.Errorf("get wireguard server %s: interface %s: нет в NDMS: %w", name, name, ErrGone)
	}
	srv := rciToWireguardServer(detail)
	srv.ID = name
	srv.InterfaceName = s.resolveSystemName(ctx, name)
	// Сбой обогащения — ошибка, как у fetchAll (F510): элемент без allow-ips
	// иначе лёг бы в кэш на TTL.
	rc, _, err := s.rcFor(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("enrich wireguard server %s: %w", name, err)
	}
	rcByKey, err := s.peerRCByKey(name, rc)
	if err != nil {
		return nil, fmt.Errorf("enrich wireguard server %s: %w", name, err)
	}
	for j := range srv.Peers {
		if rc, ok := rcByKey[srv.Peers[j].PublicKey]; ok {
			applyPeerRCFields(&srv.Peers[j], rc)
		}
	}
	return &srv, nil
}

type peerRCFields struct {
	allowedIPs []string
	comment    string
}

func applyPeerRCFields(peer *ndms.WireguardServerPeer, rc peerRCFields) {
	if len(rc.allowedIPs) > 0 {
		peer.AllowedIPs = rc.allowedIPs
	}
	if peer.Description == "" && rc.comment != "" {
		peer.Description = rc.comment
	}
}

// peerRCByKey — поля rc пиров сервера name по записи дерева raw. Записи нет
// (nil: сервер только что снят — список его тоже скоро потеряет) — пустая
// карта.
func (s *WGServerStore) peerRCByKey(name string, raw json.RawMessage) (map[string]peerRCFields, error) {
	out := make(map[string]peerRCFields)
	if raw == nil {
		return out, nil
	}
	var rc rciRCInterface
	if err := json.Unmarshal(raw, &rc); err != nil {
		return nil, err
	}
	if rc.Wireguard == nil {
		return out, nil
	}
	for _, rp := range rc.Wireguard.Peer {
		var ips []string
		for _, a := range rp.AllowIPs {
			ones := ipMaskToPrefix(a.Mask)
			if ones < 0 {
				s.log.Warnf("wg server %s peer %s has invalid allow-ips mask %q for %q", name, rp.Key, a.Mask, a.Address)
				continue
			}
			ips = append(ips, fmt.Sprintf("%s/%d", a.Address, ones))
		}
		out[rp.Key] = peerRCFields{allowedIPs: ips, comment: rp.Comment}
	}
	return out, nil
}

func (s *WGServerStore) fetchConfig(ctx context.Context, name string) (*ndms.WireguardServerConfig, error) {
	// Runtime for public key — из снимка списка; нет в нём — ErrGone.
	var detail rciWireguardDetail
	ok, err := s.snapshotDetail(ctx, name, &detail)
	if err != nil {
		return nil, fmt.Errorf("get wireguard server %s: %w", name, err)
	}
	if !ok {
		return nil, fmt.Errorf("get wireguard server %s: interface %s: нет в NDMS: %w", name, name, ErrGone)
	}
	var publicKey string
	if detail.Wireguard != nil {
		publicKey = detail.Wireguard.PublicKey
	}
	// Static config for peer details — запись дерева rc; нет и в свежем
	// дереве — пустая конфигурация.
	raw, ok, err := s.rcFor(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("get wireguard server config %s: %w", name, err)
	}
	var rc rciRCInterface
	if ok {
		if err := json.Unmarshal(raw, &rc); err != nil {
			return nil, fmt.Errorf("get wireguard server config %s: %w", name, err)
		}
	}
	cfg := rciRCToServerConfig(rc, publicKey)
	return &cfg, nil
}

func (s *WGServerStore) fetchASC(ctx context.Context, name string, extended bool) (json.RawMessage, error) {
	rc, ok, err := s.rcTree.Get(ctx, name)
	if err == nil && !ok {
		// Есть в снимке списка — дерево старше создания (R35): освежить.
		var snap *Snapshot
		if snap, err = s.interfaces.Snapshot(ctx, SnapshotRecent); err == nil {
			if _, inList := snap.Record(name); inList {
				rc, ok, err = s.rcFor(ctx, name)
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("get ASC params %s: %w", name, err)
	}
	if !ok {
		return nil, fmt.Errorf("get ASC params %s: interface %s: нет в NDMS: %w", name, name, ErrGone)
	}
	return ascParams(name, rc, extended)
}

// ASCParamsFresh — GetASCParams по дереву rc, прочитанному сейчас (Fetch: мимо
// TTL и мимо выборки в полёте, начатой до записи), — сверка после нашей записи.
// NDMS отвечает на запись, уже применив её к конфигурации, так что повторять
// чтение незачем: молча отвергнутая запись (неполный набор 3.x, 5.02.A.11)
// не появится и позже (F581).
func (s *WGServerStore) ASCParamsFresh(ctx context.Context, name string, extended bool) (json.RawMessage, error) {
	tree, err := s.rcTree.Fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("get ASC params %s: %w", name, err)
	}
	rc, ok := tree[name]
	if !ok {
		return nil, fmt.Errorf("get ASC params %s: interface %s: нет в NDMS: %w", name, name, ErrGone)
	}
	return ascParams(name, rc, extended)
}

// ascParams — параметры ASC записи дерева rc в форме GetASCParams.
func ascParams(name string, rc json.RawMessage, extended bool) (json.RawMessage, error) {
	fields, err := ascFields(rc)
	if err != nil {
		return nil, fmt.Errorf("get ASC params %s: %w", name, err)
	}
	// 5.02.A.11 отдаёт числа ("jc": 4), а не строки: разбор в map[string]string
	// падал целиком. Принимаем обе формы.
	raw := make(map[string]string, len(fields))
	for k, v := range fields {
		var str string
		if json.Unmarshal(v, &str) != nil {
			str = string(v)
		}
		raw[k] = str
	}
	if extended {
		params := struct {
			Jc   int    `json:"jc"`
			Jmin int    `json:"jmin"`
			Jmax int    `json:"jmax"`
			S1   int    `json:"s1"`
			S2   int    `json:"s2"`
			H1   string `json:"h1"`
			H2   string `json:"h2"`
			H3   string `json:"h3"`
			H4   string `json:"h4"`
			S3   int    `json:"s3"`
			S4   int    `json:"s4"`
			I1   string `json:"i1"`
			I2   string `json:"i2"`
			I3   string `json:"i3"`
			I4   string `json:"i4"`
			I5   string `json:"i5"`
		}{
			Jc: atoiSafe(raw["jc"]), Jmin: atoiSafe(raw["jmin"]), Jmax: atoiSafe(raw["jmax"]),
			S1: atoiSafe(raw["s1"]), S2: atoiSafe(raw["s2"]),
			H1: raw["h1"], H2: raw["h2"], H3: raw["h3"], H4: raw["h4"],
			S3: atoiSafe(raw["s3"]), S4: atoiSafe(raw["s4"]),
			I1: raw["i1"], I2: raw["i2"], I3: raw["i3"], I4: raw["i4"], I5: raw["i5"],
		}
		return json.Marshal(params)
	}
	params := struct {
		Jc   int    `json:"jc"`
		Jmin int    `json:"jmin"`
		Jmax int    `json:"jmax"`
		S1   int    `json:"s1"`
		S2   int    `json:"s2"`
		H1   string `json:"h1"`
		H2   string `json:"h2"`
		H3   string `json:"h3"`
		H4   string `json:"h4"`
	}{
		Jc: atoiSafe(raw["jc"]), Jmin: atoiSafe(raw["jmin"]), Jmax: atoiSafe(raw["jmax"]),
		S1: atoiSafe(raw["s1"]), S2: atoiSafe(raw["s2"]),
		H1: raw["h1"], H2: raw["h2"], H3: raw["h3"], H4: raw["h4"],
	}
	return json.Marshal(params)
}

// ASC3Fields читает с роутера (свежее дерево rc, мимо кэша) параметры ASC
// 3.x интерфейса — ключи ndms.ASC3Keys, какие есть; до 5.02.A.11 их нет вовсе.
func (s *WGServerStore) ASC3Fields(ctx context.Context, name string) (map[string]json.RawMessage, error) {
	tree, err := s.rcTree.Fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("get ASC params %s: %w", name, err)
	}
	rc, ok := tree[name]
	if !ok {
		return nil, fmt.Errorf("get ASC params %s: interface %s: нет в NDMS: %w", name, name, ErrGone)
	}
	fields, err := ascFields(rc)
	if err != nil {
		return nil, fmt.Errorf("get ASC params %s: %w", name, err)
	}
	out := make(map[string]json.RawMessage)
	for _, k := range ndms.ASC3Keys {
		if v, ok := fields[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

// rcFor — запись дерева rc для name, который есть в снимке списка. Нет в
// дереве из кэша (ifcreated потерян — дерево старше создания, R35) — ОДНО
// свежее чтение дерева; нет и в нём — false.
func (s *WGServerStore) rcFor(ctx context.Context, name string) (json.RawMessage, bool, error) {
	raw, ok, err := s.rcTree.Get(ctx, name)
	if err != nil || ok {
		return raw, ok, err
	}
	tree, err := s.rcTree.Fetch(ctx)
	if err != nil {
		return nil, false, err
	}
	raw, ok = tree[name]
	return raw, ok, nil
}

// ascFields — поля wireguard.asc записи дерева rc; поля asc нет — пустая карта.
func ascFields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var rc struct {
		Wireguard struct {
			ASC map[string]json.RawMessage `json:"asc"`
		} `json:"wireguard"`
	}
	if err := json.Unmarshal(raw, &rc); err != nil {
		return nil, err
	}
	return rc.Wireguard.ASC, nil
}

// resolveSystemName delegates to InterfaceStore so kernel-name resolution
// is memoised in one place. Preserves legacy fallback: if resolution
// fails or is empty, return the NDMS id unchanged.
func (s *WGServerStore) resolveSystemName(ctx context.Context, ndmsID string) string {
	if name := s.interfaces.ResolveSystemName(ctx, ndmsID); name != "" {
		return name
	}
	return ndmsID
}

// --- converters --------------------------------------------------------------

// ID returns the interface identifier from rciWireguardDetail. Present so that
// callers can detect empty decodes without reaching into the embedded struct.
func (d rciWireguardDetail) ID() string { return d.InterfaceName }

func peerRuntimeDescription(p rciWireguardPeer) string {
	if p.Description != "" {
		return p.Description
	}
	return p.Comment
}

func formatPeerEndpoint(p rciWireguardPeer) string {
	if p.RemoteEndpointAddress == "" && p.RemotePort == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", p.RemoteEndpointAddress, p.RemotePort)
}

// FormatHandshakeSecondsAgo converts RCI last-handshake (seconds ago) to
// RFC3339 or "". Sentinels: <= 0 or >= MaxInt32 indicate "never".
func FormatHandshakeSecondsAgo(secsAgo int64) string {
	if secsAgo <= 0 || secsAgo >= noHandshakeMarker {
		return ""
	}
	return time.Now().Add(-time.Duration(secsAgo) * time.Second).Format(time.RFC3339)
}

func rciToSystemTunnel(iface rciWireguardDetail) ndms.SystemWireguardTunnel {
	t := ndms.SystemWireguardTunnel{
		ID:          iface.InterfaceName,
		Description: iface.Description,
		Status:      iface.State,
		Connected:   iface.Connected == "yes",
		MTU:         iface.MTU,
		Address:     iface.Address,
		Mask:        iface.Mask,
		Uptime:      iface.Uptime,
	}
	if iface.Wireguard != nil && len(iface.Wireguard.Peer) > 0 {
		peer := iface.Wireguard.Peer[0]
		t.Peer = &ndms.WireguardPeerInfo{
			PublicKey:     peer.PublicKey,
			Endpoint:      formatPeerEndpoint(peer),
			Via:           peer.Via,
			RxBytes:       peer.RxBytes,
			TxBytes:       peer.TxBytes,
			LastHandshake: FormatHandshakeSecondsAgo(peer.LastHandshake),
			Online:        peer.Online,
		}
	}
	return t
}

func rciToWireguardServer(iface rciWireguardDetail) ndms.WireguardServer {
	server := ndms.WireguardServer{
		ID:          iface.InterfaceName,
		Description: iface.Description,
		Status:      iface.State,
		Connected:   iface.Connected == "yes",
		MTU:         iface.MTU,
		Address:     iface.Address,
		Mask:        iface.Mask,
	}
	if iface.Wireguard != nil {
		server.PublicKey = iface.Wireguard.PublicKey
		server.ListenPort = iface.Wireguard.ListenPort
		for _, p := range iface.Wireguard.Peer {
			server.Peers = append(server.Peers, ndms.WireguardServerPeer{
				PublicKey:     p.PublicKey,
				Description:   peerRuntimeDescription(p),
				Endpoint:      formatPeerEndpoint(p),
				RxBytes:       p.RxBytes,
				TxBytes:       p.TxBytes,
				LastHandshake: FormatHandshakeSecondsAgo(p.LastHandshake),
				Online:        p.Online,
				Enabled:       p.Enabled,
			})
		}
	}
	return server
}

// WithLivePeers накладывает на сервер из кэша списка (TTL 5 мин) живые поля
// пиров из PeerStore: rx/tx, рукопожатие, online, endpoint. Список не знает о
// трафике — его сбрасывают только хуки NDMS и мутации, и без наложения
// страница серверов показывала счётчики до 5 минут давности (F476).
// Пир, которого нет в live, остаётся как был.
func WithLivePeers(srv ndms.WireguardServer, live []ndms.Peer) ndms.WireguardServer {
	byKey := make(map[string]ndms.Peer, len(live))
	for _, p := range live {
		byKey[p.PublicKey] = p
	}
	peers := make([]ndms.WireguardServerPeer, len(srv.Peers))
	copy(peers, srv.Peers)
	for i := range peers {
		p, ok := byKey[peers[i].PublicKey]
		if !ok {
			continue
		}
		peers[i].RxBytes = p.RxBytes
		peers[i].TxBytes = p.TxBytes
		peers[i].LastHandshake = FormatHandshakeSecondsAgo(p.LastHandshakeSecondsAgo)
		peers[i].Online = p.Online
		if p.RemoteEndpointAddress != "" || p.RemotePort != 0 {
			peers[i].Endpoint = fmt.Sprintf("%s:%d", p.RemoteEndpointAddress, p.RemotePort)
		}
	}
	srv.Peers = peers
	return srv
}

func rciRCToServerConfig(rc rciRCInterface, publicKey string) ndms.WireguardServerConfig {
	cfg := ndms.WireguardServerConfig{PublicKey: publicKey}
	if rc.IP != nil {
		if rc.IP.Address != nil {
			cfg.Address = rc.IP.Address.Address
		}
		if rc.IP.MTU != "" {
			fmt.Sscanf(rc.IP.MTU, "%d", &cfg.MTU)
		}
	}
	if rc.Wireguard != nil {
		if rc.Wireguard.ListenPort != nil {
			cfg.ListenPort = rc.Wireguard.ListenPort.Port
		}
		for _, p := range rc.Wireguard.Peer {
			peer := ndms.WireguardServerPeerConfig{
				PublicKey:    p.Key,
				Description:  p.Comment,
				PresharedKey: p.PresharedKey,
			}
			for _, aip := range p.AllowIPs {
				ones := ipMaskToPrefix(aip.Mask)
				if ones < 0 {
					continue
				}
				peer.AllowedIPs = append(peer.AllowedIPs, fmt.Sprintf("%s/%d", aip.Address, ones))
				if ones == 32 && peer.Address == "" {
					peer.Address = aip.Address
				}
			}
			cfg.Peers = append(cfg.Peers, peer)
		}
	}
	return cfg
}

// ipMaskToPrefix converts an NDMS allow-ips mask field to a CIDR prefix
// length. NDMS emits two formats interchangeably:
//
//   - IPv4: dotted-quad mask (e.g. "255.255.255.0") — historical CLI form.
//   - IPv6: decimal prefix-length string (e.g. "0", "64", "128") — the
//     "::/0" default route arrives as mask="0" address="::", which the
//     previous IPv4-only parser rejected as invalid (issue #216).
//
// Returns -1 on parse failure (unknown shape / out-of-range).
func ipMaskToPrefix(mask string) int {
	mask = strings.TrimSpace(mask)
	// Decimal-only string — treat as prefix length. Covers IPv6 masks
	// and any IPv4 entries NDMS chose to encode the same way.
	if n, err := strconv.Atoi(mask); err == nil {
		if n < 0 || n > 128 {
			return -1
		}
		return n
	}
	// Dotted-quad IPv4 mask — original behaviour.
	ip := net.ParseIP(mask)
	if ip == nil {
		return -1
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return -1
	}
	ones, bits := net.IPMask(ip4).Size()
	if bits != 32 {
		return -1
	}
	return ones
}

func atoiSafe(s string) int {
	v, _ := strconv.Atoi(s)
	return v
}
