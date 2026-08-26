// Сквозной прогон нового мира: единственный тест волны, где собраны
// НАСТОЯЩИЕ instancestore + Seed + exitreg.Registry + exitreg.StoreMirror +
// настоящий стор туннелей. Фейками остаются только процессы (инстансы) и
// NDMS-уборщик — всё остальное работает так же, как на роутере.
//
// Внешний пакет (manager_test) намеренно: цепочка обязана складываться из
// экспортированной поверхности, как её собирает композиционный корень.
package manager_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/exitreg"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/manager"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// oldWdttJSON — форма старого конфига (задача 3): raw-клиент с пином
// OpkgTun18 и сервер с обеими половинами. Это путь АПГРЕЙДА: файл написан
// умершим пакетом internal/wdtt.
const oldWdttJSON = `{
  "clients": [{"id":"default","name":"Клиент","config":{
    "enabled":true,"listen":"127.0.0.1:9000","peer":"9.9.9.9:1",
    "password":"pw","vkHashes":"h","workers":24,"connMode":"raw",
    "peerWg":"1.1.1.1:56000","peerRaw":"2.2.2.2:56003","sub":"https://wsub",
    "ndmsIface":"OpkgTun18","rawIface":"opkgtun18",
    "rawClientIp":"10.70.0.5","rawClientMTU":1300,
    "policyPermits":[{"name":"Policy1","order":1},{"name":"Policy2","order":2}]}}],
  "servers": [{"id":"default","name":"Сервер","config":{
    "enabled":false,"listen":"0.0.0.0:56002","password":"spw",
    "relayMode":"raw","natMode":"full","debug":true,
    "linkPeer":"77.1.2.3:56002","linkVkHashes":"lvh","statsLog":"disk",
    "clients":[{"password":"u1","comment":"Петя","expiresAt":42,"auto":false},
               {"password":"u2","comment":"","auto":true}],
    "ndmsIface":"OpkgTun20","wgIface":"opkgtun20",
    "rawNdmsIface":"OpkgTun21","rawIface":"opkgtun21"}}]
}`

// e2eJournal — журнал обоих потребителей сразу: exitreg.Journal и
// manager.Journal — интерфейсы одной формы. Строки нужны на отказе: запертый
// гейт сертификации сообщает о себе ТОЛЬКО через журнал.
type e2eJournal struct {
	mu   sync.Mutex
	rows []string
}

func (j *e2eJournal) Info(action, target, message string) { j.add("info", action, target, message) }
func (j *e2eJournal) Warn(action, target, message string) { j.add("warn", action, target, message) }

func (j *e2eJournal) add(level, action, target, message string) {
	j.mu.Lock()
	j.rows = append(j.rows, level+" "+action+" "+target+": "+message)
	j.mu.Unlock()
}

func (j *e2eJournal) dump() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return strings.Join(j.rows, "\n")
}

// e2eInstance — процесс. Единственный фейк по существу: запускать настоящие
// бинари роутера тест не может.
type e2eInstance struct{}

func (*e2eInstance) Start(context.Context)       {}
func (*e2eInstance) Post(proxyrt.EventKind) bool { return true }
func (*e2eInstance) ResetStartBackoff()          {}
func (*e2eInstance) Stop()                       {}

// e2eSweeper — уборщик NDMS-интерфейсов: ходит в RCI, поэтому фейк.
type e2eSweeper struct{}

func (e2eSweeper) Sweep(context.Context, map[string]bool) ([]string, error) { return nil, nil }

func (e2eSweeper) OwnedNames(context.Context) ([]string, error) { return nil, nil }

// keepPin — «выдай запрошенное». Отказ на непинованном запросе не украшение:
// перепин на этом прогоне означал бы, что архитектура фикстуры или диапазон
// индексов разъехались, и молча выданный новый индекс увёл бы прогон мимо
// проверяемой цепочки — вместо цепочки проверялась бы аллокация.
func keepPin(owner string, pinned int, havePin bool) (int, error) {
	if !havePin {
		return 0, errors.New("посев не сохранил пин владельца " + owner)
	}
	return pinned, nil
}

func saveTunnel(t *testing.T, st *storage.AWGTunnelStore, rec *storage.AWGTunnel) {
	t.Helper()
	if err := st.Save(rec); err != nil {
		t.Fatal(err)
	}
}

func TestBootSeedsDeclaresMirrorsAndDeleteRemoves(t *testing.T) {
	oldDir := t.TempDir()
	wdttPath := filepath.Join(oldDir, "wdtt.json")
	if err := os.WriteFile(wdttPath, []byte(oldWdttJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	tunDir := t.TempDir()
	// S2: только WithLockDir — иначе тест берёт настоящий /opt/var/lock.
	tunStore := storage.NewAWGTunnelStoreWithLockDir(tunDir, filepath.Join(tunDir, "locks"))

	// Запись, доставшаяся от старого мира: имя и эндпоинт протухли, адрес —
	// кэш факта, снятый умершим наложением-на-чтении, а PingCheck настроил
	// пользователь и потерять его нельзя.
	saveTunnel(t, tunStore, &storage.AWGTunnel{
		ID: "wdttraw-default", Name: "Старое имя wdtt", Type: "awg",
		Backend: "wdtt-raw", WdttClientID: "default",
		RawNdmsIface: "OpkgTun18", RawKernelIface: "opkgtun18",
		Interface: storage.AWGInterface{Address: "10.70.0.9/32", MTU: 1300},
		Peer: storage.AWGPeer{Endpoint: "1.1.1.1:56000",
			AllowedIPs: []string{"0.0.0.0/0"}},
		PingCheck: &storage.TunnelPingCheck{Enabled: true, Method: "icmp",
			Target: "1.0.0.1", Interval: 30, FailThreshold: 3},
	})
	// Ничейная запись: инстанса с таким id нет ни в старом конфиге, ни в
	// новом store — её обязана снести уборка.
	saveTunnel(t, tunStore, &storage.AWGTunnel{ID: "wdttraw-ghost",
		Name: "Призрак wdtt", Type: "awg", Backend: "wdtt-raw",
		WdttClientID: "ghost"})

	jrnl := &e2eJournal{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Log("журнал прогона:\n" + jrnl.dump())
		}
	})

	mirror := exitreg.NewStoreMirror(tunStore, nil)
	reg := exitreg.New(mirror, jrnl)
	instStore := instancestore.New(t.TempDir())

	seedDeps := instancestore.SeedDeps{
		WdttPath: wdttPath,
		// Файла нет — законная чистая установка второй подсистемы.
		FreeturnPath: filepath.Join(oldDir, "freeturn.json"),
		RuntimeDir:   t.TempDir(),
		LivePermits:  func(context.Context, string) ([]string, error) { return nil, nil },
		AllocIndex:   keepPin,
		GOARCH:       "arm64",
	}

	postSeedCalls := 0
	m := manager.New(manager.Deps{
		Store:    instStore,
		Registry: reg,
		Sweeper:  e2eSweeper{},
		Journal:  jrnl,
		Factory: func(instancestore.Record, *manager.Live) (manager.RunningInstance, error) {
			return &e2eInstance{}, nil
		},
		Seed: func(ctx context.Context) (instancestore.SeedResult, error) {
			return instancestore.Seed(ctx, instStore, seedDeps)
		},
		// Добивание старого поколения и снос его правил — фейк (no-op):
		// первое ходит в kill(2), второе в iptables. Обнуление адресов —
		// настоящее: его-то цепочка и проверяет.
		PostSeed: func(_ context.Context, res instancestore.SeedResult, _ map[string]bool) error {
			postSeedCalls++
			if !res.SeededNow {
				t.Errorf("на пути апгрейда PostSeed обязан видеть свежий посев: %+v", res)
			}
			_, err := mirror.ZeroStaleAddresses()
			return err
		},
		AllocIndex:   keepPin,
		AllocListen:  func(string) (string, error) { return "127.0.0.1:9100", nil },
		ReleasePins:  func(...string) {},
		WaitDisabled: func(string, time.Duration) bool { return true },
	})

	if err := m.Boot(context.Background()); err != nil {
		t.Fatalf("боот: %v", err)
	}

	// Проверки независимы, поэтому Errorf, а не Fatalf: неполная ведомость
	// роняет сразу несколько звеньев, и видеть надо все.

	// (1) Выход разрешается реестром — с пинами, пришедшими из старого файла.
	if info, ok := reg.Lookup("wdttraw-default"); !ok {
		t.Error("выход wdttraw-default не разрешается реестром: правила пользователя указывают в никуда")
	} else if info.NDMSName != "OpkgTun18" || info.KernelIface != "opkgtun18" {
		t.Errorf("пины посева не доехали до реестра: %+v", info)
	}

	// (2) Зеркальная запись приведена к объявлению, пользовательские поля целы.
	if rec, err := tunStore.Get("wdttraw-default"); err != nil {
		t.Errorf("живой зеркальной записи нет (%v) — так выглядит снос уборкой по НЕПОЛНОЙ ведомости", err)
	} else {
		if want := exitreg.MirrorName("Клиент"); rec.Name != want {
			t.Errorf("имя записи %q, ждали %q (паритет с прежним миром)", rec.Name, want)
		}
		if rec.Peer.Endpoint != "2.2.2.2:56003" {
			t.Errorf("эндпоинт записи %q: слот raw старого конфига не доехал", rec.Peer.Endpoint)
		}
		if rec.PingCheck == nil || !rec.PingCheck.Enabled || rec.PingCheck.Target != "1.0.0.1" {
			t.Errorf("настройки pingcheck не пережили обновление записи: %+v", rec.PingCheck)
		}
		if rec.Interface.Address != "" {
			t.Errorf("адрес записи %q: протухший кэш обязан быть обнулён (требование 13)", rec.Interface.Address)
		}
	}

	// (3) Призрак снесён: гейт сертификации открылся и уборка отработала.
	if tunStore.Exists("wdttraw-ghost") {
		t.Error("призрак wdttraw-ghost жив: гейт сертификации заперт либо уборка не звалась")
	}

	// (4) Посев сертифицирован.
	if si := m.SeedInfo(); !si.Booted || !si.Certified {
		t.Errorf("SeedInfo = %+v, ждали загруженный и сертифицированный посев", si)
	}
	if postSeedCalls != 1 {
		t.Errorf("уборочные шаги посева вызваны %d раз, ждали 1", postSeedCalls)
	}

	// Удаление инстанса снимает выход с объявления и уносит его запись.
	if err := m.Delete(context.Background(), "wdtt-client:default"); err != nil {
		t.Fatalf("удаление инстанса: %v", err)
	}
	if _, ok := reg.Lookup("wdttraw-default"); ok {
		t.Error("инстанс удалён, а реестр всё ещё разрешает его выход")
	}
	if tunStore.Exists("wdttraw-default") {
		t.Error("зеркальная запись пережила удаление инстанса")
	}
}
