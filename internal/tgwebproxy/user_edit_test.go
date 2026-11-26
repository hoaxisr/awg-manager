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

	ws, eng = newFakeWorkers()
	if err := ws.ApplyToRunning(cfg, true); err != nil || eng.direct || eng.raw || eng.web {
		t.Fatalf("неработавшие движки подняты: err=%v direct=%v web=%v", err, eng.direct, eng.web)
	}
}

// Упавший выход не мешает остановить прокси и отозвать доступ; проверяется он
// при включении и при смене выхода.
// Мутации: проверять выход при любой правке; не проверять при включении или
// смене (applyLocked, PrepareCandidate) → красный.
func TestEgressValidation_OnlyOnEnableOrChange(t *testing.T) {
	for _, ups := range [][]UpstreamSpec{nil, {{ID: "up-1", Type: UpstreamTypeDirect, Device: "nwg0", Weight: 1, Enabled: true}}} {
		svc, _, _ := setupTestService(t)
		must := func(what string, err error) {
			t.Helper()
			if err != nil {
				t.Fatalf("%s: %v", what, err)
			}
		}
		refused := func(what string, err error) {
			t.Helper()
			if err == nil {
				t.Fatalf("%s: упавший выход принят", what)
			}
		}
		must("UpdateConfig", svc.UpdateConfig(Config{UpstreamDevice: "nwg0", Upstreams: ups}))
		must("Start", svc.Start())
		must("CreateUser", svc.CreateUser("гость"))

		svc.mu.Lock() // проверку читает и фоновая проверка здоровья
		svc.egressValidator = func(string) error { return errors.New("интерфейс опущен") }
		svc.mu.Unlock()
		users := svc.GetInternalConfig().Users
		must("DeleteUser", svc.DeleteUser(users[len(users)-1].ID))
		must("RotateSecret", svc.RotateSecret())
		must("UpdateConfig без смены выхода", svc.UpdateConfig(Config{TlsDomain: "www.example.com", UpstreamDevice: "nwg0", Upstreams: ups}))
		cand := svc.GetInternalConfig()
		cand.TlsDomain = "www.example.net"
		_, err := svc.PrepareCandidate("tx-same-egress", cand)
		must("PrepareCandidate без смены выхода", err)
		refused("смена выхода", svc.UpdateConfig(Config{UpstreamDevice: "nwg1"}))
		cand.UpstreamDevice, cand.Upstreams = "nwg1", nil
		_, err = svc.PrepareCandidate("tx-new-egress", cand)
		refused("PrepareCandidate со сменой выхода", err)

		must("Stop", svc.Stop())
		refused("Start", svc.Start())
		cand = svc.GetInternalConfig()
		cand.Enabled = true
		_, err = svc.PrepareCandidate("tx-enable", cand)
		refused("PrepareCandidate с включением", err)
	}
}

func TestBuildTgLinks_EffectiveTlsDomainAndCdnHttpNoMtproto(t *testing.T) {
	sec := "00112233445566778899aabbccddeeff"

	// 1. Empty TlsDomain in dual/direct_fake_tls falls back to "ya.ru" (79612e7275), matching MtgEngine
	revDual, err := BuildTgLinks(Config{
		Enabled:        true,
		Scenario:       ScenarioDual,
		PublicHostname: "tg.example.com",
		DirectHost:     "203.0.113.10",
		DirectPort:     8443,
		TlsDomain:      "",
		Secret:         sec,
	}, sec)
	if err != nil {
		t.Fatalf("BuildTgLinks dual: %v", err)
	}
	wantMtSec := "ee" + sec + "79612e7275"
	if revDual.MtproxySecret != wantMtSec {
		t.Fatalf("MtproxySecret = %q, want %q", revDual.MtproxySecret, wantMtSec)
	}
	if revDual.MtproxyURL != "tg://proxy?server=203.0.113.10&port=8443&secret="+wantMtSec {
		t.Fatalf("unexpected MtproxyURL: %q", revDual.MtproxyURL)
	}
	if revDual.TgURL != "tg://webproxy?server=tg.example.com&secret="+sec {
		t.Fatalf("unexpected TgURL: %q", revDual.TgURL)
	}

	// 2. cdn_http never emits MTProto secret, URLs, or DirectHost even if stale DirectHost sits in config
	revWebOnly, err := BuildTgLinks(Config{
		Enabled:        true,
		Scenario:       ScenarioCDNHTTP,
		PublicHostname: "tg.example.com, tg2.example.com",
		DirectHost:     "stale.example.com",
		DirectPort:     8443,
		TlsDomain:      "www.google.com",
		Secret:         sec,
	}, sec)
	if err != nil {
		t.Fatalf("BuildTgLinks cdn_http: %v", err)
	}
	if revWebOnly.MtproxySecret != "" || revWebOnly.MtproxyURL != "" || revWebOnly.MtproxyTmeURL != "" || revWebOnly.DirectHost != "" {
		t.Fatalf("cdn_http leaked MTProto fields: %+v", revWebOnly)
	}
	if revWebOnly.TgURL != "tg://webproxy?server=tg.example.com&secret="+sec {
		t.Fatalf("unexpected multi-host TgURL: %q", revWebOnly.TgURL)
	}
	if len(revWebOnly.Users) != 1 || revWebOnly.Users[0].MtproxyURL != "" || revWebOnly.Users[0].MtproxySecret != "" {
		t.Fatalf("cdn_http user reveal leaked MTProto fields: %+v", revWebOnly.Users)
	}
	if len(revWebOnly.WebProxies) != 2 ||
		revWebOnly.WebProxies[0].Hostname != "tg.example.com" ||
		revWebOnly.WebProxies[0].TgURL != "tg://webproxy?server=tg.example.com&secret="+sec ||
		revWebOnly.WebProxies[1].Hostname != "tg2.example.com" ||
		revWebOnly.WebProxies[1].TgURL != "tg://webproxy?server=tg2.example.com&secret="+sec {
		t.Fatalf("expected split single-host WebProxies, got: %+v", revWebOnly.WebProxies)
	}
}

func TestRenameDefaultAndCustomUsers_Validation(t *testing.T) {
	svc, _, _ := setupTestService(t)

	// Default synthesized user ("default" / "Основной") can be renamed directly
	if err := svc.UpdateUser("default", "  Мой телефон  ", nil); err != nil {
		t.Fatalf("rename default user: %v", err)
	}
	users := svc.GetConfig().Users
	if len(users) != 1 || users[0].ID != "default" || users[0].Remark != "Мой телефон" {
		t.Fatalf("unexpected users after default rename: %+v", users)
	}

	// Reject empty/whitespace, control characters, and >64 runes with ErrInvalidConfig
	for _, bad := range []string{"", "   ", "bad\nname", "абвгдеёжзийклмнопрстуфхцчшщъыьэюяабвгдеёжзийклмнопрстуфхцчшщъыьэюя1"} {
		if err := svc.UpdateUser("default", bad, nil); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("UpdateUser(%q): got %v, want ErrInvalidConfig", bad, err)
		}
	}

	// Unknown user returns ErrUserNotFound
	if err := svc.UpdateUser("non-existent", "Имя", nil); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("unknown user err = %v, want ErrUserNotFound", err)
	}

	// Endpoint ID path traversal & duplicate IDs are rejected
	badEps := []WebProxyEndpoint{
		{ID: "../escape", Hostname: "a.example.com", Enabled: true},
	}
	if err := svc.UpdateConfig(Config{Endpoints: badEps}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("path traversal endpoint ID err = %v, want ErrInvalidConfig", err)
	}
	dupEps := []WebProxyEndpoint{
		{ID: "ep-1", Hostname: "a.example.com", Enabled: true},
		{ID: "ep-1", Hostname: "b.example.com", Enabled: true},
	}
	if err := svc.UpdateConfig(Config{Endpoints: dupEps}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("duplicate endpoint ID err = %v, want ErrInvalidConfig", err)
	}

	// Auto-generated empty ID avoids colliding with explicit IDs and does not mutate caller slice
	callerEps := []WebProxyEndpoint{
		{ID: "ep-2", Hostname: "first.example.com", Enabled: true},
		{ID: "", Hostname: "second.example.com", Enabled: true},
	}
	if err := svc.UpdateConfig(Config{Endpoints: callerEps}); err != nil {
		t.Fatalf("auto-generated endpoint ID should not collide with explicit ep-2: %v", err)
	}
	if callerEps[1].ID != "" {
		t.Fatalf("UpdateConfig mutated caller slice: %+v", callerEps)
	}
	savedEps := svc.GetConfig().Endpoints
	if len(savedEps) != 2 || savedEps[0].ID != "ep-2" || savedEps[1].ID != "ep-3" {
		t.Fatalf("expected collision-free endpoint IDs [ep-2, ep-3], got %+v", savedEps)
	}

	// Extra endpoint auto-allocated ports skip DirectPort (e.g., 18084) in dual mode
	if err := svc.UpdateConfig(Config{
		Scenario:   ScenarioDual,
		DirectPort: 18084,
		Endpoints: []WebProxyEndpoint{
			{ID: "ep-primary", Hostname: "first.example.com", Enabled: true},
			{ID: "ep-second", Hostname: "second.example.com", Enabled: true},
		},
	}); err != nil {
		t.Fatalf("UpdateConfig with DirectPort=18084 and 2 endpoints failed: %v", err)
	}
	nonColliding := svc.GetConfig().Endpoints
	if len(nonColliding) != 2 || nonColliding[1].ListenPort == 18084 {
		t.Fatalf("expected ep[1] to skip DirectPort 18084, got %+v", nonColliding)
	}

	// Partial UpdateConfig changing DirectPort to 18087 (ep[1]'s current port) shifts ep[1] cleanly without re-sending Endpoints
	if err := svc.UpdateConfig(Config{DirectPort: nonColliding[1].ListenPort}); err != nil {
		t.Fatalf("partial UpdateConfig changing DirectPort to ep[1].ListenPort failed: %v", err)
	}
	if svc.GetConfig().Endpoints[1].ListenPort == nonColliding[1].ListenPort {
		t.Fatalf("expected partial UpdateConfig to shift ep[1] off DirectPort %d", nonColliding[1].ListenPort)
	}

	// Synthesized GetEndpoints() from comma-separated PublicHostname also skips DirectPort 18084
	synthEps := (Config{
		Scenario:       ScenarioDual,
		DirectPort:     18084,
		PublicHostname: "a.example.com, b.example.com",
	}).GetEndpoints()
	if len(synthEps) != 2 || synthEps[1].ListenPort == 18084 {
		t.Fatalf("expected synthesized GetEndpoints() ep[1] to skip DirectPort 18084, got %+v", synthEps)
	}
}

func TestDisabledUsers_DoNotFallbackToGlobalOrZeroSecret(t *testing.T) {
	sec := "00112233445566778899aabbccddeeff"
	cfg := Config{
		Enabled:    true,
		Scenario:   ScenarioDirectFakeTLS,
		DirectPort: 0,
		Secret:     sec,
		Users: []TelegramUser{
			{ID: "default", Remark: "Основной", Secret: sec, Enabled: false},
		},
	}

	spec := buildMtgSpec(cfg)
	if !spec.HasUsers || spec.AllSecretsCSV != "" {
		t.Fatalf("unexpected mtg spec for all-disabled users: %+v", spec)
	}

	profBytes, err := GenerateProfilesJSON(cfg)
	if err != nil {
		t.Fatalf("GenerateProfilesJSON: %v", err)
	}
	if slices.Contains(cfg.ActiveSecrets(), sec) {
		t.Fatal("disabled user secret still in ActiveSecrets")
	}
	var parsed struct {
		Profiles []struct {
			Secret string `json:"secret"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(profBytes, &parsed); err != nil || len(parsed.Profiles) != 1 {
		t.Fatalf("unmarshal profiles: %v, %+v", err, parsed)
	}
	if parsed.Profiles[0].Secret == sec || parsed.Profiles[0].Secret == "00000000000000000000000000000000" || len(parsed.Profiles[0].Secret) != 32 {
		t.Fatalf("unexpected dummy profile secret: %q", parsed.Profiles[0].Secret)
	}
}
