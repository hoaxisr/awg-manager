package orchestrator

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// Пуск туннеля — это секунды работы снаружи хранилища (RCI, .conf, резолв
// endpoint). Пользователь всё это время видит карточку и может её сохранить.
// Оркестратор обязан дописать СВОИ runtime-поля, не откатывая чужую правку:
// прежде он персистил снимок, снятый ДО долгой работы, и правка молча
// исчезала, а запись расходилась с .conf.
//
// Детерминизм — парковкой, не таймингом: оператор запаркован внутри Start/
// ColdStart, правка «пользователя» идёт синхронно в этом окне, и только после
// неё оператор отпускается. Гонки нет — есть строгий порядок.

// parkedOnce закрывает entered при первом заходе и ждёт release.
func park(entered, release chan struct{}) {
	if entered == nil {
		return
	}
	close(entered)
	<-release
}

// --- П7: NativeWG-путь ---

func TestExecuteStartNativeWG_ConcurrentUserEditSurvives(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{
		ID: "awg20", Name: "старое имя", Backend: "nativewg",
		Interface: storage.AWGInterface{DNS: "1.1.1.1"},
	}); err != nil {
		t.Fatalf("сид записи: %v", err)
	}

	entered, release := make(chan struct{}), make(chan struct{})
	op := &fakeNWGOp{entered: entered, release: release}
	o := &Orchestrator{state: newState(), store: store, nwgOp: op}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := o.executeStartNativeWG(context.Background(),
			Action{Type: ActionStartNativeWG, Tunnel: "awg20"}); err != nil {
			t.Errorf("executeStartNativeWG: %v", err)
		}
	}()

	<-entered
	// Пользователь сохранил карточку, пока шёл пуск.
	if err := store.Update("awg20", func(t *storage.AWGTunnel) error {
		t.Name = "новое имя"
		t.Interface.DNS = "9.9.9.9"
		return nil
	}); err != nil {
		t.Fatalf("правка пользователя: %v", err)
	}
	close(release)
	wg.Wait()

	got, err := store.Get("awg20")
	if err != nil {
		t.Fatalf("перечитать запись: %v", err)
	}
	if got.Name != "новое имя" {
		t.Errorf("name = %q, want %q (правка пользователя откатилась)", got.Name, "новое имя")
	}
	if got.Interface.DNS != "9.9.9.9" {
		t.Errorf("interface.dns = %q, want %q (правка пользователя откатилась)", got.Interface.DNS, "9.9.9.9")
	}
	if !got.Enabled {
		t.Errorf("enabled = false, want true (runtime-поле оркестратора не записано)")
	}
	if got.StartedAt == "" {
		t.Errorf("startedAt пуст (runtime-поле оркестратора не записано)")
	}
}

// --- П8: kernel-путь (ColdStart) ---

func TestExecuteColdStartKernel_ConcurrentUserEditSurvives(t *testing.T) {
	dir := t.TempDir()
	confDir := t.TempDir()
	oldConfDir := tunnel.ConfDir
	tunnel.ConfDir = confDir
	t.Cleanup(func() { tunnel.ConfDir = oldConfDir })

	prevIfaces := listInterfaces
	listInterfaces = func() ([]net.Interface, error) { return nil, nil }
	t.Cleanup(func() { listInterfaces = prevIfaces })

	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{
		ID: "awg10", Name: "старое имя", Backend: "kernel",
		ISPInterface: "eth3",
		Interface:    storage.AWGInterface{Address: "10.253.254.2", DNS: "1.1.1.1"},
		// IP-литерал: резолв endpoint не ходит в DNS.
		Peer: storage.AWGPeer{Endpoint: "203.0.113.7:51820"},
	}); err != nil {
		t.Fatalf("сид записи: %v", err)
	}

	entered, release := make(chan struct{}), make(chan struct{})
	op := &fakeKernelOp{entered: entered, release: release}
	o := &Orchestrator{state: newState(), store: store, kernelOp: op, wanModel: wan.NewModel()}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := o.executeColdStartKernel(context.Background(),
			Action{Type: ActionColdStartKernel, Tunnel: "awg10"}); err != nil {
			t.Errorf("executeColdStartKernel: %v", err)
		}
	}()

	<-entered
	if err := store.Update("awg10", func(t *storage.AWGTunnel) error {
		t.Name = "новое имя"
		t.Interface.DNS = "9.9.9.9"
		return nil
	}); err != nil {
		t.Fatalf("правка пользователя: %v", err)
	}
	close(release)
	wg.Wait()

	got, err := store.Get("awg10")
	if err != nil {
		t.Fatalf("перечитать запись: %v", err)
	}
	if got.Name != "новое имя" {
		t.Errorf("name = %q, want %q (правка пользователя откатилась)", got.Name, "новое имя")
	}
	if got.Interface.DNS != "9.9.9.9" {
		t.Errorf("interface.dns = %q, want %q (правка пользователя откатилась)", got.Interface.DNS, "9.9.9.9")
	}
	if !got.Enabled {
		t.Errorf("enabled = false, want true (runtime-поле оркестратора не записано)")
	}
	if got.ActiveWAN != "eth3" {
		t.Errorf("activeWAN = %q, want eth3 (runtime-поле оркестратора не записано)", got.ActiveWAN)
	}
	if got.StartedAt == "" {
		t.Errorf("startedAt пуст (runtime-поле оркестратора не записано)")
	}
}

// fakeKernelOp — болванка ops.Operator: всё no-op, ColdStart паркуется.
type fakeKernelOp struct {
	entered chan struct{}
	release chan struct{}

	// coldStartErr/deleteErr — отказ соответствующего шага; stops — счётчик Stop.
	coldStartErr error
	deleteErr    error
	stops        int
}

func (f *fakeKernelOp) Create(context.Context, tunnel.Config) error { return nil }
func (f *fakeKernelOp) ColdStart(context.Context, tunnel.Config) error {
	park(f.entered, f.release)
	return f.coldStartErr
}
func (f *fakeKernelOp) Stop(context.Context, string) error { f.stops++; return nil }
func (f *fakeKernelOp) Delete(context.Context, *storage.AWGTunnel) error {
	return f.deleteErr
}
func (f *fakeKernelOp) Reconcile(context.Context, tunnel.Config) error          { return nil }
func (f *fakeKernelOp) Suspend(context.Context, string) error                   { return nil }
func (f *fakeKernelOp) Resume(context.Context, string) error                    { return nil }
func (f *fakeKernelOp) ApplyConfig(context.Context, string, string) error       { return nil }
func (f *fakeKernelOp) SetDefaultRoute(context.Context, string) error           { return nil }
func (f *fakeKernelOp) RemoveDefaultRoute(context.Context, string) error        { return nil }
func (f *fakeKernelOp) CleanupEndpointRoute(context.Context, string) error      { return nil }
func (f *fakeKernelOp) GetTrackedEndpointIP(string) string                      { return "" }
func (f *fakeKernelOp) SetMTU(context.Context, string, int) error               { return nil }
func (f *fakeKernelOp) SyncDNS(context.Context, string, []string) error         { return nil }
func (f *fakeKernelOp) UpdateDescription(context.Context, string, string) error { return nil }
func (f *fakeKernelOp) GetSystemName(context.Context, string) string            { return "" }
func (f *fakeKernelOp) SetAppLogger(logging.AppLogger)                          {}
func (f *fakeKernelOp) SetupEndpointRoute(context.Context, string, string, string, string) (string, error) {
	return "", nil
}
func (f *fakeKernelOp) RestoreEndpointTracking(context.Context, string, string) (string, error) {
	return "", nil
}
func (f *fakeKernelOp) SyncAddress(context.Context, string, string, int, string) error { return nil }
func (f *fakeKernelOp) GetDefaultGatewayInterface(context.Context) (string, error)     { return "", nil }
func (f *fakeKernelOp) SetupClientRouteTable(context.Context, string, int) error       { return nil }
func (f *fakeKernelOp) AddClientRule(context.Context, string, int) error               { return nil }
func (f *fakeKernelOp) RemoveClientRule(context.Context, string, int) error            { return nil }
func (f *fakeKernelOp) CleanupClientRouteTable(context.Context, int) error             { return nil }
func (f *fakeKernelOp) ListUsedRoutingTables(context.Context) ([]int, error)           { return nil, nil }

// --- Тишина на законном исходе «записи уже нет» ---

// capturingLog собирает строки журнала, чтобы отличить «промолчали» от
// «предупредили».
type capturingLog struct{ warns []string }

func (c *capturingLog) AppLog(level logging.Level, group, subgroup, action, target, message string) {
	if level == logging.LevelWarn {
		c.warns = append(c.warns, action+"/"+target+": "+message)
	}
}

// Остановка туннеля, которого уже нет, — штатный порядок «остановили и
// удалили», а не сбой: запись мог удалить пользователь, пока шла остановка.
// Warn на нём приучал бы не читать журнал, поэтому persistWarn на ErrNotFound
// молчит. Прочие ошибки записи предупреждать обязан — иначе гейт вырождается
// в «никогда не логировать».
//
// Краснеет на мутации «убрать гард errors.Is(err, storage.ErrNotFound)».
func TestPersistWarn_SilentOnMissingRecord(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	cap := &capturingLog{}
	o := &Orchestrator{
		state:  newState(),
		store:  store,
		appLog: logging.NewScopedLogger(cap, logging.GroupTunnel, logging.SubOrchestrator),
	}

	// Записи нет вовсе — Update отдаст ErrNotFound.
	o.persistWarn("awg99", "kernel stop", store.Update("awg99",
		func(*storage.AWGTunnel) error { return nil }))
	if len(cap.warns) != 0 {
		t.Fatalf("Warn на удалённой записи: %v", cap.warns)
	}

	// Битая запись — настоящий сбой, о нём предупреждаем.
	if err := os.WriteFile(filepath.Join(dir, "awg98.json"), []byte("{сломано"), 0o644); err != nil {
		t.Fatal(err)
	}
	o.persistWarn("awg98", "kernel stop", store.Update("awg98",
		func(*storage.AWGTunnel) error { return nil }))
	if len(cap.warns) != 1 {
		t.Fatalf("битая запись не предупреждена: %v", cap.warns)
	}
}
