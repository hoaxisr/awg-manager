package ops

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// recordingPoster ловит то, что уезжает в роутер по RCI. С f — пересылает
// команды в оракул (FakeNDMS) и отвечает его ответом: E и фантомы видны там.
type recordingPoster struct {
	payloads []any
	f        *ndmsquery.FakeNDMS
}

func (p *recordingPoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	p.payloads = append(p.payloads, payload)
	if p.f != nil {
		return p.f.Post(ctx, payload)
	}
	return json.RawMessage(`[{"status":[{"status":"ok"}]}]`), nil
}

// маску ищем в payload вида {"interface":{name:{"ip":{"address":{...}}}}}
func maskFromPayloads(payloads []any, iface string) (string, bool) {
	for _, p := range payloads {
		root, ok := p.(map[string]any)
		if !ok {
			continue
		}
		ifaces, ok := root["interface"].(map[string]any)
		if !ok {
			continue
		}
		body, ok := ifaces[iface].(map[string]any)
		if !ok {
			continue
		}
		ip, ok := body["ip"].(map[string]any)
		if !ok {
			continue
		}
		addr, ok := ip["address"].(map[string]any)
		if !ok {
			continue
		}
		mask, ok := addr["mask"].(string)
		if ok {
			return mask, true
		}
	}
	return "", false
}

// Граница «оператор → NDMS»: маска, которую ввёл пользователь, обязана дойти
// до роутера. Это заглавный смысл всей правки, и без такого стража подмена
// maskFromPrefix на константу прошла бы незамеченной.
func TestSyncAddressSendsUserMaskToNDMS(t *testing.T) {
	tests := []struct {
		name     string
		prefix   int
		wantMask string
	}{
		{"подсеть пользователя", 24, "255.255.255.0"},
		{"адрес точки", 32, "255.255.255.255"},
		{"маска не задана", 0, "255.255.255.255"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			poster := &recordingPoster{}
			queries := ndmsquery.NewQueries(ndmsquery.Deps{
				Getter: ndmsquery.NewFakeNDMS(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun"}),
				Logger: ndmsquery.NopLogger(),
				IsOS5:  func() bool { return true },
			})
			cmds := ndmscommand.NewCommands(ndmscommand.Deps{
				Poster:  poster,
				Queries: queries,
				// Сохранение конфигурации роутера в тесте не нужно, но
				// координатор обязан быть непустым: он вызывается после
				// каждой мутации.
				Save:  ndmscommand.NewSaveCoordinator(poster, nil, time.Hour, time.Hour, 0, queries.RunningConfig),
				IsOS5: func() bool { return true },
			})
			o := NewOperatorOS5(queries, cmds, &MockWGClient{}, &MockBackend{}, &MockFirewall{})

			if err := o.SyncAddress(context.Background(), "awg10", "10.8.0.2", tt.prefix, ""); err != nil {
				t.Fatalf("SyncAddress: %v", err)
			}

			mask, ok := maskFromPayloads(poster.payloads, "OpkgTun10")
			if !ok {
				t.Fatalf("маска не уехала в NDMS вовсе, payloads: %+v", poster.payloads)
			}
			if mask != tt.wantMask {
				t.Errorf("в NDMS ушла маска %q, want %q", mask, tt.wantMask)
			}
		})
	}
}
