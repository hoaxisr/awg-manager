package api

import (
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// newProxyRecords кладёт записи ЧЕРЕЗ хранилище: нормализация и инварианты те
// же, что у прода, и фикстура не может застыть в форме, которой store уже не
// отдаёт.
func newProxyRecords(t *testing.T, recs ...instancestore.Record) *instancestore.Store {
	t.Helper()
	store := instancestore.New(t.TempDir())
	if _, err := store.Replace(func(st *instancestore.State) error {
		st.Records = recs
		return nil
	}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	return store
}

const linkedImportConf = `[Interface]
PrivateKey = abc
[Peer]
PublicKey = def
Endpoint = 127.0.0.1:9000
`

func TestImportHandler_patchImportContentForLinkedClient_UsesFreeTurnListen(t *testing.T) {
	h := &ImportHandler{proxyRecords: newProxyRecords(t,
		instancestore.Record{ID: "ft-1", Kind: instancestore.KindFreeTurnClient,
			FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9001"}},
	)}

	got := h.patchImportContentForLinkedClient(linkedImportConf, "ft-1", "")
	if !strings.Contains(got, "Endpoint = 127.0.0.1:9001") {
		t.Fatalf("ждали endpoint 9001 из listen клиента, получили:\n%s", got)
	}
}

func TestImportHandler_patchImportContentForLinkedClient_UsesWdttListen(t *testing.T) {
	h := &ImportHandler{proxyRecords: newProxyRecords(t,
		instancestore.Record{ID: "wd-1", Kind: instancestore.KindWdttClient,
			WdttClient: &roles.WdttClientConfig{Mode: "wg", Listen: "127.0.0.1:9002"}},
	)}

	got := h.patchImportContentForLinkedClient(linkedImportConf, "", "wd-1")
	if !strings.Contains(got, "Endpoint = 127.0.0.1:9002") {
		t.Fatalf("ждали endpoint 9002 из listen wdtt-клиента, получили:\n%s", got)
	}
}

// Идентификаторы уникальны только ВНУТРИ роли: клиент wdtt и клиент freeturn
// с одним id — законная пара, и связь туннеля обязана попасть в свою.
func TestImportHandler_patchImportContentForLinkedClient_PicksRoleOfLink(t *testing.T) {
	h := &ImportHandler{proxyRecords: newProxyRecords(t,
		instancestore.Record{ID: "default", Kind: instancestore.KindFreeTurnClient,
			FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9001"}},
		instancestore.Record{ID: "default", Kind: instancestore.KindWdttClient,
			WdttClient: &roles.WdttClientConfig{Mode: "wg", Listen: "127.0.0.1:9002"}},
	)}

	got := h.patchImportContentForLinkedClient(linkedImportConf, "", "default")
	if !strings.Contains(got, "Endpoint = 127.0.0.1:9002") {
		t.Fatalf("связь с wdtt-клиентом ждала endpoint 9002, получили:\n%s", got)
	}
}

// Клиента с таким id нет: конфиг возвращается нетронутым, а не с дефолтным
// 9000 — на этом порту вполне может слушать чужой клиент.
func TestImportHandler_patchImportContentForLinkedClient_KeepsContentWhenClientGone(t *testing.T) {
	h := &ImportHandler{proxyRecords: newProxyRecords(t,
		instancestore.Record{ID: "wd-1", Kind: instancestore.KindWdttClient,
			WdttClient: &roles.WdttClientConfig{Mode: "wg", Listen: "127.0.0.1:9002"}},
	)}

	// Endpoint фикстуры НЕ равен дефолтному 9000: иначе патч дефолтом был бы
	// неотличим от отказа патчить.
	conf := strings.Replace(linkedImportConf, "127.0.0.1:9000", "127.0.0.1:9007", 1)
	got := h.patchImportContentForLinkedClient(conf, "", "wd-2")
	if got != conf {
		t.Fatalf("конфиг без клиента должен остаться прежним, получили:\n%s", got)
	}
}
