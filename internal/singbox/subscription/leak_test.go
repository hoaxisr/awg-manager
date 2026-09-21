package subscription

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
	"github.com/hoaxisr/awg-manager/internal/testutil"
)

// Загрузка идёт стражным клиентом (httpclient.NewPublicClient), а httptest
// живёт на loopback, который страж закрывает намеренно. Границу держит
// TestFetch_BlocksInternalAddress — он возвращает страж на время теста.
var restoreGuard func()

func TestMain(m *testing.M) {
	restoreGuard = httpclient.AllowInternalDialForTest()
	testutil.Main(m)
}
