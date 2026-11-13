package metrics

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/testutil"
)

func TestMain(m *testing.M) {
	// Тесты не форкают /opt/bin/wg хоста: по умолчанию wg «нет», и туннель
	// идёт откатом через PeerStore — на нём и стоят тесты, писанные под NDMS.
	// Путь через wg подменяет сам тест (stubWg).
	wgShowDump = func(context.Context, string) (string, error) {
		return "", errors.New("wg: not installed (test)")
	}
	testutil.Main(m)
}
