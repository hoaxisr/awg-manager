package exitreg_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/exitreg"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/linkres"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/wdttclient"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// stubProvider — routing.TunnelProvider, который ничего не знает: выход
// обязан разрешаться реестром, а не наблюдением за туннелями. GetState отдаёт
// StateUnknown — иначе «выключенный выход не running» проверял бы состояние
// туннеля, а не готовность выхода.
type stubProvider struct{}

func (stubProvider) ListTunnels(context.Context) ([]routing.TunnelWithStatus, error) {
	return nil, nil
}
func (stubProvider) GetState(context.Context, string) tunnel.StateInfo { return tunnel.StateInfo{} }
func (stubProvider) WANModel() *wan.Model                              { return nil }

// stubStore — routing.StoreClient пустого хранилища. S4: Exists обязан
// отвечать false, иначе финальная проверка cat.Exists упала бы на заглушке, а
// не на снятом выходе; Get по той же причине отдаёт ошибку.
type stubStore struct{}

func (stubStore) Get(string) (routing.StoreEntry, error) {
	return routing.StoreEntry{}, errors.New("нет записи")
}
func (stubStore) Exists(string) bool { return false }

// regAdapter — та же склейка, что в композиционном корне (задача 6).
type regAdapter struct{ reg *exitreg.Registry }

func (a regAdapter) LookupExit(id string) (routing.ExitEntry, bool) {
	info, ok := a.reg.Lookup(id)
	if !ok {
		return routing.ExitEntry{}, false
	}
	return routing.ExitEntry{NDMSName: info.NDMSName, KernelIface: info.KernelIface, Ready: info.Ready}, true
}

func TestExitIDParityWithOldWorld(t *testing.T) {
	// На эти id ссылаются пользовательские правила маршрутизации — разъезд
	// генератора ломает их молча. Сверять больше не с чем: internal/wdtt
	// снесён, поэтому ожидания сняты с него до сноса и вбиты литералами.
	cases := map[string]string{
		"": "wdttraw-default",
		// Строка из одних пробелов — тоже «пусто»: без TrimSpace регулярка
		// съела бы пробелы в дефис и id разъехался бы с прежним миром.
		"   ":     "wdttraw-default",
		" de ":    "wdttraw-de",
		"default": "wdttraw-default",
		"de":      "wdttraw-de",
		"-de-":    "wdttraw-de",
		"a-b_c":   "wdttraw-a-b_c",
		"a b":     "wdttraw-a-b",
		// Небезопасные символы съедаются целиком — остаётся заглушка.
		"Германия": "wdttraw-client",
		"!!!":      "wdttraw-client",
		"--":       "wdttraw-client",
		"очень-длинное-имя-инстанса-которое-точно-длиннее-двадцати": "wdttraw-client",
		// Обрезка по 20 символам — только на латинице: кириллица до неё
		// не доживает, и без этого случая правило было бы не покрыто.
		"abcdefghijklmnopqrstuvwxyz": "wdttraw-abcdefghijklmnopqrst",
	}
	for in, want := range cases {
		if got := wdttclient.RawTunnelID(in); got != want {
			t.Fatalf("RawTunnelID(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}

func TestDeclaredExitBecomesResolvableAndMirrored(t *testing.T) {
	dir := t.TempDir()
	// S2: только WithLockDir — иначе тест берёт настоящий /opt/var/lock.
	st := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	// Настоящий ScopedLogger с nil-логгером: заодно проверка, что тип из
	// internal/logging удовлетворяет exitreg.Journal — на этом стоит проводка.
	reg := exitreg.New(
		exitreg.NewStoreMirror(st, nil),
		logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubLifecycle),
	)

	cfg := roles.WdttClientConfig{Mode: "raw", Name: "Германия", Listen: "127.0.0.1:9000",
		Peer: "1.2.3.4:56000", Password: "p", VKHashes: "h",
		NdmsIface: "OpkgTun18", RawIface: "opkgtun18"}
	if err := reg.SetDeclared(exitreg.DeclaredExits([]exitreg.InstanceConfig{
		{ID: "de", Cfg: cfg, Enabled: false}, // ВЫКЛЮЧЕН намеренно
	})); err != nil {
		t.Fatal(err)
	}

	id := wdttclient.RawTunnelID("de")

	// Зеркальная запись легла на диск сама, без единого HTTP-вызова.
	rec, err := st.Get(id)
	if err != nil {
		t.Fatalf("зеркальная запись не создана: %v", err)
	}
	if rec.Backend != "wdtt-raw" || rec.WdttClientID != "de" || rec.RawKernelIface != "opkgtun18" {
		t.Fatalf("запись собрана неверно: %+v", rec)
	}

	// Каталог разрешает ВЫКЛЮЧЕННЫЙ выход в имя (§5) и не считает его живым.
	cat := routing.NewCatalog(&stubProvider{}, nil, &stubStore{}, regAdapter{reg: reg}, nil)
	if got, err := cat.ResolveInterface(context.Background(), id); err != nil || got != "OpkgTun18" {
		t.Fatalf("ResolveInterface = %q, %v", got, err)
	}
	if _, running := cat.GetKernelIface(context.Background(), id); running {
		t.Fatal("выключенный выход не может быть running")
	}

	// Процесс дошёл до готовности — тот же выход становится проходимым.
	if err := reg.Ensure(linkres.ExitInfo{ID: id, NDMSName: "OpkgTun18",
		KernelIface: "opkgtun18", Ready: true}); err != nil {
		t.Fatal(err)
	}
	if iface, running := cat.GetKernelIface(context.Background(), id); iface != "opkgtun18" || !running {
		t.Fatalf("готовый выход: %q, %v", iface, running)
	}

	// ГЕЙТ ПОСЕВА (В9) на настоящем хранилище: инстанс снят с объявления, но
	// посев не подтверждён — файл обязан остаться на диске.
	if err := reg.SetDeclared(nil); err != nil {
		t.Fatal(err)
	}
	if !st.Exists(id) {
		t.Fatal("до подтверждения посева уборка не имеет права удалять записи")
	}
	if _, ok := reg.Lookup(id); ok {
		t.Fatal("гейт запирает удаление ФАЙЛА, а не снятие выхода с объявления")
	}

	// Посев подтверждён — то же снятие доводится до конца.
	if err := reg.MarkSeeded(1); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetDeclared(nil); err != nil {
		t.Fatal(err)
	}
	if st.Exists(id) {
		t.Fatal("зеркальная запись снятого выхода обязана быть удалена")
	}
	if list, err := st.List(); err != nil || len(list) != 0 {
		t.Fatalf("снятая запись не имеет права оставаться в списке: %v, %v", list, err)
	}
	if cat.Exists(context.Background(), id) {
		t.Fatal("снятый выход не существует")
	}

	// В8 отменено: рядом с удалённой записью НЕ остаётся ничего — ни
	// карантинного файла, ни резервной копии.
	files, err := filepath.Glob(filepath.Join(dir, id+".json*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("после удаления файлов записи остаться не должно: %v, %v", files, err)
	}
}
