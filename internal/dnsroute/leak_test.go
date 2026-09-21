package dnsroute

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
	"github.com/hoaxisr/awg-manager/internal/testutil"
)

// fetchSubscription отказывает на внутренних адресах, а httptest живёт на
// loopback. Границу держит TestFetchSubscription_RejectsInternalURL.
var restoreGuard func()

func TestMain(m *testing.M) {
	restoreGuard = httpclient.AllowInternalDialForTest()
	testutil.Main(m)
}
