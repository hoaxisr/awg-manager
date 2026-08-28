package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instance"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// Стык менеджера с НАСТОЯЩИМИ уборщиком и аллокатором. Тесты выше ходят через
// fakeSweeper, который аллокатора не спрашивает, а сам Sweeper проверяется без
// менеджера — и дефект жил ровно между ними: Delete звал уборку ДО возврата
// пинов, уборщик видел номер закреплённым и пропускал ту самую запись, ради
// которой вызван (стенд 2026-08-28, OpkgTun0/1 пережили удаление инстанса).

// scanOf — сканер NDMS: отдаёт то, что ему положили, с нашей клиентской меткой.
type scanOf struct{ names []string }

func (s *scanOf) Scan(context.Context, []string) ([]proxyrt.OwnedResource, error) {
	out := make([]proxyrt.OwnedResource, 0, len(s.names))
	for _, n := range s.names {
		out = append(out, proxyrt.OwnedResource{Label: roles.ClientDescription("Имя"), Name: n})
	}
	return out, nil
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

// opkgIndexOf — разбор имени в номер. Копия прод-адаптера (opkgTunIndex в
// cmd/awg-manager): тот живёт в package main, и импортировать его отсюда
// нечем. Отрицательные отвергаются, как и там.
func opkgIndexOf(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, "OpkgTun")
	if !ok {
		return 0, false
	}
	idx, err := strconv.Atoi(rest)
	if err != nil || idx < 0 {
		return 0, false
	}
	return idx, true
}

// liveEnv — окружение со сквозной связкой аллокатор → пины → уборщик.
type liveEnv struct {
	m     *Manager
	st    *instancestore.Store
	dir   string
	sc    *scanOf
	rm    *recRemover
	alloc *proxyrt.Allocator
	j     *recJournal
	// changed — причины уведомлений о смене состава записей.
	changed []string
}

func newLiveEnv(t *testing.T) *liveEnv {
	t.Helper()
	dir := t.TempDir()
	sc := &scanOf{}
	rm := &recRemover{sc: sc}
	alloc := proxyrt.NewAllocator(proxyrt.IndexRange{Min: roles.OpkgIndexMin, Max: roles.OpkgIndexMax})
	e := &liveEnv{st: instancestore.New(dir), dir: dir, sc: sc, rm: rm, alloc: alloc, j: &recJournal{}}
	e.m = New(Deps{
		Store:    e.st,
		Registry: &fakeRegistry{},
		Sweeper: proxyrt.NewSweeper(sc, rm, alloc,
			instance.SweepLabels(), opkgIndexOf),
		Journal: e.j,
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
		PostSeed: func(context.Context, instancestore.SeedResult, map[string]bool) error { return nil },
		AllocIndex: func(owner string, pinned int, havePin bool) (int, error) {
			if !havePin {
				pinned = -1
			}
			// Занятость снаружи аллокатора тесту не нужна: интерфейсов роутера
			// здесь нет, а пины владельцев держит сам аллокатор.
			return alloc.AllocIndex(owner, pinned, nil)
		},
		AllocListen: func(string) (string, error) { return "127.0.0.1:9007", nil },
		ReleasePins: func(keys ...string) {
			for _, k := range keys {
				alloc.Release(k)
			}
		},
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
		WdttClient: &roles.WdttClientConfig{Mode: "raw", Peer: "1.1.1.1:1",
			Password: "pw", VKHashes: "h"}}
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
	idx, err := e.alloc.AllocIndex("другой", -1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("OpkgTun%d", idx); got != c.NdmsIface {
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
			WdttServer: &roles.WdttServerConfig{Listen: "0.0.0.0:56000", Password: "pw",
				ConfigDir: cfgDir, NdmsIface: "OpkgTun20", WgIface: "opkgtun20",
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
				WdttServer: &roles.WdttServerConfig{Listen: "0.0.0.0:56000", Password: "pw",
					ConfigDir: dir, NdmsIface: "OpkgTun20", WgIface: "opkgtun20",
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
			WdttServer: &roles.WdttServerConfig{Listen: "0.0.0.0:56000", Password: "pw",
				ConfigDir: foreign, NdmsIface: "OpkgTun20", WgIface: "opkgtun20",
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
