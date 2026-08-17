package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/wdtt"
)

// Тексты отказов различаются намеренно: пользователю они — единственное
// объяснение, почему ссылка не выдана. Каждый тест сверяет свой текст, иначе
// вырождение всех отказов в один переживёт набор.

func linkCfgWithClient() wdtt.ServerConfig {
	return wdtt.ServerConfig{
		Password: "adminpass",
		Clients: []wdtt.ServerClient{
			{Password: "abonent1", Comment: "Абонент 1"},
		},
	}
}

func TestLinkPasswordFor_RejectsEmpty(t *testing.T) {
	_, err := linkPasswordFor(WdttGenerateLinkRequest{}, linkCfgWithClient())
	if err == nil {
		t.Fatal("пустой пароль обязан быть отказом: дефолта на главный пароль больше нет")
	}
	if !strings.Contains(err.Error(), "выберите абонента") {
		t.Fatalf("ожидался текст про выбор абонента, получено: %v", err)
	}
}

func TestLinkPasswordFor_RejectsServerMainPassword(t *testing.T) {
	_, err := linkPasswordFor(WdttGenerateLinkRequest{Password: "adminpass"}, linkCfgWithClient())
	if err == nil {
		t.Fatal("главный пароль сервера не имеет права попасть в ссылку")
	}
	if !strings.Contains(err.Error(), "главный пароль сервера") {
		t.Fatalf("ожидался текст про главный пароль, получено: %v", err)
	}
}

func TestLinkPasswordFor_RejectsUnknownPassword(t *testing.T) {
	// Строка не принадлежит никому: проверка «не равен главному» такую
	// пропустила бы, и ссылка получилась бы мёртвой молча.
	_, err := linkPasswordFor(WdttGenerateLinkRequest{Password: "ничей-пароль"}, linkCfgWithClient())
	if err == nil {
		t.Fatal("пароль вне списка абонентов обязан быть отказом")
	}
	if !strings.Contains(err.Error(), "не принадлежит ни одному абоненту") {
		t.Fatalf("ожидался текст про чужой пароль, получено: %v", err)
	}

	// Сравнение при приёме — точное. Ослабление до сравнения без регистра
	// (strings.EqualFold) выдало бы ссылку с паролем, которого у сервера нет:
	// wrap-ключи он собирает по точной строке из passwords.json — та самая
	// «мёртвая молча» ссылка, ради которой заведена проверка членства.
	_, err = linkPasswordFor(WdttGenerateLinkRequest{Password: "ABONENT1"}, linkCfgWithClient())
	if err == nil {
		t.Fatal("пароль абонента в другом регистре — чужой пароль, ссылку выдавать нельзя")
	}
	if !strings.Contains(err.Error(), "не принадлежит ни одному абоненту") {
		t.Fatalf("ожидался текст про чужой пароль для другого регистра, получено: %v", err)
	}
}

func TestLinkPasswordFor_RejectsExpiredClient(t *testing.T) {
	cfg := linkCfgWithClient()
	cfg.Clients = append(cfg.Clients, wdtt.ServerClient{
		Password:  "botpass",
		Comment:   "выдан ботом",
		ExpiresAt: time.Now().Add(-time.Hour).Unix(),
	})

	// ОГОВОРКА К ВЫВОДУ ПРИЧИНЫ ИСКЛЮЧЕНИЕМ (ревью задачи 5). Текст «просрочен»
	// получается вычитанием: пароль есть в списке, непуст и не равен главному —
	// значит непригоден по сроку. Цепочка исчерпывающа ровно для ТРЁХ текущих
	// условий wdtt.UsableServerClients. Появится четвёртое (скажем,
	// IsDeactivated в предикате) — отказ останется верным, а текст станет
	// ложным. Чистое решение — классификатор причины на стороне wdtt; пока
	// его нет, этот тест и есть напоминание.
	_, err := linkPasswordFor(WdttGenerateLinkRequest{Password: "botpass"}, cfg)
	if err == nil {
		t.Fatal("просроченный абонент обязан быть отказом: сервер его пароль не примет")
	}
	if !strings.Contains(err.Error(), "просрочен") {
		t.Fatalf("ожидался текст про просроченного абонента, получено: %v", err)
	}

	// Рабочий сосед в том же конфиге ссылку получает — отказ адресный, а не
	// «в конфиге есть просроченный».
	if _, err := linkPasswordFor(WdttGenerateLinkRequest{Password: "abonent1"}, cfg); err != nil {
		t.Fatalf("рабочий абонент рядом с просроченным обязан получить ссылку: %v", err)
	}
}

// Состояние «рабочих абонентов нет» достижимо: ручная правка wdtt.json и
// конфиг до первого старта (вход из задачи 4). Отказ обязан объяснять, что
// делать, а не выдавать пустую ссылку.
func TestLinkPasswordFor_RejectsWhenNoUsableClients(t *testing.T) {
	cfg := wdtt.ServerConfig{
		Password: "adminpass",
		Clients: []wdtt.ServerClient{
			{Password: "botpass", ExpiresAt: time.Now().Add(-time.Hour).Unix()},
		},
	}

	for _, req := range []WdttGenerateLinkRequest{{}, {Password: "botpass"}} {
		_, err := linkPasswordFor(req, cfg)
		if err == nil {
			t.Fatalf("сервер без рабочих абонентов не может выдать ссылку (запрос %+v)", req)
		}
		if !strings.Contains(err.Error(), "нет ни одного рабочего абонента") {
			t.Fatalf("ожидался текст про отсутствие рабочих абонентов, получено: %v", err)
		}
	}
}

func TestLinkPasswordFor_AcceptsClientPassword(t *testing.T) {
	got, err := linkPasswordFor(WdttGenerateLinkRequest{Password: "abonent1"}, linkCfgWithClient())
	if err != nil {
		t.Fatalf("пароль абонента обязан приниматься: %v", err)
	}
	if got != "abonent1" {
		t.Fatalf("ожидался пароль абонента, получено %q", got)
	}

	// Трим на границе входа: без него пароль с пробелами не нашёлся бы среди
	// абонентов и ссылка отказала бы на ровном месте.
	got, err = linkPasswordFor(WdttGenerateLinkRequest{Password: "  abonent1  "}, linkCfgWithClient())
	if err != nil {
		t.Fatalf("пароль с пробелами обязан приниматься после трима: %v", err)
	}
	if got != "abonent1" {
		t.Fatalf("ожидался подрезанный пароль, получено %q", got)
	}
}

// stubWdttForLink подменяет только конфиг сервера — остальные 45 методов
// приезжают из заглушки соседнего теста.
type stubWdttForLink struct {
	stubWdttForImport
	srvCfg wdtt.ServerConfig
}

func (s *stubWdttForLink) ServerConfigForLink(string) (wdtt.ServerConfig, error) {
	return s.srvCfg, nil
}

// Ручка обязана звать linkPasswordFor и отвечать WDTT_LINK_NO_CLIENT, а не
// собирать ссылку на главном пароле.
func TestGenerateLinkCore_RefusesLinkWithoutClientPassword(t *testing.T) {
	h := &WdttHandler{svc: &stubWdttForLink{srvCfg: linkCfgWithClient()}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/wdtt/servers/default/link", nil)
	h.generateLinkCore(rec, req, "default", WdttGenerateLinkRequest{Peer: "203.0.113.7:5555"})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ожидался 400, получено %d (тело: %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("ответ не разобран: %v (тело: %s)", err, rec.Body.String())
	}
	if resp.Code != "WDTT_LINK_NO_CLIENT" {
		t.Fatalf("ожидался код WDTT_LINK_NO_CLIENT, получено %q", resp.Code)
	}
	if strings.Contains(rec.Body.String(), "wdtt://") {
		t.Fatalf("отказ не имеет права содержать ссылку: %s", rec.Body.String())
	}
}

func TestGenerateLinkCore_BuildsLinkOnClientPassword(t *testing.T) {
	h := &WdttHandler{svc: &stubWdttForLink{srvCfg: linkCfgWithClient()}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/wdtt/servers/default/link", nil)
	h.generateLinkCore(rec, req, "default", WdttGenerateLinkRequest{
		Peer:     "203.0.113.7:5555",
		Password: "abonent1",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("ожидался 200, получено %d (тело: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "wdtt://") {
		t.Fatalf("ссылка не собрана: %s", body)
	}
	if strings.Contains(body, "adminpass") {
		t.Fatalf("главный пароль уехал в ответ: %s", body)
	}
}
