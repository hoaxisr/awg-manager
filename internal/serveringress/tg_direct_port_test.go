package serveringress

import (
	"context"
	"errors"
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

// Мутация: снова глотать ошибку UpdateConfig → красный.
func TestApplyDispatcherConfig_ReturnsTelegramUpdateError(t *testing.T) {
	boom := errors.New("boom")
	tg := &mockTgWebProxy{updateError: boom}
	coord := New(t.TempDir(), &mockXray{}, &mockDispatcher{}, tg)
	if err := coord.ApplyDispatcherConfig(context.Background(), cdndispatcher.Config{}); !errors.Is(err, boom) {
		t.Fatalf("ошибка UpdateConfig потеряна: %v", err)
	}
}
