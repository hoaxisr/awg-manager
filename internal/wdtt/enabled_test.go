package wdtt

import (
	"errors"
	"os/exec"
	"testing"
	"time"
)

// sleepSeam подменяет реальный запуск клиента на долгий sleep, чтобы Start
// пережил startupGrace и вернул nil (успех) без настоящего бинаря.
func sleepSeam(p *process) {
	p.startCmd = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", "sleep 30")
	}
}

// validClientCfg — конфиг, проходящий валидацию StartClientInstance.
func validClientCfg(peer string) ClientConfig {
	c := DefaultClientConfig()
	c.Peer = peer
	c.VKHashes = "h1"
	c.Password = "p"
	return c
}

func TestService_StartClientSetsEnabled(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	if err := s.UpdateClientConfig(validClientCfg("127.0.0.1:56000")); err != nil {
		t.Fatal(err)
	}
	sleepSeam(s.clientProcs.get(DefaultInstanceID))

	if err := s.StartClientInstance(DefaultInstanceID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	got, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Clients[0].Config.Enabled {
		t.Fatal("Enabled должно стать true после успешного старта")
	}
}

func TestService_StartClientFailKeepsEnabledFalse(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	// Дефолтный клиент без Peer/VK/Password → валидация падает до spawn.
	if err := s.StartClientInstance(DefaultInstanceID); err == nil {
		t.Fatal("ожидалась ошибка валидации")
	}
	got, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.Clients[0].Config.Enabled {
		t.Fatal("Enabled не должно выставляться при неуспешном старте")
	}
}

func TestService_StopClientClearsEnabled(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	cfg, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Clients[0].Config = validClientCfg("h:1")
	cfg.Clients[0].Config.Enabled = true
	if err := s.store.Save(cfg); err != nil {
		t.Fatal(err)
	}

	if err := s.StopClientInstance(DefaultInstanceID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetConfig()
	if got.Clients[0].Config.Enabled {
		t.Fatal("пользовательский Stop должен сбросить Enabled")
	}
}

func TestService_StopExitKeepsEnabled(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	cfg, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Clients[0].Config.Enabled = true
	if err := s.store.Save(cfg); err != nil {
		t.Fatal(err)
	}

	s.Stop() // выход демона — Enabled трогать нельзя

	got, _ := s.GetConfig()
	if !got.Clients[0].Config.Enabled {
		t.Fatal("Service.Stop() не должен сбрасывать Enabled")
	}
}

func TestService_UpdateClientPreservesEnabled(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	cfg, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Clients[0].Config = validClientCfg("h:1")
	cfg.Clients[0].Config.Enabled = true
	if err := s.store.Save(cfg); err != nil {
		t.Fatal(err)
	}

	stale := validClientCfg("h:2")
	stale.Enabled = false // UI часто шлёт false, не перечитав конфиг после Start
	if err := s.UpdateClientInstance(DefaultInstanceID, stale); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetConfig()
	if !got.Clients[0].Config.Enabled {
		t.Fatal("UpdateClientInstance не должен сбрасывать Enabled")
	}
	if got.Clients[0].Config.Peer != "h:2" {
		t.Fatal("peer должен обновиться")
	}
}

func TestService_UpdateServerPreservesEnabled(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	cfg, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Servers[0].Config.Password = "mainpass0000000000000000"
	cfg.Servers[0].Config.Enabled = true
	if err := s.store.Save(cfg); err != nil {
		t.Fatal(err)
	}

	stale := DefaultServerConfig()
	stale.Password = "otherpass000000000000000"
	stale.Enabled = false
	if _, err := s.UpdateServerInstance(DefaultInstanceID, stale); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetConfig()
	if !got.Servers[0].Config.Enabled {
		t.Fatal("UpdateServerInstance не должен сбрасывать Enabled")
	}
	if got.Servers[0].Config.Password != "otherpass000000000000000" {
		t.Fatal("прочие поля должны обновляться")
	}
}

// TestService_StartClientInstance_ConcurrentSameIDSerializes: два конкурентных
// StartClientInstance для одного id не гоняются друг с другом — второй сразу
// (без RCI-работы) получает ErrClientStartInFlight, пока первый ещё держит
// TryLock внутри Start() (startupGrace ~1.5с).
func TestService_StartClientInstance_ConcurrentSameIDSerializes(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	if err := s.UpdateClientConfig(validClientCfg("127.0.0.1:56000")); err != nil {
		t.Fatal(err)
	}
	sleepSeam(s.clientProcs.get(DefaultInstanceID))

	firstErr := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		firstErr <- s.StartClientInstance(DefaultInstanceID)
	}()
	<-started
	time.Sleep(100 * time.Millisecond) // первый старт уже внутри Start() и держит лок

	if err := s.StartClientInstance(DefaultInstanceID); !errors.Is(err, ErrClientStartInFlight) {
		t.Fatalf("второй конкурентный старт: ожидали ErrClientStartInFlight, получили %v", err)
	}

	if err := <-firstErr; err != nil {
		t.Fatalf("первый старт: %v", err)
	}
	defer s.Stop()
}

// TestService_StartClientInstance_DifferentIDsNotBlocked: лок per-client — старт
// клиента A не блокирует конкурентный старт клиента B.
func TestService_StartClientInstance_DifferentIDsNotBlocked(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	cfg, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Clients[0].Config = validClientCfg("h:1")
	b := ClientInstance{ID: "b", Name: "B", Config: validClientCfg("h:2")}
	cfg.Clients = append(cfg.Clients, b)
	if err := s.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	sleepSeam(s.clientProcs.get(DefaultInstanceID))
	sleepSeam(s.clientProcs.get("b"))

	aErr := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		aErr <- s.StartClientInstance(DefaultInstanceID)
	}()
	<-started
	time.Sleep(100 * time.Millisecond) // старт A уже внутри Start() и держит свой лок

	if err := s.StartClientInstance("b"); err != nil {
		t.Fatalf("старт b не должен блокироваться стартом default: %v", err)
	}
	if err := <-aErr; err != nil {
		t.Fatalf("старт default: %v", err)
	}
	defer s.Stop()
}

func TestService_ResumeEnabledStartsOnlyEnabled(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	cfg, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Clients[0].Config = validClientCfg("h:1")
	cfg.Clients[0].Config.Enabled = true
	b := ClientInstance{ID: "b", Name: "B", Config: validClientCfg("h:2")}
	b.Config.Enabled = false
	cfg.Clients = append(cfg.Clients, b)
	if err := s.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	sleepSeam(s.clientProcs.get(DefaultInstanceID))
	sleepSeam(s.clientProcs.get("b"))

	s.ResumeEnabled()
	defer s.Stop()

	if running, _ := s.clientProcs.get(DefaultInstanceID).IsRunning(); !running {
		t.Fatal("Enabled-клиент должен быть запущен ResumeEnabled")
	}
	if running, _ := s.clientProcs.get("b").IsRunning(); running {
		t.Fatal("disabled-клиент не должен запускаться")
	}
}
