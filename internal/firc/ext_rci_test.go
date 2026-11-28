package firc

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
)

type fakeRCI struct {
	asked   []string
	answers map[string]string
	err     error
}

func (f *fakeRCI) GetRaw(_ context.Context, path string) ([]byte, error) {
	f.asked = append(f.asked, path)
	if f.err != nil {
		return nil, f.err
	}
	if body, ok := f.answers[path]; ok {
		return []byte(body), nil
	}
	return nil, &transport.HTTPError{Status: http.StatusNotFound}
}

func TestFetchRCIServers(t *testing.T) {
	rci := &fakeRCI{answers: map[string]string{
		"/show/rc/wireguard-server": `{"config":{"enable":true,"subnet":"172.16.6.1/24"}}`,
		"/show/rc/crypto":           `{"l2tp-server":{"enable":true,"pool-start":"172.16.2.33"}}`,
	}}
	installed := []string{"wireguard-server", "vpnserver-l2tp", "sstp-server"}
	op := &Operator{rci: rci, hasComponent: func(n string) bool { return slices.Contains(installed, n) }}

	snap, ok := op.fetchRCIServers(context.Background())
	if !ok || !snap.WireguardServer.Config.Enable || snap.Crypto.L2tpServer.PoolStart != "172.16.2.33" {
		t.Fatalf("ok=%v snap=%+v", ok, snap)
	}
	// Не установленные (pptp, oc) не читаются: NDMS пишет на это E.
	want := []string{"/show/rc/wireguard-server", "/show/rc/sstp-server", "/show/rc/crypto"}
	if !slices.Equal(rci.asked, want) {
		t.Fatalf("asked %v, want %v", rci.asked, want)
	}

	// 404 запомнен — повторно раздел не спрашиваем.
	rci.asked = nil
	op.fetchRCIServers(context.Background())
	if slices.Contains(rci.asked, "/show/rc/sstp-server") {
		t.Fatalf("отсутствующий раздел перечитан: %v", rci.asked)
	}

	rci.err = errors.New("rci down")
	if _, ok := op.fetchRCIServers(context.Background()); ok {
		t.Fatal("отказ RCI должен давать ok=false")
	}
}
