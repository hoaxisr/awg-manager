package serveringress

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/cdndispatcher"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

// Отказ по сохранённому порту — до журнала: откат неподготовленной транзакции
// записал бы Telegram чужой домен (домен VLESS).
// Мутация: убрать validateTgDirectPort из applyLocked или
// ExecuteIngressTransaction → красный.
func TestTelegramStart_InvalidStoredPortRefusedBeforeTransaction(t *testing.T) {
	newCoord := func() (*Coordinator, *mockTgWebProxy) {
		tg := &mockTgWebProxy{
			cfg:       tgwebproxy.Config{Scenario: tgwebproxy.ScenarioDirectFakeTLS, DirectPort: 443, ListenPort: 8085, PublicHostname: "tg.example.com"},
			prepError: tgwebproxy.ErrInvalidDirectPort,
		}
		x := &mockXray{cfg: vlessserver.Config{Enabled: true, ListenPort: 9008, PublicDomain: "vless.example.com"}}
		disp := &mockDispatcher{cfg: cdndispatcher.Config{ListenAddr: ":9009"}, running: true}
		return New(t.TempDir(), x, disp, tg), tg
	}
	check := func(what string, tg *mockTgWebProxy, err error) {
		t.Helper()
		if !errors.Is(err, tgwebproxy.ErrInvalidDirectPort) {
			t.Fatalf("%s: ждали ErrInvalidDirectPort, получили %v", what, err)
		}
		if len(tg.applyCalls) != 0 || tg.cfg.PublicHostname != "tg.example.com" {
			t.Fatalf("%s: отказ изменил конфигурацию Telegram: calls=%+v cfg=%+v", what, tg.applyCalls, tg.cfg)
		}
	}

	coord, tg := newCoord()
	check("SetTelegramEnabled", tg, coord.SetTelegramEnabled(context.Background(), true))

	coord, tg = newCoord()
	check("ExecuteIngressTransaction", tg, coord.ExecuteIngressTransaction(context.Background(), IngressTransactionParams{
		TxID: "tx-port", ServerKind: "tgwebproxy",
		Telegram: &IngressTelegramCandidate{Enabled: true, Scenario: tgwebproxy.ScenarioDirectFakeTLS, DirectHost: "203.0.113.10", DirectPort: 443},
	}))
}

// Маршруты сохранены — сбой Telegram не ошибка, а предупреждение; без смены его
// доменов Telegram не трогаем вовсе.
// Мутации: глотать ошибку UpdateConfig; возвращать её ошибкой; звать UpdateConfig
// без смены доменов → красный.
func TestApplyDispatcherConfig_TelegramUpdateIsWarning(t *testing.T) {
	dir := t.TempDir()
	tg := &mockTgWebProxy{cfg: tgwebproxy.Config{PublicHostname: "B.example.com, a.example.com"}, updateError: errors.New("boom")}
	coord := New(dir, &mockXray{}, &mockDispatcher{}, tg)
	routes := func(domains ...string) cdndispatcher.Config {
		var cfg cdndispatcher.Config
		for _, d := range domains {
			cfg.Routes = append(cfg.Routes, cdndispatcher.CDNRouteRule{ID: d, Domain: d, RouteTelegram: true, Enabled: true})
		}
		return cfg
	}
	if w, err := coord.ApplyDispatcherConfig(context.Background(), routes("a.example.com", "b.example.com")); err != nil || w != "" {
		t.Fatalf("домены прежние: %q, %v", w, err)
	}
	w, err := coord.ApplyDispatcherConfig(context.Background(), routes("c.example.com"))
	if err != nil || !strings.Contains(w, "boom") {
		t.Fatalf("сбой Telegram: %q, %v", w, err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "dispatcher", "config.json")); err != nil || !strings.Contains(string(data), "c.example.com") {
		t.Fatalf("маршруты не сохранены: %v", err)
	}
	tg.updateError = nil
	if w, err := coord.ApplyDispatcherConfig(context.Background(), routes("c.example.com")); err != nil || w != "" || tg.cfg.PublicHostname != "c.example.com" {
		t.Fatalf("обновление: %q, %v, %q", w, err, tg.cfg.PublicHostname)
	}
}

// Сверка файрвола после фиксации, отката и сброса не отменяет сделанное.
// Мутация: возвращать её ошибку из applyLocked, failAndRollback,
// ApplyDispatcherConfig, ExecuteIngressTransaction или сбросов → красный.
func TestWANFirewallSyncErrorDoesNotFailCommittedWork(t *testing.T) {
	tg := &mockTgWithFWSync{
		mockTgWebProxy: &mockTgWebProxy{cfg: tgwebproxy.Config{Enabled: true, Scenario: tgwebproxy.ScenarioDirectFakeTLS, DirectPort: 8443, ListenPort: 8085}},
		syncErr:        errors.New("xtables lock"),
	}
	coord := New(t.TempDir(), &mockXray{}, &mockDispatcher{}, tg)
	ctx := context.Background()
	if err := coord.SetTelegramEnabled(ctx, false); err != nil || tg.cfg.Enabled {
		t.Fatalf("выключение: err=%v enabled=%v", err, tg.cfg.Enabled)
	}
	if _, err := coord.ApplyDispatcherConfig(ctx, cdndispatcher.Config{}); err != nil {
		t.Fatalf("ApplyDispatcherConfig: %v", err)
	}
	tg.prepError = errors.New("подготовка не удалась")
	if err := coord.SetTelegramEnabled(ctx, true); !errors.Is(err, tg.prepError) || errors.Is(err, tg.syncErr) {
		t.Fatalf("откат: %v", err)
	}
	if tg.syncCalls < 3 {
		t.Fatalf("сверок %d", tg.syncCalls)
	}

	tg.prepError = nil
	if err := coord.ExecuteIngressTransaction(ctx, IngressTransactionParams{
		TxID: "tx-fw", ServerKind: "tgwebproxy",
		Telegram: &IngressTelegramCandidate{Enabled: true, Scenario: tgwebproxy.ScenarioDirectFakeTLS, DirectHost: "203.0.113.10", DirectPort: 8443},
	}); err != nil {
		t.Fatalf("ExecuteIngressTransaction: %v", err)
	}

	resettable := &resettableTg{mockTgWebProxy: tg.mockTgWebProxy}
	coord = New(t.TempDir(), &resettableXray{mockXray: &mockXray{}}, &mockDispatcher{}, &struct {
		*resettableTg
		failingSync
	}{resettableTg: resettable})
	if _, err := coord.ResetTelegram(ctx, ResetOptions{}); err != nil || !resettable.resetCalled {
		t.Fatalf("ResetTelegram: %v", err)
	}
	if _, err := coord.ResetVless(ctx, ResetOptions{}); err != nil {
		t.Fatalf("ResetVless: %v", err)
	}
}

type failingSync struct{}

func (failingSync) SyncDirectWANFirewall() error { return errors.New("xtables lock") }
