package api

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/procport"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// failingProxyRecords — хранилище, которое не читается. Только этот случай и
// стоит подделывать: состав записей даёт НАСТОЯЩИЙ store (см. proxyRecords).
type failingProxyRecords struct{ err error }

func (f failingProxyRecords) Load() (instancestore.State, error) {
	return instancestore.State{}, f.err
}

// proxyRecords кладёт записи ЧЕРЕЗ прод-хранилище: форма, инварианты и
// нормализация — те же, что у демона. Заглушка со своим составом принимала бы
// записи, которых store выдать не может (сервер без пинов отвергается
// validateState с ErrMissingPins), и тест был бы зелен на несуществующих
// данных.
//
// ВСЕ значения портов выбраны так, чтобы НЕ совпадать с дефолтами: 9000 —
// дефолт listen клиента FreeTurn, 56000 — его сервера, 56002/56001 — DTLS и
// внутренний WG сервера WDTT. Фикстура, равная дефолту, не отличает чтение
// поля от подстановки константы.
func proxyRecords(t *testing.T) *instancestore.Store {
	t.Helper()
	store := instancestore.New(t.TempDir())
	if _, err := store.Replace(func(st *instancestore.State) error {
		st.Records = []instancestore.Record{
			{ID: "default", Kind: instancestore.KindFreeTurnClient, Name: "FT",
				FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9007"}},
			{ID: "default", Kind: instancestore.KindFreeTurnServer, Name: "FT-S",
				FreeTurnServer: &roles.FreeTurnServerConfig{Listen: "0.0.0.0:57000", Mode: "tcp"}},
			{ID: "default", Kind: instancestore.KindWdttClient, Name: "WD",
				WdttClient: &roles.WdttClientConfig{Listen: "127.0.0.1:9100"}},
			{ID: "default", Kind: instancestore.KindWdttServer, Name: "WD-S",
				WdttServer: &roles.WdttServerConfig{
					// RawListen задан ЯВНО и не равен конвенции DTLS+1
					// (57003): совпадение не отличило бы чтение поля от
					// вычисления по Listen.
					Listen:       "0.0.0.0:57002",
					RawListen:    "0.0.0.0:57013",
					DirectListen: "0.0.0.0:57014",
					WgPort:       57001,
					NdmsIface:    "OpkgTun20", WgIface: "opkgtun20",
					RawNdmsIface: "OpkgTun21", RawIface: "opkgtun21",
				}},
		}
		return nil
	}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	return store
}

func testProxyListenerHandler(t *testing.T) *ProxyListenerHandler {
	t.Helper()
	return NewProxyListenerHandler(proxyRecords(t))
}

func TestProxyListenerOwnsOnlyConfiguredPorts(t *testing.T) {
	h := testProxyListenerHandler(t)
	owns := func(port int, proto procport.Proto) bool { return len(h.ownedBindHosts(port, proto)) > 0 }

	if !owns(9007, procport.ProtoUDP) {
		t.Fatal("listen freeturn-клиента должен считаться своим")
	}
	if !owns(9100, procport.ProtoUDP) {
		t.Fatal("listen wdtt-клиента должен считаться своим")
	}
	if !owns(57000, procport.ProtoTCP) {
		t.Fatal("tcp-сервер freeturn должен считаться своим")
	}
	if !owns(57002, procport.ProtoUDP) {
		t.Fatal("dtls wdtt-сервера должен считаться своим")
	}
	if !owns(57013, procport.ProtoUDP) {
		t.Fatal("raw wdtt-сервера должен считаться своим")
	}
	if !owns(57014, procport.ProtoUDP) {
		t.Fatal("direct wdtt-сервера должен считаться своим")
	}
	if !owns(57001, procport.ProtoUDP) {
		t.Fatal("wg internal wdtt-сервера должен считаться своим")
	}
	if owns(57000, procport.ProtoUDP) {
		t.Fatal("протокол обязан совпадать: udp-сокет на tcp-порту сервера — чужой")
	}
	if owns(57003, procport.ProtoUDP) {
		t.Fatal("DTLS+1 не занят: у сервера явный rawListen на другом порту")
	}
	// Дефолты чужие: ни один из них в этой фикстуре не задан, и попасть в
	// ведомость владения они могут только подстановкой константы.
	for _, p := range []int{9000, 56000, 56001, 56002, 56003} {
		if owns(p, procport.ProtoUDP) || owns(p, procport.ProtoTCP) {
			t.Errorf("порт %d — дефолт, а не значение фикстуры: он не может быть нашим", p)
		}
	}
	if owns(79, procport.ProtoTCP) {
		t.Fatal("порт RCI роутера не наш")
	}
	if owns(53, procport.ProtoUDP) {
		t.Fatal("порт DNS не наш")
	}
}

// Отказ чтения хранилища обязан закрывать kill, а не открывать: неизвестное
// владение — не наше владение, иначе один POST сносит ndm или DNS роутера.
func TestProxyListenerFailsClosedOnStoreError(t *testing.T) {
	h := NewProxyListenerHandler(failingProxyRecords{err: errors.New("хранилище не читается")})
	if hosts := h.ownedBindHosts(9007, procport.ProtoUDP); len(hosts) != 0 {
		t.Fatal("при нечитаемом хранилище порт не может считаться своим")
	}
}

// Главный инвариант: чужой порт не доходит до kill вообще (kill шлёт SIGKILL
// от root, промах = убитый сервис роутера).
func TestKillListenerRejectsForeignPort(t *testing.T) {
	h := testProxyListenerHandler(t)
	called := fakeKill(t)
	req := httptest.NewRequest(http.MethodPost, "/api/proxy/kill-listener",
		bytes.NewBufferString(`{"host":"127.0.0.1","port":79,"proto":"tcp"}`))
	rec := httptest.NewRecorder()

	h.KillListener(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rec.Code, rec.Body.String())
	}
	if len(*called) != 0 {
		t.Fatalf("до KillListener дойти не должно: %v", *called)
	}
}

// fakeKill подменяет procport.KillListener: настоящий шлёт сигналы от root.
// Возвращает адреса, с которыми его звали.
func fakeKill(t *testing.T) *[]string {
	t.Helper()
	var hosts []string
	prev := killListener
	killListener = func(host string, port int, proto procport.Proto) (procport.ListenerInfo, error) {
		hosts = append(hosts, host)
		return procport.ListenerInfo{Open: true, PID: 4242, Host: host, Port: port, Proto: string(proto)}, nil
	}
	t.Cleanup(func() { killListener = prev })
	return &hosts
}

func postKill(h *ProxyListenerHandler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/proxy/kill-listener", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.KillListener(rec, req)
	return rec
}

// Конфликт ищется с адресом bind'а из конфига инстанса. Хост из запроса цель
// не расширяет: прежде host=192.168.1.1 на своём порту 9007 сносил процесс на
// чужом IP, а host=0.0.0.0 — любой процесс на этом номере порта.
func TestKillListenerUsesConfiguredBindHost(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"чужой IP в запросе", `{"host":"192.168.1.1","port":9007,"proto":"udp"}`, "127.0.0.1"},
		{"wildcard в запросе", `{"host":"0.0.0.0","port":9007,"proto":"udp"}`, "127.0.0.1"},
		{"хост не задан", `{"port":9007,"proto":"udp"}`, "127.0.0.1"},
		{"точный адрес клиента", `{"host":"127.0.0.1","port":9007,"proto":"udp"}`, "127.0.0.1"},
		// Сервер на 0.0.0.0 конфликтует с любым адресом на порту — освобождать
		// его надо в той же ширине, сколько бы ни прислал UI.
		{"wildcard-сервер", `{"host":"127.0.0.1","port":57000,"proto":"tcp"}`, "0.0.0.0"},
		// wireguard-go слушает wg-port на всех адресах, UI шлёт 0.0.0.0.
		{"внутренний WG", `{"host":"0.0.0.0","port":57001,"proto":"udp"}`, "0.0.0.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := testProxyListenerHandler(t)
			called := fakeKill(t)
			rec := postKill(h, tc.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
			}
			if len(*called) != 1 || (*called)[0] != tc.want {
				t.Fatalf("KillListener звали с %v, want [%s]", *called, tc.want)
			}
		})
	}
}

// Протокол обязан совпадать: tcp-сервер на 57000 не даёт права на udp-сокет
// того же номера, и наоборот.
func TestKillListenerRejectsOtherProto(t *testing.T) {
	h := testProxyListenerHandler(t)
	called := fakeKill(t)
	for _, body := range []string{
		`{"host":"0.0.0.0","port":57000,"proto":"udp"}`,
		`{"host":"127.0.0.1","port":9007,"proto":"tcp"}`,
	} {
		if rec := postKill(h, body); rec.Code != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want 403", body, rec.Code)
		}
	}
	if len(*called) != 0 {
		t.Fatalf("до KillListener дойти не должно: %v", *called)
	}
}

// Два инстанса на одном номере порта и разных адресах: хост запроса выбирает
// один из адресов конфига, а неизвестный или wildcard — отказ, а не «любой».
func TestKillListenerDisambiguatesInstancesOnSamePort(t *testing.T) {
	store := instancestore.New(t.TempDir())
	if _, err := store.Replace(func(st *instancestore.State) error {
		st.Records = []instancestore.Record{
			{ID: "a", Kind: instancestore.KindFreeTurnServer, Name: "A",
				FreeTurnServer: &roles.FreeTurnServerConfig{Listen: "192.168.1.1:58000"}},
			{ID: "b", Kind: instancestore.KindFreeTurnServer, Name: "B",
				FreeTurnServer: &roles.FreeTurnServerConfig{Listen: "10.0.0.1:58000"}},
		}
		return nil
	}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	h := NewProxyListenerHandler(store)
	called := fakeKill(t)

	if rec := postKill(h, `{"host":"10.0.0.1","port":58000,"proto":"udp"}`); rec.Code != http.StatusOK {
		t.Fatalf("адрес инстанса B: status = %d; body: %s", rec.Code, rec.Body.String())
	}
	for _, host := range []string{"0.0.0.0", "172.16.0.1", "127.0.0.1", ""} {
		body := `{"host":"` + host + `","port":58000,"proto":"udp"}`
		if rec := postKill(h, body); rec.Code != http.StatusForbidden {
			t.Fatalf("host=%q: status = %d, want 403", host, rec.Code)
		}
	}
	if len(*called) != 1 || (*called)[0] != "10.0.0.1" {
		t.Fatalf("KillListener звали с %v, want [10.0.0.1]", *called)
	}
}
