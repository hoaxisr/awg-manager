package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// cachedVsFreshSvc: кэш и роутер расходятся в живых полях.
type cachedVsFreshSvc struct{}

func (cachedVsFreshSvc) List(context.Context) ([]ndms.SystemWireguardTunnel, error) {
	return []ndms.SystemWireguardTunnel{{ID: "Wireguard0", Uptime: 10}}, nil
}

func (cachedVsFreshSvc) ListFresh(context.Context) ([]ndms.SystemWireguardTunnel, error) {
	return []ndms.SystemWireguardTunnel{{ID: "Wireguard0", Uptime: 300}}, nil
}

func (cachedVsFreshSvc) Get(context.Context, string) (*ndms.SystemWireguardTunnel, error) {
	return nil, nil
}

func (cachedVsFreshSvc) GetASCParams(context.Context, string) (json.RawMessage, error) {
	return nil, nil
}

func (cachedVsFreshSvc) SetASCParams(context.Context, string, json.RawMessage) error { return nil }

// F467: список для показа (GET /system-tunnels и снимок /tunnels/all) читается
// мимо кэша состава — иначе uptime и счётчики стоят до TTL в 5 минут.
func TestListSystemTunnels_ReadsFreshNotCached(t *testing.T) {
	h := NewSystemTunnelsHandler(cachedVsFreshSvc{}, storage.NewSettingsStore(t.TempDir()), nil, nil)
	got, err := h.listSystemTunnels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Uptime != 300 {
		t.Fatalf("listSystemTunnels = %+v, ожидали свежий uptime 300", got)
	}
}

// countingSvc считает обращения к роутеру (Get), состав отдаёт из «кэша».
type countingSvc struct {
	cachedVsFreshSvc
	gets int
}

func (s *countingSvc) Get(_ context.Context, name string) (*ndms.SystemWireguardTunnel, error) {
	s.gets++
	return &ndms.SystemWireguardTunnel{ID: name, Status: "down"}, nil
}

// Шаг 5 плана нагрузки: проверка связности (раз в минуту на карточку) берёт
// статус из кэша состава и не ходит в роутер; промах — прежнее чтение.
func TestCachedTunnel_HitSkipsRouterMissFallsBack(t *testing.T) {
	svc := &countingSvc{}
	h := NewSystemTunnelsHandler(svc, storage.NewSettingsStore(t.TempDir()), nil, nil)

	got, err := h.cachedTunnel(context.Background(), "Wireguard0")
	if err != nil || got.ID != "Wireguard0" || svc.gets != 0 {
		t.Fatalf("попадание: %+v, %v, gets=%d — ждали из кэша без Get", got, err, svc.gets)
	}
	got, err = h.cachedTunnel(context.Background(), "Wireguard9")
	if err != nil || got.ID != "Wireguard9" || svc.gets != 1 {
		t.Fatalf("промах: %+v, %v, gets=%d — ждали один Get", got, err, svc.gets)
	}
}
