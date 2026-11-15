package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// #1210: правка, убравшая параметр из .conf работающего kernel-туннеля,
// даёт NeedsRestart — `awg setconf` его в ядре не сбросит. Смена
// значения, остановленный туннель и NativeWG флага не дают.
// Мутации: флаг всегда; без проверки состояния; без проверки бэкенда → красный.
func TestTunnelUpdate_NeedsRestartOnDroppedParam(t *testing.T) {
	const iface = `"address":"10.0.0.2/32","mtu":1280,"jc":4,"jmin":40,"jmax":70,"h1":"1","h2":"2","h3":"3","h4":"4","i1":"<b 0x01>"`
	const dropped = `{"interface":{` + iface + `}}`
	const changed = `{"interface":{` + iface + `,"i2":"<b 0xff>"}}`

	for _, tc := range []struct {
		name    string
		backend string
		state   tunnel.State
		body    string
		want    bool
	}{
		{"kernel работает, I2 убран", "kernel", tunnel.StateRunning, dropped, true},
		{"kernel работает, I2 изменён", "kernel", tunnel.StateRunning, changed, false},
		{"kernel остановлен, I2 убран", "kernel", tunnel.StateStopped, dropped, false},
		{"nativewg работает, I2 убран", "nativewg", tunnel.StateRunning, dropped, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, store := newTunnelsUpdateHarness(t, &stubTunnelSvc{
				stateFn: func(string) tunnel.StateInfo { return tunnel.StateInfo{State: tc.state} },
			})
			seed := &storage.AWGTunnel{}
			if err := json.Unmarshal([]byte(`{"id":"awg10","name":"t","enabled":true,"backend":"`+tc.backend+
				`","interface":{`+iface+`,"i2":"<b 0x02>"},"peer":{"endpoint":"1.2.3.4:51820"}}`), seed); err != nil {
				t.Fatal(err)
			}
			if err := store.Create(seed); err != nil {
				t.Fatalf("seed: %v", err)
			}

			var req storage.AWGTunnel
			if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
				t.Fatal(err)
			}
			edited, err := h.EditTunnel(context.Background(), "awg10", req)
			if err != nil {
				t.Fatalf("EditTunnel: %v", err)
			}
			if edited.NeedsRestart != tc.want {
				t.Fatalf("NeedsRestart = %v, want %v", edited.NeedsRestart, tc.want)
			}
		})
	}
}
