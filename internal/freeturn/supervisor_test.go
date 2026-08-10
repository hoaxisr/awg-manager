package freeturn

import (
	"context"
	"testing"
	"time"
)

func enabledClientService(t *testing.T, peer string) *Service {
	t.Helper()
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	cfg, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Clients[0].Config = validClientCfg(peer)
	cfg.Clients[0].Config.Enabled = true
	if err := s.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	return s
}

// Демон поднимает супервизор ДО boot-последовательности (main.go), поэтому
// прохода «сразу при старте» быть не должно: автостарт прокси намеренно ждёт
// NDMS/WAN и DNS.
func TestStartSupervisorSkipsImmediatePass(t *testing.T) {
	s := enabledClientService(t, "127.0.0.1:56000")
	sleepSeam(s.clientProcs.get(DefaultInstanceID))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.StartSupervisor(ctx, func() bool { return true })
	time.Sleep(200 * time.Millisecond)
	defer s.Stop()

	if running, _ := s.clientProcs.get(DefaultInstanceID).IsRunning(); running {
		t.Fatal("супервизор не должен стартовать клиентов до первого тика")
	}
}

func TestSuperviseEnabledStartsDeadClient(t *testing.T) {
	s := enabledClientService(t, "127.0.0.1:56000")
	sleepSeam(s.clientProcs.get(DefaultInstanceID))
	defer s.Stop()

	s.superviseEnabled(context.Background())

	if running, _ := s.clientProcs.get(DefaultInstanceID).IsRunning(); !running {
		t.Fatal("enabled-клиент с мёртвым процессом должен быть перезапущен")
	}
}

// Повторять неудачный старт каждые 30 с нельзя: у WDTT-сервера путь старта
// тянет NDMS/RCI, да и лог захлёбывается.
func TestSuperviseEnabledBacksOffFailingStart(t *testing.T) {
	s := enabledClientService(t, "") // пустой peer → старт падает на валидации
	defer s.Stop()

	s.superviseEnabled(context.Background())

	if s.startBackoff.Allow(clientKey(DefaultInstanceID), time.Now()) {
		t.Fatal("после неудачного старта следующая попытка должна быть отложена")
	}
	if !s.startBackoff.Allow(clientKey(DefaultInstanceID), time.Now().Add(supervisorInterval)) {
		t.Fatal("после паузы попытка снова разрешена")
	}
}

// Пользователь чинит конфиг после серии неудачных стартов — ждать окно backoff
// (до 15 минут) он не должен.
func TestUpdateConfigClearsBackoff(t *testing.T) {
	s := enabledClientService(t, "")
	defer s.Stop()

	s.superviseEnabled(context.Background())
	if s.startBackoff.Allow(clientKey(DefaultInstanceID), time.Now()) {
		t.Fatal("после неудачного старта попытка должна быть отложена")
	}

	if err := s.UpdateClientInstance(DefaultInstanceID, validClientCfg("127.0.0.1:56000")); err != nil {
		t.Fatal(err)
	}
	if !s.startBackoff.Allow(clientKey(DefaultInstanceID), time.Now()) {
		t.Fatal("после правки конфига попытка должна быть разрешена сразу")
	}
}

// StartServerInstance нормализует listen через UpdateServerInstance. Если тот
// сбрасывает backoff, окно у серверов навсегда остаётся минимальным: супервизор
// делает Allow → Start (сброс) → Fail (счётчик с нуля), и рост до 15 минут не
// работает — при том что путь старта сервера самый дорогой.
func TestStartServerKeepsBackoff(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	defer s.Stop()
	now := time.Now()
	s.startBackoff.Fail(serverKey(DefaultInstanceID), now)

	// Дефолтный сервер без -connect: старт падает уже после нормализации listen.
	if err := s.StartServerInstance(DefaultInstanceID); err == nil {
		t.Fatal("ожидалась ошибка старта сервера без backend-адреса")
	}
	if s.startBackoff.Allow(serverKey(DefaultInstanceID), now) {
		t.Fatal("старт сервера не должен стирать окно backoff")
	}
}

func TestDeleteClientForgetsBackoff(t *testing.T) {
	s := enabledClientService(t, "")
	defer s.Stop()

	s.superviseEnabled(context.Background())
	if err := s.DeleteClient(DefaultInstanceID); err != nil {
		t.Fatal(err)
	}
	if !s.startBackoff.Allow(clientKey(DefaultInstanceID), time.Now()) {
		t.Fatal("состояние удалённого инстанса не должно оставаться в памяти")
	}
}

// I2: рестарт по relayBad не должен игнорировать backoff — если предыдущий
// health-рестарт уже исчерпал окно, новый рестарт на этом тике не происходит
// (обрыв живых DTLS-потоков рестартом на каждом тике недопустим).
func TestSuperviseRelayBadGatedByBackoff(t *testing.T) {
	s := enabledClientService(t, "127.0.0.1:56000")
	defer s.Stop()
	proc := s.clientProcs.get(DefaultInstanceID)
	sleepSeam(proc)
	if err := s.StartClientInstance(DefaultInstanceID); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-2 * clientHealthGrace)
	proc.startedAt = &past

	s.SetRelayProbe(fakeRelayProbe{ok: map[string]bool{"awg0": false}}) // проба всегда падает
	s.SetLinkedTunnelResolver(fakeLinkedTunnels{iface: "awg0", ok: true})

	before := proc.Status().StartedAt
	s.startBackoff.Fail(clientHealthKey(DefaultInstanceID), time.Now())

	ctx := context.Background()
	for i := 0; i < clientHealthStrikes; i++ {
		s.superviseEnabled(ctx)
	}

	after := proc.Status().StartedAt
	if after == nil || before == nil || !after.Equal(*before) {
		t.Fatal("backoff-отказ должен был запретить рестарт по relayBad")
	}
}

func TestSuperviseEnabledSkipsDisabled(t *testing.T) {
	s := enabledClientService(t, "127.0.0.1:56000")
	cfg, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Clients[0].Config.Enabled = false
	if err := s.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	sleepSeam(s.clientProcs.get(DefaultInstanceID))
	defer s.Stop()

	s.superviseEnabled(context.Background())

	if running, _ := s.clientProcs.get(DefaultInstanceID).IsRunning(); running {
		t.Fatal("остановленный пользователем клиент не должен воскресать")
	}
}
