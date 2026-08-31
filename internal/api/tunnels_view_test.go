package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
)

type viewStubSvc struct {
	stubTunnelSvc
	got *service.TunnelWithStatus
}

func (s *viewStubSvc) Get(context.Context, string) (*service.TunnelWithStatus, error) {
	return s.got, nil
}

// F56: GET-ответ туннеля не несёт ключевого материала в interface.
// F71: и самого поля configPreview в ответе больше нет — фронт его не читал,
// а генерация .conf на каждый GET была мёртвой работой.
func TestBuildTunnelResponse_RedactsSecrets(t *testing.T) {
	const priv = "real-private-secret"
	const psk = "real-preshared-secret"

	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{
		ID:   "awg1",
		Name: "t1",
		Interface: storage.AWGInterface{
			PrivateKey:     priv,
			Address:        "10.0.0.2/32",
			MTU:            1420,
			AWGObfuscation: storage.AWGObfuscation{Jc: 5},
		},
		Peer: storage.AWGPeer{PublicKey: "pub", PresharedKey: psk},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	svc := &viewStubSvc{got: &service.TunnelWithStatus{
		ID: "awg1", Name: "t1",
	}}

	req := httptest.NewRequest(http.MethodGet, "/api/tunnels/awg1", nil)
	resp, err := BuildTunnelResponse(req, svc, store, "awg1", time.Time{})
	if err != nil {
		t.Fatalf("BuildTunnelResponse: %v", err)
	}

	iface, ok := resp["interface"].(storage.AWGInterface)
	if !ok {
		t.Fatalf("interface = %T, want storage.AWGInterface", resp["interface"])
	}
	if iface.PrivateKey != "" {
		t.Errorf("interface.privateKey = %q, want пустой", iface.PrivateKey)
	}
	// Полнота: редактирование не должно съесть остальные поля.
	if iface.Address != "10.0.0.2/32" || iface.MTU != 1420 || iface.Jc != 5 {
		t.Errorf("interface = %+v, want адрес/MTU/обфускацию на месте", iface)
	}

	if _, ok := resp["configPreview"]; ok {
		t.Errorf("configPreview остался в ответе: %v", resp["configPreview"])
	}
}
