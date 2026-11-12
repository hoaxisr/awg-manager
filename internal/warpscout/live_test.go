package warpscout

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestLive_RegisterAndScan — сквозная проверка против настоящего Cloudflare:
// регистрация учётки Go-кодом и скан C-модулем через BinaryRunner с
// AWG-сигнатурой. Пропускается без AWGM_WARPSCOUT_LIVE=1 и
// AWGM_WARPSCOUT_BIN (путь к собранному c-core/warpscout).
func TestLive_RegisterAndScan(t *testing.T) {
	if os.Getenv("AWGM_WARPSCOUT_LIVE") != "1" {
		t.Skip("live-тест: задайте AWGM_WARPSCOUT_LIVE=1 и AWGM_WARPSCOUT_BIN")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	hc := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{}}
	defer hc.CloseIdleConnections()
	acct, err := RegisterAccount(ctx, hc)
	if err != nil {
		t.Fatalf("регистрация: %v", err)
	}
	t.Logf("учётка %s, v4=%s, reserved=%v", acct.ID, acct.AddressV4, acct.Reserved())
	defer func() { _ = DeleteAccount(context.Background(), hc, acct) }()

	st, _ := NewStore(t.TempDir(), plainCipher{})
	_ = st.SetAccount(acct)
	s := New(Deps{Store: st, Runner: &BinaryRunner{Path: os.Getenv("AWGM_WARPSCOUT_BIN")}})
	defer s.Stop()
	awg := s.awgForUse()
	if awg.I1 == "" {
		t.Fatal("сигнатура не сгенерирована")
	}
	f := DefaultFilters()
	p, err := s.buildParams(ctx, acct, awg, f, 3, 300, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	var hits []Hit
	err = s.d.Runner.Run(ctx, p, func(e EngineEvent) {
		t.Logf("%s %s %s %s %d", e.Type, e.Endpoint, e.Colo, e.Loc, e.PingMs)
		if e.Type == "hit" {
			hits = append(hits, e.Hit())
		}
	})
	if err != nil {
		t.Fatalf("скан: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("ни одного узла")
	}
	t.Logf("конфиг:\n%s", BuildConf(acct, awg, ProtocolAWG, hits[0].Endpoint))
}
