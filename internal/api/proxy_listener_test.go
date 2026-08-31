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

	if !h.ownsListener(9007, procport.ProtoUDP) {
		t.Fatal("listen freeturn-клиента должен считаться своим")
	}
	if !h.ownsListener(9100, procport.ProtoUDP) {
		t.Fatal("listen wdtt-клиента должен считаться своим")
	}
	if !h.ownsListener(57000, procport.ProtoTCP) {
		t.Fatal("tcp-сервер freeturn должен считаться своим")
	}
	if !h.ownsListener(57002, procport.ProtoUDP) {
		t.Fatal("dtls wdtt-сервера должен считаться своим")
	}
	if !h.ownsListener(57013, procport.ProtoUDP) {
		t.Fatal("raw wdtt-сервера должен считаться своим")
	}
	if !h.ownsListener(57014, procport.ProtoUDP) {
		t.Fatal("direct wdtt-сервера должен считаться своим")
	}
	if !h.ownsListener(57001, procport.ProtoUDP) {
		t.Fatal("wg internal wdtt-сервера должен считаться своим")
	}
	if h.ownsListener(57000, procport.ProtoUDP) {
		t.Fatal("протокол обязан совпадать: udp-сокет на tcp-порту сервера — чужой")
	}
	if h.ownsListener(57003, procport.ProtoUDP) {
		t.Fatal("DTLS+1 не занят: у сервера явный rawListen на другом порту")
	}
	// Дефолты чужие: ни один из них в этой фикстуре не задан, и попасть в
	// ведомость владения они могут только подстановкой константы.
	for _, p := range []int{9000, 56000, 56001, 56002, 56003} {
		if h.ownsListener(p, procport.ProtoUDP) || h.ownsListener(p, procport.ProtoTCP) {
			t.Errorf("порт %d — дефолт, а не значение фикстуры: он не может быть нашим", p)
		}
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
	h := NewProxyListenerHandler(failingProxyRecords{err: errors.New("хранилище не читается")})
	if h.ownsListener(9007, procport.ProtoUDP) {
		t.Fatal("при нечитаемом хранилище порт не может считаться своим")
	}
}

// Главный инвариант: чужой порт не доходит до kill вообще (kill шлёт SIGKILL
// от root, промах = убитый сервис роутера).
func TestKillListenerRejectsForeignPort(t *testing.T) {
	h := testProxyListenerHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/proxy/kill-listener",
		bytes.NewBufferString(`{"host":"127.0.0.1","port":79,"proto":"tcp"}`))
	rec := httptest.NewRecorder()

	h.KillListener(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rec.Code, rec.Body.String())
	}
}
