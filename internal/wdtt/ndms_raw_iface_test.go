package wdtt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Стражи raw-половины сервера в NDMS. Второй интерфейс (raw-IP без WireGuard)
// до этой задачи в роутере не заводился вовсе и жил на одних iptables
// менеджера: в веб-морде его не было, маршрутизировать его было нечем.

// fakeServerBin — заглушка wdtt-server, печатающая в stderr перечисленные
// флаги. probeBinaryHelp читает именно её вывод, и по нему решается, знает ли
// бинарь -wg-iface / -raw-iface.
func fakeServerBin(t *testing.T, flags ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wdtt-server")
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	for _, f := range flags {
		fmt.Fprintf(&b, "echo '  %s string' >&2\n", f)
	}
	b.WriteString("exit 0\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o755); err != nil {
		t.Fatalf("заглушка бинаря: %v", err)
	}
	return path
}

func newRawNDMSTestService(t *testing.T, flags ...string) (*Service, *fakeOpkgCommands) {
	t.Helper()
	dir := t.TempDir()
	svc := NewService(dir, filepath.Join(dir, "run"), "", fakeServerBin(t, flags...))
	fake := &fakeOpkgCommands{}
	svc.SetNDMSInterfaceCommands(fake)
	svc.SetOpkgTunIndexLister(&routerOpkgStub{live: map[int]bool{}})
	return svc, fake
}

// ndmsServerConfigWithRaw — обе половины выделены, как после старта на новом бинаре.
func ndmsServerConfigWithRaw() ServerConfig {
	cfg := ndmsServerConfig()
	cfg.RawNdmsIface = "OpkgTun18"
	cfg.RawIface = "opkgtun18"
	return cfg
}

func TestEnsureServerOpkgIndexAllocatesRawSecondIndex(t *testing.T) {
	svc, _ := newRawNDMSTestService(t, "-wg-iface", "-raw-iface")

	cfg, err := svc.ensureServerOpkgIndex(context.Background(), DefaultInstanceID, DefaultServerConfig())
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if cfg.NdmsIface != "OpkgTun17" || cfg.WgIface != "opkgtun17" {
		t.Fatalf("WG-половина: %q/%q", cfg.NdmsIface, cfg.WgIface)
	}
	if cfg.RawNdmsIface != "OpkgTun18" || cfg.RawIface != "opkgtun18" {
		t.Fatalf("raw-половина: %q/%q", cfg.RawNdmsIface, cfg.RawIface)
	}

	full, err := svc.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := full.Servers[0].Config.RawNdmsIface; got != "OpkgTun18" {
		t.Fatalf("raw-индекс не сохранён в wdtt.json: %q", got)
	}
}

// Уже выделенная WG-половина не должна отдать свой индекс raw-половине:
// configReservedOpkgIndices пропускает наш собственный сервер целиком.
func TestEnsureServerOpkgIndexRawDoesNotReuseWGIndex(t *testing.T) {
	svc, _ := newRawNDMSTestService(t, "-wg-iface", "-raw-iface")
	cfg := ndmsServerConfig() // OpkgTun17 уже наш

	cfg, err := svc.ensureServerOpkgIndex(context.Background(), DefaultInstanceID, cfg)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if cfg.NdmsIface != "OpkgTun17" {
		t.Fatalf("WG-половина переехала: %q", cfg.NdmsIface)
	}
	if cfg.RawNdmsIface == cfg.NdmsIface {
		t.Fatalf("raw получил индекс WG: %q", cfg.RawNdmsIface)
	}
	if cfg.RawNdmsIface != "OpkgTun18" {
		t.Fatalf("raw-половина: %q", cfg.RawNdmsIface)
	}
}

// Старый бинарь не знает -raw-iface: имя интерфейса задать нечем, а
// зарегистрировать wdttraw0 в NDMS нельзя — тип выводится из имени.
func TestEnsureServerOpkgIndexSkipsRawWithoutFlag(t *testing.T) {
	svc, _ := newRawNDMSTestService(t, "-wg-iface")

	cfg, err := svc.ensureServerOpkgIndex(context.Background(), DefaultInstanceID, DefaultServerConfig())
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if cfg.NdmsIface != "OpkgTun17" {
		t.Fatalf("WG-половина не выделена: %q", cfg.NdmsIface)
	}
	if cfg.RawNdmsIface != "" || cfg.RawIface != "" {
		t.Fatalf("raw выделен без флага: %q/%q", cfg.RawNdmsIface, cfg.RawIface)
	}
}

func TestPrepareAndActivateRawOpkgTun(t *testing.T) {
	svc, fake := newNDMSTestService(t)
	cfg := ndmsServerConfigWithRaw()
	ctx := context.Background()

	if err := svc.prepareNDMSOpkgTun(ctx, cfg); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if fake.index("create OpkgTun18 "+wdttRawOpkgDescription+" private") < 0 {
		t.Fatalf("raw создаётся не тем: %v", fake.calls)
	}
	if fake.index(fmt.Sprintf("mtu OpkgTun18 %d", wdttRawOpkgMTU)) < 0 {
		t.Fatalf("MTU raw-интерфейса: %v", fake.calls)
	}
	// Тот же инвариант PR #544, что и у WG: адрес — только после появления
	// kernel-интерфейса, иначе ndm уходит в вечный nginx-reload.
	if fake.index("address OpkgTun18") >= 0 {
		t.Fatalf("prepare выставил адрес raw до kernel-интерфейса: %v", fake.calls)
	}

	if err := svc.activateNDMSOpkgTun(ctx, cfg); err != nil {
		t.Fatalf("activate: %v", err)
	}
	addrAt := fake.index("address OpkgTun18 " + DefaultRawServerGatewayAddr)
	upAt := fake.index("up OpkgTun18")
	aclAt := fake.index("acl OpkgTun18")
	if addrAt < 0 || upAt < 0 || addrAt > upAt {
		t.Fatalf("адрес raw ставится не там: %v", fake.calls)
	}
	if aclAt < 0 || aclAt < upAt {
		t.Fatalf("permit-all ACL raw должен идти после up: %v", fake.calls)
	}
	if fake.index("address OpkgTun18 "+DefaultRawServerAddr) >= 0 {
		t.Fatalf("NDMS повесил адрес самого wdtt-server — он уже занят: %v", fake.calls)
	}
}

// Выключенный тумблер — сервер остаётся внутренним интерфейсом: private и
// никакого `ip global` (иначе обе половины становятся кандидатами в default route).
func TestServerOpkgTunStaysPrivateByDefault(t *testing.T) {
	svc, fake := newNDMSTestService(t)
	cfg := ndmsServerConfigWithRaw()
	ctx := context.Background()

	if err := svc.prepareNDMSOpkgTun(ctx, cfg); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := svc.activateNDMSOpkgTun(ctx, cfg); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if fake.index("ip-global") >= 0 {
		t.Fatalf("ip global без тумблера: %v", fake.calls)
	}
	for _, name := range []string{"OpkgTun17", "OpkgTun18"} {
		if fake.index("create "+name+" ") < 0 {
			t.Fatalf("интерфейс %s не создан: %v", name, fake.calls)
		}
		if fake.index("security "+name+" public") >= 0 {
			t.Fatalf("%s стал public без тумблера: %v", name, fake.calls)
		}
	}
}

func TestServerOpkgTunExposedToPolicies(t *testing.T) {
	svc, fake := newNDMSTestService(t)
	cfg := ndmsServerConfigWithRaw()
	cfg.ExposeToPolicies = true
	ctx := context.Background()

	if err := svc.prepareNDMSOpkgTun(ctx, cfg); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := svc.activateNDMSOpkgTun(ctx, cfg); err != nil {
		t.Fatalf("activate: %v", err)
	}
	for _, name := range []string{"OpkgTun17", "OpkgTun18"} {
		if fake.index("create "+name+" ") < 0 {
			t.Fatalf("интерфейс %s не создан: %v", name, fake.calls)
		}
		if fake.index("create "+name+" ") >= 0 && !strings.HasSuffix(fake.calls[fake.index("create "+name+" ")], " public") {
			t.Fatalf("%s создан не public: %v", name, fake.calls)
		}
		globalAt := fake.index("ip-global " + name)
		upAt := fake.index("up " + name)
		if globalAt < 0 {
			t.Fatalf("нет ip global на %s: %v", name, fake.calls)
		}
		if upAt < 0 || globalAt < upAt {
			t.Fatalf("ip global на %s раньше up: %v", name, fake.calls)
		}
	}
}

// Переживший интерфейс создан с прошлым уровнем — тумблер обязан доехать и до
// него, иначе включение «в политиках» ничего не меняет до удаления интерфейса.
func TestPrepareSetsSecurityLevelOnExistingIface(t *testing.T) {
	svc, fake := newNDMSTestService(t)
	svc.SetOpkgTunExistChecker(&fakeOpkgExist{exists: true})
	cfg := ndmsServerConfigWithRaw()
	cfg.ExposeToPolicies = true

	if err := svc.prepareNDMSOpkgTun(context.Background(), cfg); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	for _, name := range []string{"OpkgTun17", "OpkgTun18"} {
		if fake.index("security "+name+" public") < 0 {
			t.Fatalf("уровень %s не выставлен: %v", name, fake.calls)
		}
	}
}

func TestTeardownRemovesBothServerIfaces(t *testing.T) {
	svc, fake := newNDMSTestService(t)

	if err := svc.teardownServerOpkgTun(context.Background(), ndmsServerConfigWithRaw()); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if fake.index("delete OpkgTun17") < 0 {
		t.Fatalf("WG-половина не снята: %v", fake.calls)
	}
	if fake.index("delete OpkgTun18") < 0 {
		t.Fatalf("raw-половина не снята: %v", fake.calls)
	}
}

func TestReapRemovesBothIfacesWithoutRunningServer(t *testing.T) {
	svc, fake := newNDMSTestService(t)
	full, err := svc.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	full.Servers = append(full.Servers, ServerInstance{ID: "srv1", Config: ndmsServerConfigWithRaw()})
	if err := svc.store.Save(full); err != nil {
		t.Fatal(err)
	}

	svc.reapOrphanOpkgTuns(context.Background())
	if fake.index("delete OpkgTun18") < 0 {
		t.Fatalf("raw-сирота не снята: %v", fake.calls)
	}
}

// Обратная половина: пока процесс жив, скан по description не должен принять
// raw-интерфейс за бесхозный — иначе реап сносит его каждые 15 с из-под сервера.
func TestReapKeepsRawIfaceOfRunningServer(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(dir, filepath.Join(dir, "run"), "", "/bin/sh")
	fake := &fakeOpkgCommands{}
	svc.SetNDMSInterfaceCommands(fake)
	full, err := svc.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	full.Servers[0].Config = ndmsServerConfigWithRaw()
	if err := svc.store.Save(full); err != nil {
		t.Fatal(err)
	}
	svc.SetOpkgTunScanner(func(_ context.Context, desc string) ([]string, error) {
		switch desc {
		case wdttOpkgDescription:
			return []string{"OpkgTun17"}, nil
		case wdttRawOpkgDescription:
			return []string{"OpkgTun18"}, nil
		}
		return nil, nil
	})
	p := svc.serverProcs.get(full.Servers[0].ID)
	sleepSeam(p)
	if err := p.Start(nil); err != nil {
		t.Fatalf("старт заглушки: %v", err)
	}
	defer p.Stop()

	svc.reapOrphanOpkgTuns(context.Background())
	if len(fake.calls) != 0 {
		t.Fatalf("реап тронул интерфейсы живого сервера: %v", fake.calls)
	}
}

func TestBuildServerArgsPassesRawIface(t *testing.T) {
	cfg := ndmsServerConfigWithRaw()
	args := strings.Join(buildServerArgs(cfg), " ")
	if !strings.Contains(args, "-raw-iface opkgtun18") {
		t.Fatalf("-raw-iface не передан: %s", args)
	}

	legacy := ndmsServerConfig()
	if strings.Contains(strings.Join(buildServerArgs(legacy), " "), "-raw-iface") {
		t.Fatal("-raw-iface передан без выделенного интерфейса")
	}
}

// Правила адресуются по kernel-имени: переименованный интерфейс без этого
// остался бы без NAT/FORWARD/MSS и без policy-mark.
func TestEntwarePlansFollowRawIfaceName(t *testing.T) {
	cfg := ndmsServerConfigWithRaw()
	ifaces := cfg.serverEntwareNATIfacesForMode("full")
	if len(ifaces) != 1 || ifaces[0] != "opkgtun18" {
		t.Fatalf("план NAT: %v", ifaces)
	}
	if got := ndmsServerConfig().serverEntwareNATIfacesForMode("full"); got[0] != DefaultRawServerIface {
		t.Fatalf("legacy-имя потеряно: %v", got)
	}
}

func TestStatusReportsServerIfaceNames(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(dir, filepath.Join(dir, "run"), "", "")
	full, err := svc.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	full.Servers[0].Config = ndmsServerConfigWithRaw()
	if err := svc.store.Save(full); err != nil {
		t.Fatal(err)
	}

	st := svc.Status()
	if st.Server.NdmsIface != "OpkgTun17" {
		t.Fatalf("NDMS-имя WG: %q", st.Server.NdmsIface)
	}
	if st.Server.RawNdmsIface != "OpkgTun18" {
		t.Fatalf("NDMS-имя raw: %q", st.Server.RawNdmsIface)
	}
	if st.Server.RawIface != "opkgtun18" {
		t.Fatalf("kernel-имя raw: %q", st.Server.RawIface)
	}
}

// Устройство со шлюзовым адресом обречено: адрес локален на самом интерфейсе,
// обратный трафик к нему в туннель не уйдёт.
func TestSanitizeDropsRawGatewayDevice(t *testing.T) {
	devices := map[string]any{
		"dev-raw-gw": map[string]any{"ip": "10.66.0.5", "raw_ip": DefaultRawServerGatewayAddr},
		"dev-ok":     map[string]any{"ip": "10.66.0.5", "raw_ip": "10.70.0.2"},
	}
	out, changed := sanitizePasswordsDevices(devices)
	if !changed {
		t.Fatal("шлюзовое устройство не замечено")
	}
	if _, ok := out["dev-raw-gw"]; ok {
		t.Fatalf("устройство со шлюзовым raw_ip осталось: %v", out)
	}
	if _, ok := out["dev-ok"]; !ok {
		t.Fatalf("чужое устройство выброшено: %v", out)
	}
}

// Старт ждёт ИМЕННО выделенный raw-интерфейс. Ожидание wdttraw0 на новом
// бинаре означало бы отказ старта каждый раз: интерфейс с таким именем
// wdtt-server больше не создаёт.
func TestStartServerInstanceWaitsForConfiguredRawIface(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(dir, filepath.Join(dir, "run"), "", "/bin/sh")
	defer svc.Stop()
	svc.SetNDMSInterfaceCommands(&fakeOpkgCommands{})
	checker := &recordingIfaceChecker{asked: map[string]bool{}}
	svc.SetInterfaceChecker(checker)
	cfg := ndmsServerConfigWithRaw()
	cfg.Password = "mainpass0000000000000000"
	cfg.ConfigDir = t.TempDir()
	if _, err := svc.UpdateServerInstance(DefaultInstanceID, cfg); err != nil {
		t.Fatal(err)
	}
	sleepSeam(svc.serverProcs.get(DefaultInstanceID))

	if err := svc.StartServerInstance(DefaultInstanceID); err == nil {
		t.Fatal("ожидался отказ: ни один интерфейс не появился")
	}
	// Спрашиваем ПО ФАКТУ ОПРОСА, а не по тексту ошибки: текст берёт имя из
	// конфига и пережил бы ожидание чужого интерфейса.
	if !checker.wasAsked("opkgtun18") {
		t.Fatalf("выделенный raw-интерфейс никто не ждал: %v", checker.names())
	}
	if checker.wasAsked(DefaultRawServerIface) {
		t.Fatalf("старт ждал legacy-имя %s: %v", DefaultRawServerIface, checker.names())
	}
}

// recordingIfaceChecker запоминает, о каких интерфейсах спрашивали.
type recordingIfaceChecker struct {
	mu    sync.Mutex
	asked map[string]bool
}

func (c *recordingIfaceChecker) InterfaceExists(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked[name] = true
	return false
}

func (c *recordingIfaceChecker) InterfaceOperUp(string) bool { return false }

func (c *recordingIfaceChecker) wasAsked(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.asked[name]
}

func (c *recordingIfaceChecker) names() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.asked))
	for n := range c.asked {
		out = append(out, n)
	}
	return out
}

// Смена имени raw-интерфейса меняет и аргументы процесса (-raw-iface), и
// адресацию правил: без перезапуска и переприменения доступа сервер остался бы
// с правилами на прежнем имени.
func TestRawIfaceChangeTriggersRestartAndAccess(t *testing.T) {
	prev := ndmsServerConfig()
	next := ndmsServerConfigWithRaw()

	if !serverProcessConfigChanged(prev, next) {
		t.Fatal("смена raw-интерфейса не перезапускает процесс")
	}
	if !serverAccessConfigChanged(prev, next) {
		t.Fatal("смена raw-интерфейса не переприменяет доступ")
	}
	if serverProcessConfigChanged(next, next) || serverAccessConfigChanged(next, next) {
		t.Fatal("неизменный конфиг объявлен изменившимся")
	}
}

// Тумблер «в политиках» применяется ТОЛЬКО на старте: перезапускать живой
// сервер (и рвать всех абонентов) ради видимости интерфейса — несоразмерно, а
// снять `ip global` иначе как пересозданием интерфейса всё равно нельзя.
func TestExposeToPoliciesDoesNotRestartLiveServer(t *testing.T) {
	prev := ndmsServerConfigWithRaw()
	next := prev
	next.ExposeToPolicies = true

	if serverProcessConfigChanged(prev, next) {
		t.Fatal("тумблер перезапускает сервер — решение было обратным")
	}
}
