package api

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
	"github.com/hoaxisr/awg-manager/internal/testutil"
)

// Загрузчики подписок и Phobos идут стражным клиентом (httpclient), а
// httptest живёт на loopback, который страж закрывает намеренно.
func TestMain(m *testing.M) {
	httpclient.AllowInternalDialForTest()
	testutil.Main(m)
}
