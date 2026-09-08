package nwg

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// rciStubServer replies with a fixed body to every POST and records the
// last request body — enough to assert GetState/ResolveActiveWAN use the
// batch-POST form ({"show":{"interface":{"name":…}}}) instead of GET.
type rciStubServer struct {
	srv      *httptest.Server
	response string
	status   int
	lastBody string
}

func newRCIStubServer(t *testing.T, response string) *rciStubServer {
	t.Helper()
	s := &rciStubServer{response: response, status: http.StatusOK}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.lastBody = string(body)
		w.WriteHeader(s.status)
		_, _ = w.Write([]byte(s.response))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func newStateTestOperator(t *testing.T, srvURL string) *OperatorNativeWG {
	t.Helper()
	o := &OperatorNativeWG{
		transport:    transport.NewWithURL(srvURL, transport.NewSemaphore(2)),
		appLog:       logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubOps),
		supportsASC:  func() bool { return true },
		hasProxySlot: func(int) bool { return false },
	}
	t.Cleanup(o.Close)
	return o
}

func TestGetState_ViaPost_Running(t *testing.T) {
	s := newRCIStubServer(t, `{"show":{"interface":{
		"id":"Wireguard5","link":"up",
		"summary":{"layer":{"conf":"running"}},
		"wireguard":{"status":"up","peer":[{"online":true,"last-handshake":12,"rxbytes":100,"txbytes":200,"via":"PPPoE0"}]}
	}}}`)
	op := newStateTestOperator(t, s.srv.URL)

	info := op.GetState(context.Background(), &storage.AWGTunnel{NWGIndex: 5})

	if info.State != tunnel.StateRunning {
		t.Fatalf("State = %v, want %v", info.State, tunnel.StateRunning)
	}
	if !info.InterfaceUp || info.RxBytes != 100 || info.TxBytes != 200 || !info.HasHandshake {
		t.Fatalf("unexpected StateInfo: %+v", info)
	}
	// The request must be the POST form with the name in the body.
	if !strings.Contains(s.lastBody, `"show"`) || !strings.Contains(s.lastBody, `"name":"Wireguard5"`) {
		t.Fatalf("request is not batch-POST show.interface form:\n%s", s.lastBody)
	}
}

func TestGetState_StatusErrorWithoutID_NotCreated(t *testing.T) {
	// NDMS replies HTTP 200 + status-error object (no "id") for a missing
	// interface — same semantics the old GET path had with {}.
	s := newRCIStubServer(t, `{"show":{"interface":{
		"status":[{"status":"error","code":"6553619","message":"unable to find"}]
	}}}`)
	op := newStateTestOperator(t, s.srv.URL)

	info := op.GetState(context.Background(), &storage.AWGTunnel{NWGIndex: 5})
	if info.State != tunnel.StateNotCreated {
		t.Fatalf("State = %v, want %v", info.State, tunnel.StateNotCreated)
	}
}

func TestGetState_TransportError_NotCreated(t *testing.T) {
	s := newRCIStubServer(t, `boom`)
	s.status = http.StatusInternalServerError
	op := newStateTestOperator(t, s.srv.URL)

	info := op.GetState(context.Background(), &storage.AWGTunnel{NWGIndex: 5})
	if info.State != tunnel.StateNotCreated {
		t.Fatalf("State = %v, want %v", info.State, tunnel.StateNotCreated)
	}
}

func TestResolveActiveWAN_NoVia_ReturnsEmpty(t *testing.T) {
	s := newRCIStubServer(t, `{"show":{"interface":{
		"id":"Wireguard5","link":"up",
		"wireguard":{"status":"up","peer":[{"online":true}]}
	}}}`)
	op := newStateTestOperator(t, s.srv.URL)

	if got := op.ResolveActiveWAN(context.Background(), &storage.AWGTunnel{NWGIndex: 5}); got != "" {
		t.Fatalf("ResolveActiveWAN = %q, want empty", got)
	}
	if !strings.Contains(s.lastBody, `"name":"Wireguard5"`) {
		t.Fatalf("request is not batch-POST show.interface form:\n%s", s.lastBody)
	}
}
