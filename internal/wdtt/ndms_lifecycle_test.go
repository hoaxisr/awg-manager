package wdtt

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// fakeOpkgCommands записывает порядок RCI-вызовов.
type fakeOpkgCommands struct {
	calls     []string
	deleteErr error
	// mtuErrOn — если > 0, N-й по счёту вызов SetMTU (1-based) возвращает
	// mtuErr вместо nil; нужно для симуляции отказа повторного prepare.
	mtuErrOn int
	mtuErr   error
	mtuCalls int
}

func (f *fakeOpkgCommands) rec(format string, args ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *fakeOpkgCommands) CreateOpkgTunWithSecurityLevel(_ context.Context, name, desc, level string) error {
	f.rec("create %s %s %s", name, desc, level)
	return nil
}
func (f *fakeOpkgCommands) SetDescription(_ context.Context, name, desc string) error {
	f.rec("description %s %s", name, desc)
	return nil
}
func (f *fakeOpkgCommands) DeleteOpkgTun(_ context.Context, name string) error {
	f.rec("delete %s", name)
	return f.deleteErr
}
func (f *fakeOpkgCommands) SetSecurityLevel(_ context.Context, name, level string) error {
	f.rec("security %s %s", name, level)
	return nil
}
func (f *fakeOpkgCommands) SetIPGlobal(_ context.Context, name string) error {
	f.rec("ip-global %s", name)
	return nil
}
func (f *fakeOpkgCommands) SetAddress(_ context.Context, name, addr, _ string) error {
	f.rec("address %s %s", name, addr)
	return nil
}
func (f *fakeOpkgCommands) ClearAddress(_ context.Context, name string) error {
	f.rec("clear-address %s", name)
	return nil
}
func (f *fakeOpkgCommands) SetMTU(_ context.Context, name string, mtu int) error {
	f.rec("mtu %s %d", name, mtu)
	f.mtuCalls++
	if f.mtuErrOn > 0 && f.mtuCalls == f.mtuErrOn {
		return f.mtuErr
	}
	return nil
}
func (f *fakeOpkgCommands) InterfaceUp(_ context.Context, name string) error {
	f.rec("up %s", name)
	return nil
}
func (f *fakeOpkgCommands) InterfaceDown(_ context.Context, name string) error {
	f.rec("down %s", name)
	return nil
}
func (f *fakeOpkgCommands) SetPermitAllACL(_ context.Context, name string) error {
	f.rec("acl %s", name)
	return nil
}
func (f *fakeOpkgCommands) RemovePermitAllACL(_ context.Context, name string) error {
	f.rec("acl-remove %s", name)
	return nil
}

func (f *fakeOpkgCommands) index(prefix string) int {
	for i, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

func newNDMSTestService(t *testing.T) (*Service, *fakeOpkgCommands) {
	t.Helper()
	dir := t.TempDir()
	svc := NewService(dir, filepath.Join(dir, "run"), "", "")
	fake := &fakeOpkgCommands{}
	svc.SetNDMSInterfaceCommands(fake)
	return svc, fake
}

// Инвариант из PR #544: OpkgTun с настроенным `ip address`, но без
// kernel-адреса вгоняет ndm в вечный nginx-reload. Адрес обязан появляться
// только на активации — уже после того, как wdtt-server поднял opkgtunN.
func TestPrepareNDMSOpkgTunDoesNotSetAddress(t *testing.T) {
	svc, fake := newNDMSTestService(t)
	cfg := ndmsServerConfig()

	if err := svc.prepareNDMSOpkgTun(context.Background(), cfg); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if i := fake.index("address "); i >= 0 {
		t.Fatalf("prepare выставил адрес до появления kernel-интерфейса: %v", fake.calls)
	}
	if fake.index("create OpkgTun17 "+wdttOpkgDescription+" private") < 0 {
		t.Fatalf("ожидали create с security-level private, получили %v", fake.calls)
	}

	if err := svc.activateNDMSOpkgTun(context.Background(), cfg); err != nil {
		t.Fatalf("activate: %v", err)
	}
	addrAt, upAt, aclAt := fake.index("address "), fake.index("up "), fake.index("acl ")
	if addrAt < 0 || upAt < 0 || addrAt > upAt {
		t.Fatalf("адрес должен ставиться на активации и до up: %v", fake.calls)
	}
	if aclAt < 0 || aclAt < upAt {
		t.Fatalf("permit-all ACL должен ставиться после up: %v", fake.calls)
	}
}

// Провал delete обязан оставлять интерфейс без адреса — иначе сирота крутит
// nginx-reload до ручного вмешательства.
func TestTeardownClearsAddressWhenDeleteFails(t *testing.T) {
	svc, fake := newNDMSTestService(t)
	fake.deleteErr = fmt.Errorf("busy")

	if err := svc.teardownServerOpkgTun(context.Background(), ndmsServerConfig()); err == nil {
		t.Fatal("ожидали ошибку delete")
	}
	if fake.index("clear-address OpkgTun17") < 0 {
		t.Fatalf("после провала delete адрес не снят: %v", fake.calls)
	}
}

func TestTeardownSkipsClearOnSuccess(t *testing.T) {
	svc, fake := newNDMSTestService(t)

	if err := svc.teardownServerOpkgTun(context.Background(), ndmsServerConfig()); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if fake.index("clear-address") >= 0 {
		t.Fatalf("на happy-path clear-address — лишний RCI: %v", fake.calls)
	}
}

// Сервер в конфиге есть, процесс не запущен → интерфейс сносим: это состояние
// после SIGKILL демона или падения wdtt-server.
func TestReapRemovesOpkgTunWithoutRunningServer(t *testing.T) {
	svc, fake := newNDMSTestService(t)
	full, err := svc.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	full.Servers = append(full.Servers, ServerInstance{ID: "srv1", Config: ndmsServerConfig()})
	if err := svc.store.Save(full); err != nil {
		t.Fatal(err)
	}

	svc.reapOrphanOpkgTuns(context.Background())
	if fake.index("delete OpkgTun17") < 0 {
		t.Fatalf("сирота не снята: %v", fake.calls)
	}
}

// Интерфейс с нашим description, которого нет ни в одном конфиге (сервер
// удалён восстановлением бэкапа), ловится сканом по description.
func TestReapRemovesUnreferencedOpkgTun(t *testing.T) {
	svc, fake := newNDMSTestService(t)
	svc.SetOpkgTunScanner(func(context.Context, string) ([]string, error) {
		return []string{"OpkgTun23"}, nil
	})

	svc.reapOrphanOpkgTuns(context.Background())
	if fake.index("delete OpkgTun23") < 0 {
		t.Fatalf("бесхозный OpkgTun не снят: %v", fake.calls)
	}
}

// Старт в полёте: между create и запуском процесса reap не должен трогать
// интерфейс, иначе снесёт его из-под StartServerInstance.
func TestReapSkipsWhileStartInFlight(t *testing.T) {
	svc, fake := newNDMSTestService(t)
	svc.SetOpkgTunScanner(func(context.Context, string) ([]string, error) {
		return []string{"OpkgTun23"}, nil
	})

	svc.beginOpkgStart()
	svc.reapOrphanOpkgTuns(context.Background())
	svc.endOpkgStart()
	if len(fake.calls) != 0 {
		t.Fatalf("reap отработал во время старта: %v", fake.calls)
	}
}

// fakeOpkgExist — заглушка чекера существования OpkgTun в NDMS.
type fakeOpkgExist struct {
	exists bool
	calls  []string
}

func (f *fakeOpkgExist) OpkgTunExists(_ context.Context, name string) bool {
	f.calls = append(f.calls, name)
	return f.exists
}

// Выключенный пользователем сервер остаётся в конфиге навсегда, а его процесс
// не запущен — под условие реапа он подходит на каждом тике. Без проверки
// существования teardown каждые 15 с гонял 4 RCI-мутации, флеш-сейв и полную
// инвалидацию кэшей NDMS, а `interface <name> up false` создавал интерфейс по
// обращению, чтобы следующая команда его снесла: отсюда поток ifdestroyed-хуков
// от интерфейса, которого в NDMS нет.
func TestReapSkipsOpkgTunAbsentInNDMS(t *testing.T) {
	svc, fake := newNDMSTestService(t)
	exist := &fakeOpkgExist{exists: false}
	svc.SetOpkgTunExistChecker(exist)
	full, err := svc.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	full.Servers = append(full.Servers, ServerInstance{ID: "srv1", Config: ndmsServerConfig()})
	if err := svc.store.Save(full); err != nil {
		t.Fatal(err)
	}

	svc.reapOrphanOpkgTuns(context.Background())
	if len(fake.calls) != 0 {
		t.Fatalf("reap трогал несуществующий интерфейс: %v", fake.calls)
	}
	if len(exist.calls) == 0 {
		t.Fatal("чекер существования не спрошен")
	}
}

// Обратная половина: интерфейс в NDMS есть, владельца нет — снос обязан идти.
func TestReapRemovesOpkgTunPresentInNDMS(t *testing.T) {
	svc, fake := newNDMSTestService(t)
	svc.SetOpkgTunExistChecker(&fakeOpkgExist{exists: true})
	full, err := svc.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	full.Servers = append(full.Servers, ServerInstance{ID: "srv1", Config: ndmsServerConfig()})
	if err := svc.store.Save(full); err != nil {
		t.Fatal(err)
	}

	svc.reapOrphanOpkgTuns(context.Background())
	if fake.index("delete OpkgTun17") < 0 {
		t.Fatalf("живая сирота не снята: %v", fake.calls)
	}
}

func TestWdttPeerCIDRIsNormalizedNetwork(t *testing.T) {
	// iptables -S печатает сеть, а не адрес с маской: несовпадение делало
	// entwareLANPresent вечно ложным, и ресинк переписывал правила каждые 15 с.
	if got := wdttPeerCIDR(); got != "10.66.0.0/16" {
		t.Fatalf("wdttPeerCIDR() = %q, want 10.66.0.0/16", got)
	}
}
