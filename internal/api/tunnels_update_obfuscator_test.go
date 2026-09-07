package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func seedObfTunnel(t *testing.T, store *storage.AWGTunnelStore) {
	t.Helper()
	if err := store.Create(&storage.AWGTunnel{
		ID: "awg20", Name: "phobos", Backend: "nativewg", NWGIndex: 3,
		Interface: storage.AWGInterface{Address: "10.25.0.4/32", MTU: 1420, PrivateKey: "k"},
		Peer:      storage.AWGPeer{PublicKey: "p", Endpoint: "127.0.0.1:39000"},
		Obfuscator: &storage.Obfuscator{
			Flavor: storage.ObfuscatorFlavorPhobos, Target: "1.2.3.4:51824", Key: "old",
			Masking: "STUN", MaxDummy: 4, LocalPort: 39000,
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestTunnelUpdate_ObfuscatorUserFieldsOnly(t *testing.T) {
	h, store := newTunnelsUpdateHarness(t, &stubTunnelSvc{})
	seedObfTunnel(t, store)
	body := `{"obfuscator":{"flavor":"clusterm","target":"5.6.7.8:1","key":"new","masking":"NONE","maxDummy":9,"idleTimeout":120,"obfuscateBytes":16,"localPort":1}}`
	// Код ответа не проверяем: stubTunnelSvc.Get отдаёт ошибку и BuildTunnelResponse
	// даёт 400 (см. комментарий в tunnels_update_test.go:277-280); истина — стор.
	h.Update(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/tunnels/update?id=awg20", strings.NewReader(body)))
	saved, _ := store.Get("awg20")
	o := saved.Obfuscator
	if o.Flavor != "phobos" || o.LocalPort != 39000 {
		t.Fatalf("flavor/localPort must stay: %+v", o)
	}
	if o.Target != "5.6.7.8:1" || o.Key != "new" || o.Masking != "NONE" || o.MaxDummy != 9 || o.IdleTimeout != 120 || o.ObfuscateBytes != 16 {
		t.Fatalf("user fields not applied: %+v", o)
	}
	if saved.Peer.Endpoint != "127.0.0.1:39000" {
		t.Fatalf("endpoint must stay loopback, got %s", saved.Peer.Endpoint)
	}
	// Пустой key = «не менять» (ключ отдаётся в GET, но UI может прислать пусто).
	h.Update(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/tunnels/update?id=awg20",
		strings.NewReader(`{"obfuscator":{"target":"5.6.7.8:2","key":"","masking":"STUN","maxDummy":1}}`)))
	saved, _ = store.Get("awg20")
	if saved.Obfuscator.Key != "new" || saved.Obfuscator.Target != "5.6.7.8:2" {
		t.Fatalf("empty key must keep previous: %+v", saved.Obfuscator)
	}
}

// UI это заблокировал (endpoint disabled, вкладка «Обфускация» скрыта), а
// MCP/curl — нет: чужой endpoint увёл бы WG мимо релея, AWG-параметры легли бы
// ASC-обфускацией поверх обфускации релея.
func TestTunnelUpdate_ObfuscatorEndpointAndAWGRejected(t *testing.T) {
	h, store := newTunnelsUpdateHarness(t, &stubTunnelSvc{})
	seedObfTunnel(t, store)

	for name, body := range map[string]string{
		"endpoint": `{"peer":{"publicKey":"p","endpoint":"9.9.9.9:51820"}}`,
		"awg":      `{"interface":{"address":"10.25.0.4/32","mtu":1420,"jc":4,"jmin":40,"jmax":70,"s1":15,"s2":15,"h1":"1","h2":"2","h3":"3","h4":"4"}}`,
	} {
		rec := httptest.NewRecorder()
		h.Update(rec, httptest.NewRequest(http.MethodPost, "/tunnels/update?id=awg20", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "INVALID_OBFUSCATOR") {
			t.Errorf("%s: status=%d body=%s", name, rec.Code, rec.Body.String())
		}
		saved, _ := store.Get("awg20")
		if saved.Peer.Endpoint != "127.0.0.1:39000" {
			t.Errorf("%s: endpoint изменён на %s", name, saved.Peer.Endpoint)
		}
		if saved.Interface.AWGObfuscation.Jc != 0 {
			t.Errorf("%s: AWG-параметры записаны: %+v", name, saved.Interface.AWGObfuscation)
		}
	}
}

func TestTunnelUpdate_ObfuscatorCannotBeAddedOrRemoved(t *testing.T) {
	h, store := newTunnelsUpdateHarness(t, &stubTunnelSvc{})
	if err := store.Create(&storage.AWGTunnel{
		ID: "awg10", Name: "plain", Interface: storage.AWGInterface{Address: "10.0.0.2/32", MTU: 1420},
		Peer: storage.AWGPeer{Endpoint: "1.2.3.4:51820"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h.Update(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/tunnels/update?id=awg10",
		strings.NewReader(`{"obfuscator":{"flavor":"phobos","target":"1.2.3.4:1","key":"k","masking":"STUN"}}`)))
	if saved, _ := store.Get("awg10"); saved.Obfuscator != nil {
		t.Fatalf("obfuscator must not be attachable via update")
	}
	seedObfTunnel(t, store)
	h.Update(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/tunnels/update?id=awg20",
		strings.NewReader(`{"name":"renamed"}`)))
	if saved, _ := store.Get("awg20"); saved.Obfuscator == nil || saved.Name != "renamed" {
		t.Fatalf("obfuscator lost on unrelated patch: %+v", saved)
	}
}
