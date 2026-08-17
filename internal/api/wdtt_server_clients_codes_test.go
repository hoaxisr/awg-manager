package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/wdtt"
)

// failingWdttClients роняет все три операции над абонентами — коды отказа тут и
// проверяются.
type failingWdttClients struct {
	stubWdttForImport
}

func (s *failingWdttClients) ListServerClients(string) (wdtt.ServerClientsStatus, error) {
	return wdtt.ServerClientsStatus{}, errors.New("boom")
}

func (s *failingWdttClients) AddServerClient(string, string, string, string, string) (wdtt.ServerClientsStatus, error) {
	return wdtt.ServerClientsStatus{}, errors.New("boom")
}

func (s *failingWdttClients) RemoveServerClient(string, string) (wdtt.ServerClientsStatus, error) {
	return wdtt.ServerClientsStatus{}, errors.New("boom")
}

// TestServeServerClients_ErrorCodes фиксирует КОНТРАКТ кодов: по ним фронт
// различает отказы, и переименование (WDTT_PANEL_* -> WDTT_SERVER_CLIENT*)
// ломает совместимость молча — ни одна проверка типов его не видит.
func TestServeServerClients_ErrorCodes(t *testing.T) {
	cases := []struct {
		name   string
		method string
		sub    []string
		body   string
		want   string
	}{
		{"список", http.MethodGet, nil, "", "WDTT_SERVER_CLIENTS_FAILED"},
		{"добавление", http.MethodPost, nil, `{"password":"abonent1"}`, "WDTT_SERVER_CLIENT_ADD_FAILED"},
		{"удаление", http.MethodDelete, []string{"abonent1"}, "", "WDTT_SERVER_CLIENT_DELETE_FAILED"},
	}
	for _, tc := range cases {
		h := &WdttHandler{svc: &failingWdttClients{}}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, "/api/wdtt/servers/default/users", strings.NewReader(tc.body))
		h.serveServerClients(rec, req, "default", tc.sub)

		var resp struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("%s: ответ не разобран: %v (тело: %s)", tc.name, err, rec.Body.String())
		}
		if resp.Code != tc.want {
			t.Fatalf("%s: код = %q, ожидался %q", tc.name, resp.Code, tc.want)
		}
	}
}
