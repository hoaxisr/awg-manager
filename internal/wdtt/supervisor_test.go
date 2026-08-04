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
