package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/opkgtun"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instance"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// Стык менеджера с НАСТОЯЩИМ уборщиком. Тесты выше ходят через fakeSweeper, а
// сам Sweeper проверяется без менеджера — и дефект жил ровно между ними:
// Delete звал уборку ДО возврата пинов, уборщик видел номер закреплённым в
// аллокаторе и пропускал ту самую запись, ради которой вызван (стенд
// 2026-08-28, OpkgTun0/1 пережили удаление инстанса). Консультации с
// аллокатором больше нет, и стык стережёт исходы сноса; сам порядок
// «скан → ведомость → снос» стерегут крючковые тесты в manager_test.go.

// scanOf — сканер NDMS: отдаёт то, что ему положили, с нашей клиентской меткой.
type scanOf struct{ names []string }

func (s *scanOf) Scan(context.Context, []string) ([]proxyrt.OwnedResource, error) {
	out := make([]proxyrt.OwnedResource, 0, len(s.names))
	for _, n := range s.names {
		out = append(out, proxyrt.OwnedResource{Label: roles.ClientDescription("Имя"), Name: n})
	}
	return out, nil
}

// recordIfaces — NDMS-имена записи по полю. Копия прод-адаптера
// (proxyRecordIfaces в cmd/awg-manager): тот живёт в package main, и
// импортировать его отсюда нечем.
//
// СРЕЗ, а не карта, по той же причине, что и в проде: у битой записи сервера
// оба имени могут совпасть, и победитель на карте определялся бы обходом, то
// есть менялся от запуска к запуску.
func recordIfaces(rec instancestore.Record) []struct{ field, iface string } {
	switch {
	case rec.WdttClient != nil:
		return []struct{ field, iface string }{{iface: rec.WdttClient.NdmsIface}}
	case rec.WdttServer != nil:
		return []struct{ field, iface string }{
			{field: "wg", iface: rec.WdttServer.NdmsIface},
			{field: "raw", iface: rec.WdttServer.RawNdmsIface},
		}
	}
	return nil
}

// recRemover — снос с записью снесённого и вычёркиванием из скана.
type recRemover struct {
	sc      *scanOf
	removed []string
}

func (r *recRemover) Remove(_ context.Context, res proxyrt.OwnedResource) error {
	r.removed = append(r.removed, res.Name)
	kept := r.sc.names[:0]
	for _, n := range r.sc.names {
		if n != res.Name {
			kept = append(kept, n)
		}
	}
	r.sc.names = kept
	return nil
}

// liveEnv — окружение со сквозной связкой аллокатор → пины → уборщик.
type liveEnv struct {
	m    *Manager
	st   *instancestore.Store
	dir  string
	sc   *scanOf
	rm   *recRemover
	pool *opkgtun.Pool
	j    *recJournal
	// changed — причины уведомлений о смене состава записей.
	changed []string
}

func newLiveEnv(t *testing.T) *liveEnv {
	t.Helper()
	dir := t.TempDir()
	sc := &scanOf{}
	rm := &recRemover{sc: sc}
	// Пул поверх ЖИВЫХ записей стора: именно он отвечает на вопрос, вернулся
	// ли номер удалённого инстанса в оборот.
	pool := opkgtun.NewPool(49, opkgtun.Source{
		Name: "записи прокси",
		Read: func(context.Context) (opkgtun.Taken, error) {
			st, err := instancestore.New(dir).Load()
			if err != nil {
				return nil, err
			}
			out := opkgtun.Taken{}
			for _, rec := range st.Records {
				for _, half := range recordIfaces(rec) {
					if idx, ok := opkgtun.IndexOf(half.iface); ok {
						out[idx] = opkgtun.ProxyHolder(rec.Key(), half.field, rec.Name)
					}
				}
			}
			return out, nil
		},
	})
	e := &liveEnv{st: instancestore.New(dir), dir: dir, sc: sc, rm: rm, pool: pool, j: &recJournal{}}
	e.m = New(Deps{
		Store:    e.st,
		Registry: &fakeRegistry{},
		Sweeper:  proxyrt.NewSweeper(sc, rm, instance.SweepLabels()),
		Journal:  e.j,
		Factory: func(instancestore.Record, *Live) (RunningInstance, error) {
			return &fakeInstance{}, nil
		},
		Seed: func(context.Context) (instancestore.SeedResult, error) {
			st, err := e.st.Load()
			if err != nil {
				return instancestore.SeedResult{}, err
			}
			return instancestore.SeedResult{State: st, SeededNow: !st.Seeded}, nil
		},
		PostSeed:    func(context.Context, instancestore.SeedResult, map[string]bool) error { return nil },
		OpkgTunPool: pool,
		AllocListen: func(_ string, _ instancestore.Kind, _, current string) (string, error) {
			if current != "" {
				return current, nil
			}
			return "127.0.0.1:9007", nil
		},
		// Номера в ведомость возврата больше не входят: их держит резервация
		// пула, а занятость считается по ЖИВЫМ записям стора.
		ReleasePins:    func(...string) {},
		WaitDisabled:   func(string, time.Duration) bool { return true },
		RecordsChanged: func(reason string) { e.changed = append(e.changed, reason) },
	})
	return e
}

func TestDeleteSweepsInterfaceOfDeletedInstance(t *testing.T) {
	e := newLiveEnv(t)
	if _, err := e.st.Replace(func(st *instancestore.State) error {
		st.SeededFrom = []string{"test"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Без пинов: номер выдаёт аллокатор — именно он и остаётся закреплённым
	// на момент уборки.
	rec := instancestore.Record{ID: "de", Kind: instancestore.KindWdttClient,
		Name: "Имя", Enabled: true,
		WdttClient: &roles.WdttClientConfig{Mode: "raw", Peer: "1.1.1.1:1", VKHashes: "h"}}
	if err := e.m.Create(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	st, _ := e.st.Load()
	c, _ := st.Records[0].WdttClientConfig()
	e.sc.names = []string{c.NdmsIface} // интерфейс создан на роутере

	if err := e.m.Delete(context.Background(), "wdtt-client:de"); err != nil {
		t.Fatal(err)
	}
	if len(e.rm.removed) != 1 || e.rm.removed[0] != c.NdmsIface {
		t.Fatalf("снесено %v, ждали [%s]: иначе запись NDMS переживает инстанс и съедает индекс",
			e.rm.removed, c.NdmsIface)
	}
	// Индекс обязан вернуться в оборот: следующий владелец получает тот же.
	res, err := e.pool.Reserve(context.Background(),
		opkgtun.Want(opkgtun.ProxyHolder("wdtt-client:другой", "", "другой")))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	if got := fmt.Sprintf("OpkgTun%d", res.Numbers()[0]); got != c.NdmsIface {
		t.Fatalf("следующий номер %s, ждали освободившийся %s", got, c.NdmsIface)
	}
}

func TestDeleteSweepKeepsInterfaceOfSurvivor(t *testing.T) {
	// Обратная сторона: возврат пинов не должен открыть уборщику соседа.
	e := newLiveEnv(t)
	if _, err := e.st.Replace(func(st *instancestore.State) error {
		st.Records = append(st.Records,
			rawRec("de", "OpkgTun18", "opkgtun18"), rawRec("dv", "OpkgTun19", "opkgtun19"))
		st.SeededFrom = []string{"test"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.sc.names = []string{"OpkgTun18", "OpkgTun19"}

	if err := e.m.Delete(context.Background(), "wdtt-client:de"); err != nil {
		t.Fatal(err)
	}
	if len(e.rm.removed) != 1 || e.rm.removed[0] != "OpkgTun18" {
		t.Fatalf("снесено %v, ждали ровно [OpkgTun18]", e.rm.removed)
	}
}

func TestDeleteRemovesDataDirOfServer(t *testing.T) {
	e := newLiveEnv(t)
	cfgDir := filepath.Join(e.dir, "wdtt", "server", "srv")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "passwords.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Replace(func(st *instancestore.State) error {
		st.Records = append(st.Records, instancestore.Record{
			ID: "srv", Kind: instancestore.KindWdttServer, Name: "S", Enabled: true,
			WdttServer: &roles.WdttServerConfig{Listen: "0.0.0.0:56000", ConfigDir: cfgDir, NdmsIface: "OpkgTun20", WgIface: "opkgtun20",
				RawNdmsIface: "OpkgTun21", RawIface: "opkgtun21"}})
		st.SeededFrom = []string{"test"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Delete(context.Background(), "wdtt-server:srv"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfgDir); !os.IsNotExist(err) {
		t.Fatalf("каталог данных пережил удаление инстанса: %v", err)
	}
}

func TestDeleteKeepsDataDirItself(t *testing.T) {
	// configDir правится через API как обычная строка. Указанный на САМ
	// каталог данных, он снёс бы всё приложение: записи инстансов, туннели,
	// настройки. Равенство путей — не «внутри».
	e := newLiveEnv(t)
	marker := filepath.Join(e.dir, "proxy-instances.json")
	for _, dir := range []string{e.dir, filepath.Join(e.dir, "wdtt")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.Replace(func(st *instancestore.State) error {
			st.Records = []instancestore.Record{{
				ID: "srv", Kind: instancestore.KindWdttServer, Name: "S", Enabled: true,
				WdttServer: &roles.WdttServerConfig{Listen: "0.0.0.0:56000", ConfigDir: dir, NdmsIface: "OpkgTun20", WgIface: "opkgtun20",
					RawNdmsIface: "OpkgTun21", RawIface: "opkgtun21"}}}
			st.SeededFrom = []string{"test"}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := e.m.Boot(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := e.m.Delete(context.Background(), "wdtt-server:srv"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("снесён %s: %v", dir, err)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("снесён каталог данных целиком (%s): %v", marker, err)
		}
	}
}

func TestDeleteKeepsDataPathOutsideDataDir(t *testing.T) {
	// Путь списка задаёт пользователь: увёл его наружу каталога данных — файл
	// не наш, и сносить его удаление инстанса права не имеет.
	e := newLiveEnv(t)
	outside := filepath.Join(t.TempDir(), "clients.json")
	if err := os.WriteFile(outside, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Replace(func(st *instancestore.State) error {
		st.Records = append(st.Records, instancestore.Record{
			ID: "fts", Kind: instancestore.KindFreeTurnServer, Name: "F", Enabled: true,
			FreeTurnServer: &roles.FreeTurnServerConfig{Listen: "0.0.0.0:7000", ClientsFile: outside}})
		st.SeededFrom = []string{"test"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Delete(context.Background(), "freeturn-server:fts"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("снесён файл вне каталога данных: %v", err)
	}
}

func TestDeleteRemovesAllowlistLeftByDisable(t *testing.T) {
	// ftlink.Disable снимает clientsFile с конфига, файл оставляя. По одному
	// конфигу он переставал быть виден навсегда — уборка знает и путь по
	// умолчанию.
	e := newLiveEnv(t)
	orphan := instancestore.FreeTurnAllowlistPath(e.dir, "fts")
	if err := os.MkdirAll(filepath.Dir(orphan), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphan, []byte(`{"clients":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Replace(func(st *instancestore.State) error {
		st.Records = append(st.Records, instancestore.Record{
			ID: "fts", Kind: instancestore.KindFreeTurnServer, Name: "F", Enabled: true,
			FreeTurnServer: &roles.FreeTurnServerConfig{Listen: "0.0.0.0:7000"}}) // список выключен
		st.SeededFrom = []string{"test"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Delete(context.Background(), "freeturn-server:fts"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("список разрешённых пережил удаление инстанса: %v", err)
	}
}

func TestDeleteKeepsDataPathOutsideOwnSubtree(t *testing.T) {
	// Внутри каталога данных, но не в своём поддереве: там живут туннели,
	// настройки и данные СОСЕДНИХ подсистем — сносить их удаление инстанса
	// права не имеет.
	e := newLiveEnv(t)
	foreign := filepath.Join(e.dir, "tunnels")
	if err := os.MkdirAll(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Replace(func(st *instancestore.State) error {
		st.Records = append(st.Records, instancestore.Record{
			ID: "srv", Kind: instancestore.KindWdttServer, Name: "S", Enabled: true,
			WdttServer: &roles.WdttServerConfig{Listen: "0.0.0.0:56000", ConfigDir: foreign, NdmsIface: "OpkgTun20", WgIface: "opkgtun20",
				RawNdmsIface: "OpkgTun21", RawIface: "opkgtun21"}})
		st.SeededFrom = []string{"test"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Delete(context.Background(), "wdtt-server:srv"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("снесён чужой каталог внутри каталога данных: %v", err)
	}
}

func TestDeleteKeepsDataClaimedByNeighbour(t *testing.T) {
	// Имя файла по умолчанию строится из ID, где недопустимые символы
	// заменяются подчёркиванием: «ft x» и «ft_x» дают ОДИН путь. Удаление
	// первого не имеет права унести живой список второго.
	e := newLiveEnv(t)
	shared := instancestore.FreeTurnAllowlistPath(e.dir, "ft_x")
	if err := os.MkdirAll(filepath.Dir(shared), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, []byte(`{"clients":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Replace(func(st *instancestore.State) error {
		st.Records = append(st.Records,
			instancestore.Record{ID: "ft x", Kind: instancestore.KindFreeTurnServer, Name: "A",
				FreeTurnServer: &roles.FreeTurnServerConfig{Listen: "0.0.0.0:7001"}},
			instancestore.Record{ID: "ft_x", Kind: instancestore.KindFreeTurnServer, Name: "B",
				FreeTurnServer: &roles.FreeTurnServerConfig{Listen: "0.0.0.0:7002", ClientsFile: shared}})
		st.SeededFrom = []string{"test"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Delete(context.Background(), "freeturn-server:ft x"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(shared); err != nil {
		t.Fatalf("снесён список живого соседа: %v", err)
	}
}

func TestDeleteSaysNothingAboutMissingData(t *testing.T) {
	// Путь по умолчанию есть у КАЖДОГО freeturn-сервера, а список мог не
	// включаться ни разу: RemoveAll на отсутствующем молчит, и «данные
	// удалены» в журнале было бы неправдой.
	e := newLiveEnv(t)
	if _, err := e.st.Replace(func(st *instancestore.State) error {
		st.Records = append(st.Records, instancestore.Record{
			ID: "nofile", Kind: instancestore.KindFreeTurnServer, Name: "F",
			FreeTurnServer: &roles.FreeTurnServerConfig{Listen: "0.0.0.0:7003"}})
		st.SeededFrom = []string{"test"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Delete(context.Background(), "freeturn-server:nofile"); err != nil {
		t.Fatal(err)
	}
	for _, msg := range e.j.journalMsgs() {
		if strings.Contains(msg, "данные удалены") {
			t.Fatalf("журнал врёт про снос несуществующего: %q", msg)
		}
	}
}

func TestCreateAndDeleteNotifyRecordsChanged(t *testing.T) {
	// Уведомление живёт в менеджере, а не в HTTP-обработчике: запись создаёт
	// и импорт ссылки, идущий тем же путём мимо ручки инстансов.
	e := newLiveEnv(t)
	if _, err := e.st.Replace(func(st *instancestore.State) error {
		st.SeededFrom = []string{"test"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.changed) != 0 {
		t.Fatalf("боот не меняет состав записей: %v", e.changed)
	}
	if err := e.m.Create(context.Background(), rawRec("de", "OpkgTun18", "opkgtun18")); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Delete(context.Background(), "wdtt-client:de"); err != nil {
		t.Fatal(err)
	}
	if len(e.changed) != 2 || e.changed[0] != "created" || e.changed[1] != "deleted" {
		t.Fatalf("уведомления: %v (ждали [created deleted])", e.changed)
	}
}

func TestDeleteRemovesEnabledAllowlistOnce(t *testing.T) {
	// Самый частый случай: список включён и лежит по умолчанию — оба пути
	// указывают на один файл. Снос обязан быть один, и запись в журнале одна.
	e := newLiveEnv(t)
	path := instancestore.FreeTurnAllowlistPath(e.dir, "ftlive")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"clients":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Replace(func(st *instancestore.State) error {
		st.Records = append(st.Records, instancestore.Record{
			ID: "ftlive", Kind: instancestore.KindFreeTurnServer, Name: "F",
			FreeTurnServer: &roles.FreeTurnServerConfig{Listen: "0.0.0.0:7004", ClientsFile: path}})
		st.SeededFrom = []string{"test"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Delete(context.Background(), "freeturn-server:ftlive"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("список не удалён: %v", err)
	}
	n := 0
	for _, msg := range e.j.journalMsgs() {
		if strings.Contains(msg, "данные удалены") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("записей о сносе %d, ждали 1: оба пути ведут в один файл", n)
	}
}

func TestCreateRefusesBadIDBeforePersist(t *testing.T) {
	// Отказ обязан приходить ДО записи: раньше идентификатор проверялся при
	// сборке пути сокета, и запись с пробелом оставалась на диске навсегда —
	// ручка удаления такой ключ не находит (стенд 2026-08-28).
	e := newLiveEnv(t)
	if _, err := e.st.Replace(func(st *instancestore.State) error {
		st.SeededFrom = []string{"test"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Create(context.Background(), rawRec("ft x", "OpkgTun18", "opkgtun18")); err == nil {
		t.Fatal("идентификатор с пробелом обязан отвергаться")
	}
	st, err := e.st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Records) != 0 {
		t.Fatalf("запись легла на диск вопреки отказу: %+v", st.Records)
	}
	if len(e.changed) != 0 {
		t.Fatalf("уведомление об отказанной записи: %v", e.changed)
	}
}

// F146: Delete отдаёт удалённую запись хуку уборки файлов рантайма — ровно
// одну и ровно ту; отказ хука удаления не отменяет.
func TestDeleteCallsRemoveRuntime(t *testing.T) {
	e := newLiveEnv(t)
	if _, err := e.st.Replace(func(st *instancestore.State) error {
		st.Records = append(st.Records, instancestore.Record{
			ID: "srv", Kind: instancestore.KindWdttServer, Name: "S", Enabled: true,
			WdttServer: &roles.WdttServerConfig{Listen: "0.0.0.0:56000", ConfigDir: filepath.Join(e.dir, "wdtt", "server", "srv"),
				NdmsIface: "OpkgTun20", WgIface: "opkgtun20", RawNdmsIface: "OpkgTun21", RawIface: "opkgtun21"}})
		st.SeededFrom = []string{"test"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	var got []string
	e.m.deps.RemoveRuntime = func(rec instancestore.Record) error {
		got = append(got, rec.Key())
		return errors.New("tmpfs read-only")
	}
	if err := e.m.Delete(context.Background(), "wdtt-server:srv"); err != nil {
		t.Fatalf("отказ хука не должен ронять удаление: %v", err)
	}
	if len(got) != 1 || got[0] != "wdtt-server:srv" {
		t.Fatalf("хук уборки рантайма получил %v, ждали ровно [wdtt-server:srv]", got)
	}
}

// Уборка на БООТЕ сквозь настоящий уборщик. Остальные тесты этого файла — про
// удаление, а фейковый уборщик найденное игнорирует: без этого теста боот мог
// бы передавать в снос пустоту, уборка молча перестала бы сносить, и сироты
// копились бы вечно без единого сигнала — ровно тот класс, ради которого
// уборщик и написан.
func TestBootSweepsOrphansThroughRealSweeper(t *testing.T) {
	e := newLiveEnv(t)
	rec := instancestore.Record{ID: "de", Kind: instancestore.KindWdttClient,
		Name: "Имя", Enabled: true,
		WdttClient: &roles.WdttClientConfig{Mode: "raw", Peer: "1.1.1.1:1", VKHashes: "h",
			NdmsIface: "OpkgTun18", RawIface: "opkgtun18"}}
	if _, err := e.st.Replace(func(st *instancestore.State) error {
		st.Records = append(st.Records, rec)
		st.SeededFrom = []string{"test"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// На роутере живут интерфейс объявленного инстанса и сирота.
	e.sc.names = []string{"OpkgTun18", "OpkgTun19"}

	if err := e.m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.rm.removed) != 1 || e.rm.removed[0] != "OpkgTun19" {
		t.Fatalf("снесено %v, ждали [OpkgTun19]: объявленный обязан уцелеть, сирота — уйти",
			e.rm.removed)
	}
}
