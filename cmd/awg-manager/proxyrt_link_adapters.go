package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"syscall"

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

// linkedTo — то же сравнение, что у предикатов api (tunnelLinkedToWdttClient):
// пустой clientID не связывает НИЧЕГО, иначе под «связанные» попали бы все
// туннели без связи.
func (s proxyEndpointSync) linkedTo(tun storage.AWGTunnel, clientID string) bool {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return false
	}
	switch s.field {
	case api.LinkedWdtt:
		return strings.TrimSpace(tun.WdttClientID) == clientID
	case api.LinkedFreeTurn:
		return strings.TrimSpace(tun.FreeTurnClientID) == clientID
	}
	return false
}

func (s proxyEndpointSync) List(_ context.Context, clientID string) ([]linkres.LinkedTunnel, error) {
	if s.store == nil {
		return nil, nil
	}
	all, err := s.store.List()
	if err != nil {
		return nil, err
	}
	var out []linkres.LinkedTunnel
	for _, tun := range all {
		if !s.linkedTo(tun, clientID) {
			continue
		}
		out = append(out, linkres.LinkedTunnel{ID: tun.ID, Endpoint: strings.TrimSpace(tun.Peer.Endpoint)})
	}
	return out, nil
}

func (s proxyEndpointSync) Sync(ctx context.Context, clientID, listen string) (int, error) {
	updated, failed := api.SyncLinkedProxyEndpoints(ctx, s.store, s.svc, s.field, clientID, listen)
	if len(updated) > 0 && s.pub != nil {
		for _, res := range []string{api.ResourceTunnels, api.ResourceRoutingTunnels} {
			s.pub.Publish("resource:invalidated", events.ResourceInvalidatedEvent{
				Resource: res, Reason: "proxy-linked-endpoint",
			})
		}
	}
	if len(failed) > 0 {
		// Молчаливое отбрасывание failed — вечный невидимый дрейф: endpoint
		// остаётся на старом порту, а ресурс рапортует успех.
		return len(updated), fmt.Errorf("endpoint не обновлён у: %s", strings.Join(failed, ", "))
	}
	return len(updated), nil
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

// proxyFW — netres.FW поверх internal/listenfirewall: своя метка, свой хук и
// декларативный Reconcile, снимающий stale по живым правилам.
type proxyFW struct{}

func (proxyFW) Managed(ctx context.Context) ([]netres.PortSpec, error) {
	specs, err := listenfirewall.ListManaged(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]netres.PortSpec, 0, len(specs))
	for _, s := range specs {
		out = append(out, netres.PortSpec{Port: s.Port, Proto: s.Proto})
	}
	return out, nil
}

func (proxyFW) Reconcile(ctx context.Context, desired []netres.PortSpec) error {
	specs := make([]listenfirewall.PortSpec, 0, len(desired))
	for _, s := range desired {
		specs = append(specs, listenfirewall.PortSpec{Port: s.Port, Proto: s.Proto})
	}
	listenfirewall.Reconcile(ctx, specs)
	return nil
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
// поколения и уборка наследия — только при первом посеве.
func proxyPostSeed(mirror *exitreg.StoreMirror, ipt netres.IPT, cmds proxyNDMSCommands,
	ifaces ifaceLister, binaries []string) func(context.Context, instancestore.SeedResult, map[string]bool) error {
	return func(ctx context.Context, res instancestore.SeedResult, declaredNDMS map[string]bool) error {
		var errs []error
		if _, err := mirror.ZeroStaleAddresses(); err != nil {
			errs = append(errs, err)
		}
		if res.SeededNow {
			for _, pid := range res.OldGenPIDs {
				killOldGeneration(pid, binaries)
			}
			if err := legacyCleanup(ctx, ipt, cmds, ifaces, declaredNDMS, res.LegacyKernelIfaces); err != nil {
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
func killOldGeneration(pid int, binaries []string) {
	if !childproc.IsAlive(pid) {
		return
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

// legacyCleanup — ОДНОРАЗОВАЯ уборка наследия старого движка (только при
// первом посеве). Три источника форм, и все три обязаны быть здесь: правила
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
	// Голая метка AWGM_WDTT — ЖИВАЯ: её ставит и новый движок (netres.Comment),
	// поэтому её flush законен только при пустом желаемом. Сервер в желаемом
	// есть ровно тогда, когда посев отдал его прежние kernel-имена
	// (instancestore/seed.go:400-410) — при живом сервере старые правила
	// приведёт его собственный RuleSet.
	flushLiveComment := len(kernelIfaces) == 0

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
				if flushLiveComment {
					return true
				}
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
			return flushLiveComment && commentTag(fields) == netres.Comment
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
