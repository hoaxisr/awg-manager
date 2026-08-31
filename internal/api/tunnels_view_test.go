package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

// F56: GET-ответ туннеля не несёт ключевого материала — ни в interface, ни во
// втором канале configPreview (тот же ключ печатает config.Generate).
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
		ConfigPreview: "[Interface]\nPrivateKey = " + priv + "\nAddress = 10.0.0.2/32\n\n[Peer]\nPresharedKey = " + psk + "\n",
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

	preview, _ := resp["configPreview"].(string)
	if strings.Contains(preview, priv) {
		t.Errorf("configPreview несёт приватный ключ: %q", preview)
	}
	if strings.Contains(preview, psk) {
		t.Errorf("configPreview несёт preshared-ключ: %q", preview)
	}
	if !strings.Contains(preview, "Address = 10.0.0.2/32") {
		t.Errorf("configPreview потерял несекретные строки: %q", preview)
	}
}
