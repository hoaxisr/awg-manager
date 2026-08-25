package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/childproc"
	"github.com/hoaxisr/awg-manager/internal/clientroute"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/listenfirewall"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/exitreg"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/linkres"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/netres"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/wdttserver"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/iptables"
)

// Прод-адаптеры связей инстанса с остальной системой (linkres/netres) и
// одноразовые шаги после посева: добивание процессов старого поколения и
// уборка правил и интерфейсов, оставшихся от старого движка.

// ------------------------------------------------------------- linkres

// proxyEndpointSync — linkres.EndpointSync поверх экспорта api. Публикацию
// списка туннелей делает сам адаптер (хелперу передан th=nil): пути прокси-
// рантайма живут вне HTTP-хендлеров, а фронт обязан узнать о смене endpoint'а.
type proxyEndpointSync struct {
	store *storage.AWGTunnelStore
	svc   api.TunnelService
	field api.LinkedField
	pub   proxyrt.Publisher
}

func newProxyEndpointSync(store *storage.AWGTunnelStore, svc api.TunnelService,
	field api.LinkedField, pub proxyrt.Publisher) proxyEndpointSync {
	return proxyEndpointSync{store: store, svc: svc, field: field, pub: pub}
}

// invalidateTunnels — фронт обязан узнать об изменении связанных туннелей:
// пути прокси-рантайма живут вне HTTP-хендлеров, где публикацию делал бы
// TunnelsHandler.
func (s proxyEndpointSync) invalidateTunnels(changed int, reason string) {
	if changed == 0 || s.pub == nil {
		return
	}
	for _, res := range []string{api.ResourceTunnels, api.ResourceRoutingTunnels} {
		s.pub.Publish("resource:invalidated", events.ResourceInvalidatedEvent{
			Resource: res, Reason: reason,
		})
	}
}

func (s proxyEndpointSync) List(ctx context.Context, clientID string) ([]linkres.LinkedTunnel, error) {
	items, err := api.ListLinkedProxyTunnels(ctx, s.store, s.svc, s.field, clientID)
	if err != nil {
		return nil, err
	}
	out := make([]linkres.LinkedTunnel, 0, len(items))
	for _, it := range items {
		out = append(out, linkres.LinkedTunnel{
			ID: it.ID, Endpoint: it.Endpoint,
			Running: it.Running, Lifecycle: it.Lifecycle,
		})
	}
	return out, nil
}

func (s proxyEndpointSync) Sync(ctx context.Context, clientID, listen string) (int, error) {
	updated, failed := api.SyncLinkedProxyEndpoints(ctx, s.store, s.svc, s.field, clientID, listen)
	s.invalidateTunnels(len(updated), "proxy-linked-endpoint")
	if len(failed) > 0 {
		// Молчаливое отбрасывание failed — вечный невидимый дрейф: endpoint
		// остаётся на старом порту, а ресурс рапортует успех.
		return len(updated), fmt.Errorf("endpoint не обновлён у: %s", strings.Join(failed, ", "))
	}
	return len(updated), nil
}

func (s proxyEndpointSync) SetState(ctx context.Context, clientID string, up bool) (int, error) {
	changed, failed := api.SetLinkedProxyTunnelsState(ctx, s.store, s.svc, s.field, clientID, up)
	s.invalidateTunnels(len(changed), "proxy-linked-tunnel-state")
	if len(failed) > 0 {
		// Тот же довод: проглоченный отказ оставляет туннель не в том
		// состоянии, а ресурс считает себя сошедшимся.
		verb := "опущены"
		if up {
			verb = "подняты"
		}
		return len(changed), fmt.Errorf("связанные туннели не %s: %s", verb, strings.Join(failed, ", "))
	}
	return len(changed), nil
}

var _ linkres.EndpointSync = proxyEndpointSync{}

// recordLister — срез instancestore для занятости портов.
type recordLister interface {
	Load() (instancestore.State, error)
}

// awgLister — срез стора туннелей.
type awgLister interface {
	List() ([]storage.AWGTunnel, error)
}

// proxyOccupancy — linkres.Occupancy. Исключается СВОЁ: и собственная запись
// (по Key()), и связанные туннели самого инстанса. Без второго исключения
// клиент со связанным туннелем (штатный итог импорта ссылки) считал бы свой
// же порт занятым — ресурс listen_port не перевыделяет, и весь инстанс уходил
// бы в Blocked после апгрейда. Паритет OccupiedLocalListenPorts старого мира
// (proxylisten/checker.go:43-47).
type proxyOccupancy struct {
	records  recordLister
	tunnels  awgLister
	selfKind instancestore.Kind
	selfID   string
}

func newProxyOccupancy(records recordLister, tunnels awgLister,
	selfKind instancestore.Kind, selfID string) proxyOccupancy {
	return proxyOccupancy{records: records, tunnels: tunnels, selfKind: selfKind, selfID: selfID}
}

func (o proxyOccupancy) OccupiedLocalListenPorts(context.Context) (map[int]bool, error) {
	used := map[int]bool{}
	if o.records != nil {
		st, err := o.records.Load()
		if err != nil {
			return nil, err
		}
		self := instancestore.Record{Kind: o.selfKind, ID: o.selfID}.Key()
		for _, rec := range st.Records {
			if rec.Key() == self {
				continue
			}
			for _, port := range recordPorts(rec) {
				used[port] = true
			}
		}
	}
	if o.tunnels != nil {
		all, err := o.tunnels.List()
		if err != nil {
			return nil, err
		}
		for _, tun := range all {
			if o.linkedToSelf(tun) {
				continue
			}
			if port, ok := localhostPort(tun.Peer.Endpoint); ok {
				used[port] = true
			}
		}
	}
	return used, nil
}

func (o proxyOccupancy) linkedToSelf(tun storage.AWGTunnel) bool {
	if strings.TrimSpace(o.selfID) == "" {
		return false
	}
	switch o.selfKind {
	case instancestore.KindWdttClient:
		return strings.TrimSpace(tun.WdttClientID) == o.selfID
	case instancestore.KindFreeTurnClient:
		return strings.TrimSpace(tun.FreeTurnClientID) == o.selfID
	}
	return false
}

// recordPorts — порты, которые запись занимает на роутере. WgPort сервера —
// паритет старого чек-листа (proxylisten/checker.go:100-102): внутренний
// WG-порт занят так же, как DTLS-listen.
func recordPorts(rec instancestore.Record) []int {
	var addrs []string
	var ports []int
	switch {
	case rec.WdttClient != nil:
		addrs = append(addrs, rec.WdttClient.Listen)
	case rec.WdttServer != nil:
		addrs = append(addrs, rec.WdttServer.Listen)
		if rec.WdttServer.WgPort > 0 {
			ports = append(ports, rec.WdttServer.WgPort)
		}
	case rec.FreeTurnClient != nil:
		addrs = append(addrs, rec.FreeTurnClient.Listen)
	case rec.FreeTurnServer != nil:
		addrs = append(addrs, rec.FreeTurnServer.Listen)
	}
	for _, addr := range addrs {
		if port, ok := anyHostPort(addr); ok {
			ports = append(ports, port)
		}
	}
	return ports
}

func anyHostPort(addr string) (int, bool) {
	_, portStr, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return 0, false
	}
	return port, true
}

// localhostPort — порт локального адреса. Три формы хоста — паритет
// freeturn.LocalListenPort: endpoint связанного туннеля правит и человек.
func localhostPort(addr string) (int, bool) {
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return 0, false
	}
	switch strings.Trim(strings.ToLower(host), "[]") {
	case "127.0.0.1", "localhost", "::1":
	default:
		return 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return 0, false
	}
	return port, true
}

var _ linkres.Occupancy = proxyOccupancy{}

// proxyRouteHooks — linkres.RouteHooks поверх clientroute-сервиса (сигнатуры
// совпадают; интерфейс сужает его до двух методов жизненного цикла).
type proxyRouteHooks struct {
	svc clientroute.Service
}

func (h proxyRouteHooks) OnTunnelStart(ctx context.Context, tunnelID, kernelIface string) error {
	return h.svc.OnTunnelStart(ctx, tunnelID, kernelIface)
}

func (h proxyRouteHooks) OnTunnelStop(ctx context.Context, tunnelID string) error {
	return h.svc.OnTunnelStop(ctx, tunnelID)
}

var _ linkres.RouteHooks = proxyRouteHooks{}

// --------------------------------------------------------------- netres

// proxyIPT — netres.IPT поверх internal/sys/iptables.
type proxyIPT struct{}

func (proxyIPT) Run(ctx context.Context, args ...string) error {
	return iptables.Run(ctx, args...)
}

func (proxyIPT) Output(ctx context.Context, args ...string) (string, error) {
	return iptables.RunOutput(ctx, args...)
}

var _ netres.IPT = proxyIPT{}

// proxyFWBook — ОБЩАЯ ведомость INPUT-портов прокси-рантайма поверх
// internal/listenfirewall. Ресурс netres.InputPort пер-инстансный, а
// listenfirewall глобален: ListManaged отдаёт правила ВСЕХ наших инстансов, а
// Reconcile переписывает единственный хук ТОЛЬКО переданными портами. Прямой
// пер-инстансный адаптер поверх такой пары даёт вечный цикл: при живых
// wdtt-сервере и freeturn-сервере каждый видит порты соседа лишними, закрывает
// их и переписывает хук своей половиной — и так каждые 15 секунд, с записью на
// флеш.
//
// Ведомость держит вклад каждого инстанса и раздаёт хендлы forInstance:
//   - Managed(key) — живые правила МИНУС порты, желаемые ДРУГИМИ: лишним
//     считается лишь то, что не нужно никому;
//   - Reconcile(key, own) — вклад запоминается, в listenfirewall уезжает
//     ОБЪЕДИНЕНИЕ вкладов.
//
// Сузить Observe до своих правил нечем: признака принадлежности в правиле нет
// (метка listenfirewall.Comment одна на всех). Аддитивный Reconcile отвергнут:
// порт, открытый прежним поколением, не закрыл бы никто, а собственный хук
// вечно его восстанавливал бы.
type proxyFWBook struct {
	mu      sync.Mutex
	want    map[string][]netres.PortSpec
	pending map[string]bool
	expired bool
	// list/apply — швы прода (listenfirewall) ради теста: весь смысл ведомости
	// в СОСТАВЕ желаемого, а наблюдать его иначе нечем.
	list  func(ctx context.Context) ([]listenfirewall.PortSpec, error)
	apply func(ctx context.Context, desired []listenfirewall.PortSpec)
}

// proxyFWGrace — окно ожидания отчётов. Пока не отчитался хоть один серверный
// инстанс, ведомость щадит чужие порты (см. pendingLocked). Окно нужно
// потому, что молчание не отличимо от «портов не хочу»: выключенный сервер и
// сервер со снятым тумблером firewall'а объявляют ПУСТОЕ желаемое, netres
// .InputPort на нём не зовёт Apply вовсе (и не заводит будильник), то есть не
// отчитается никогда. Без окна порты мёртвого поколения оставались бы открыты
// вечно.
const proxyFWGrace = 2 * time.Minute

// proxyFWAfterFunc — переменная ради теста (тот же приём, что у killPID и
// legacyHookPath): будильник окна, который в тесте ждать нельзя.
var proxyFWAfterFunc = time.AfterFunc

// newProxyFWBook — ведомость на список ключей серверных инстансов (тех, у чьих
// ролей есть ресурс input_port). Список знает фабрика.
func newProxyFWBook(serverKeys []string) *proxyFWBook {
	b := &proxyFWBook{
		want:    map[string][]netres.PortSpec{},
		pending: map[string]bool{},
		list:    listenfirewall.ListManaged,
		apply:   listenfirewall.Reconcile,
	}
	for _, k := range serverKeys {
		b.pending[k] = true
	}
	proxyFWAfterFunc(proxyFWGrace, b.graceOver)
	return b
}

// graceOver — окно ожидания истекло: один проход приведения к объединению.
// Ведомость перестаёт быть пассивной, и это осознанная цена. Без прохода
// истечение окна не запускает НИЧЕГО: вычистка держится на будильнике
// netres.InputPort, а он ненулевой только при непустом желаемом — при всех
// выключенных серверах порты мёртвого поколения жили бы в INPUT до включения
// хоть одного сервера или перезапуска демона.
func (b *proxyFWBook) graceOver() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expired = true
	// Ошибки быть не может: при истёкшем окне объединение считается без
	// листинга живых правил.
	_ = b.applyUnionLocked(context.Background())
}

// forInstance — хендл netres.FW для одного инстанса.
func (b *proxyFWBook) forInstance(key string) netres.FW { return proxyFW{book: b, key: key} }

// forget — снять вклад инстанса и тут же привести объединение. Штатное
// удаление обнуляет вклад само (teardown-прогон с пустым желаемым), но на
// нештатном пути его не будет: ожидание снятия ресурсов ограничено таймаутом,
// а прогон рвётся на первом отказе применения — ресурс input_port у серверной
// роли последний, и до него просто не доходит очередь. Оставленный вклад
// входил бы в каждое следующее объединение, то есть правило не просто не
// снималось бы, а восстанавливалось любым соседним приведением — до
// перезапуска демона.
func (b *proxyFWBook) forget(ctx context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, known := b.want[key]
	if !known && !b.pending[key] {
		return nil // чужой или уже забытый ключ: приводить нечего
	}
	delete(b.want, key)
	delete(b.pending, key)
	return b.applyUnionLocked(ctx)
}

// pendingLocked — круг отчётов не полон и окно ожидания не истекло.
func (b *proxyFWBook) pendingLocked() bool {
	return !b.expired && len(b.pending) > 0
}

func fwPortKey(port int, proto string) string {
	return strings.ToLower(strings.TrimSpace(proto)) + "/" + strconv.Itoa(port)
}

// visible — ведомость для инстанса key.
func (b *proxyFWBook) visible(key string, live []listenfirewall.PortSpec) []netres.PortSpec {
	b.mu.Lock()
	defer b.mu.Unlock()
	keep := map[string]bool{}
	if b.pendingLocked() {
		// Круг не полон: чьи именно порты открыты — неизвестно, поэтому
		// инстансу видны ТОЛЬКО его собственные живые порты, и лишних у него
		// нет. Не отчитавшийся видит пустую ведомость и потому приводит своё
		// желаемое сам — иначе его вклад не узнать, netres.InputPort сообщает
		// желаемое только через Reconcile.
		for _, s := range b.want[key] {
			keep[fwPortKey(s.Port, s.Proto)] = true
		}
	} else {
		for _, s := range live {
			keep[fwPortKey(s.Port, s.Proto)] = true
		}
		for k, specs := range b.want {
			if k == key {
				continue
			}
			for _, s := range specs {
				delete(keep, fwPortKey(s.Port, s.Proto))
			}
		}
	}
	out := make([]netres.PortSpec, 0, len(live))
	for _, s := range live {
		if keep[fwPortKey(s.Port, s.Proto)] {
			out = append(out, netres.PortSpec{Port: s.Port, Proto: s.Proto})
		}
	}
	return out
}

// reconcile — записать вклад инстанса и привести listenfirewall к объединению.
func (b *proxyFWBook) reconcile(ctx context.Context, key string, own []netres.PortSpec) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(own) == 0 {
		// Пустой вклад держать незачем, а карта иначе копит выключенных и
		// снятых.
		delete(b.want, key)
	} else {
		b.want[key] = append([]netres.PortSpec(nil), own...)
	}
	delete(b.pending, key)
	return b.applyUnionLocked(ctx)
}

// applyUnionLocked — привести listenfirewall к объединению вкладов. Зовётся
// под удержанным b.mu.
func (b *proxyFWBook) applyUnionLocked(ctx context.Context) error {
	seen := map[string]bool{}
	var desired []listenfirewall.PortSpec
	add := func(port int, proto string) {
		k := fwPortKey(port, proto)
		if seen[k] {
			return
		}
		seen[k] = true
		desired = append(desired, listenfirewall.PortSpec{Port: port, Proto: proto})
	}
	for _, specs := range b.want {
		for _, s := range specs {
			add(s.Port, s.Proto)
		}
	}
	if b.pendingLocked() {
		// Пока сосед не отчитался, объединение обязано нести и его живые
		// порты: без этого приведение по СВОЕЙ причине (свой порт пропал)
		// закрыло бы чужое. Отказ листинга здесь fail-closed — состав
		// неизвестен, приведение отменяется, ресурс повторит.
		live, err := b.list(ctx)
		if err != nil {
			return err
		}
		for _, s := range live {
			add(s.Port, s.Proto)
		}
	}
	b.apply(ctx, desired)
	return nil
}

// proxyFW — netres.FW одного инстанса поверх общей ведомости.
type proxyFW struct {
	book *proxyFWBook
	key  string
}

func (f proxyFW) Managed(ctx context.Context) ([]netres.PortSpec, error) {
	live, err := f.book.list(ctx)
	if err != nil {
		return nil, err
	}
	return f.book.visible(f.key, live), nil
}

func (f proxyFW) Reconcile(ctx context.Context, desired []netres.PortSpec) error {
	return f.book.reconcile(ctx, f.key, desired)
}

var _ netres.FW = proxyFW{}

// ---------------------------------------------- wdttserver: доступ и ingress

// natPolicyLANApplier — срез managed.Service: NDMS NAT-режим, hotspot policy и
// LAN-ACL. *managed.Service удовлетворяет как есть.
type natPolicyLANApplier interface {
	ApplyNATModeToInterface(ctx context.Context, iface, mode, prevWAN string) (string, error)
	ApplyPolicyToInterface(ctx context.Context, iface, policy string) error
	ApplyLANSegmentsToInterface(ctx context.Context, iface, addr, mask string, segments []string) error
}

// permitACLSetter — срез InterfaceCommands: разрешающий ACL на интерфейсе.
type permitACLSetter interface {
	SetPermitAllACL(ctx context.Context, name string) error
}

// proxyAccessApplier — wdttserver.AccessApplier. Логика — прежний
// wdttAccessAdapter (router_adapters.go:407+), сведённая к четырём методам
// интерфейса; возвращённый ApplyNATModeToInterface WAN уходит в Detail
// ресурса ndms_access.
type proxyAccessApplier struct {
	svc    natPolicyLANApplier
	ifaces permitACLSetter
}

func (a proxyAccessApplier) ApplyNATModeToInterface(ctx context.Context, iface, mode, prevWAN string) (string, error) {
	if a.svc == nil {
		return "", fmt.Errorf("managed service not available")
	}
	return a.svc.ApplyNATModeToInterface(ctx, iface, mode, prevWAN)
}

func (a proxyAccessApplier) ApplyPolicyToInterface(ctx context.Context, iface, policy string) error {
	if a.svc == nil {
		return fmt.Errorf("managed service not available")
	}
	return a.svc.ApplyPolicyToInterface(ctx, iface, policy)
}

func (a proxyAccessApplier) ApplyLANSegmentsToInterface(ctx context.Context, iface, addr, mask string, segments []string) error {
	if a.svc == nil {
		return fmt.Errorf("managed service not available")
	}
	return a.svc.ApplyLANSegmentsToInterface(ctx, iface, addr, mask, segments)
}

func (a proxyAccessApplier) EnsureInterfaceFirewallPermit(ctx context.Context, iface string) error {
	if a.ifaces == nil {
		return nil
	}
	return a.ifaces.SetPermitAllACL(ctx, iface)
}

var _ wdttserver.AccessApplier = proxyAccessApplier{}

// ingressSettingsStore — срез *storage.SettingsStore.
type ingressSettingsStore interface {
	Load() (*storage.Settings, error)
	Save(settings *storage.Settings) error
}

// routerReconciler — срез реконсиляции конфига sing-box.
type routerReconciler interface {
	Reconcile(ctx context.Context) error
}

// proxyIngressEnsurer — wdttserver.IngressEnsurer. Логика — прежний
// wdttIngressEnsurer (router_adapters.go:506-531): settings-store → ensure →
// save → reconcile роутера.
type proxyIngressEnsurer struct {
	settings ingressSettingsStore
	router   routerReconciler
}

func (e proxyIngressEnsurer) EnsureWdttServerIngressRefs(ctx context.Context, wgKernelIface, rawKernelIface string) error {
	if e.settings == nil {
		return nil
	}
	settings, err := e.settings.Load()
	if err != nil {
		return err
	}
	next, changed := EnsureWdttIngressRefs(settings.SingboxRouter.IngressInterfaces, wgKernelIface, rawKernelIface)
	if !changed {
		return nil
	}
	settings.SingboxRouter.IngressInterfaces = next
	if err := e.settings.Save(settings); err != nil {
		return err
	}
	if e.router != nil {
		return e.router.Reconcile(ctx)
	}
	return nil
}

var _ wdttserver.IngressEnsurer = proxyIngressEnsurer{}

// DefaultWdttIface/DefaultRawServerIface — legacy kernel-имена половин сервера
// (копия wdtt/types.go:174,184). Локальные константы, а не импорт: пакет
// internal/wdtt умирает вместе со старым движком.
const (
	DefaultWdttIface      = "wdtt0"
	DefaultRawServerIface = "wdttraw0"
)

// WdttServerIngressRefs returns sing-box ingress interface refs for a WDTT
// server: kernel WG iface (opkgtunN/wdtt0) and raw relay iface (opkgtunM/wdttraw0).
func WdttServerIngressRefs(wgKernelIface, rawKernelIface string) []string {
	wg := strings.TrimSpace(wgKernelIface)
	if wg == "" {
		wg = DefaultWdttIface
	}
	raw := strings.TrimSpace(rawKernelIface)
	if raw == "" {
		raw = DefaultRawServerIface
	}
	return []string{"iface:" + wg, "iface:" + raw}
}

// staleWdttIngressRefs — наши прежние имена, которых на роутере больше нет:
// legacy wdtt0/wdttraw0 после переезда интерфейса в OpkgTun. Ссылка на
// несуществующий интерфейс остаётся в настройках ingress навсегда — чистим её
// тем же проходом, что добавляет актуальную.
func staleWdttIngressRefs(want []string) map[string]bool {
	stale := map[string]bool{}
	for _, legacy := range []string{"iface:" + DefaultWdttIface, "iface:" + DefaultRawServerIface} {
		if !slices.Contains(want, legacy) {
			stale[legacy] = true
		}
	}
	return stale
}

// EnsureWdttIngressRefs adds the raw ingress ref when the WG kernel ref is
// present but raw is missing. Returns the updated slice and whether it changed.
//
// В новом мире WgIface/RawIface непусты по Validate (roles/config.go:219-224),
// поэтому legacy-фолбэк wdtt0/wdttraw0 обслуживает ТОЛЬКО вычисление
// протухших ссылок — снос его как «мёртвого» сломает уборку.
func EnsureWdttIngressRefs(refs []string, wgKernelIface, rawKernelIface string) ([]string, bool) {
	want := WdttServerIngressRefs(wgKernelIface, rawKernelIface)
	wgRef := want[0]
	rawRef := want[1]
	hasWG := false
	hasRaw := false
	for _, ref := range refs {
		switch ref {
		case wgRef:
			hasWG = true
		case rawRef:
			hasRaw = true
		}
	}
	if !hasWG {
		return refs, false
	}
	stale := staleWdttIngressRefs(want)
	out := make([]string, 0, len(refs)+1)
	dropped := false
	for _, ref := range refs {
		if stale[ref] {
			dropped = true
			continue
		}
		out = append(out, ref)
	}
	if hasRaw {
		if !dropped {
			return refs, false
		}
		return out, true
	}
	return append(out, rawRef), true
}

// ------------------------------------------------- одноразовые шаги посева

// proxyPostSeed: обнуление адресов — ВСЕГДА (замечание 2 ревью: одноразовый
// вызов с проглоченной ошибкой делал потерю вечной); добивание старого
// поколения и уборка наследия — пока отметка уборки не снята.
//
// Амендмент A3: признаком «посев только что состоялся» шаги гонять нельзя —
// транзиентный отказ (занятая блокировка iptables при рерайте таблиц роутером,
// недоступная команда) навсегда съедал бы единственный шанс, а отказ шагов
// боот не останавливает. Отметка живёт на диске и снимается ОТДЕЛЬНОЙ
// транзакцией (clearCleanup) только после успешного прохода. Идемпотентность
// шагов уже обеспечена: свипы — реплей листинга, добивание гардировано
// проверкой имени бинаря.
func proxyPostSeed(mirror *exitreg.StoreMirror, ipt netres.IPT, cmds opkgTunDeleter,
	ifaces ifaceLister, binaries []string,
	clearCleanup func() error) func(context.Context, instancestore.SeedResult, map[string]bool) error {
	return func(ctx context.Context, res instancestore.SeedResult, declaredNDMS map[string]bool) error {
		var errs []error
		if _, err := mirror.ZeroStaleAddresses(); err != nil {
			errs = append(errs, err)
		}
		if res.SeededNow || res.CleanupPending {
			for _, proc := range res.OldGenProcs {
				killOldGeneration(proc, binaries)
			}
			if err := legacyCleanup(ctx, ipt, cmds, ifaces, declaredNDMS, res.LegacyKernelIfaces); err != nil {
				errs = append(errs, err)
			} else if err := clearCleanup(); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
}

// killPID — переменная ради теста: убийство чужого процесса в тесте
// недопустимо, перехватывается подменой.
var killPID = syscall.Kill

// killOldGeneration — B3 (блокер): pid-файл на флешке (<dataDir>/run)
// переживает ребут, и PID мог достаться постороннему процессу. Убиваем
// только живой процесс НАШЕГО бинаря (паритет pidIsOurs старого мира,
// wdtt/process.go:301-308: childproc.MatchesBinary по /proc cmdline).
//
// Сверка бинаря — необходимая, но НЕ достаточная: уборка повторяется, пока
// висит отметка (A3), а бинарь у старого и нового поколения один и тот же,
// поэтому переиспользованный номер прошёл бы её и убил своего. Решает
// отпечаток — время старта процесса, снятое на посеве.
func killOldGeneration(proc instancestore.OldGenProc, binaries []string) {
	pid := proc.PID
	if !childproc.IsAlive(pid) {
		return
	}
	if proc.StartTime == 0 {
		return // отпечатка нет: на посеве процесса уже не было
	}
	if start, ok := childproc.StartTime(pid); !ok || start != proc.StartTime {
		return // номер переиспользован системой
	}
	ours := false
	for _, b := range binaries {
		if childproc.MatchesBinary(pid, b) {
			ours = true
			break
		}
	}
	if !ours {
		return
	}
	_ = killPID(-pid, syscall.SIGKILL) // группа; одиночный pid — фолбэк
	_ = killPID(pid, syscall.SIGKILL)
}

// Отпечатки старого движка. Ни одну из этих форм новый мир не строит, поэтому
// снимаются они всегда; снос обязан случиться сейчас, потому что вместе с
// пакетом internal/wdtt исчезнут и сами метки — снять будет нечем.
const (
	// legacyLANComment — пары FORWARD peer↔LAN (entware_lan_linux.go:12).
	// У правил НЕТ ни -i, ни -o: ветка по имени интерфейса их не найдёт.
	legacyLANComment = "AWGM_WDTT_LAN"
	// legacyPolicyComment — mangle-правила raw-политики прежних версий
	// (server_raw_policy_linux.go:91).
	legacyPolicyComment = "AWGM-WDTT-POLICY"
	// legacyDescSuffix — окончание описания NDMS старого мира
	// (wdtt.TunnelNameFromClient, names.go:11).
	legacyDescSuffix = " wdtt"
)

// legacyHookPath — переменная ради теста: путь абсолютный, и наблюдать снос
// иначе нечем. Приём тот же, что у sweepLegacyRawServerRules старого мира.
var legacyHookPath = netres.HookPath

// opkgTunDeleter — срез ndmsres.Commands для уборки legacy-интерфейсов.
type opkgTunDeleter interface {
	DeleteOpkgTun(ctx context.Context, name string) error
}

// legacyChain — цепочка, из которой сносим реплеем `-S`: iptables -D требует
// точного совпадения спеки, а формы старого мира зависят от WAN, CIDR и метки.
type legacyChain struct {
	table string // "" = filter
	chain string
}

func (c legacyChain) listArgs() []string {
	if c.table == "" {
		return []string{"-S", c.chain}
	}
	return []string{"-t", c.table, "-S", c.chain}
}

func (c legacyChain) deleteArgs(spec []string) []string {
	if c.table == "" {
		return append([]string{"-D"}, spec...)
	}
	return append([]string{"-t", c.table, "-D"}, spec...)
}

// legacyCleanup — уборка наследия старого движка. Гоняется, пока висит отметка
// cleanupPending (амендмент A3): первый посев её ставит, снимает её успешный
// проход. Три источника форм, и все три обязаны быть здесь: правила
// по прежним kernel-именам (а), помеченные правила трёх разных меток (б) и
// legacy-описания NDMS (в).
//
// declaredNDMS — ведомость ОБЪЯВЛЕННЫХ и живых NDMS-имён. Её пустота значит
// «объявленных нет», а не «щадить нечего»: правила объявленного интерфейса
// уборка не трогает, поэтому подмена ведомости пустой снесла бы живое.
// В kernelIfaces при сохранённом пине попадают и ЖИВЫЕ имена (посев кладёт
// туда прежние имена сервера как есть) — их отсеивает та же ведомость.
func legacyCleanup(ctx context.Context, ipt netres.IPT, cmds opkgTunDeleter,
	ifaces ifaceLister, declaredNDMS map[string]bool, kernelIfaces []string) error {
	stale := legacyStaleIfaces(kernelIfaces, declaredNDMS)
	// Помеченные метками формы снимаются БЕЗУСЛОВНО, живой сервер тут ни при
	// чём: усыновить помеченное правило некому. netres.RuleSet.markedOrphans
	// заводит область поиска только по правилам своего желаемого с непустой
	// меткой, а новый FORWARD строится БЕЗ метки (netres/wdtt.go:26-27) — то
	// есть помеченный FORWARD не снял бы ни он, ни уборка. Помеченный
	// MASQUERADE живой сервер пересоберёт первым же прогоном: уборка
	// одноразовая и идёт ДО старта инстансов, окно — секунды, установившиеся
	// потоки NAT от снятия правила не рвутся.
	//
	// noServers — только для netfilter.d-хука ниже: его снос при живом сервере
	// был бы гонкой с ресурсом netres.Hook, пишущим тот же путь.
	noServers := len(kernelIfaces) == 0

	byStaleIface := func(line string) bool {
		for _, iface := range stale {
			if lineHasIface(line, "-i", iface) || lineHasIface(line, "-o", iface) {
				return true
			}
		}
		return false
	}

	var errs []error
	sweeps := []struct {
		chain  legacyChain
		doomed func(line string, fields []string) bool
	}{
		{legacyChain{chain: "FORWARD"}, func(line string, fields []string) bool {
			switch commentTag(fields) {
			case legacyLANComment:
				return true
			case netres.Comment:
				return true
			}
			// FORWARD accept обеих форм — помеченной (≤2.16.x) и голой.
			return byStaleIface(line) && hasTarget(fields, "ACCEPT")
		}},
		{legacyChain{chain: "INPUT"}, func(line string, _ []string) bool {
			return byStaleIface(line) && strings.Contains(line, "--dport 53")
		}},
		{legacyChain{table: "nat", chain: "PREROUTING"}, func(line string, _ []string) bool {
			return byStaleIface(line) && strings.Contains(line, "--dport 53")
		}},
		{legacyChain{table: "nat", chain: "POSTROUTING"}, func(_ string, fields []string) bool {
			return commentTag(fields) == netres.Comment
		}},
		{legacyChain{table: "mangle", chain: "PREROUTING"}, func(line string, fields []string) bool {
			if commentTag(fields) == legacyPolicyComment {
				return true
			}
			return byStaleIface(line) && (hasTarget(fields, "MARK") || hasTarget(fields, "CONNMARK"))
		}},
	}
	for _, s := range sweeps {
		if err := sweepLegacyChain(ctx, ipt, s.chain, s.doomed); err != nil {
			errs = append(errs, err)
		}
	}

	// Цепочка clamp с её jump. Имя то же, что у новой (netres.MSSChain), и
	// снос безопасен только потому, что уборка идёт ДО старта инстансов:
	// ресурс mss_clamp пересоберёт цепочку на первом же прогоне.
	for i := 0; i < 3; i++ {
		_ = ipt.Run(ctx, "-t", "mangle", "-D", "FORWARD", "-j", netres.MSSChain)
	}
	_ = ipt.Run(ctx, "-t", "mangle", "-F", netres.MSSChain)
	_ = ipt.Run(ctx, "-t", "mangle", "-X", netres.MSSChain)

	if noServers {
		// netfilter.d-хук старого движка — ЕДИНСТВЕННЫЙ его артефакт,
		// переживающий и перезапись таблиц ndm, и перезагрузку: всё остальное
		// живёт лишь до следующего рерайта таблиц. При живом сервере файл
		// перепишет ресурс netres.Hook (путь тот же), при выключенном — снимет;
		// но когда серверов не осталось вовсе, ресурса Hook не существует, и
		// файл остался бы навсегда — снять его после сноса internal/wdtt будет
		// нечем.
		_ = os.Remove(legacyHookPath)
	}

	if err := dropLegacyNDMSIfaces(ctx, cmds, ifaces, declaredNDMS); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// legacyStaleIfaces — прежние kernel-имена, которых больше нет. Объявленное
// имя щадится: kernel-имя объявленного OpkgTunN — opkgtunN (посев,
// instancestore/seed.go:308), поэтому сравнение идёт в нижнем регистре.
func legacyStaleIfaces(kernelIfaces []string, declaredNDMS map[string]bool) []string {
	live := make(map[string]bool, len(declaredNDMS))
	for name := range declaredNDMS {
		live[strings.ToLower(strings.TrimSpace(name))] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, name := range kernelIfaces {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] || live[strings.ToLower(name)] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func sweepLegacyChain(ctx context.Context, ipt netres.IPT, ch legacyChain,
	doomed func(line string, fields []string) bool) error {
	out, err := ipt.Output(ctx, ch.listArgs()...)
	if err != nil {
		return fmt.Errorf("листинг %s: %w", strings.Join(ch.listArgs(), " "), err)
	}
	var errs []error
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "-A" || fields[1] != ch.chain {
			continue
		}
		if !doomed(line, fields) {
			continue
		}
		// Дубли снимаются сами собой: каждая напечатанная строка — отдельный
		// -D, а -D снимает ровно одну копию.
		if err := ipt.Run(ctx, ch.deleteArgs(fields[1:])...); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// commentTag — значение --comment правила. Сравнение по ПОЛЮ, не по подстроке:
// AWGM_WDTT — префикс AWGM_WDTT_LAN, и подстрочная сверка спутала бы живую
// метку с legacy-формой.
func commentTag(fields []string) string {
	for i, f := range fields {
		if f == "--comment" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

func hasTarget(fields []string, target string) bool {
	for i, f := range fields {
		if f == "-j" && i+1 < len(fields) && fields[i+1] == target {
			return true
		}
	}
	return false
}

// lineHasIface — границы токена обязательны: "-i opkgtun1" иначе ловит
// "-i opkgtun17" (та же ловушка, что в entwareForwardIfacesPresent).
func lineHasIface(line, dir, iface string) bool {
	return strings.Contains(line, " "+dir+" "+iface+" ") ||
		strings.HasSuffix(strings.TrimSpace(line), " "+dir+" "+iface)
}

// dropLegacyNDMSIfaces — В6: интерфейсы OpkgTun с описанием старого мира,
// которых нет в ведомости объявленных. Совпадение по СУФФИКСУ: " wdtt" в
// середине описания — чужое имя, не наша форма.
func dropLegacyNDMSIfaces(ctx context.Context, cmds opkgTunDeleter,
	ifaces ifaceLister, declaredNDMS map[string]bool) error {
	if ifaces == nil || cmds == nil {
		return nil
	}
	list, err := ifaces.List(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, it := range list {
		if _, ok := opkgTunIndex(it.ID); !ok {
			continue
		}
		if declaredNDMS[it.ID] {
			continue // описание поправит ресурс роли
		}
		if !strings.HasSuffix(strings.ToLower(strings.TrimSpace(it.Description)), legacyDescSuffix) {
			continue
		}
		if err := cmds.DeleteOpkgTun(ctx, it.ID); err != nil {
			errs = append(errs, fmt.Errorf("снять legacy-интерфейс %s: %w", it.ID, err))
		}
	}
	return errors.Join(errs...)
}
