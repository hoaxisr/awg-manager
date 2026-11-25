package tgwebproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

type appLogSpy struct{ lines []string }

func (l *appLogSpy) AppLog(level logging.Level, group, subgroup, action, target, message string) {
	l.lines = append(l.lines, string(level)+" "+group+"/"+subgroup+" "+action+" "+target+" "+message)
}

// Порт на WAN открывает только заданный прямой адрес в сценарии с прямым
// подключением; без него прежнее правило снимается.
// Мутации: OpensDirectWAN без проверки DirectHost; синхронизация по
// IsDirectEnabled; UpdateConfigWithDirectHost игнорирует пустую строку → красный.
func TestService_DirectWANNeedsDirectHost(t *testing.T) {
	svc, _, _ := setupTestService(t)
	last := ""
	svc.directWANFirewall = func(port int, enabled bool) error {
		last = fmt.Sprintf("%d/%v", port, enabled)
		return nil
	}
	expect := func(what, want string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if last != want {
			t.Fatalf("%s: файрвол %q, ждали %q", what, last, want)
		}
	}

	expect("dual без адреса", "0/false", svc.Start())
	expect("direct_fake_tls без адреса", "0/false", svc.UpdateConfig(Config{Scenario: ScenarioDirectFakeTLS}))
	expect("direct_fake_tls с адресом", "8443/true", svc.UpdateConfig(Config{DirectHost: "203.0.113.10"}))
	expect("адрес не прислали", "8443/true", svc.UpdateConfig(Config{TlsDomain: "www.example.com"}))
	expect("пустой сценарий с адресом", "8443/true", svc.UpdateConfig(Config{Scenario: ScenarioDual}))
	expect("cdn_http с адресом", "0/false", svc.UpdateConfig(Config{Scenario: ScenarioCDNHTTP}))
	expect("снова dual", "8443/true", svc.UpdateConfig(Config{Scenario: ScenarioDual}))
	expect("адрес снят", "0/false", svc.UpdateConfigWithDirectHost(Config{}))
	if got := svc.GetInternalConfig().DirectHost; got != "" {
		t.Fatalf("адрес не снят: %q", got)
	}
	svc.mu.Lock()
	svc.config.Scenario = ""
	svc.mu.Unlock()
	expect("пустой сценарий без адреса", "0/false", svc.Restart())
}

// Выключенный прокси на загрузке снимает оставшееся правило.
// Мутация: убрать синхронизацию из ветки !Enabled у StartConfigured → красный.
func TestStartConfigured_DisabledClosesDirectWAN(t *testing.T) {
	svc, _, _ := setupTestService(t)
	calls := 0
	svc.directWANFirewall = func(_ int, enabled bool) error {
		if enabled {
			t.Fatal("выключенный прокси открыл порт")
		}
		calls++
		return nil
	}
	if err := svc.StartConfigured(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("синхронизаций %d, ждали 1", calls)
	}
}

// Сбой файрвола не мешает закрыть доступ: остановить, отозвать пользователя,
// сменить секрет, уйти на веб, сбросить. Он виден в журнале и в статусе.
// Мутации: возвращать ошибку не только при открытии порта (хук транзакции,
// CommitPrepared, rollbackLocked, FactoryReset, StartConfigured, Restart); не
// писать в журнал; не снимать прошлую ошибку → красный.
func TestService_FirewallFailureDoesNotBlockClosing(t *testing.T) {
	svc, tempDir, _ := setupTestService(t)
	spy := &appLogSpy{}
	svc.SetAppLogger(spy)
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	must("UpdateConfig", svc.UpdateConfig(Config{DirectHost: "203.0.113.10"}))
	must("Start", svc.Start())
	must("CreateUser", svc.CreateUser("гость"))

	fwErr := errors.New("xtables lock")
	svc.directWANFirewall = func(int, bool) error { return fwErr }
	users := svc.GetInternalConfig().Users
	must("DeleteUser", svc.DeleteUser(users[len(users)-1].ID))
	must("RotateSecret", svc.RotateSecret())
	must("Restart", svc.Restart())
	must("StartConfigured", svc.StartConfigured())
	must("уход на веб", svc.UpdateConfig(Config{Scenario: ScenarioCDNHTTP}))

	cand := svc.GetInternalConfig()
	cand.TlsDomain = "www.example.net"
	txID, err := svc.PrepareCandidate("tx-fw-close", cand)
	must("PrepareCandidate", err)
	must("CommitPrepared", svc.CommitPrepared(txID))
	must("RollbackPrepared", svc.RollbackPrepared(txID))
	must("Stop", svc.Stop())
	must("StartConfigured выключенного", svc.StartConfigured())

	if got := svc.GetStatus().DirectWANError; got != "xtables lock" {
		t.Fatalf("статус: %q", got)
	}
	if len(spy.lines) == 0 || !strings.Contains(spy.lines[0], "server/firewall wan-firewall") || !strings.Contains(spy.lines[0], "xtables lock") {
		t.Fatalf("журнал: %q", spy.lines)
	}
	var saved Config
	data, err := os.ReadFile(filepath.Join(tempDir, "tproxy", "tgwebproxy-settings.json"))
	must("чтение", err)
	must("разбор", json.Unmarshal(data, &saved))
	if saved.Enabled || svc.GetInternalConfig().Enabled {
		t.Fatal("после Stop прокси остался включённым")
	}
	must("FactoryReset", svc.FactoryReset(context.Background()))

	fwErr = nil
	must("SyncDirectWANFirewall", svc.SyncDirectWANFirewall())
	if got := svc.GetStatus().DirectWANError; got != "" {
		t.Fatalf("после успеха ошибка осталась: %q", got)
	}
}

// Открытие порта не удалось — операция отменена целиком: память, диск, движки.
// Мутации: игнорировать ошибку в ApplyWorkers хука или в CommitPrepared;
// присвоить s.config до сверки файрвола → красный.
func TestService_FirewallFailureOnOpeningRollsBack(t *testing.T) {
	svc, tempDir, engines := setupTestService(t)
	if err := svc.UpdateConfig(Config{DirectHost: "203.0.113.10", Scenario: ScenarioDirectFakeTLS}); err != nil {
		t.Fatal(err)
	}
	fwErr := errors.New("xtables lock")
	var last string
	svc.directWANFirewall = func(port int, enabled bool) error {
		last = fmt.Sprintf("%d/%v", port, enabled)
		if enabled {
			return fwErr
		}
		return nil
	}
	check := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, fwErr) || !strings.Contains(err.Error(), "открыть порт MTProto на WAN") {
			t.Fatalf("%s: %v", what, err)
		}
		if svc.GetInternalConfig().Enabled || svc.IsRunning() || last != "0/false" {
			t.Fatalf("%s: откат неполный: enabled=%v running=%v файрвол=%s", what, svc.GetInternalConfig().Enabled, svc.IsRunning(), last)
		}
		var saved Config
		data, err := os.ReadFile(filepath.Join(tempDir, "tproxy", "tgwebproxy-settings.json"))
		if err != nil || json.Unmarshal(data, &saved) != nil || saved.Enabled {
			t.Fatalf("%s: на диске осталась включённая конфигурация (%v)", what, err)
		}
		engines.mu.Lock()
		defer engines.mu.Unlock()
		if engines.direct {
			t.Fatalf("%s: слушатель остался", what)
		}
	}
	check("Start", svc.Start())

	cand := svc.GetInternalConfig()
	cand.Enabled = true
	txID, err := svc.PrepareCandidate("tx-fw-open", cand)
	if err != nil {
		t.Fatal(err)
	}
	check("CommitPrepared", svc.CommitPrepared(txID))
}

// Порт из прежней версии и добавленный прямой адрес: открыть нельзя — 400 до
// транзакции, а не сбой файрвола.
// Мутация: убрать opensDirectWANAnew из ValidateDirectPortTransition → красный.
func TestLegacyDirectPort_AddingDirectHostRefused(t *testing.T) {
	svc, _, _ := setupTestService(t)
	if err := svc.Start(); err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	svc.config.DirectPort = 443
	svc.mu.Unlock()
	if err := svc.UpdateConfig(Config{DirectHost: "203.0.113.10"}); !errors.Is(err, ErrInvalidDirectPort) {
		t.Fatalf("ждали ErrInvalidDirectPort: %v", err)
	}
}

// На загрузке WAN ещё не поднят: прокси работает, сверка повторяется позже.
// Мутации: валить StartConfigured; не повторять; повторять недопустимый порт.
func TestRetryDirectWANFirewall(t *testing.T) {
	svc, _, _ := setupTestService(t)
	if err := svc.UpdateConfig(Config{DirectHost: "203.0.113.10"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(); err != nil {
		t.Fatal(err)
	}
	calls, fwErr := 0, errors.New("нет активного WAN-интерфейса")
	svc.directWANFirewall = func(int, bool) error { calls++; return fwErr }
	if err := svc.StartConfigured(); err != nil || calls != 1 {
		t.Fatalf("StartConfigured: %v, сверок %d", err, calls)
	}
	svc.backgroundTick()
	if calls != 2 || svc.GetStatus().DirectWANError == "" {
		t.Fatalf("повтор: сверок %d, статус %q", calls, svc.GetStatus().DirectWANError)
	}
	fwErr = nil
	svc.retryDirectWANFirewall()
	svc.retryDirectWANFirewall() // ошибки больше нет
	if calls != 3 || svc.GetStatus().DirectWANError != "" {
		t.Fatalf("после успеха: сверок %d, статус %q", calls, svc.GetStatus().DirectWANError)
	}
	fwErr = ErrInvalidDirectPort
	_ = svc.SyncDirectWANFirewall()
	svc.retryDirectWANFirewall()
	if calls != 4 {
		t.Fatalf("недопустимый порт повторён: %d", calls)
	}
}

type refusingMtg struct{ fakeMtg }

func (f refusingMtg) Apply(cfg Config) error {
	if cfg.Enabled {
		return errors.New("порт занят")
	}
	return f.fakeMtg.Apply(cfg)
}

// Откат не поднимает движок, который до транзакции не работал, и возвращает
// прежнюю конфигурацию работавшему.
// Мутации: ApplyWorkers(prevCfg) вместо RestoreConfig; Restart вместо Apply → красный.
func TestRestoreConfig_KeepsStoppedWorkerStopped(t *testing.T) {
	ws, eng := newFakeWorkers()
	prev := Config{Enabled: true, Scenario: ScenarioDual, DirectPort: 8443}
	next := prev
	next.DirectPort = 9443
	_ = ws.ApplyWorkers(next)
	if err := ws.RestoreConfig(prev, map[string]WorkerProcessState{
		workerDirect: {Running: true}, workerTproxy: {Running: true},
	}); err != nil || eng.mtgCfg.DirectPort != 8443 || !eng.direct || !eng.web {
		t.Fatalf("работавшие движки: err=%v port=%d direct=%v web=%v", err, eng.mtgCfg.DirectPort, eng.direct, eng.web)
	}

	ws, eng = newFakeWorkers()
	ws.mtg = refusingMtg{fakeMtg{eng}}
	if err := ws.RestoreConfig(prev, map[string]WorkerProcessState{workerTproxy: {Running: true}}); err != nil || eng.direct || !eng.web {
		t.Fatalf("остановленный mtg: err=%v direct=%v web=%v", err, eng.direct, eng.web)
	}
	if err := ws.RestoreConfig(prev, map[string]WorkerProcessState{workerDirect: {Running: true}, workerTproxy: {Running: true}}); err == nil || !eng.web {
		t.Fatalf("сбой одного движка обязан быть виден и не мешать другому: err=%v web=%v", err, eng.web)
	}
}

// Хук и Go выбирают одни и те же интерфейсы; «открывать не на чем» — сбой
// только для нашего запуска, ndm получает 0.
// Мутации: разойтись списком исключений или обработкой `!`; вернуть ndm
// ненулевой код при пустом списке или отсутствии цепочки → красный.
func TestDirectWANHook_MatchesGoParser(t *testing.T) {
	const tail = " -m set --match-set _NDM_I4AL_x dst -m ndmslin --slin 3 (public) -j _NDM_IP_PUBLIC\n"
	all := ""
	for _, i := range []string{"ppp0", "eth3", "eth2.4", "apcli0", "usb0", "qmi_br0", "lo", "br0", "br1", "opkgtun10", "t2s0", "tun0", "tap0", "wg0", "nwg1", "awgm0", "sstp0", "ovpn0", "ppp0"} {
		all += "-A _NDM_INPUT -i " + i + tail
	}
	for name, rules := range map[string]string{
		"все классы":     all,
		"отрицание":      "-A _NDM_INPUT ! -i ppp0" + tail + "-A _NDM_INPUT -i eth3" + tail,
		"без интерфейса": "-A _NDM_INPUT -j _NDM_IP_PUBLIC\n",
		"другая цель":    "-A _NDM_INPUT -i ppp0 -j _NDM_IP_PRIVATE\n-A INPUT -i eth3 -j _NDM_IP_PUBLIC\n",
		"маски в имени":  "-A _NDM_INPUT -i ppp+" + tail + "-A _NDM_INPUT -i *" + tail,
		"пусто":          "-N _NDM_INPUT\n",
		"нет цепочки":    "",
	} {
		t.Run(name, func(t *testing.T) {
			tmp := t.TempDir()
			log := filepath.Join(tmp, "log")
			if err := os.WriteFile(filepath.Join(tmp, "rules"), []byte(rules), 0644); err != nil {
				t.Fatal(err)
			}
			list := "exit 0"
			if rules == "" {
				list = "exit 1"
			}
			stub := fmt.Sprintf(`#!/bin/sh
case " $* " in
  *" -L _NDM_IP_PUBLIC "*) %s ;;
  *" -S _NDM_INPUT "*) cat %s/rules ;;
  *" -C "*) exit 1 ;;
  *" -I "*) echo "$*" >> %s ;;
esac
`, list, tmp, log)
			if err := os.WriteFile(filepath.Join(tmp, "iptables"), []byte(stub), 0755); err != nil {
				t.Fatal(err)
			}
			hook := strings.Replace(renderDirectWANHook(8443), "/opt/sbin/iptables", tmp+"/iptables", 1)
			run := func(env ...string) error {
				_ = os.Remove(log)
				cmd := exec.Command("/bin/sh", "-c", hook)
				cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "type=iptables", "table=filter"}, env...)
				return cmd.Run()
			}
			want := parsePublicWANIfaces(rules)
			for _, sync := range []bool{false, true} {
				var err error
				if sync {
					err = run(directWANSyncEnv + "=1")
				} else {
					err = run()
				}
				var got []string
				data, _ := os.ReadFile(log)
				for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
					if f := strings.Fields(line); slices.Contains(f, "-i") {
						got = append(got, f[slices.Index(f, "-i")+1])
					}
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("sync=%v: хук открыл %v, Go ждёт %v", sync, got, want)
				}
				if wantErr := sync && len(want) == 0; (err != nil) != wantErr {
					t.Fatalf("sync=%v: код хука %v", sync, err)
				}
			}
		})
	}
}

// Наш запуск хука помечен: иначе причина «открывать не на чем» не дошла бы.
// Мутация: убрать directWANSyncEnv из окружения → красный.
func TestSyncDirectWANFirewall_ReportsNotReady(t *testing.T) {
	nfDir := filepath.Join(t.TempDir(), "netfilter.d")
	if err := os.MkdirAll(nfDir, 0755); err != nil {
		t.Fatal(err)
	}
	stubDirectWANSeams(t, nfDir, filepath.Join(nfDir, "68.sh"), func(env []string, name string, args ...string) ([]byte, error) {
		if name == "/bin/sh" && slices.Contains(env, directWANSyncEnv+"=1") {
			return []byte("нет активного WAN-интерфейса"), errors.New("exit status 1")
		}
		return nil, nil
	})
	if err := SyncDirectWANFirewall(8443, true); err == nil || !strings.Contains(err.Error(), "нет активного WAN-интерфейса") {
		t.Fatalf("причина не дошла: %v", err)
	}
}
