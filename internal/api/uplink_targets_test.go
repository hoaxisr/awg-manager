package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

type nativeTargetListing struct{ uplink.Adapter }

func (nativeTargetListing) UplinkTargets(context.Context) ([]uplink.TargetInfo, error) {
	return []uplink.TargetInfo{{Target: uplink.Target{Engine: "wireguard", Ref: "awg50"}, Label: "Native WG"}}, nil
}
func (nativeTargetListing) UplinkCapabilities(uplink.Target) uplink.Capabilities {
	return uplink.Capabilities{IndependentEngine: true}
}
func TestUplinkSnapshotListsNativeTargetsAndCapabilities(t *testing.T) {
	store, err := uplink.OpenStore(filepath.Join(t.TempDir(), "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := uplink.New(store, emptyProviders{}, map[string]uplink.Adapter{"wireguard": nativeTargetListing{}})
	r := httptest.NewRecorder()
	(&UplinkHandler{Controller: c}).Policies(r, httptest.NewRequest(http.MethodGet, "/api/uplink/policies", nil))
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"ref":"awg50"`) || !strings.Contains(r.Body.String(), `"independentEngine":true`) || !strings.Contains(r.Body.String(), `"automaticFailback":false`) {
		t.Fatal(r.Code, r.Body.String())
	}
}

type emptyProviders struct{}

func (emptyProviders) Providers() []uplink.Provider { return nil }
