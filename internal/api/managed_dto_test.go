package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Карточка сервера отдаёт чужие привязки ACL интерфейса (foreignAcls): наш
// AWGM_ вычтен службой, `_WEBADMIN_` показывается — с #879 мы его не снимаем,
// и предупреждение на карточке заменило снятие.
func TestManagedList_ForeignACLs(t *testing.T) {
	_, store, _, _, svc := newServersNATHarness(t) // см. правку возврата харнесса
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: "Wireguard1", Address: "10.66.66.1", Mask: "255.255.255.0", ListenPort: 51820,
	}); err != nil {
		t.Fatal(err)
	}
	h := NewManagedServerHandler(svc, nil)
	rr := httptest.NewRecorder()
	h.List(rr, httptest.NewRequest(http.MethodGet, "/api/managed-servers", nil))
	var env struct {
		Data []struct {
			ForeignAcls []string `json:"foreignAcls"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil || len(env.Data) != 1 {
		t.Fatalf("ответ: %v %s", err, rr.Body.String())
	}
	// AWGM_ — наш, вычтен службой; остальные показываем как есть.
	if want := []string{"GUEST_ACL", "_WEBADMIN_Wireguard1"}; !slices.Equal(env.Data[0].ForeignAcls, want) {
		t.Fatalf("foreignAcls = %v, want %v", env.Data[0].ForeignAcls, want)
	}
}

// Без чужих привязок (пустой список после вычета наших) поле опускается
// целиком — omitempty, а не пустой массив.
func TestManagedList_NoForeignACLs_OmitsField(t *testing.T) {
	_, store, _, _, svc := newServersNATHarness(t)
	// Wireguard2 уже есть в running-config харнесса (для NAT-тестов), но без
	// каких-либо `ip access-group … in` — список чужих привязок пуст.
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: "Wireguard2", Address: "10.77.77.1", Mask: "255.255.255.0", ListenPort: 51821,
	}); err != nil {
		t.Fatal(err)
	}
	h := NewManagedServerHandler(svc, nil)
	rr := httptest.NewRecorder()
	h.List(rr, httptest.NewRequest(http.MethodGet, "/api/managed-servers", nil))
	if strings.Contains(rr.Body.String(), "foreignAcls") {
		t.Fatalf("foreignAcls не должен появляться без чужих ACL: %s", rr.Body.String())
	}
}

// Сигнатура пира доезжает до UI: карточка сервера отдаёт i1..i5 и профиль,
// приватный ключ при этом не утекает.
func TestToManagedServerResponse_PeerCarriesSignature(t *testing.T) {
	sv := &storage.ManagedServer{InterfaceName: "Wireguard1", Peers: []storage.ManagedPeer{
		{PublicKey: "P", PrivateKey: "secret", I1: "<b 0x01>", I2: "<r 3>", SignatureProfile: "stun"},
	}}
	resp := toManagedServerResponse(sv, nil)
	p := resp.Peers[0]
	if p.I1 != "<b 0x01>" || p.I2 != "<r 3>" || p.SignatureProfile != "stun" {
		t.Fatalf("%+v", p)
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret") {
		t.Fatal("приватный ключ не должен утекать")
	}
	if !strings.Contains(string(raw), `"signatureProfile":"stun"`) {
		t.Fatalf("json: %s", raw)
	}
}
