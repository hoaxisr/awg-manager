package router

import (
	"net/netip"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/testutil"
)

// Маршруты tun по умолчанию — пусто, а не /proc хоста: иначе тесты reconcile
// зависели бы от того, есть ли на машине opkgtun0 (ревью F756).
func TestMain(m *testing.M) {
	fakeIPTunRoutes = func(string) ([]netip.Prefix, error) { return nil, nil }
	testutil.Main(m)
}
