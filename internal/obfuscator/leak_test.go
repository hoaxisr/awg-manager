package obfuscator

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
	"github.com/hoaxisr/awg-manager/internal/testutil"
)

// Загрузка идёт стражным клиентом, а httptest живёт на loopback, который
// страж закрывает намеренно. Границу держит TestFetchPhobosConf_BlocksInternal.
var restoreGuard func()

func TestMain(m *testing.M) {
	restoreGuard = httpclient.AllowInternalDialForTest()
	testutil.Main(m)
}
