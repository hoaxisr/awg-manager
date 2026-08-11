package wdtt

import "testing"

type portTaker map[int]bool

func (p portTaker) OccupiedLocalListenPorts(_, _ string) (map[int]bool, error) {
	return p, nil
}

// listen-repair переназначил порт → linked-туннели обязаны узнать новый listen.
func TestStartClient_ListenRepairSyncsLinkedEndpoints(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	cfg := validClientCfg("127.0.0.1:56002")
	cfg.Listen = "127.0.0.1:9000"
	if err := s.UpdateClientConfig(cfg); err != nil {
		t.Fatal(err)
	}
	// 9000 занят чужим процессом → ensureUniqueListenAddr уводит клиента на 9001.
	s.SetListenPortChecker(portTaker{9000: true})

	var gotID, gotListen string
	calls := 0
	s.SetLinkedEndpointSync(func(clientID, listen string) (int, error) {
		calls++
		gotID, gotListen = clientID, listen
		return 1, nil
	})
	sleepSeam(s.clientProcs.get(DefaultInstanceID))

	if err := s.StartClientInstance(DefaultInstanceID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	if calls != 1 {
		t.Fatalf("ожидался один вызов sync, получено %d", calls)
	}
	if gotID != DefaultInstanceID {
		t.Errorf("clientID = %q, ожидался %q", gotID, DefaultInstanceID)
	}
	got, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if gotListen != got.Clients[0].Config.Listen {
		t.Errorf("sync получил listen %q, в конфиге %q", gotListen, got.Clients[0].Config.Listen)
	}
}

// Порт не менялся — дёргать туннели незачем.
func TestStartClient_NoListenRepairNoSync(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	cfg := validClientCfg("127.0.0.1:56002")
	cfg.Listen = "127.0.0.1:9000"
	if err := s.UpdateClientConfig(cfg); err != nil {
		t.Fatal(err)
	}

	called := 0
	s.SetLinkedEndpointSync(func(string, string) (int, error) {
		called++
		return 0, nil
	})
	sleepSeam(s.clientProcs.get(DefaultInstanceID))

	if err := s.StartClientInstance(DefaultInstanceID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	if called != 0 {
		t.Fatalf("sync вызван %d раз при неизменном listen", called)
	}
}
