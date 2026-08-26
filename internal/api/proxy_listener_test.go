package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/procport"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

type stubProxyRecords struct {
	st  instancestore.State
	err error
}

func (s stubProxyRecords) Load() (instancestore.State, error) { return s.st, s.err }

func testProxyListenerHandler() *ProxyListenerHandler {
	return NewProxyListenerHandler(stubProxyRecords{st: instancestore.State{Records: []instancestore.Record{
		{ID: "default", Kind: instancestore.KindFreeTurnClient,
			FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9000"}},
		{ID: "default", Kind: instancestore.KindFreeTurnServer,
			FreeTurnServer: &roles.FreeTurnServerConfig{Listen: "0.0.0.0:56000", Mode: "tcp"}},
		{ID: "default", Kind: instancestore.KindWdttClient,
			WdttClient: &roles.WdttClientConfig{Listen: "127.0.0.1:9100"}},
		{ID: "default", Kind: instancestore.KindWdttServer,
			WdttServer: &roles.WdttServerConfig{
				// RawListen задан ЯВНО и не равен конвенции DTLS+1 (56003):
				// совпадение с дефолтом не отличило бы чтение поля от
				// вычисления по Listen.
				Listen:       "0.0.0.0:56002",
				RawListen:    "0.0.0.0:56013",
				DirectListen: "0.0.0.0:56014",
				WgPort:       56001,
			}},
	}}})
}

func TestProxyListenerOwnsOnlyConfiguredPorts(t *testing.T) {
	h := testProxyListenerHandler()

	if !h.ownsListener(9000, procport.ProtoUDP) {
		t.Fatal("listen freeturn-клиента должен считаться своим")
	}
	if !h.ownsListener(9100, procport.ProtoUDP) {
		t.Fatal("listen wdtt-клиента должен считаться своим")
	}
	if !h.ownsListener(56000, procport.ProtoTCP) {
		t.Fatal("tcp-сервер freeturn должен считаться своим")
	}
	if !h.ownsListener(56002, procport.ProtoUDP) {
		t.Fatal("dtls wdtt-сервера должен считаться своим")
	}
	if !h.ownsListener(56013, procport.ProtoUDP) {
		t.Fatal("raw wdtt-сервера должен считаться своим")
	}
	if !h.ownsListener(56014, procport.ProtoUDP) {
		t.Fatal("direct wdtt-сервера должен считаться своим")
	}
	if !h.ownsListener(56001, procport.ProtoUDP) {
		t.Fatal("wg internal wdtt-сервера должен считаться своим")
	}
	if h.ownsListener(56000, procport.ProtoUDP) {
		t.Fatal("протокол обязан совпадать: udp-сокет на tcp-порту сервера — чужой")
	}
	if h.ownsListener(56003, procport.ProtoUDP) {
		t.Fatal("DTLS+1 не занят: у сервера явный rawListen на другом порту")
	}
	if h.ownsListener(79, procport.ProtoTCP) {
		t.Fatal("порт RCI роутера не наш")
	}
	if h.ownsListener(53, procport.ProtoUDP) {
		t.Fatal("порт DNS не наш")
	}
}

// Отказ чтения хранилища обязан закрывать kill, а не открывать: неизвестное
// владение — не наше владение, иначе один POST сносит ndm или DNS роутера.
func TestProxyListenerFailsClosedOnStoreError(t *testing.T) {
	h := NewProxyListenerHandler(stubProxyRecords{err: http.ErrServerClosed})
	if h.ownsListener(9000, procport.ProtoUDP) {
		t.Fatal("при нечитаемом хранилище порт не может считаться своим")
	}
}

// Главный инвариант: чужой порт не доходит до kill вообще (kill шлёт SIGKILL
// от root, промах = убитый сервис роутера).
func TestKillListenerRejectsForeignPort(t *testing.T) {
	h := testProxyListenerHandler()
	req := httptest.NewRequest(http.MethodPost, "/api/proxy/kill-listener",
		bytes.NewBufferString(`{"host":"127.0.0.1","port":79,"proto":"tcp"}`))
	rec := httptest.NewRecorder()

	h.KillListener(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rec.Code, rec.Body.String())
	}
}
