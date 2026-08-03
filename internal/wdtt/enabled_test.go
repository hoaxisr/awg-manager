package wdtt

import (
	"os/exec"
	"testing"
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
