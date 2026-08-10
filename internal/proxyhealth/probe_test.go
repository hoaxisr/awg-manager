package proxyhealth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestAWGLinkedTunnelResolverFreeTurn(t *testing.T) {
	dir := t.TempDir()
	tunnelsDir := filepath.Join(dir, "tunnels")
	if err := os.MkdirAll(tunnelsDir, 0755); err != nil {
		t.Fatal(err)
	}
	store := storage.NewAWGTunnelStoreWithLockDir(tunnelsDir, filepath.Join(dir, "locks"))
	if err := store.Save(&storage.AWGTunnel{
		ID:               "awg10",
		Enabled:          true,
		FreeTurnClientID: "default",
	}); err != nil {
		t.Fatal(err)
	}

	r := &AWGLinkedTunnelResolver{Store: store}
	iface, ok := r.FreeTurnLinkedIface("default")
	if !ok || iface != "opkgtun10" {
		t.Fatalf("FreeTurnLinkedIface = %q, %v; want opkgtun10, true", iface, ok)
	}
	if _, ok := r.FreeTurnLinkedIface("missing"); ok {
		t.Fatal("expected false for unknown client")
	}
}

func TestHTTPRelayProbeNilSafe(t *testing.T) {
	var p *HTTPRelayProbe
	if !p.ProbeInterface(context.Background(), "") {
		t.Fatal("nil/empty iface should be treated as ok")
	}
}
