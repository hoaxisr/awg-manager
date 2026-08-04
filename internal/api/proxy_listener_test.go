package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/freeturn"
	"github.com/hoaxisr/awg-manager/internal/procport"
	"github.com/hoaxisr/awg-manager/internal/wdtt"
)

type stubFreeTurnConfig struct {
	FreeTurnService
	cfg freeturn.Config
}

func (s stubFreeTurnConfig) GetConfig() (freeturn.Config, error) { return s.cfg, nil }

type stubWdttConfig struct {
	WdttService
	cfg wdtt.Config
}

func (s stubWdttConfig) GetConfig() (wdtt.Config, error) { return s.cfg, nil }

func testProxyListenerHandler() *ProxyListenerHandler {
	ft := stubFreeTurnConfig{cfg: freeturn.Config{
		Clients: []freeturn.ClientInstance{{ID: "default", Config: freeturn.ClientConfig{Listen: "127.0.0.1:9000"}}},
		Servers: []freeturn.ServerInstance{{ID: "default", Config: freeturn.ServerConfig{Listen: "0.0.0.0:56000", Mode: "tcp"}}},
	}}
	wd := stubWdttConfig{cfg: wdtt.Config{
		Clients: []wdtt.ClientInstance{{ID: "default", Config: wdtt.ClientConfig{Listen: "127.0.0.1:9100"}}},
	}}
	return NewProxyListenerHandler(ft, wd)
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
	if h.ownsListener(56000, procport.ProtoUDP) {
		t.Fatal("протокол обязан совпадать: udp-сокет на tcp-порту сервера — чужой")
	}
	if h.ownsListener(79, procport.ProtoTCP) {
		t.Fatal("порт RCI роутера не наш")
	}
	if h.ownsListener(53, procport.ProtoUDP) {
		t.Fatal("порт DNS не наш")
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
