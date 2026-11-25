package tgwebproxy

import (
	"errors"
	"strings"
	"testing"
)

// Порт, сохранённый прежней версией (443) или попавший в резерв (2222), не
// должен мешать отзыву доступа и остальным правкам работающего прокси.
// Мутация: вернуть проверку диапазона в applyConfigLocked, проверять
// неизменённый порт в UpdateConfig или PrepareCandidate → красный.
func TestLegacyDirectPort_DoesNotBlockEditsStopAndRollback(t *testing.T) {
	for _, port := range []int{443, 2222} {
		svc, tempDir, _ := setupTestService(t)
		if err := svc.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
		svc.mu.Lock()
		svc.config.DirectPort = port
		svc.mu.Unlock()

		if err := svc.CreateUser("гость"); err != nil {
			t.Fatalf("port %d: CreateUser: %v", port, err)
		}
		users := svc.GetInternalConfig().Users
		if err := svc.UpdateUser(users[len(users)-1].ID, "гость 2", nil); err != nil {
			t.Fatalf("port %d: UpdateUser: %v", port, err)
		}
		if err := svc.DeleteUser(users[len(users)-1].ID); err != nil {
			t.Fatalf("port %d: DeleteUser: %v", port, err)
		}
		if err := svc.RotateSecret(); err != nil {
			t.Fatalf("port %d: RotateSecret: %v", port, err)
		}
		if err := svc.RevokeLegacySecret(); err != nil {
			t.Fatalf("port %d: RevokeLegacySecret: %v", port, err)
		}
		for _, cfg := range []Config{{TlsDomain: "www.example.com"}, {TlsDomain: "www.example.org", DirectPort: port}} {
			if err := svc.UpdateConfig(cfg); err != nil {
				t.Fatalf("port %d: UpdateConfig без смены порта: %v", port, err)
			}
		}
		if err := svc.StartConfigured(); err != nil {
			t.Fatalf("port %d: StartConfigured: %v", port, err)
		}

		// Правка через координатор и откат сохраняют порт как был.
		cand := svc.GetInternalConfig()
		cand.TlsDomain = "www.example.net"
		txID, err := svc.PrepareCandidate("tx-legacy-port", cand)
		if err != nil {
			t.Fatalf("port %d: PrepareCandidate без смены порта: %v", port, err)
		}
		if err := svc.CommitPrepared(txID); err != nil {
			t.Fatalf("port %d: CommitPrepared: %v", port, err)
		}
		if err := svc.RollbackPrepared(txID); err != nil {
			t.Fatalf("port %d: RollbackPrepared: %v", port, err)
		}
		if err := svc.ApplyManagedIngress(ManagedIngressConfig{Enabled: true, ListenPort: DefaultListenPort}); err != nil {
			t.Fatalf("port %d: ApplyManagedIngress: %v", port, err)
		}

		// Остановка обоими путями.
		stop := svc.GetInternalConfig()
		stop.Enabled = false
		if _, err := svc.PrepareCandidate("tx-legacy-stop", stop); err != nil {
			t.Fatalf("port %d: PrepareCandidate stop: %v", port, err)
		}
		if err := svc.Stop(); err != nil {
			t.Fatalf("port %d: Stop: %v", port, err)
		}

		reloaded := &Service{dataDir: tempDir}
		if err := reloaded.loadAndMigrateConfig(); err != nil {
			t.Fatalf("port %d: загрузка: %v", port, err)
		}
		if got := reloaded.GetInternalConfig().DirectPort; got != port {
			t.Fatalf("загрузка изменила порт: %d, было %d", got, port)
		}
	}
}

// Мутация: убрать ValidateDirectPortTransition из Start / PrepareCandidate или
// подсказку из ошибки → красный.
func TestLegacyDirectPort_StartRefusesWithHint(t *testing.T) {
	svc, _, _ := setupTestService(t)
	svc.mu.Lock()
	svc.config.DirectPort = 443
	svc.mu.Unlock()

	check := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, ErrInvalidDirectPort) || !strings.Contains(err.Error(), "Смените прямой порт") {
			t.Fatalf("%s: ждали ErrInvalidDirectPort с подсказкой, получили %v", what, err)
		}
	}
	check("Start", svc.Start())
	cand := svc.GetInternalConfig()
	cand.Enabled = true
	_, err := svc.PrepareCandidate("tx-legacy-start", cand)
	check("PrepareCandidate", err)

	// Без прямого подключения порт не используется — запуск разрешён.
	svc.mu.Lock()
	svc.config.Scenario = ScenarioCDNHTTP
	svc.mu.Unlock()
	if err := svc.Start(); err != nil {
		t.Fatalf("Start cdn_http: %v", err)
	}
	// Включение прямого подключения — тот же отказ, что у Start.
	check("UpdateConfig", svc.UpdateConfig(Config{Scenario: ScenarioDual}))
}
