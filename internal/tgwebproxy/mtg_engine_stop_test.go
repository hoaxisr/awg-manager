package tgwebproxy

import (
	"encoding/hex"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/testutil"
)

// Все пользователи выключены: движок не должен впускать по глобальному
// секрету (он совпадает с секретом выключенного «Основного»).
func TestMtgEngine_AllUsersDisabled_NoGlobalSecret(t *testing.T) {
	sec := "00112233445566778899aabbccddeeff"
	engine := NewMtgEngine()
	defer engine.Stop()
	cfg := Config{
		Enabled:    true,
		Scenario:   ScenarioDirectFakeTLS,
		DirectPort: testutil.FreePort(t),
		Secret:     sec,
		TlsDomain:  "ya.ru",
		Users:      []TelegramUser{{ID: "default", Secret: sec, Enabled: false}},
	}
	if err := engine.Apply(cfg); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	key, _ := hex.DecodeString(sec)
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if len(engine.secrets) == 0 {
		t.Fatal("expected a dummy secret, got none")
	}
	for _, s := range engine.secrets {
		if string(s.Key[:]) == string(key) {
			t.Fatalf("disabled user's secret is still accepted")
		}
	}
}

// Остановка и перезапуск под потоком входящих подключений: под -race
// ловит Add у WaitGroup прокси после начатого Shutdown().Wait().
func TestMtgEngine_StopUnderIncomingConnections(t *testing.T) {
	port := testutil.FreePort(t)
	engine := NewMtgEngine()
	defer engine.Stop()
	cfg := Config{
		Enabled:    true,
		Scenario:   ScenarioDirectFakeTLS,
		DirectPort: port,
		Secret:     "00112233445566778899aabbccddeeff",
		TlsDomain:  "ya.ru",
	}
	if err := engine.Apply(cfg); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var stop atomic.Bool
	var wg sync.WaitGroup
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
				if err != nil {
					continue
				}
				_, _ = c.Write([]byte{0x16, 0x03, 0x01, 0x00, 0x05})
				_ = c.Close()
			}
		}()
	}
	for i := 0; i < 40; i++ {
		engine.Stop()
		if err := engine.Apply(cfg); err != nil {
			stop.Store(true)
			wg.Wait()
			t.Fatalf("Apply #%d: %v", i, err)
		}
	}
	stop.Store(true)
	wg.Wait()
}
