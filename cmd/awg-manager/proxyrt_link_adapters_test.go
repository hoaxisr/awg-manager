package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/exitreg"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/netres"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// fakeIPT — модель iptables: листинги по ключу аргументов и ЖУРНАЛ команд.
// Журнал, а не счётчик: уборка отличается от порчи только тем, С ЧЕМ её
// позвали, поэтому все сверки ниже — по составу аргументов.
type fakeIPT struct {
	out  map[string]string
	runs [][]string
}

func (f *fakeIPT) Run(_ context.Context, args ...string) error {
	f.runs = append(f.runs, append([]string(nil), args...))
	return nil
}

func (f *fakeIPT) Output(_ context.Context, args ...string) (string, error) {
	if f.out == nil {
		return "", nil
	}
	return f.out[strings.Join(args, " ")], nil
}

func (f *fakeIPT) ran(args ...string) bool {
	want := strings.Join(args, " ")
	for _, r := range f.runs {
		if strings.Join(r, " ") == want {
			return true
		}
	}
	return false
}

func (f *fakeIPT) ranWith(sub string) bool {
	for _, r := range f.runs {
		if strings.Contains(strings.Join(r, " "), sub) {
			return true
		}
	}
	return false
}

type fakeOpkgDeleter struct{ deleted []string }

func (f *fakeOpkgDeleter) DeleteOpkgTun(_ context.Context, name string) error {
	f.deleted = append(f.deleted, name)
	return nil
}

type fakePublisher struct{ events []string }

func (p *fakePublisher) Publish(eventType string, _ any) {
	p.events = append(p.events, eventType)
}

// ---------------------------------------------------------------- п. 1: sync

// failingUpdate — api.TunnelService, у которого падает ровно Update. Интерфейс
// встроен nil-ом: любой другой метод в этом пути — паника, то есть видимый
// дефект теста, а не тихо проглоченный вызов.
type failingUpdate struct{ api.TunnelService }

func (failingUpdate) Update(_ context.Context, _, _ *storage.AWGTunnel) error {
	return errors.New("boom")
}

func linkedStore(t *testing.T) *storage.AWGTunnelStore {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	for _, tun := range []*storage.AWGTunnel{
		{ID: "awgm1", Name: "WD", WdttClientID: "c1", Peer: storage.AWGPeer{Endpoint: "127.0.0.1:9000"}},
		{ID: "awgm2", Name: "FT", FreeTurnClientID: "c1", Peer: storage.AWGPeer{Endpoint: "127.0.0.1:9000"}},
	} {
		if err := store.Save(tun); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func TestProxyEndpointSyncListsAndUpdates(t *testing.T) {
	store := linkedStore(t)
	pub := &fakePublisher{}
	s := newProxyEndpointSync(store, nil, api.LinkedWdtt, pub)

	list, err := s.List(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "awgm1" || list[0].Endpoint != "127.0.0.1:9000" {
		t.Fatalf("список связанных: %+v", list)
	}

	n, err := s.Sync(context.Background(), "c1", "127.0.0.1:9001")
	if err != nil || n != 1 {
		t.Fatalf("Sync = (%d, %v)", n, err)
	}
	got, err := store.Get("awgm1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Peer.Endpoint != "127.0.0.1:9001" {
		t.Fatalf("endpoint = %q", got.Peer.Endpoint)
	}
	if len(pub.events) == 0 || pub.events[0] != "resource:invalidated" {
		t.Fatalf("инвалидация не опубликована: %v", pub.events)
	}
}

// Молчаливое отбрасывание failed было бы вечным невидимым дрейфом: endpoint
// туннеля остаётся на старом порту, а ресурс рапортует успех.
func TestProxyEndpointSyncReportsFailed(t *testing.T) {
	store := linkedStore(t)
	s := newProxyEndpointSync(store, failingUpdate{}, api.LinkedWdtt, nil)

	n, err := s.Sync(context.Background(), "c1", "127.0.0.1:9001")
	if err == nil {
		t.Fatal("отказ обновления проглочен")
	}
	if n != 0 {
		t.Fatalf("updated = %d", n)
	}
	if !strings.Contains(err.Error(), "endpoint не обновлён у") || !strings.Contains(err.Error(), "awgm1") {
		t.Fatalf("ошибка не называет туннель: %v", err)
	}
}

// ----------------------------------------------------------- п. 2: занятость

type fakeRecords struct {
	recs []instancestore.Record
	err  error
}

func (f fakeRecords) Load() (instancestore.State, error) {
	return instancestore.State{Records: f.recs}, f.err
}

type fakeAWGList struct {
	list []storage.AWGTunnel
	err  error
}

func (f fakeAWGList) List() ([]storage.AWGTunnel, error) { return f.list, f.err }

func TestProxyOccupancyExcludesSelfRecordAndLinkedTunnels(t *testing.T) {
	recs := fakeRecords{recs: []instancestore.Record{
		{ID: "c1", Kind: instancestore.KindWdttClient,
			WdttClient: &roles.WdttClientConfig{Listen: "127.0.0.1:9001"}},
		{ID: "c2", Kind: instancestore.KindFreeTurnClient,
			FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9002"}},
		{ID: "s1", Kind: instancestore.KindWdttServer,
			WdttServer: &roles.WdttServerConfig{Listen: "0.0.0.0:56000", WgPort: 51820}},
	}}
	tuns := fakeAWGList{list: []storage.AWGTunnel{
		// Свой связанный туннель: его endpoint ещё на прежнем порту (дрейф) —
		// исключается по связи, а не по совпадению с listen.
		{ID: "awgm1", WdttClientID: "c1", Peer: storage.AWGPeer{Endpoint: "127.0.0.1:9005"}},
		{ID: "awgm2", WdttClientID: "c9", Peer: storage.AWGPeer{Endpoint: "127.0.0.1:9003"}},
		{ID: "awgm3", Peer: storage.AWGPeer{Endpoint: "1.2.3.4:9004"}},
	}}
	occ := newProxyOccupancy(recs, tuns, instancestore.KindWdttClient, "c1")

	used, err := occ.OccupiedLocalListenPorts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if used[9001] {
		t.Fatal("собственная запись занимает свой же порт")
	}
	if used[9005] {
		t.Fatal("связанный туннель самого инстанса занимает порт")
	}
	for _, port := range []int{9002, 9003, 56000, 51820} {
		if !used[port] {
			t.Fatalf("порт %d не учтён: %v", port, used)
		}
	}
	if used[9004] {
		t.Fatal("нелокальный endpoint учтён как занятый порт")
	}
}

// ------------------------------------------------ п. 4: добивание поколения

type killCall struct {
	pid int
	sig syscall.Signal
}

func interceptKill(t *testing.T) *[]killCall {
	t.Helper()
	var calls []killCall
	prev := killPID
	killPID = func(pid int, sig syscall.Signal) error {
		calls = append(calls, killCall{pid: pid, sig: sig})
		return nil
	}
	t.Cleanup(func() { killPID = prev })
	return &calls
}

func TestKillOldGenerationSparesForeignAndDeadPIDs(t *testing.T) {
	calls := interceptKill(t)

	// Живой, но ЧУЖОЙ процесс: pid-файл на флешке переживает ребут, и PID
	// достаётся постороннему (B3).
	killOldGeneration(os.Getpid(), []string{"/opt/bin/nonexistent"})
	if len(*calls) != 0 {
		t.Fatalf("чужой живой процесс получил сигнал: %+v", *calls)
	}

	cmd := exec.Command("/bin/true")
	if err := cmd.Start(); err != nil {
		t.Skipf("/bin/true недоступен: %v", err)
	}
	dead := cmd.Process.Pid
	_ = cmd.Wait()
	killOldGeneration(dead, []string{"/opt/bin/" + filepath.Base(os.Args[0])})
	if len(*calls) != 0 {
		t.Fatalf("мёртвый pid получил сигнал: %+v", *calls)
	}
}

func TestKillOldGenerationKillsOwnBinary(t *testing.T) {
	calls := interceptKill(t)

	pid := os.Getpid()
	killOldGeneration(pid, []string{"/opt/bin/" + filepath.Base(os.Args[0])})
	want := []killCall{{pid: -pid, sig: syscall.SIGKILL}, {pid: pid, sig: syscall.SIGKILL}}
	if len(*calls) != len(want) {
		t.Fatalf("вызовы kill: %+v", *calls)
	}
	for i, c := range *calls {
		if c != want[i] {
			t.Fatalf("kill[%d] = %+v, ждали %+v", i, c, want[i])
		}
	}
}

// --------------------------------------------------- п. 5: уборка наследия

// Формы правил выписаны из ещё живого кода старого движка: entware_nat_linux.go
// (FORWARD accept, DNAT/INPUT :53, MASQUERADE с меткой AWGM_WDTT),
// server_raw_policy_linux.go (mangle-пара и метка AWGM-WDTT-POLICY),
// entware_lan_linux.go (пары FORWARD peer↔LAN с меткой AWGM_WDTT_LAN).
func legacyTables() map[string]string {
	return map[string]string{
		"-S FORWARD": strings.Join([]string{
			"-P FORWARD ACCEPT",
			"-A FORWARD -i wdtt0 -j ACCEPT",
			"-A FORWARD -o wdtt0 -j ACCEPT",
			"-A FORWARD -i wdttraw0 -m comment --comment AWGM_WDTT -j ACCEPT",
			"-A FORWARD -s 10.66.0.0/16 -d 192.168.1.0/24 -m comment --comment AWGM_WDTT_LAN -j ACCEPT",
			"-A FORWARD -s 192.168.1.0/24 -d 10.66.0.0/16 -m comment --comment AWGM_WDTT_LAN -j ACCEPT",
			"-A FORWARD -i opkgtun17 -j ACCEPT",
			// Помеченная форма ≤2.16.x на ЖИВОМ объявленном интерфейсе: пока
			// сервер есть, метка AWGM_WDTT принадлежит ему.
			"-A FORWARD -i opkgtun17 -m comment --comment AWGM_WDTT -j ACCEPT",
			"-A FORWARD -i br0 -j ACCEPT",
		}, "\n"),
		"-S INPUT": strings.Join([]string{
			"-A INPUT -i wdttraw0 -p udp --dport 53 -j ACCEPT",
			"-A INPUT -i opkgtun17 -p udp --dport 53 -j ACCEPT",
		}, "\n"),
		"-t nat -S PREROUTING": strings.Join([]string{
			"-A PREROUTING -i wdttraw0 -p udp --dport 53 -j DNAT --to-destination 10.70.0.1:53",
			"-A PREROUTING -i opkgtun18 -p tcp --dport 53 -j DNAT --to-destination 10.70.0.1:53",
		}, "\n"),
		"-t nat -S POSTROUTING": strings.Join([]string{
			"-A POSTROUTING -s 10.66.0.0/16 ! -o wdtt0 -m comment --comment AWGM_WDTT -j MASQUERADE",
		}, "\n"),
		"-t mangle -S PREROUTING": strings.Join([]string{
			"-A PREROUTING -i wdttraw0 -j CONNMARK --save-mark --nfmask 0xffffffff --ctmask 0xffffffff",
			"-A PREROUTING -i wdttraw0 -j MARK --set-xmark 0xffffd00/0xffffffff",
			"-A PREROUTING -i eth3 -m comment --comment AWGM-WDTT-POLICY -j MARK --set-xmark 0x1/0xffffffff",
			"-A PREROUTING -i br0 -j MARK --set-xmark 0x2/0xffffffff",
		}, "\n"),
	}
}

func TestLegacyCleanupWithLiveServer(t *testing.T) {
	ipt := &fakeIPT{out: legacyTables()}
	cmds := &fakeOpkgDeleter{}
	ifaces := fakeIfaces{list: []ndms.Interface{
		{ID: "OpkgTun17", Description: "Германия wdtt"},
		{ID: "OpkgTun18", Description: "AWGM proxy wdtt-server default"},
		{ID: "OpkgTun19", Description: "Германия wdtt релей"},
		{ID: "OpkgTun20", Description: "Италия wdtt"},
		{ID: "Wireguard1", Description: "чужой wdtt"},
	}}
	declared := map[string]bool{"OpkgTun17": true, "OpkgTun18": true}

	if err := legacyCleanup(context.Background(), ipt, cmds, ifaces, declared,
		[]string{"wdtt0", "wdttraw0", "opkgtun17"}); err != nil {
		t.Fatal(err)
	}

	// (а) непомеченные правила прежних kernel-имён.
	for _, want := range [][]string{
		{"-D", "FORWARD", "-i", "wdtt0", "-j", "ACCEPT"},
		{"-D", "FORWARD", "-o", "wdtt0", "-j", "ACCEPT"},
		{"-D", "FORWARD", "-i", "wdttraw0", "-m", "comment", "--comment", "AWGM_WDTT", "-j", "ACCEPT"},
		{"-D", "INPUT", "-i", "wdttraw0", "-p", "udp", "--dport", "53", "-j", "ACCEPT"},
		{"-t", "nat", "-D", "PREROUTING", "-i", "wdttraw0", "-p", "udp", "--dport", "53",
			"-j", "DNAT", "--to-destination", "10.70.0.1:53"},
		{"-t", "mangle", "-D", "PREROUTING", "-i", "wdttraw0", "-j", "CONNMARK",
			"--save-mark", "--nfmask", "0xffffffff", "--ctmask", "0xffffffff"},
		{"-t", "mangle", "-D", "PREROUTING", "-i", "wdttraw0", "-j", "MARK",
			"--set-xmark", "0xffffd00/0xffffffff"},
	} {
		if !ipt.ran(want...) {
			t.Fatalf("не снесено: %v", want)
		}
	}

	// (б) помеченные формы: пара LAN без -i/-o и legacy-метка политики.
	for _, want := range [][]string{
		{"-D", "FORWARD", "-s", "10.66.0.0/16", "-d", "192.168.1.0/24",
			"-m", "comment", "--comment", "AWGM_WDTT_LAN", "-j", "ACCEPT"},
		{"-D", "FORWARD", "-s", "192.168.1.0/24", "-d", "10.66.0.0/16",
			"-m", "comment", "--comment", "AWGM_WDTT_LAN", "-j", "ACCEPT"},
		{"-t", "mangle", "-D", "PREROUTING", "-i", "eth3", "-m", "comment",
			"--comment", "AWGM-WDTT-POLICY", "-j", "MARK", "--set-xmark", "0x1/0xffffffff"},
	} {
		if !ipt.ran(want...) {
			t.Fatalf("не снесено: %v", want)
		}
	}

	// Цепочка clamp с её jump — всегда.
	if !ipt.ran("-t", "mangle", "-D", "FORWARD", "-j", netres.MSSChain) ||
		!ipt.ran("-t", "mangle", "-F", netres.MSSChain) ||
		!ipt.ran("-t", "mangle", "-X", netres.MSSChain) {
		t.Fatalf("цепочка %s не снята: %v", netres.MSSChain, ipt.runs)
	}

	// Живая метка при живом сервере не трогается: его собственный RuleSet
	// приведёт эти правила сам.
	if ipt.ranWith("POSTROUTING -s 10.66.0.0/16") {
		t.Fatalf("голая метка AWGM_WDTT снесена при живом сервере: %v", ipt.runs)
	}

	// Объявленные интерфейсы щадим — и в правилах, и в NDMS.
	if ipt.ranWith("opkgtun17") || ipt.ranWith("opkgtun18") {
		t.Fatalf("тронуты правила объявленного интерфейса: %v", ipt.runs)
	}
	if ipt.ranWith("br0") {
		t.Fatalf("тронуто чужое правило: %v", ipt.runs)
	}

	// (в) legacy-описания: суффикс " wdtt" в СЕРЕДИНЕ описания не считается,
	// объявленный интерфейс не сносится, чужой тип интерфейса не наш.
	if len(cmds.deleted) != 1 || cmds.deleted[0] != "OpkgTun20" {
		t.Fatalf("снесены NDMS-интерфейсы: %v", cmds.deleted)
	}
}

// Голая метка AWGM_WDTT — единственная форма, чей снос условен: она ЖИВАЯ
// (её ставит и новый движок), поэтому сносится только когда после посева не
// осталось ни одного wdtt-сервера.
func TestLegacyCleanupFlushesLiveCommentWithoutServers(t *testing.T) {
	ipt := &fakeIPT{out: legacyTables()}
	cmds := &fakeOpkgDeleter{}

	if err := legacyCleanup(context.Background(), ipt, cmds, fakeIfaces{},
		map[string]bool{}, nil); err != nil {
		t.Fatal(err)
	}

	if !ipt.ran("-t", "nat", "-D", "POSTROUTING", "-s", "10.66.0.0/16", "!", "-o", "wdtt0",
		"-m", "comment", "--comment", "AWGM_WDTT", "-j", "MASQUERADE") {
		t.Fatalf("голая метка не снесена при пустом желаемом: %v", ipt.runs)
	}
	if !ipt.ran("-D", "FORWARD", "-i", "wdttraw0", "-m", "comment", "--comment",
		"AWGM_WDTT", "-j", "ACCEPT") {
		t.Fatalf("помеченный FORWARD не снесён при пустом желаемом: %v", ipt.runs)
	}
}

// ------------------------------------------------------------ п. 4: PostSeed

func mirrorWithStaleAddress(t *testing.T) (*exitreg.StoreMirror, *storage.AWGTunnelStore) {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Save(&storage.AWGTunnel{
		ID: "wdttraw-c1", Name: "raw", Backend: "wdtt-raw",
		Interface: storage.AWGInterface{Address: "10.70.0.7/32"},
	}); err != nil {
		t.Fatal(err)
	}
	return exitreg.NewStoreMirror(store, nil), store
}

// Обнуление адресов — на КАЖДОМ бооте: одноразовый вызов с проглоченной
// ошибкой делал бы потерю вечной. Добивание и уборка — только при первом посеве.
func TestProxyPostSeedZeroesAddressesEveryBoot(t *testing.T) {
	mirror, store := mirrorWithStaleAddress(t)
	calls := interceptKill(t)
	ipt := &fakeIPT{out: legacyTables()}
	post := proxyPostSeed(mirror, ipt, proxyNDMSCommands{}, fakeIfaces{},
		[]string{"/opt/bin/" + filepath.Base(os.Args[0])})

	err := post(context.Background(), instancestore.SeedResult{
		SeededNow:  false,
		OldGenPIDs: []int{os.Getpid()},
	}, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.Get("wdttraw-c1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Interface.Address != "" {
		t.Fatalf("адрес зеркала не обнулён: %q", got.Interface.Address)
	}
	if len(*calls) != 0 {
		t.Fatalf("повторный боот добивал старое поколение: %+v", *calls)
	}
	if len(ipt.runs) != 0 {
		t.Fatalf("повторный боот убирал наследие: %v", ipt.runs)
	}
}

func TestProxyPostSeedRunsOneShotStepsOnFirstSeed(t *testing.T) {
	mirror, _ := mirrorWithStaleAddress(t)
	calls := interceptKill(t)
	ipt := &fakeIPT{out: legacyTables()}
	post := proxyPostSeed(mirror, ipt, proxyNDMSCommands{}, fakeIfaces{},
		[]string{"/opt/bin/" + filepath.Base(os.Args[0])})

	err := post(context.Background(), instancestore.SeedResult{
		SeededNow:          true,
		OldGenPIDs:         []int{os.Getpid()},
		LegacyKernelIfaces: []string{"wdtt0"},
	}, map[string]bool{"OpkgTun17": true})
	if err != nil {
		t.Fatal(err)
	}

	if len(*calls) != 2 {
		t.Fatalf("старое поколение не добито: %+v", *calls)
	}
	if !ipt.ran("-D", "FORWARD", "-i", "wdtt0", "-j", "ACCEPT") {
		t.Fatalf("наследие не убрано: %v", ipt.runs)
	}
}

// -------------------------------------------------- п. 6а: ingress-refs/access

type fakeSettings struct {
	refs      []string
	saved     [][]string
	reconcile int
}

func (f *fakeSettings) Load() (*storage.Settings, error) {
	s := &storage.Settings{}
	s.SingboxRouter.IngressInterfaces = append([]string(nil), f.refs...)
	return s, nil
}

func (f *fakeSettings) Save(s *storage.Settings) error {
	f.saved = append(f.saved, append([]string(nil), s.SingboxRouter.IngressInterfaces...))
	f.refs = append([]string(nil), s.SingboxRouter.IngressInterfaces...)
	return nil
}

func (f *fakeSettings) Reconcile(context.Context) error {
	f.reconcile++
	return nil
}

func TestProxyIngressEnsurerDropsStaleAndSaves(t *testing.T) {
	st := &fakeSettings{refs: []string{"iface:opkgtun17", "iface:wdttraw0", "managed:Wireguard3"}}
	e := proxyIngressEnsurer{settings: st, router: st}

	if err := e.EnsureWdttServerIngressRefs(context.Background(), "opkgtun17", "opkgtun18"); err != nil {
		t.Fatal(err)
	}
	if len(st.saved) != 1 {
		t.Fatalf("сохранений: %d", len(st.saved))
	}
	saved := strings.Join(st.saved[0], " ")
	if !strings.Contains(saved, "iface:opkgtun18") {
		t.Fatalf("новая raw-ссылка не сохранена: %v", st.saved[0])
	}
	if strings.Contains(saved, "iface:wdttraw0") {
		t.Fatalf("протухшая ссылка сохранена: %v", st.saved[0])
	}
	if !strings.Contains(saved, "managed:Wireguard3") {
		t.Fatalf("чужая ссылка потеряна: %v", st.saved[0])
	}
	if st.reconcile != 1 {
		t.Fatalf("реконсиляций роутера: %d", st.reconcile)
	}

	// Второй проход ничего не меняет — ни записи, ни реконсиляции.
	if err := e.EnsureWdttServerIngressRefs(context.Background(), "opkgtun17", "opkgtun18"); err != nil {
		t.Fatal(err)
	}
	if len(st.saved) != 1 || st.reconcile != 1 {
		t.Fatalf("повторный проход тронул настройки: saved=%d reconcile=%d", len(st.saved), st.reconcile)
	}
}

type fakeNATAccess struct {
	nat    []string
	policy []string
	lan    []string
	wan    string
}

func (f *fakeNATAccess) ApplyNATModeToInterface(_ context.Context, iface, mode, prevWAN string) (string, error) {
	f.nat = append(f.nat, strings.Join([]string{iface, mode, prevWAN}, "|"))
	return f.wan, nil
}

func (f *fakeNATAccess) ApplyPolicyToInterface(_ context.Context, iface, policy string) error {
	f.policy = append(f.policy, iface+"|"+policy)
	return nil
}

func (f *fakeNATAccess) ApplyLANSegmentsToInterface(_ context.Context, iface, addr, mask string, segments []string) error {
	f.lan = append(f.lan, strings.Join(append([]string{iface, addr, mask}, segments...), "|"))
	return nil
}

type fakePermit struct{ ifaces []string }

func (f *fakePermit) SetPermitAllACL(_ context.Context, name string) error {
	f.ifaces = append(f.ifaces, name)
	return nil
}

func TestProxyAccessApplierPassesArgsAndWAN(t *testing.T) {
	svc := &fakeNATAccess{wan: "ISP2"}
	permit := &fakePermit{}
	a := proxyAccessApplier{svc: svc, ifaces: permit}

	wan, err := a.ApplyNATModeToInterface(context.Background(), "OpkgTun17", "internet-only", "ISP1")
	if err != nil {
		t.Fatal(err)
	}
	if wan != "ISP2" {
		t.Fatalf("WAN не проброшен: %q", wan)
	}
	if len(svc.nat) != 1 || svc.nat[0] != "OpkgTun17|internet-only|ISP1" {
		t.Fatalf("аргументы NAT: %v", svc.nat)
	}

	if err := a.ApplyPolicyToInterface(context.Background(), "OpkgTun17", "Policy0"); err != nil {
		t.Fatal(err)
	}
	if len(svc.policy) != 1 || svc.policy[0] != "OpkgTun17|Policy0" {
		t.Fatalf("аргументы policy: %v", svc.policy)
	}

	if err := a.ApplyLANSegmentsToInterface(context.Background(), "OpkgTun17",
		"10.66.0.1", "255.255.0.0", []string{"Home"}); err != nil {
		t.Fatal(err)
	}
	if len(svc.lan) != 1 || svc.lan[0] != "OpkgTun17|10.66.0.1|255.255.0.0|Home" {
		t.Fatalf("аргументы LAN: %v", svc.lan)
	}

	if err := a.EnsureInterfaceFirewallPermit(context.Background(), "OpkgTun17"); err != nil {
		t.Fatal(err)
	}
	if len(permit.ifaces) != 1 || permit.ifaces[0] != "OpkgTun17" {
		t.Fatalf("аргументы permit: %v", permit.ifaces)
	}
}

// Переехавшие из internal/wdtt тесты замкнутого набора ingress-refs.
func TestEnsureWdttIngressRefs(t *testing.T) {
	refs := []string{"iface:opkgtun17", "managed:Wireguard3"}
	next, changed := EnsureWdttIngressRefs(refs, "opkgtun17", "")
	if !changed {
		t.Fatal("expected change when raw ref missing")
	}
	if !containsRef(next, "iface:wdttraw0") {
		t.Fatalf("next = %v", next)
	}
	if !containsRef(next, "managed:Wireguard3") {
		t.Fatalf("must preserve unrelated refs: %v", next)
	}

	next, changed = EnsureWdttIngressRefs(next, "opkgtun17", "")
	if changed {
		t.Fatalf("already paired, changed again: %v", next)
	}
}

func TestEnsureWdttIngressRefsDropsLegacyRawRef(t *testing.T) {
	refs := []string{"iface:opkgtun17", "iface:wdttraw0", "managed:Wireguard3"}
	next, changed := EnsureWdttIngressRefs(refs, "opkgtun17", "opkgtun18")
	if !changed {
		t.Fatalf("переезд не отражён: %v", next)
	}
	if containsRef(next, "iface:wdttraw0") {
		t.Fatalf("протухшая ссылка осталась: %v", next)
	}
	if !containsRef(next, "iface:opkgtun18") {
		t.Fatalf("новая raw-ссылка не добавлена: %v", next)
	}
	if !containsRef(next, "managed:Wireguard3") {
		t.Fatalf("чужая ссылка потеряна: %v", next)
	}

	next, changed = EnsureWdttIngressRefs(next, "opkgtun17", "opkgtun18")
	if changed {
		t.Fatalf("повторный проход снова что-то поменял: %v", next)
	}
}

func containsRef(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
