package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/childproc"
	"github.com/hoaxisr/awg-manager/internal/listenfirewall"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/exitreg"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/linkres"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/netres"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// fakeIPT — модель iptables: листинги по ключу аргументов и ЖУРНАЛ команд.
// Журнал, а не счётчик: уборка отличается от порчи только тем, С ЧЕМ её
// позвали, поэтому все сверки ниже — по составу аргументов.
type fakeIPT struct {
	out    map[string]string
	outErr error
	runs   [][]string
}

func (f *fakeIPT) Run(_ context.Context, args ...string) error {
	f.runs = append(f.runs, append([]string(nil), args...))
	return nil
}

func (f *fakeIPT) Output(_ context.Context, args ...string) (string, error) {
	if f.outErr != nil {
		return "", f.outErr
	}
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

// Ветка поля связи исполняется целиком: freeturn-клиент отбирает туннели по
// FreeTurnClientID. Чтение чужого поля не даёт ошибки — оно даёт ПУСТОЙ список,
// из которого Observe считает нулевой дрейф, а Plan не порождает шага: endpoint
// не синхронизируется никогда и молча.
func TestProxyEndpointSyncListsByLinkedField(t *testing.T) {
	store := linkedStore(t)
	for field, want := range map[api.LinkedField]string{
		api.LinkedWdtt:     "awgm1",
		api.LinkedFreeTurn: "awgm2",
	} {
		list, err := newProxyEndpointSync(store, nil, field, nil).List(context.Background(), "c1")
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].ID != want {
			t.Fatalf("поле %d: список %+v, ждали %s", field, list, want)
		}
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

// selfProc — отпечаток своего процесса: номер плюс время старта из /proc.
func selfProc(t *testing.T) instancestore.OldGenProc {
	t.Helper()
	start, ok := childproc.StartTime(os.Getpid())
	if !ok {
		t.Skip("/proc недоступен")
	}
	return instancestore.OldGenProc{PID: os.Getpid(), StartTime: start}
}

func TestKillOldGenerationSparesForeignAndDeadPIDs(t *testing.T) {
	calls := interceptKill(t)

	// Живой, но ЧУЖОЙ процесс: pid-файл на флешке переживает ребут, и PID
	// достаётся постороннему (B3).
	foreign := selfProc(t)
	killOldGeneration(foreign, []string{"/opt/bin/nonexistent"})
	if len(*calls) != 0 {
		t.Fatalf("чужой живой процесс получил сигнал: %+v", *calls)
	}

	cmd := exec.Command("/bin/true")
	if err := cmd.Start(); err != nil {
		t.Skipf("/bin/true недоступен: %v", err)
	}
	dead := cmd.Process.Pid
	_ = cmd.Wait()
	killOldGeneration(instancestore.OldGenProc{PID: dead, StartTime: 1},
		[]string{"/opt/bin/" + filepath.Base(os.Args[0])})
	if len(*calls) != 0 {
		t.Fatalf("мёртвый pid получил сигнал: %+v", *calls)
	}
}

// C1: номер, переиспользованный системой, отсекается отпечатком. Имя бинаря
// тут не спасает — у старого и нового поколения он ОДИН И ТОТ ЖЕ, и без
// сверки времени старта повторный проход уборки убивал бы усыновлённый
// процесс нового мира.
func TestKillOldGenerationSparesReusedPID(t *testing.T) {
	calls := interceptKill(t)
	own := selfProc(t)

	reused := own
	reused.StartTime++ // тот же номер, другой процесс
	killOldGeneration(reused, []string{"/opt/bin/" + filepath.Base(os.Args[0])})
	if len(*calls) != 0 {
		t.Fatalf("переиспользованный номер получил сигнал: %+v", *calls)
	}

	// Отпечатка нет вовсе (на посеве процесс был уже мёртв): добивать нечего,
	// а номер к этому моменту мог достаться кому угодно.
	noPrint := own
	noPrint.StartTime = 0
	killOldGeneration(noPrint, []string{"/opt/bin/" + filepath.Base(os.Args[0])})
	if len(*calls) != 0 {
		t.Fatalf("номер без отпечатка получил сигнал: %+v", *calls)
	}
}

func TestKillOldGenerationKillsOwnBinary(t *testing.T) {
	calls := interceptKill(t)

	pid := os.Getpid()
	killOldGeneration(selfProc(t), []string{"/opt/bin/" + filepath.Base(os.Args[0])})
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
			// Помеченная форма ≤2.16.x на ЖИВОМ объявленном интерфейсе:
			// FORWARD-правил с меткой новый мир не строит, значит это
			// наследие — и снять его больше некому.
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

	// (б) помеченные формы — БЕЗУСЛОВНО, живой сервер тут ни при чём: пара LAN
	// без -i/-o, legacy-метка политики, помеченный FORWARD на живом
	// интерфейсе (такую форму новый мир не строит вовсе) и помеченный
	// MASQUERADE (его живой сервер пересоберёт первым же прогоном).
	for _, want := range [][]string{
		{"-D", "FORWARD", "-s", "10.66.0.0/16", "-d", "192.168.1.0/24",
			"-m", "comment", "--comment", "AWGM_WDTT_LAN", "-j", "ACCEPT"},
		{"-D", "FORWARD", "-s", "192.168.1.0/24", "-d", "10.66.0.0/16",
			"-m", "comment", "--comment", "AWGM_WDTT_LAN", "-j", "ACCEPT"},
		{"-t", "mangle", "-D", "PREROUTING", "-i", "eth3", "-m", "comment",
			"--comment", "AWGM-WDTT-POLICY", "-j", "MARK", "--set-xmark", "0x1/0xffffffff"},
		{"-D", "FORWARD", "-i", "opkgtun17", "-m", "comment", "--comment",
			"AWGM_WDTT", "-j", "ACCEPT"},
		{"-t", "nat", "-D", "POSTROUTING", "-s", "10.66.0.0/16", "!", "-o", "wdtt0",
			"-m", "comment", "--comment", "AWGM_WDTT", "-j", "MASQUERADE"},
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

	// Объявленные интерфейсы щадим — но только в НЕПОМЕЧЕННЫХ формах: там
	// имя интерфейса и есть признак владения, и правило принадлежит живой
	// роли.
	if ipt.ran("-D", "FORWARD", "-i", "opkgtun17", "-j", "ACCEPT") {
		t.Fatalf("снесено непомеченное правило объявленного интерфейса: %v", ipt.runs)
	}
	if ipt.ranWith("opkgtun18") {
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

// Тот же снос помеченных форм, когда сервера не осталось вовсе: здесь их не
// приведёт уже никто, и уборка — последний шанс.
func TestLegacyCleanupFlushesMarkedFormsWithoutServers(t *testing.T) {
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
	cleared := 0
	post := proxyPostSeed(mirror, ipt, proxyNDMSCommands{}, fakeIfaces{},
		[]string{"/opt/bin/" + filepath.Base(os.Args[0])},
		func() error { cleared++; return nil })

	err := post(context.Background(), instancestore.SeedResult{
		SeededNow:   false,
		OldGenProcs: []instancestore.OldGenProc{selfProc(t)},
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
	if cleared != 0 {
		t.Fatal("отметка уборки снята без самой уборки")
	}
}

func TestProxyPostSeedRunsOneShotStepsOnFirstSeed(t *testing.T) {
	mirror, _ := mirrorWithStaleAddress(t)
	calls := interceptKill(t)
	ipt := &fakeIPT{out: legacyTables()}
	cmds := &fakeOpkgDeleter{}
	ifaces := fakeIfaces{list: []ndms.Interface{{ID: "OpkgTun17", Description: "Германия wdtt"}}}
	cleared := 0
	post := proxyPostSeed(mirror, ipt, cmds, ifaces,
		[]string{"/opt/bin/" + filepath.Base(os.Args[0])},
		func() error { cleared++; return nil })

	err := post(context.Background(), instancestore.SeedResult{
		SeededNow:          true,
		OldGenProcs:        []instancestore.OldGenProc{selfProc(t)},
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
	// Ведомость объявленных обязана ДОЕХАТЬ до уборки: подмена её на пустую
	// снесла бы правила живого интерфейса и удалила бы живой OpkgTun. Цена
	// односторонняя и одноразовая, поэтому сверяется сам стык.
	if ipt.ran("-D", "FORWARD", "-i", "opkgtun17", "-j", "ACCEPT") {
		t.Fatalf("ведомость объявленных не доехала: снесено непомеченное правило opkgtun17: %v", ipt.runs)
	}
	if len(cmds.deleted) != 0 {
		t.Fatalf("ведомость объявленных не доехала: снесены NDMS-интерфейсы %v", cmds.deleted)
	}
	if cleared != 1 {
		t.Fatalf("отметка уборки снята %d раз(а)", cleared)
	}
}

// Амендмент A3: отказ уборочных шагов — транзиентный (занятая блокировка
// iptables при рерайте таблиц роутером, недоступная команда). Отметка на
// диске висит, пока проход не удался, и следующий боот гоняет шаги снова —
// с ТЕМ ЖЕ списком прежних kernel-имён и со свежими pid'ами.
func TestProxyPostSeedRepeatsCleanupWhilePending(t *testing.T) {
	mirror, _ := mirrorWithStaleAddress(t)
	calls := interceptKill(t)
	ipt := &fakeIPT{out: legacyTables()}
	cmds := &fakeOpkgDeleter{}
	cleared := 0
	post := proxyPostSeed(mirror, ipt, cmds, fakeIfaces{},
		[]string{"/opt/bin/" + filepath.Base(os.Args[0])},
		func() error { cleared++; return nil })

	err := post(context.Background(), instancestore.SeedResult{
		SeededNow:          false,
		CleanupPending:     true,
		OldGenProcs:        []instancestore.OldGenProc{selfProc(t)},
		LegacyKernelIfaces: []string{"wdtt0"},
	}, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 {
		t.Fatalf("старое поколение не добито на повторе: %+v", *calls)
	}
	if !ipt.ran("-D", "FORWARD", "-i", "wdtt0", "-j", "ACCEPT") {
		t.Fatalf("наследие не убрано на повторе: %v", ipt.runs)
	}
	if cleared != 1 {
		t.Fatalf("отметка уборки снята %d раз(а)", cleared)
	}
}

// Отметка снимается ТОЛЬКО после успешного прохода: иначе транзиентный отказ
// съедал бы шанс ровно так же, как до амендмента.
func TestProxyPostSeedKeepsPendingOnCleanupFailure(t *testing.T) {
	mirror, _ := mirrorWithStaleAddress(t)
	interceptKill(t)
	ipt := &fakeIPT{outErr: errors.New("iptables занят")}
	cleared := 0
	post := proxyPostSeed(mirror, ipt, &fakeOpkgDeleter{}, fakeIfaces{},
		[]string{"/opt/bin/" + filepath.Base(os.Args[0])},
		func() error { cleared++; return nil })

	err := post(context.Background(), instancestore.SeedResult{
		SeededNow:          true,
		LegacyKernelIfaces: []string{"wdtt0"},
	}, map[string]bool{})
	if err == nil {
		t.Fatal("отказ уборки обязан быть виден вызывающему")
	}
	if cleared != 0 {
		t.Fatal("отметка снята при неудавшейся уборке")
	}
}

// I1: netfilter.d-хук старого движка — ЕДИНСТВЕННЫЙ артефакт, переживающий и
// перезапись таблиц ndm, и перезагрузку. При живом сервере его перепишет
// netres.Hook, а когда серверов не осталось — ресурса Hook нет вовсе, и файл
// остался бы навсегда (снять его после задачи 17 будет нечем).
func TestLegacyCleanupRemovesHookOnlyWithoutServers(t *testing.T) {
	newHook := func(t *testing.T) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "61-awgm-wdtt-forward.sh")
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		prev := legacyHookPath
		legacyHookPath = path
		t.Cleanup(func() { legacyHookPath = prev })
		return path
	}

	path := newHook(t)
	if err := legacyCleanup(context.Background(), &fakeIPT{out: legacyTables()},
		&fakeOpkgDeleter{}, fakeIfaces{}, map[string]bool{"OpkgTun17": true},
		[]string{"wdtt0", "wdttraw0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("хук снят при живом сервере: %v", err)
	}

	path = newHook(t)
	if err := legacyCleanup(context.Background(), &fakeIPT{out: legacyTables()},
		&fakeOpkgDeleter{}, fakeIfaces{}, map[string]bool{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("хук не снят при отсутствии серверов: %v", err)
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

// ------------------------------------------ амендмент A1: общая ведомость портов

// fakeListenFW — модель listenfirewall: живые правила и ЖУРНАЛ желаемых
// составов. Сверяется именно состав: весь смысл ведомости в том, КАКОЕ
// объединение уехало в Reconcile и КАКОЙ список вернул Managed.
type fakeListenFW struct {
	live    []listenfirewall.PortSpec
	applied [][]listenfirewall.PortSpec
	listErr error
}

func (f *fakeListenFW) list(context.Context) ([]listenfirewall.PortSpec, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]listenfirewall.PortSpec(nil), f.live...), nil
}

func (f *fakeListenFW) reconcile(_ context.Context, desired []listenfirewall.PortSpec) {
	f.applied = append(f.applied, append([]listenfirewall.PortSpec(nil), desired...))
	// Прод-Reconcile приводит живые правила к желаемому — модель делает то же,
	// иначе следующий Managed врал бы про состояние роутера.
	f.live = append([]listenfirewall.PortSpec(nil), desired...)
}

func (f *fakeListenFW) lastApplied() []string {
	if len(f.applied) == 0 {
		return nil
	}
	out := make([]string, 0, len(f.applied[len(f.applied)-1]))
	for _, s := range f.applied[len(f.applied)-1] {
		out = append(out, s.Proto+"/"+strconv.Itoa(s.Port))
	}
	sort.Strings(out)
	return out
}

// bookOver — ведомость на модели listenfirewall с ПЕРЕХВАЧЕННЫМ будильником
// окна: возвращённый fire() играет истечение окна. Настоящий таймер в тесте
// негоден — ждать две минуты нельзя, а дать ему выстрелить по живому
// listenfirewall тем более.
func bookOver(t *testing.T, fw *fakeListenFW, keys ...string) (*proxyFWBook, func()) {
	t.Helper()
	var fire func()
	prev := proxyFWAfterFunc
	proxyFWAfterFunc = func(d time.Duration, f func()) *time.Timer {
		if d != proxyFWGrace {
			t.Fatalf("будильник заведён на %v, а не на окно ожидания", d)
		}
		fire = f
		return nil
	}
	b := newProxyFWBook(keys, func() bool { return true })
	b.armGrace()
	proxyFWAfterFunc = prev
	b.list = fw.list
	b.apply = fw.reconcile
	if fire == nil {
		t.Fatal("ведомость не завела будильник окна")
	}
	return b, fire
}

func spec(port int, proto string) netres.PortSpec {
	return netres.PortSpec{Port: port, Proto: proto}
}

func liveSpecs(in ...netres.PortSpec) []listenfirewall.PortSpec {
	out := make([]listenfirewall.PortSpec, 0, len(in))
	for _, s := range in {
		out = append(out, listenfirewall.PortSpec{Port: s.Port, Proto: s.Proto})
	}
	return out
}

func managedNames(t *testing.T, fw netres.FW) []string {
	t.Helper()
	got, err := fw.Managed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(got))
	for _, s := range got {
		out = append(out, s.Proto+"/"+strconv.Itoa(s.Port))
	}
	sort.Strings(out)
	return out
}

func sameNames(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// Два серверных инстанса на общей ведомости: в listenfirewall уезжает
// ОБЪЕДИНЕНИЕ вкладов, и ни один не видит порты соседа лишними. Пер-инстансный
// адаптер здесь давал вечный цикл: каждый закрывал порты другого раз в 15 с.
func TestProxyFWBookUnionOfContributions(t *testing.T) {
	fw := &fakeListenFW{}
	book, _ := bookOver(t, fw, "wdtt-server:s1", "freeturn-server:s2")
	a := book.forInstance("wdtt-server:s1")
	b := book.forInstance("freeturn-server:s2")

	if err := a.Reconcile(context.Background(), []netres.PortSpec{spec(56000, "udp")}); err != nil {
		t.Fatal(err)
	}
	if err := b.Reconcile(context.Background(), []netres.PortSpec{spec(3478, "udp")}); err != nil {
		t.Fatal(err)
	}
	if got := fw.lastApplied(); !sameNames(got, []string{"udp/3478", "udp/56000"}) {
		t.Fatalf("желаемое listenfirewall = %v, ждали объединение вкладов", got)
	}

	// Ведомость каждого — живые правила МИНУС желаемое соседа.
	if got := managedNames(t, a); !sameNames(got, []string{"udp/56000"}) {
		t.Fatalf("ведомость A = %v", got)
	}
	if got := managedNames(t, b); !sameNames(got, []string{"udp/3478"}) {
		t.Fatalf("ведомость B = %v", got)
	}
}

// Выключение инстанса закрывает его порты штатно: пустой вклад уходит из
// объединения.
func TestProxyFWBookEmptyContributionClosesPorts(t *testing.T) {
	fw := &fakeListenFW{}
	book, _ := bookOver(t, fw, "wdtt-server:s1", "freeturn-server:s2")
	a := book.forInstance("wdtt-server:s1")
	b := book.forInstance("freeturn-server:s2")
	if err := a.Reconcile(context.Background(), []netres.PortSpec{spec(56000, "udp")}); err != nil {
		t.Fatal(err)
	}
	if err := b.Reconcile(context.Background(), []netres.PortSpec{spec(3478, "udp")}); err != nil {
		t.Fatal(err)
	}
	if err := b.Reconcile(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := fw.lastApplied(); !sameNames(got, []string{"udp/56000"}) {
		t.Fatalf("порт выключенного инстанса не закрыт: %v", got)
	}
	// Пустой вклад в карте не остаётся: иначе она копит выключенных и снятых,
	// а снятие уже выключенного инстанса переписывает хук впустую.
	before := len(fw.applied)
	if err := book.forget(context.Background(), "freeturn-server:s2"); err != nil {
		t.Fatal(err)
	}
	if len(fw.applied) != before {
		t.Fatalf("ведомость помнит выключенный инстанс: лишнее приведение %v", fw.lastApplied())
	}
}

// Мёртвое поколение: порт, которого не желает никто, виден лишним и уходит из
// объединения. Аддитивный Reconcile оставил бы его навсегда.
func TestProxyFWBookSweepsUnclaimedPort(t *testing.T) {
	fw := &fakeListenFW{}
	book, _ := bookOver(t, fw, "wdtt-server:s1", "freeturn-server:s2")
	a := book.forInstance("wdtt-server:s1")
	b := book.forInstance("freeturn-server:s2")
	if err := a.Reconcile(context.Background(), []netres.PortSpec{spec(56000, "udp")}); err != nil {
		t.Fatal(err)
	}
	if err := b.Reconcile(context.Background(), []netres.PortSpec{spec(3478, "udp")}); err != nil {
		t.Fatal(err)
	}
	// Правило прежнего поколения на роутере.
	fw.live = append(fw.live, listenfirewall.PortSpec{Port: 9999, Proto: "tcp"})

	if got := managedNames(t, a); !sameNames(got, []string{"tcp/9999", "udp/56000"}) {
		t.Fatalf("ничей порт обязан быть виден лишним: %v", got)
	}
	if err := a.Reconcile(context.Background(), []netres.PortSpec{spec(56000, "udp")}); err != nil {
		t.Fatal(err)
	}
	if got := fw.lastApplied(); !sameNames(got, []string{"udp/3478", "udp/56000"}) {
		t.Fatalf("ничей порт не вычищен либо снесён чужой: %v", got)
	}
}

// Гонка первого запуска: сосед ещё не отчитался, его порт уже открыт прошлым
// запуском демона. Ни ведомость, ни объединение не имеют права его потерять.
func TestProxyFWBookSparesPortsOfUnreportedInstance(t *testing.T) {
	fw := &fakeListenFW{live: liveSpecs(spec(3478, "udp"))}
	book, _ := bookOver(t, fw, "wdtt-server:s1", "freeturn-server:s2")
	a := book.forInstance("wdtt-server:s1")

	// Инстанс, который сам ещё не отчитался, лишних портов не видит вовсе —
	// иначе он снёс бы чужое, не зная, чьё оно.
	if got := managedNames(t, a); len(got) != 0 {
		t.Fatalf("до своего отчёта инстанс видит чужие порты: %v", got)
	}
	if err := a.Reconcile(context.Background(), []netres.PortSpec{spec(56000, "udp")}); err != nil {
		t.Fatal(err)
	}
	if got := fw.lastApplied(); !sameNames(got, []string{"udp/3478", "udp/56000"}) {
		t.Fatalf("порт неотчитавшегося соседа закрыт: %v", got)
	}
	// Отчитавшийся видит СВОИ живые порты (чтобы чинить пропажу), но не чужие.
	if got := managedNames(t, a); !sameNames(got, []string{"udp/56000"}) {
		t.Fatalf("ведомость отчитавшегося при неполном круге = %v", got)
	}
}

// Пока круг не полон, объединение считается от ЖИВЫХ правил, и отказ листинга
// делает состав неизвестным: применять нельзя — ресурс повторит.
func TestProxyFWBookFailsClosedWhenListingBrokenDuringGrace(t *testing.T) {
	fw := &fakeListenFW{listErr: errors.New("boom")}
	book, _ := bookOver(t, fw, "wdtt-server:s1", "freeturn-server:s2")
	a := book.forInstance("wdtt-server:s1")
	if err := a.Reconcile(context.Background(), []netres.PortSpec{spec(56000, "udp")}); err == nil {
		t.Fatal("отказ листинга обязан отменить приведение")
	}
	if len(fw.applied) != 0 {
		t.Fatalf("приведение состоялось на неизвестном составе: %v", fw.applied)
	}
}

// Инстанс, которому порты не нужны (выключенный сервер, снятый тумблер), не
// отчитается НИКОГДА: netres.InputPort зовёт Reconcile только когда есть что
// приводить. Ожидание закрыто окном: по его истечении ведомость перестаёт
// щадить ничьи порты.
func TestProxyFWBookGraceEndsWaitForSilentInstance(t *testing.T) {
	fw := &fakeListenFW{live: liveSpecs(spec(9999, "tcp"))}
	book, graceOver := bookOver(t, fw, "wdtt-server:s1", "freeturn-server:s2")
	a := book.forInstance("wdtt-server:s1")

	if err := a.Reconcile(context.Background(), []netres.PortSpec{spec(56000, "udp")}); err != nil {
		t.Fatal(err)
	}
	if got := fw.lastApplied(); !sameNames(got, []string{"tcp/9999", "udp/56000"}) {
		t.Fatalf("в окне ожидания ничей порт обязан быть сохранён: %v", got)
	}
	if got := managedNames(t, a); !sameNames(got, []string{"udp/56000"}) {
		t.Fatalf("в окне ожидания лишних быть не может: %v", got)
	}

	graceOver()
	if got := managedNames(t, a); !sameNames(got, []string{"udp/56000"}) {
		t.Fatalf("после окна ведомость = %v (ничьё уже вычищено проходом окна)", got)
	}
	if err := a.Reconcile(context.Background(), []netres.PortSpec{spec(56000, "udp")}); err != nil {
		t.Fatal(err)
	}
	if got := fw.lastApplied(); !sameNames(got, []string{"udp/56000"}) {
		t.Fatalf("после окна ничей порт обязан быть вычищен: %v", got)
	}
}

// B1: при ВСЕХ выключенных серверах ведомость никто не наблюдает —
// netres.InputPort на пустом желаемом не заводит будильник и не зовёт Apply.
// Порты мёртвого поколения ушли бы только до следующего включения сервера или
// перезапуска демона, поэтому окно закрывается собственным одноразовым
// проходом приведения.
func TestProxyFWBookSweepsUnclaimedWithoutLiveServers(t *testing.T) {
	fw := &fakeListenFW{live: liveSpecs(spec(9999, "tcp"), spec(56000, "udp"))}
	_, graceOver := bookOver(t, fw, "wdtt-server:s1", "freeturn-server:s2")

	graceOver()

	if len(fw.applied) != 1 {
		t.Fatalf("проход окна не состоялся: %v", fw.applied)
	}
	if got := fw.lastApplied(); len(got) != 0 {
		t.Fatalf("порты мёртвого поколения не вычищены: %v", got)
	}
}

// C3 (сильнейшая форма B1): серверных инстансов нет ВОВСЕ, а правила мёртвого
// поколения в INPUT есть. Наблюдать ведомость некому — ни одного ресурса
// input_port в системе не существует, — поэтому будильник окна обязан быть
// заведён и на пустом списке ключей: иначе правила живут до появления первого
// сервера.
func TestProxyFWBookSweepsWithoutAnyServerInstances(t *testing.T) {
	fw := &fakeListenFW{live: liveSpecs(spec(9999, "tcp"))}
	_, graceOver := bookOver(t, fw)

	graceOver()

	if len(fw.applied) != 1 {
		t.Fatalf("проход окна не состоялся: %v", fw.applied)
	}
	if got := fw.lastApplied(); len(got) != 0 {
		t.Fatalf("порты мёртвого поколения не вычищены: %v", got)
	}
}

// B2: нештатное снятие инстанса (таймаут ожидания teardown или отказ ресурса
// РАНЬШЕ input_port — он в серверной роли последний) оставляет вклад в
// ведомости. Фантомный вклад входит в каждое следующее объединение, то есть
// правило не просто не снимается, а активно восстанавливается соседом.
func TestProxyFWBookForgetDropsPhantomContribution(t *testing.T) {
	fw := &fakeListenFW{}
	book, _ := bookOver(t, fw, "wdtt-server:s1", "freeturn-server:s2")
	a := book.forInstance("wdtt-server:s1")
	b := book.forInstance("freeturn-server:s2")
	if err := a.Reconcile(context.Background(), []netres.PortSpec{spec(56000, "udp")}); err != nil {
		t.Fatal(err)
	}
	if err := b.Reconcile(context.Background(), []netres.PortSpec{spec(3478, "udp")}); err != nil {
		t.Fatal(err)
	}

	if err := book.forget(context.Background(), "freeturn-server:s2"); err != nil {
		t.Fatal(err)
	}
	if got := fw.lastApplied(); !sameNames(got, []string{"udp/56000"}) {
		t.Fatalf("вклад снятого инстанса не забыт: %v", got)
	}
	// И сосед его не воскрешает.
	if err := a.Reconcile(context.Background(), []netres.PortSpec{spec(56000, "udp")}); err != nil {
		t.Fatal(err)
	}
	if got := fw.lastApplied(); !sameNames(got, []string{"udp/56000"}) {
		t.Fatalf("фантомный вклад воскрешён соседним приведением: %v", got)
	}
}

// Снятие инстанса, который отчитаться не успел, закрывает круг ожидания —
// иначе ведомость ждала бы покойника всё окно.
func TestProxyFWBookForgetClosesPendingCircle(t *testing.T) {
	fw := &fakeListenFW{live: liveSpecs(spec(9999, "tcp"))}
	book, _ := bookOver(t, fw, "wdtt-server:s1", "freeturn-server:s2")
	a := book.forInstance("wdtt-server:s1")
	if err := a.Reconcile(context.Background(), []netres.PortSpec{spec(56000, "udp")}); err != nil {
		t.Fatal(err)
	}
	if err := book.forget(context.Background(), "freeturn-server:s2"); err != nil {
		t.Fatal(err)
	}
	if got := fw.lastApplied(); !sameNames(got, []string{"udp/56000"}) {
		t.Fatalf("круг не закрыт: ничьё осталось в объединении: %v", got)
	}
	// Чужой и уже забытый ключ приведения не порождают.
	before := len(fw.applied)
	if err := book.forget(context.Background(), "freeturn-server:s2"); err != nil {
		t.Fatal(err)
	}
	if len(fw.applied) != before {
		t.Fatal("повторное снятие переписало хук впустую")
	}
}

// stateSvc — api.TunnelService с моделью состояния. Журнал Start/Stop, а не
// счётчик: регресс живёт в том, КАКИЕ записи тронуты.
type stateSvc struct {
	api.TunnelService
	running map[string]bool
	asked   []string
	started []string
	stopped []string
}

// asked — ЖУРНАЛ идентификаторов, у которых спрашивали состояние. Нужен не
// для факта, а для цены: каждый такой вопрос при промахе кэша стоит запроса к
// роутеру, и записи вне жизненного цикла обязаны его не стоить.
func (s *stateSvc) GetState(_ context.Context, id string) tunnel.StateInfo {
	s.asked = append(s.asked, id)
	if s.running[id] {
		return tunnel.StateInfo{State: tunnel.StateRunning}
	}
	return tunnel.StateInfo{State: tunnel.StateStopped}
}

func (s *stateSvc) Start(_ context.Context, id string) error {
	s.started = append(s.started, id)
	s.running[id] = true
	return nil
}

func (s *stateSvc) Stop(_ context.Context, id string) error {
	s.stopped = append(s.stopped, id)
	s.running[id] = false
	return nil
}

// failingStop — падает ровно Stop: отказ обязан доехать до ресурса, иначе
// туннель остаётся поднятым, а ресурс рапортует успех.
type failingStop struct {
	api.TunnelService
}

func (failingStop) GetState(context.Context, string) tunnel.StateInfo {
	return tunnel.StateInfo{State: tunnel.StateRunning}
}

func (failingStop) Stop(context.Context, string) error { return errors.New("boom") }

func TestProxyEndpointSyncSetsTunnelState(t *testing.T) {
	store := linkedStore(t)
	pub := &fakePublisher{}
	svc := &stateSvc{running: map[string]bool{}}
	s := newProxyEndpointSync(store, svc, api.LinkedWdtt, pub)

	n, err := s.SetState(context.Background(), "c1", true)
	if err != nil || n != 1 {
		t.Fatalf("SetState(up) = (%d, %v)", n, err)
	}
	if len(svc.started) != 1 || svc.started[0] != "awgm1" {
		t.Fatalf("подняты: %v (freeturn-туннель обязан остаться)", svc.started)
	}
	if len(pub.events) == 0 || pub.events[0] != "resource:invalidated" {
		t.Fatalf("инвалидация не опубликована: %v", pub.events)
	}

	n, err = s.SetState(context.Background(), "c1", false)
	if err != nil || n != 1 {
		t.Fatalf("SetState(down) = (%d, %v)", n, err)
	}
	if len(svc.stopped) != 1 || svc.stopped[0] != "awgm1" {
		t.Fatalf("опущены: %v", svc.stopped)
	}
}

// Список несёт состояние и участие в жизненном цикле — по ним ресурс считает
// расхождение; без них выключение клиента не увидело бы поднятый туннель.
func TestProxyEndpointSyncListCarriesState(t *testing.T) {
	store := linkedStore(t)
	svc := &stateSvc{running: map[string]bool{"awgm1": true}}
	list, err := newProxyEndpointSync(store, svc, api.LinkedWdtt, nil).List(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].Running || !list[0].Lifecycle {
		t.Fatalf("список: %+v", list)
	}
}

func TestProxyEndpointSyncSetStateReportsFailed(t *testing.T) {
	store := linkedStore(t)
	s := newProxyEndpointSync(store, failingStop{}, api.LinkedWdtt, nil)

	n, err := s.SetState(context.Background(), "c1", false)
	if err == nil {
		t.Fatal("отказ остановки проглочен")
	}
	if n != 0 {
		t.Fatalf("changed = %d", n)
	}
	if !strings.Contains(err.Error(), "awgm1") {
		t.Fatalf("ошибка не называет туннель: %v", err)
	}
}

// mirrorStore — клиент, переживший смену режима wg → raw: wg-туннель со
// ссылкой остался, рядом появилось raw-зеркало. Обе записи связаны с одним
// клиентом, но зеркало вне жизненного цикла.
func mirrorStore(t *testing.T) *storage.AWGTunnelStore {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	for _, tun := range []*storage.AWGTunnel{
		{ID: "awgm1", Name: "WD", WdttClientID: "c1", Peer: storage.AWGPeer{Endpoint: "127.0.0.1:9000"}},
		{ID: "wdttraw-c1", Name: "RAW", WdttClientID: "c1", Backend: "wdtt-raw"},
	} {
		if err := store.Save(tun); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// Признак жизненного цикла обязан доехать через адаптер: зеркало попадает в
// список (доводка endpoint'а его видит), но подъёму и остановке не подлежит.
// Заодно пиннится цена: состояние у зеркала не спрашивают вовсе.
func TestProxyEndpointSyncListMarksMirrorOutOfLifecycle(t *testing.T) {
	svc := &stateSvc{running: map[string]bool{"awgm1": true}}
	list, err := newProxyEndpointSync(mirrorStore(t), svc, api.LinkedWdtt, nil).
		List(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("список: %+v (зеркало обязано попасть в связь)", list)
	}
	for _, it := range list {
		switch it.ID {
		case "awgm1":
			if !it.Lifecycle || !it.Running {
				t.Fatalf("wg-туннель: %+v", it)
			}
		case "wdttraw-c1":
			if it.Lifecycle || it.Running {
				t.Fatalf("зеркало: %+v", it)
			}
		default:
			t.Fatalf("посторонняя запись: %+v", it)
		}
	}
	if len(svc.asked) != 1 || svc.asked[0] != "awgm1" {
		t.Fatalf("состояние спрошено у: %v (у зеркала спрашивать нечего)", svc.asked)
	}
}

// Зеркало не поднимается и не опускается: это не туннель роутера.
func TestProxyEndpointSyncSetStateSkipsMirror(t *testing.T) {
	// Зеркало ПОДНЯТО: иначе остановка пропустила бы его как уже стоящее, и
	// подмена предиката жизненного цикла предикатом связи прошла бы мимо.
	svc := &stateSvc{running: map[string]bool{"awgm1": true, "wdttraw-c1": true}}
	s := newProxyEndpointSync(mirrorStore(t), svc, api.LinkedWdtt, nil)
	if _, err := s.SetState(context.Background(), "c1", false); err != nil {
		t.Fatal(err)
	}
	if len(svc.stopped) != 1 || svc.stopped[0] != "awgm1" {
		t.Fatalf("опущены: %v", svc.stopped)
	}
}

// Д1 целиком, на живом хранилище: включённый raw-клиент, единственная связанная
// запись — его собственное зеркало с адресом реального реле. План обязан быть
// пуст, а адрес в хранилище — нетронут. Сквозной прогон адаптер + экспорт api +
// ресурс: по отдельности каждый слой выглядел безобидно.
func TestRawMirrorSurvivesEnabledClientReconcile(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Save(&storage.AWGTunnel{
		ID: "wdttraw-c1", Name: "RAW", WdttClientID: "c1",
		Backend: "wdtt-raw",
		Peer:    storage.AWGPeer{Endpoint: "vps.example:56003"},
	}); err != nil {
		t.Fatal(err)
	}
	svc := &stateSvc{running: map[string]bool{}}
	pub := &fakePublisher{}

	le := linkres.NewLinkedEndpoint("linked_endpoint",
		newProxyEndpointSync(store, svc, api.LinkedWdtt, pub))
	le.SetDesired("c1", "127.0.0.1:9000", true)

	obs, err := le.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if steps := le.Plan(obs); len(steps) != 0 {
		t.Fatalf("включённый raw-клиент планирует шаги по своему зеркалу: %+v", steps)
	}
	got, err := store.Get("wdttraw-c1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Peer.Endpoint != "vps.example:56003" {
		t.Fatalf("адрес зеркала стал %q", got.Peer.Endpoint)
	}
	if len(pub.events) != 0 {
		t.Fatalf("качели инвалидации фронта: %v", pub.events)
	}
}
