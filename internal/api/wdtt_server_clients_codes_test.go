package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/wdtt"
)

// stubWdttForImport — пустышка api.WdttService: база для фейков этого пакета.
type stubWdttForImport struct {
	cfg wdtt.Config
}

func (s *stubWdttForImport) GetConfig() (wdtt.Config, error) { return s.cfg, nil }

func (s *stubWdttForImport) UpdateClientConfig(wdtt.ClientConfig) error           { return nil }
func (s *stubWdttForImport) UpdateClientInstance(string, wdtt.ClientConfig) error { return nil }
func (s *stubWdttForImport) CreateClient(wdtt.CreateClientInput) (wdtt.ClientInstance, error) {
	return wdtt.ClientInstance{}, nil
}
func (s *stubWdttForImport) DeleteClient(string) error         { return nil }
func (s *stubWdttForImport) RenameClient(string, string) error { return nil }
func (s *stubWdttForImport) ImportLink(string, string) (wdtt.ClientInstance, wdtt.ImportPayload, error) {
	return wdtt.ClientInstance{}, wdtt.ImportPayload{}, nil
}
func (s *stubWdttForImport) DecodeLink(string) (wdtt.LinkDecodeResult, error) {
	return wdtt.LinkDecodeResult{}, nil
}
func (s *stubWdttForImport) Status() wdtt.Status              { return wdtt.Status{} }
func (s *stubWdttForImport) StartClient() error               { return nil }
func (s *stubWdttForImport) StopClient() error                { return nil }
func (s *stubWdttForImport) StartClientInstance(string) error { return nil }
func (s *stubWdttForImport) StopClientInstance(string) error  { return nil }
func (s *stubWdttForImport) RefreshSubscription(string) (wdtt.ClientInstance, wdtt.ImportPayload, error) {
	return wdtt.ClientInstance{}, wdtt.ImportPayload{}, nil
}
func (s *stubWdttForImport) UpdateServerConfig(wdtt.ServerConfig) error { return nil }
func (s *stubWdttForImport) UpdateServerInstance(string, wdtt.ServerConfig) (wdtt.ServerConfig, error) {
	return wdtt.ServerConfig{}, nil
}
func (s *stubWdttForImport) SetServerNATMode(context.Context, string, string) (wdtt.ServerConfig, error) {
	return wdtt.ServerConfig{}, nil
}
func (s *stubWdttForImport) SetServerPolicy(context.Context, string, string) (wdtt.ServerConfig, error) {
	return wdtt.ServerConfig{}, nil
}
func (s *stubWdttForImport) SetServerLANSegments(context.Context, string, []string) (wdtt.ServerConfig, error) {
	return wdtt.ServerConfig{}, nil
}
func (s *stubWdttForImport) CreateServer(wdtt.CreateServerInput) (wdtt.ServerInstance, error) {
	return wdtt.ServerInstance{}, nil
}
func (s *stubWdttForImport) DeleteServer(string) error         { return nil }
func (s *stubWdttForImport) RenameServer(string, string) error { return nil }
func (s *stubWdttForImport) ServerConfigForLink(string) (wdtt.ServerConfig, error) {
	return wdtt.ServerConfig{}, nil
}
func (s *stubWdttForImport) StartServer() error                    { return nil }
func (s *stubWdttForImport) StopServer() error                     { return nil }
func (s *stubWdttForImport) StartServerInstance(string) error      { return nil }
func (s *stubWdttForImport) StopServerInstance(string) error       { return nil }
func (s *stubWdttForImport) InstallBinaries(context.Context) error { return nil }
func (s *stubWdttForImport) Stop()                                 {}
func (s *stubWdttForImport) ListServerClients(string) (wdtt.ServerClientsStatus, error) {
	return wdtt.ServerClientsStatus{}, nil
}
func (s *stubWdttForImport) AddServerClient(string, string, string, string, string) (wdtt.ServerClientsStatus, error) {
	return wdtt.ServerClientsStatus{}, nil
}
func (s *stubWdttForImport) RenameServerClient(string, string, string) (wdtt.ServerClientsStatus, error) {
	return wdtt.ServerClientsStatus{}, nil
}
func (s *stubWdttForImport) RemoveServerClient(string, string) (wdtt.ServerClientsStatus, error) {
	return wdtt.ServerClientsStatus{}, nil
}

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

// partialAddWdttClients — абонент заведён в конфигурации, а passwords.json не
// записан: сервис отдаёт завёрнутый ErrServerClientFileNotWritten.
type partialAddWdttClients struct {
	stubWdttForImport
}

func (s *partialAddWdttClients) AddServerClient(string, string, string, string, string) (wdtt.ServerClientsStatus, error) {
	return wdtt.ServerClientsStatus{}, fmt.Errorf("%w: read-only file system", wdtt.ErrServerClientFileNotWritten)
}

// TestServeServerClients_PartialSuccessCode — частичный успех отличается от
// полного отказа ТОЛЬКО кодом: в конверте отказа больше ничего нет. Без этого
// различия UI обязан говорить «абонент не создан», хотя абонент есть и виден в
// списке.
func TestServeServerClients_PartialSuccessCode(t *testing.T) {
	h := &WdttHandler{svc: &partialAddWdttClients{}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/wdtt/servers/default/users", strings.NewReader(`{"password":"abonent1"}`))

	h.serveServerClients(rec, req, "default", nil)

	var resp struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("ответ не разобран: %v (тело: %s)", err, rec.Body.String())
	}
	if resp.Code != "WDTT_SERVER_CLIENT_ADD_NOT_APPLIED" {
		t.Fatalf("код частичного успеха = %q", resp.Code)
	}
	// Причина обязана доехать до пользователя: read-only и «нет места»
	// лечатся по-разному.
	if !strings.Contains(resp.Message, "read-only file system") {
		t.Fatalf("причина отказа потеряна в ответе: %q", resp.Message)
	}
}

// mainPasswordNotSavedWdttClients — абонент заведён и применён целиком, а
// пароль сервера не сохранён: сервис отдаёт завёрнутый
// ErrServerMainPasswordNotSaved.
type mainPasswordNotSavedWdttClients struct {
	stubWdttForImport
}

func (s *mainPasswordNotSavedWdttClients) AddServerClient(string, string, string, string, string) (wdtt.ServerClientsStatus, error) {
	return wdtt.ServerClientsStatus{}, fmt.Errorf("%w: read-only file system", wdtt.ErrServerMainPasswordNotSaved)
}

// TestServeServerClients_MainPasswordNotSavedCode — второй частичный успех той
// же ручки: абонент создан и применён, не сохранился пароль сервера. Код обязан
// отличаться и от полного отказа, и от «файл не записан» — лечится он другим
// действием (сохранить пароль в настройках сервера).
func TestServeServerClients_MainPasswordNotSavedCode(t *testing.T) {
	h := &WdttHandler{svc: &mainPasswordNotSavedWdttClients{}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/wdtt/servers/default/users", strings.NewReader(`{"password":"abonent1","mainPassword":"mainpass0000000000000000"}`))

	h.serveServerClients(rec, req, "default", nil)

	var resp struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("ответ не разобран: %v (тело: %s)", err, rec.Body.String())
	}
	if resp.Code != "WDTT_SERVER_MAIN_PASSWORD_NOT_SAVED" {
		t.Fatalf("код несохранённого пароля сервера = %q", resp.Code)
	}
	if !strings.Contains(resp.Message, "read-only file system") {
		t.Fatalf("причина отказа потеряна в ответе: %q", resp.Message)
	}
}
