package tgwebproxy

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"slices"
	"testing"
)

// Слушатель не может подняться (порт занят) — пользователи всё равно правятся и
// отзываются, движок не поднимается, статус честный, Stop проходит.
// Мутации: applyConfigLocked вместо applyUserEditLocked в любой из правок;
// готовность движков обязательна; поднимать неработающий движок → красный.
func TestUserEdits_PassWhenListenerCannotStart(t *testing.T) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	svc, _, _ := setupTestService(t)
	svc.workers.mtg = NewMtgEngine()
	svc.config.Scenario = ScenarioDirectFakeTLS
	svc.config.DirectPort = ln.Addr().(*net.TCPAddr).Port
	svc.config.Enabled = true // прошлая версия включила, слушатель не поднялся
	if err := svc.StartConfigured(); err == nil {
		t.Fatal("порт занят, а StartConfigured прошёл")
	}

	saved := func() Config {
		t.Helper()
		data, err := os.ReadFile(svc.cfgPath())
		var cfg Config
		if err != nil || json.Unmarshal(data, &cfg) != nil {
			t.Fatalf("конфигурация на диске: %v", err)
		}
		return cfg
	}
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if svc.workers.mtg.IsDirectRunning() || svc.GetStatus().DirectOnline || svc.GetStatus().Running {
			t.Fatalf("%s: статус показывает работающий слушатель", what)
		}
		if mem := svc.GetInternalConfig().ActiveSecrets(); !slices.Equal(saved().ActiveSecrets(), mem) {
			t.Fatalf("%s: диск и память разошлись", what)
		}
	}
	must("CreateUser", svc.CreateUser("гость"))
	users := svc.GetInternalConfig().Users
	guest := users[len(users)-1]
	off := false
	must("выключение пользователя", svc.UpdateUser(guest.ID, "", &off))
	on := true
	must("включение пользователя", svc.UpdateUser(guest.ID, "", &on))
	must("новый секрет пользователя", svc.UpdateUserWithOptions(guest.ID, UpdateUserOptions{RegenerateSecret: true}))
	must("DeleteUser", svc.DeleteUser(guest.ID))
	if slices.Contains(saved().ActiveSecrets(), guest.Secret) {
		t.Fatal("секрет удалённого пользователя остался")
	}
	old := svc.GetInternalConfig().Secret
	must("RotateSecret", svc.RotateSecret())
	must("RevokeLegacySecret", svc.RevokeLegacySecret())
	if slices.Contains(saved().ActiveSecrets(), old) {
		t.Fatal("отозванный секрет остался")
	}
	must("Stop", svc.Stop())
	if saved().Enabled {
		t.Fatal("Stop не сохранён")
	}
}

type failingTproxy struct{ fakeTproxy }

func (f failingTproxy) Restart() error {
	return errors.New("конфигурация не принята")
}

// Работавший движок, не принявший правку, останавливается: со старыми секретами
// он продолжал бы пускать отозванного. Соседний движок правку получает.
// Мутации: не останавливать mtg или tproxy при ошибке → красный.
func TestApplyToRunning_StopsWorkerThatRejectsEdit(t *testing.T) {
	cfg := Config{Enabled: true, Scenario: ScenarioDual, DirectPort: 8443}
	ws, eng := newFakeWorkers()
	_ = ws.ApplyWorkers(cfg)
	ws.mtg = refusingMtg{fakeMtg{eng}}
	if err := ws.ApplyToRunning(cfg, true); err == nil || eng.direct || eng.raw || !eng.web {
		t.Fatalf("mtg: err=%v direct=%v raw=%v web=%v", err, eng.direct, eng.raw, eng.web)
	}

	ws, eng = newFakeWorkers()
	_ = ws.ApplyWorkers(cfg)
	ws.tproxy = failingTproxy{fakeTproxy{eng}}
	if err := ws.ApplyToRunning(cfg, true); err == nil || eng.web || !eng.direct {
		t.Fatalf("tproxy: err=%v web=%v direct=%v", err, eng.web, eng.direct)
	}
}
