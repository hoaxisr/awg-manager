package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

// guardedControlSvc — служба, чей страж uplink — настоящий контроллер.
type guardedControlSvc struct {
	stubTunnelSvc
	controller *uplink.Controller
	guarded    []string
}

func (s *guardedControlSvc) GuardUplinkMutation(ctx context.Context, id, operation string) (func(), error) {
	s.guarded = append(s.guarded, id+":"+operation)
	return s.controller.GuardNativeMutation(ctx, id, operation)
}
func (s *guardedControlSvc) List(context.Context) ([]service.TunnelWithStatus, error) {
	return []service.TunnelWithStatus{{ID: "awg10", Enabled: true}}, nil
}

// (6) Кнопки карточки зовут оркестратор напрямую — и всё равно проходят
// страж uplink: туннель политики под паузой бэкапа не трогается. Оркестратор
// не подключён нарочно: обход стража упал бы паникой.
func TestControlLifecycleGoesThroughUplinkGuard(t *testing.T) {
	dir := t.TempDir()
	policies, err := uplink.OpenStore(filepath.Join(dir, "uplink.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policies.Put(uplink.Policy{ID: "p", Target: uplink.Target{Engine: "wireguard", Ref: "awg10"}, Providers: []string{"a", "b"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	c := uplink.New(policies, nil, nil)
	if err := c.Pause(context.Background()); err != nil {
		t.Fatal(err)
	}
	svc := &guardedControlSvc{controller: c}
	h := NewControlHandler(svc, storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks")), nil)
	for name, call := range map[string]func(http.ResponseWriter, *http.Request){
		"start":       h.Start,
		"stop":        h.Stop,
		"restart":     h.Restart,
		"restart-all": h.RestartAll,
	} {
		svc.guarded = nil
		rec := httptest.NewRecorder()
		call(rec, httptest.NewRequest(http.MethodPost, "/api/control/"+name+"?id=awg10", nil))
		if len(svc.guarded) != 1 || svc.guarded[0] != "awg10:lifecycle" {
			t.Fatalf("%s: guard calls %v", name, svc.guarded)
		}
		if !strings.Contains(rec.Body.String(), "paused for backup") {
			t.Fatalf("%s: %s", name, rec.Body.String())
		}
	}
}
