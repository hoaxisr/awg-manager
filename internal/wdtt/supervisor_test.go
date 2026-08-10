package wdtt

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
// прохода «сразу при старте» быть не должно: vkcalls бьётся о мёртвый DNS, а
// старт сервера — о неготовый NDMS.
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

// См. пояснение в freeturn: сброс backoff внутри UpdateServerInstance отключил
// бы рост окна, потому что StartServerInstance зовёт его сам.
func TestStartServerKeepsBackoff(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	defer s.Stop()
	now := time.Now()
	s.startBackoff.Fail(serverKey(DefaultInstanceID), now)

	// Дефолтный сервер без пароля: старт падает уже после нормализации listen.
	if err := s.StartServerInstance(DefaultInstanceID); err == nil {
		t.Fatal("ожидалась ошибка старта сервера без пароля")
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

// fakeOpkgIndexLister — минимальный OpkgTunIndexLister: raw-клиент в тестах
// использует уже валидный индекс (OpkgTun18), поэтому список живых интерфейсов
// реально не запрашивается (см. ensureClientOpkgIndex), но само поле обязано
// быть не-nil — иначе StartClientInstance откажет ДО спавна процесса.
type fakeOpkgIndexLister struct{}

func (fakeOpkgIndexLister) LiveOpkgTunIndices(context.Context) (map[int]bool, error) {
	return map[int]bool{}, nil
}

// enabledRawClientService — запущенный raw-клиент (OpkgTun) со StartedAt,
// искусственно отодвинутым в прошлое за оба grace (NDMS и health), чтобы
// health-предикаты сразу давали реальный (не «ещё рано») сигнал без реального
// ожидания.
//
// Процесс стартует напрямую через process.Start (как sleepSeam-тесты в
// process_test.go), в обход Service.StartClientInstance: реальный raw-путь
// передаёт TUN fd через настоящий TUNSETIFF на opkgtun18, которого в тестовой
// песочнице нет. NDMS/opkg-фейки всё равно нужны — их использует эскалационный
// рестарт супервизора (restartClientInstance зовёт настоящий StartClientInstance).
func enabledRawClientService(t *testing.T, rawClientIP string) (*Service, *process) {
	t.Helper()
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	s.SetNDMSInterfaceCommands(&fakeOpkgCommands{})
	s.SetOpkgTunIndexLister(fakeOpkgIndexLister{})
	cfg := validClientCfg("127.0.0.1:56000")
	cfg.ConnMode = ConnModeRaw
	cfg.NdmsIface = "OpkgTun18"
	cfg.RawIface = "opkgtun18"
	cfg.RawClientIP = rawClientIP
	cfg.Enabled = true
	full, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	full.Clients[0].Config = cfg
	if err := s.store.Save(full); err != nil {
		t.Fatal(err)
	}

	proc := s.clientProcs.get(DefaultInstanceID)
	sleepSeam(proc)
	if err := proc.Start(nil); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-2 * clientNDMSGrace)
	proc.startedAt = &past
	return s, proc
}

// C1: reconcile — первая попытка лечения, но если OpkgTun после неё всё равно
// не ожил (operUp остаётся false), это должно копить страйки в том же
// трекере, что peerBad/relayBad, а не крутиться вечно мимо него, и на пороге
// заэскалировать в рестарт через backoff (I2). Сам рестарт в песочнице падает
// на реальном TUNSETIFF (см. enabledRawClientService) — эскалацию проверяем по
// тому, что до неё уже реально дошли: backoff и трекер страйков.
func TestSuperviseNDMSReconcileFailureEscalatesToRestart(t *testing.T) {
	s, _ := enabledRawClientService(t, "10.70.0.5")
	defer s.Stop()
	s.SetInterfaceChecker(fakeIfaceChecker{
		exists: map[string]bool{"opkgtun18": true},
		operUp: map[string]bool{"opkgtun18": false}, // никогда не оживает
	})

	ctx := context.Background()
	for i := 0; i < clientHealthStrikes; i++ {
		s.superviseEnabled(ctx)
	}

	if s.startBackoff.Allow(clientHealthKey(DefaultInstanceID), time.Now()) {
		t.Fatal("после эскалации health-backoff должен отложить следующий рестарт")
	}
	if strikes := s.clientHealth.strikes[DefaultInstanceID]; strikes != 0 {
		t.Fatalf("трекер страйков должен быть сброшен после эскалации, strikes=%d", strikes)
	}
}

// I2: рестарт по relayBad не должен игнорировать backoff — если предыдущий
// health-рестарт уже исчерпал окно, новый рестарт на этом тике не происходит
// (Allow отказывает раньше, чем цикл вообще пытается перезапустить клиент).
func TestSuperviseRelayBadGatedByBackoff(t *testing.T) {
	s, proc := enabledRawClientService(t, "10.70.0.5")
	defer s.Stop()
	s.SetInterfaceChecker(fakeIfaceChecker{
		exists: map[string]bool{"opkgtun18": true},
		operUp: map[string]bool{"opkgtun18": true}, // NDMS-интерфейс жив, дело в relay
	})
	s.SetRelayProbe(fakeRelayProbe{ok: map[string]bool{"opkgtun18": false}}) // проба всегда падает

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

// C1: (false, nil) от reconcile (например, пустой RawClientIP) — это не
// молчаливый успех, страйки обязаны копиться так же, как при явной ошибке.
func TestSuperviseNDMSReconcileNoopIsNotSuccess(t *testing.T) {
	s, _ := enabledRawClientService(t, "") // пустой RawClientIP → reconcile вернёт (false, nil)
	defer s.Stop()
	s.SetInterfaceChecker(fakeIfaceChecker{
		exists: map[string]bool{"opkgtun18": true},
		operUp: map[string]bool{"opkgtun18": false},
	})

	ctx := context.Background()
	for i := 0; i < clientHealthStrikes; i++ {
		s.superviseEnabled(ctx)
	}

	if s.startBackoff.Allow(clientHealthKey(DefaultInstanceID), time.Now()) {
		t.Fatal("(false, nil) от reconcile не должен считаться выздоровлением: страйки должны были дойти до порога")
	}
}
