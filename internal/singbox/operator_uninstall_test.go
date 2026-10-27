package singbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/installer"
)

// seedInstallation раскладывает то, что оставляет после себя установка и
// работа движка: бинарь, слоты config.d, кэш FakeIP и pid.
func seedInstallation(t *testing.T, dir string) (binary, cache string) {
	t.Helper()
	binary = filepath.Join(dir, "sing-box")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	confDir := filepath.Join(dir, "config.d")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, slot := range []string{"00-base.json", "10-tunnels.json", "20-router.json"} {
		if err := os.WriteFile(filepath.Join(confDir, slot), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cache = filepath.Join(dir, "cache.db")
	if err := os.WriteFile(cache, []byte("fakeip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sing-box.pid"), []byte("4242"), 0o644); err != nil {
		t.Fatal(err)
	}
	return binary, cache
}

func TestOperator_Uninstall_RemovesBinaryAndConfig(t *testing.T) {
	dir := t.TempDir()
	binary, cache := seedInstallation(t, dir)
	op := NewOperator(OperatorDeps{Dir: dir, Binary: binary})

	if err := op.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	for _, path := range []string{binary, filepath.Join(dir, "config.d"), cache, filepath.Join(dir, "sing-box.pid")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("остался после удаления: %s (err=%v)", path, err)
		}
	}
}

// Удаление идемпотентно: повторный вызов на пустом каталоге — не ошибка.
func TestOperator_Uninstall_Idempotent(t *testing.T) {
	dir := t.TempDir()
	binary, _ := seedInstallation(t, dir)
	op := NewOperator(OperatorDeps{Dir: dir, Binary: binary})

	if err := op.Uninstall(context.Background()); err != nil {
		t.Fatalf("первый вызов: %v", err)
	}
	if err := op.Uninstall(context.Background()); err != nil {
		t.Errorf("повторный вызов: %v", err)
	}
}

// Каталог движка сносится целиком — вместе со всем, что в нём лежало.
func TestOperator_Uninstall_RemovesEngineDirectory(t *testing.T) {
	dir := t.TempDir()
	binary, _ := seedInstallation(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "20-router.json.bak"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	op := NewOperator(OperatorDeps{Dir: dir, Binary: binary})

	if err := op.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("каталог движка остался: %v", err)
	}
}

// Установка после удаления должна быть возможна: статус обязан честно
// сказать «не установлен».
func TestOperator_Uninstall_StatusReportsMissing(t *testing.T) {
	dir := t.TempDir()
	binary, _ := seedInstallation(t, dir)
	op := NewOperator(OperatorDeps{Dir: dir, Binary: binary})

	if err := op.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if s := op.GetStatus(context.Background()); s.Installed {
		t.Errorf("статус после удаления: installed=%v", s.Installed)
	}
}

// seedTunnels кладёт в 10-tunnels.json два туннеля: A на Proxy1, B на Proxy2.
func seedTunnels(t *testing.T, dir string) {
	t.Helper()
	cfg := NewConfig()
	for i, tag := range []string{"A", "B"} {
		ob := json.RawMessage(`{"type":"vless","tag":"` + tag + `"}`)
		if err := cfg.AddTunnelWithListenPort(tag, "vless", "h", 443, 1081+i, ob); err != nil {
			t.Fatal(err)
		}
	}
	if err := cfg.Save(filepath.Join(dir, "config.d", "10-tunnels.json")); err != nil {
		t.Fatal(err)
	}
}

// F723: ProxyN туннелей снимаются до сноса каталога — иначе остаются в NDMS
// сиротами на мёртвом порту.
func TestOperator_Uninstall_RemovesTunnelProxies(t *testing.T) {
	dir := t.TempDir()
	binary, _ := seedInstallation(t, dir)
	seedTunnels(t, dir)
	op := NewOperator(OperatorDeps{Dir: dir, Binary: binary})
	proxies := &fakeProxies{}
	op.proxyMgr = proxies
	op.subProxies = &staticSubProxies{list: []SubscriptionProxy{{Index: 7, Port: 1087, Label: "sub1"}}}

	if err := op.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !slices.Equal(proxies.removed, []int{1, 2, 7}) {
		t.Fatalf("сняты прокси %v, want [1 2 7] (туннели и подписка)", proxies.removed)
	}
}

// fileDownloader кладёт в DestPath заданные байты — Install без сети.
type fileDownloader struct{ body []byte }

func (d fileDownloader) DownloadFile(_ context.Context, req installer.DownloadFileRequest) (installer.DownloadFileResult, error) {
	if err := os.WriteFile(req.DestPath, d.body, 0o644); err != nil {
		return installer.DownloadFileResult{}, err
	}
	return installer.DownloadFileResult{Path: req.DestPath, Size: int64(len(d.body))}, nil
}

// F723: подписки пережили удаление в settings.json, а их ProxyN снят —
// установка возвращает их прокси.
func TestOperator_Install_RestoresSubscriptionProxies(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "sing-box")
	op := NewOperator(OperatorDeps{Dir: dir, Binary: binary})
	body := []byte("#!/bin/sh\nexit 0\n")
	sum := sha256.Sum256(body)
	inst := installer.New(binary, "test-arch", installer.BinarySpec{
		Version: "1.2.3", URL: "u", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body)),
	}, nil)
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 1 << 30, true })
	inst.SetDownloader(fileDownloader{body: body})
	op.SetInstaller(inst)
	synced := 0
	op.SetSubscriptionProxySync(func(context.Context) error { synced++; return nil })

	if err := op.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if synced != 1 {
		t.Fatalf("синхронизаций прокси подписок %d, want 1", synced)
	}
}

// Отказ NDMS — удаление прерывается, ничего не стерев: метки отложенного
// сноса лежат в том же каталоге и пропали бы вместе с ним.
func TestOperator_Uninstall_ProxyFailureKeepsEngine(t *testing.T) {
	dir := t.TempDir()
	binary, _ := seedInstallation(t, dir)
	seedTunnels(t, dir)
	op := NewOperator(OperatorDeps{Dir: dir, Binary: binary})
	op.proxyMgr = &fakeProxies{removeErr: errors.New("rci down")}

	if err := op.Uninstall(context.Background()); err == nil {
		t.Fatal("Uninstall = nil при отказе NDMS")
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("бинарь стёрт при отказе снятия прокси: %v", err)
	}
}

// Чужая запись на индексе туннеля — не повод отказывать в удалении.
func TestOperator_Uninstall_ForeignProxySkipped(t *testing.T) {
	dir := t.TempDir()
	binary, _ := seedInstallation(t, dir)
	seedTunnels(t, dir)
	op := NewOperator(OperatorDeps{Dir: dir, Binary: binary})
	op.proxyMgr = &fakeProxies{removeErr: ErrProxyForeign}

	if err := op.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("каталог движка остался: %v", err)
	}
}

// F724: удаление пакета (Cleanup) снимает ProxyN не только туннелей, но и
// подписок со сводными группами — иначе они остаются в NDMS на мёртвом порту.
func TestOperator_Cleanup_RemovesTunnelAndSubscriptionProxies(t *testing.T) {
	dir := t.TempDir()
	binary, _ := seedInstallation(t, dir)
	seedTunnels(t, dir)
	op := NewOperator(OperatorDeps{Dir: dir, Binary: binary})
	proxies := &fakeProxies{}
	op.proxyMgr = proxies
	op.subProxies = &staticSubProxies{list: []SubscriptionProxy{{Index: 7, Port: 1087, Label: "sub1"}, {Index: 9, Port: 1089, Label: "group1"}}}

	if err := op.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if !slices.Equal(proxies.removed, []int{1, 2, 7, 9}) {
		t.Fatalf("сняты прокси %v, want [1 2 7 9] (туннели, подписка, группа)", proxies.removed)
	}
}

// Нечитаемый 10-tunnels.json не мешает снять прокси подписок.
func TestOperator_Cleanup_BrokenTunnelsFileStillRemovesSubscriptionProxies(t *testing.T) {
	dir := t.TempDir()
	binary, _ := seedInstallation(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "config.d", "10-tunnels.json"), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	op := NewOperator(OperatorDeps{Dir: dir, Binary: binary})
	proxies := &fakeProxies{}
	op.proxyMgr = proxies
	op.subProxies = &staticSubProxies{list: []SubscriptionProxy{{Index: 7, Port: 1087, Label: "sub1"}}}

	if err := op.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if !slices.Equal(proxies.removed, []int{7}) {
		t.Fatalf("сняты прокси %v, want [7]", proxies.removed)
	}
}
