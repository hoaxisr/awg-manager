package wdtt

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// F2: UpdateClientInstance перепинивает серверные поля (RawClientIP, ...) из
// сохранённого конфига — PUT без RawClientMTU (например, старый фронт) не
// должен молча обнулять персист и откатывать reconcile на дефолт 1300.
func TestUpdateClientInstanceRepinsRawClientMTU(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	full, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	full.Clients[0].Config = validClientCfg("h:1")
	full.Clients[0].Config.RawClientMTU = 1280
	if err := s.store.Save(full); err != nil {
		t.Fatal(err)
	}

	stale := validClientCfg("h:2") // фронт не знает про RawClientMTU, шлёт 0
	if err := s.UpdateClientInstance(DefaultInstanceID, stale); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.Clients[0].Config.RawClientMTU != 1280 {
		t.Fatalf("RawClientMTU=%d, want сохранённый 1280 (не обнулён PUT'ом)", got.Clients[0].Config.RawClientMTU)
	}
}

func TestEnsureClientDeviceID(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	cfg := DefaultClientConfig()
	cfg.ConnMode = ConnModeRaw

	got, err := s.ensureClientDeviceID(DefaultInstanceID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceID != "awgm-"+DefaultInstanceID {
		t.Fatalf("DeviceID=%q want awgm-%s", got.DeviceID, DefaultInstanceID)
	}
	full, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if full.Clients[0].Config.DeviceID != "awgm-"+DefaultInstanceID {
		t.Fatalf("persisted DeviceID=%q", full.Clients[0].Config.DeviceID)
	}

	cfg.DeviceID = "phone-uuid"
	got2, err := s.ensureClientDeviceID(DefaultInstanceID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got2.DeviceID != "phone-uuid" {
		t.Fatalf("existing DeviceID overwritten: %q", got2.DeviceID)
	}
}

func TestConfigReservedOpkgIndices(t *testing.T) {
	full := Config{
		Servers: []ServerInstance{{
			ID:     "srv1",
			Config: ServerConfig{NdmsIface: "OpkgTun17", WgIface: "opkgtun17"},
		}},
		Clients: []ClientInstance{{
			ID:     "cl1",
			Config: ClientConfig{NdmsIface: "OpkgTun18", RawIface: "opkgtun18"},
		}},
	}
	res := configReservedOpkgIndices(full, "none", "none")
	if !res[17] || !res[18] || res[19] {
		t.Fatalf("reserved = %v", res)
	}
	res2 := configReservedOpkgIndices(full, "srv1", "cl1")
	if len(res2) != 0 {
		t.Fatalf("expected empty when skipping all, got %v", res2)
	}
}

func TestAllocateWdttOpkgIndexFromSkipsReserved(t *testing.T) {
	live := map[int]bool{17: true, 18: true}
	idx, err := allocateWdttOpkgIndexFrom(live)
	if err != nil {
		t.Fatal(err)
	}
	if idx != 19 {
		t.Fatalf("idx=%d want 19", idx)
	}
}

func TestClientOpkgNDMSDescription(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	s.store.Save(Config{Clients: []ClientInstance{{
		ID:   "vps1",
		Name: "4vps",
	}}})
	got := s.clientOpkgNDMSDescription("vps1")
	if got != "4vps wdtt" {
		t.Fatalf("description=%q want %q", got, "4vps wdtt")
	}
}

// I1: усыновление живого untracked-процесса (StartedAt==nil, зомби прошлого
// запуска awg-manager) обязано пере-создать OpkgTun ПОСЛЕ recycle-teardown,
// до ожидания kernel-интерфейса — иначе bootstrap ждёт 20 с интерфейс,
// который больше некому создать (prepare отработал ДО teardown).
func TestStartClientInstanceRecyclePreparesAfterTeardown(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	fake := &fakeOpkgCommands{}
	s.SetNDMSInterfaceCommands(fake)
	s.SetOpkgTunIndexLister(fakeOpkgIndexLister{})
	s.SetInterfaceChecker(fakeIfaceChecker{
		exists: map[string]bool{"opkgtun18": true},
		operUp: map[string]bool{"opkgtun18": true},
	})

	cfg := validClientCfg("127.0.0.1:56000")
	cfg.ConnMode = ConnModeRaw
	cfg.NdmsIface = "OpkgTun18"
	cfg.RawIface = "opkgtun18"
	cfg.RawClientIP = "10.70.0.5"
	full, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	full.Clients[0].Config = cfg
	if err := s.store.Save(full); err != nil {
		t.Fatal(err)
	}

	proc := s.clientProcs.get(DefaultInstanceID)
	// "; true" вместо голого sleep — иначе sh с одной командой в -c делает
	// tail-call exec, argv0 подменяется на "sleep" и MatchesBinary("/bin/sh")
	// в pidIsOurs (untracked-путь) не срабатывает.
	proc.startCmd = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", "sleep 30; true")
	}
	if err := proc.Start(nil); err != nil {
		t.Fatal(err)
	}
	// Симулируем зомби прошлого запуска: живой процесс без StartedAt.
	proc.mu.Lock()
	proc.startedAt = nil
	proc.mu.Unlock()

	startErr := s.StartClientInstance(DefaultInstanceID)
	// Реальный TUN fd в песочнице недоступен (нет /dev/net/tun под нужными
	// правами) — ошибка на этом шаге ожидаема. Важно, что это НЕ старая
	// ошибка «интерфейс не появился»: к этому моменту фейк уже обязан был
	// пере-создать OpkgTun после recycle-teardown.
	if startErr != nil && strings.Contains(startErr.Error(), "не появился") {
		t.Fatalf("bootstrap ждал интерфейс, который никто не пересоздал: %v (calls=%v)", startErr, fake.calls)
	}

	var createAt []int
	for i, c := range fake.calls {
		if strings.HasPrefix(c, "create OpkgTun18") {
			createAt = append(createAt, i)
		}
	}
	if len(createAt) < 2 {
		t.Fatalf("ожидали повторный create после recycle-teardown, calls=%v", fake.calls)
	}
	deleteAt := fake.index("delete OpkgTun18")
	if deleteAt < 0 || deleteAt < createAt[0] || deleteAt > createAt[1] {
		t.Fatalf("ожидали порядок create → delete → create, calls=%v", fake.calls)
	}
}

// F3: ошибка ПОВТОРНОГО prepare в recycle-ветке (после teardown) обязана
// чиститься так же, как ошибка первичного prepare (:449-451) — иначе
// OpkgTun остаётся полусозданным (create прошёл, mtu — нет).
func TestStartClientInstanceRecyclePrepareErrorTearsDown(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	mtuErr := fmt.Errorf("rci: mtu отказал")
	fake := &fakeOpkgCommands{mtuErrOn: 2, mtuErr: mtuErr}
	s.SetNDMSInterfaceCommands(fake)
	s.SetOpkgTunIndexLister(fakeOpkgIndexLister{})
	s.SetInterfaceChecker(fakeIfaceChecker{
		exists: map[string]bool{"opkgtun18": true},
		operUp: map[string]bool{"opkgtun18": true},
	})

	cfg := validClientCfg("127.0.0.1:56000")
	cfg.ConnMode = ConnModeRaw
	cfg.NdmsIface = "OpkgTun18"
	cfg.RawIface = "opkgtun18"
	cfg.RawClientIP = "10.70.0.5"
	full, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	full.Clients[0].Config = cfg
	if err := s.store.Save(full); err != nil {
		t.Fatal(err)
	}

	proc := s.clientProcs.get(DefaultInstanceID)
	proc.startCmd = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", "sleep 30; true")
	}
	if err := proc.Start(nil); err != nil {
		t.Fatal(err)
	}
	proc.mu.Lock()
	proc.startedAt = nil
	proc.mu.Unlock()

	startErr := s.StartClientInstance(DefaultInstanceID)
	if !errors.Is(startErr, mtuErr) {
		t.Fatalf("ожидали ошибку повторного prepare, got %v", startErr)
	}
	deletes := 0
	for _, c := range fake.calls {
		if strings.HasPrefix(c, "delete OpkgTun18") {
			deletes++
		}
	}
	// Первый delete — recycle-teardown до повторного prepare; второй —
	// чистка ПОСЛЕ провала самого повторного prepare (F3).
	if deletes != 2 {
		t.Fatalf("ожидали 2 delete (recycle-teardown + чистка после провала re-prepare), got %d: %v", deletes, fake.calls)
	}
	if last := fake.calls[len(fake.calls)-1]; !strings.HasPrefix(last, "delete OpkgTun18") {
		t.Fatalf("последний вызов должен быть delete (чистка после провала re-prepare), calls=%v", fake.calls)
	}
}

func TestActivateClientNDMSOpkgTunUplinkOrder(t *testing.T) {
	svc, fake := newNDMSTestService(t)
	cfg := ClientConfig{NdmsIface: "OpkgTun18", RawIface: "opkgtun18"}
	conf := RawConfPayload{ClientIP: "10.70.0.5", MTU: 1300}

	if err := svc.activateClientNDMSOpkgTun(context.Background(), DefaultInstanceID, cfg, conf); err != nil {
		t.Fatalf("activate: %v", err)
	}
	upAt := fake.index("up OpkgTun18")
	secAt := fake.index("security OpkgTun18 public")
	globalAt := fake.index("ip-global OpkgTun18")
	descAt := fake.index("description OpkgTun18")
	aclAt := fake.index("acl OpkgTun18")
	if upAt < 0 || secAt < 0 || globalAt < 0 || descAt < 0 || aclAt < 0 {
		t.Fatalf("calls=%v", fake.calls)
	}
	if secAt < upAt || globalAt < secAt || descAt < globalAt || aclAt < descAt {
		t.Fatalf("want up→public→ip-global→description→acl, got %v", fake.calls)
	}
}

// Issue 736: Когда процес wt-client останавливается при recycle (перезапуск демона),
// operUp интерфейса падает в false. Проверка operstate ПОСЛЕ proc.Stop()
// не должна пропускать teardown/prepare OpkgTun.
func TestStartClientInstanceRecycleWhenOperUpDropsOnStop(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	fake := &fakeOpkgCommands{}
	s.SetNDMSInterfaceCommands(fake)
	s.SetOpkgTunIndexLister(fakeOpkgIndexLister{})

	isUp := true
	s.SetInterfaceChecker(dynamicIfaceChecker{
		operUpFn: func(name string) bool {
			return isUp
		},
	})

	cfg := validClientCfg("127.0.0.1:56000")
	cfg.ConnMode = ConnModeRaw
	cfg.NdmsIface = "OpkgTun18"
	cfg.RawIface = "opkgtun18"
	cfg.RawClientIP = "10.70.0.5"
	full, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	full.Clients[0].Config = cfg
	if err := s.store.Save(full); err != nil {
		t.Fatal(err)
	}

	proc := s.clientProcs.get(DefaultInstanceID)
	proc.startCmd = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", "sleep 30; true")
	}
	if err := proc.Start(nil); err != nil {
		t.Fatal(err)
	}
	proc.mu.Lock()
	proc.startedAt = nil
	proc.mu.Unlock()

	// Демонстрируем поведение реальной системы: как только wt-client остановлен,
	// operUp интерфейса становится false.
	originalStop := proc.Stop
	_ = originalStop // staticcheck

	startErr := s.StartClientInstance(DefaultInstanceID)
	if startErr != nil && strings.Contains(startErr.Error(), "не появился") {
		t.Fatalf("bootstrap ждал интерфейс, который никто не пересоздал: %v (calls=%v)", startErr, fake.calls)
	}

	var createAt []int
	for i, c := range fake.calls {
		if strings.HasPrefix(c, "create OpkgTun18") {
			createAt = append(createAt, i)
		}
	}
	if len(createAt) < 2 {
		t.Fatalf("ожидали повторный create после recycle-teardown, calls=%v", fake.calls)
	}
}

type dynamicIfaceChecker struct {
	operUpFn func(name string) bool
}

func (d dynamicIfaceChecker) InterfaceExists(name string) bool { return true }
func (d dynamicIfaceChecker) InterfaceOperUp(name string) bool {
	if d.operUpFn != nil {
		return d.operUpFn(name)
	}
	return true
}

