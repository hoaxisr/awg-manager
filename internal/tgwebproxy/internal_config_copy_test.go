package tgwebproxy

import "testing"

// Мастер правит полученный конфиг на месте; живой конфиг службы от этого
// меняться не должен.
func TestGetInternalConfig_ReturnsCopy(t *testing.T) {
	s := &Service{config: Config{
		Endpoints: []WebProxyEndpoint{{ID: "ep-primary", Hostname: "a.example"}},
		Users:     []TelegramUser{{ID: "default", Remark: "Основной"}},
	}}
	cfg := s.GetInternalConfig()
	cfg.Endpoints[0].Hostname = "evil.example"
	cfg.Users[0].Remark = "x"
	if s.config.Endpoints[0].Hostname != "a.example" || s.config.Users[0].Remark != "Основной" {
		t.Fatalf("live config mutated through GetInternalConfig: %+v", s.config)
	}
	eps := s.config.GetEndpoints()
	eps[0].Hostname = "evil.example"
	if s.config.Endpoints[0].Hostname != "a.example" {
		t.Fatal("live config mutated through GetEndpoints")
	}
}
