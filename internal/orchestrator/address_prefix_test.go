package orchestrator

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Маска, введённая пользователем, обязана доезжать до оператора: до этого она
// срезалась вместе с префиксом, и интерфейс всегда получал /32 — то есть
// маршрут на VPN-подсеть не появлялся.
func TestStoredToConfigCarriesAddressPrefix(t *testing.T) {
	tests := []struct {
		name       string
		address    string
		wantIPv4   string
		wantPrefix int
	}{
		{"обычная подсеть", "10.55.0.2/24", "10.55.0.2", 24},
		{"адрес точки", "10.102.0.2/32", "10.102.0.2", 32},
		{"без маски", "10.8.0.2", "10.8.0.2", 0},
		{"два адреса", "10.8.0.2/24, fd00::2/64", "10.8.0.2", 24},
		{"пусто", "", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := StoredToConfig(&storage.AWGTunnel{
				ID:        "awg10",
				Interface: storage.AWGInterface{Address: tt.address},
			})
			if cfg.Address != tt.wantIPv4 {
				t.Errorf("Address = %q, want %q", cfg.Address, tt.wantIPv4)
			}
			if cfg.AddressPrefix != tt.wantPrefix {
				t.Errorf("AddressPrefix = %d, want %d", cfg.AddressPrefix, tt.wantPrefix)
			}
		})
	}
}
