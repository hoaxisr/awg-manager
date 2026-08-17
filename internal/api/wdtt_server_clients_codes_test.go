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

func (s *failingWdttClients) RenameServerClient(string, string, string) (wdtt.ServerClientsStatus, error) {
	return wdtt.ServerClientsStatus{}, errors.New("boom")
}

func (s *failingWdttClients) RemoveServerClient(string, string) (wdtt.ServerClientsStatus, error) {
	return wdtt.ServerClientsStatus{}, errors.New("boom")
}

// TestServeServerClients_ErrorCodes фиксирует КОНТРАКТ кодов: по ним фронт
// различает отказы, а переименование кода ломает совместимость молча — ни одна
// проверка типов его не видит.
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
		{"переименование", http.MethodPatch, []string{"abonent1"}, `{"name":"Пётр"}`, "WDTT_SERVER_CLIENT_RENAME_FAILED"},
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

// recordingRenameWdtt запоминает аргументы переименования: у serverID, пароля и
// имени один тип, и перепутанный порядок не поймает ни компилятор, ни проверка
// кода отказа.
type recordingRenameWdtt struct {
	stubWdttForImport
	serverID string
	password string
	name     string
}

func (s *recordingRenameWdtt) RenameServerClient(serverID, password, name string) (wdtt.ServerClientsStatus, error) {
	s.serverID, s.password, s.name = serverID, password, name
	return wdtt.ServerClientsStatus{Users: []wdtt.ServerClientEntry{{Password: password, Comment: name}}}, nil
}

// TestServeServers_PatchUserRenames — маршрут переименования абонента целиком,
// от URL до аргументов сервиса: PATCH /api/wdtt/servers/{id}/users/{password}.
func TestServeServers_PatchUserRenames(t *testing.T) {
	svc := &recordingRenameWdtt{}
	h := &WdttHandler{svc: svc}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/wdtt/servers/default/users/abonent1", strings.NewReader(`{"name":"Иван Петров"}`))

	h.ServeServers(rec, req)

	if svc.serverID != "default" || svc.password != "abonent1" || svc.name != "Иван Петров" {
		t.Fatalf("сервис вызван с (%q, %q, %q)", svc.serverID, svc.password, svc.name)
	}
	if !strings.Contains(rec.Body.String(), "Иван Петров") {
		t.Fatalf("ответ ручки не содержит нового имени: %s", rec.Body.String())
	}
}
