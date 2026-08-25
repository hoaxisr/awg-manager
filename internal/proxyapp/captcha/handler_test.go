package captcha

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

const wantBase = "http://192.168.1.1/api/proxyrt/instances/freeturn-client:default/captcha"

func serviceWithOneClient(open bool) *Service {
	recs := &fakeRecords{recs: []instancestore.Record{ftClient("default", "Дом")}}
	lis := &fakeListener{owner: 41, open: open}
	return New(Deps{
		Records:   recs,
		Instances: recs,
		Snapshots: snapshots(map[string]int{keyDefault: 41}),
		Log:       logs(map[string]string{keyDefault: waitingLog}),
		Listener:  lis.fn,
	})
}

func decodeEnvelope(t *testing.T, body string) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("ответ не json: %v (%q)", err, body)
	}
	return env
}

func TestServeStatus_Form(t *testing.T) {
	s := serviceWithOneClient(true)
	rec := httptest.NewRecorder()
	s.ServeStatus(rec, httptest.NewRequest(http.MethodGet, "/api/proxyrt/freeturn/captcha/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("код = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	env := decodeEnvelope(t, rec.Body.String())
	if env["success"] != true {
		t.Fatalf("конверт: %v", env)
	}
	data := env["data"].(map[string]any)
	if data["portOpen"] != true || data["ownerClientId"] != keyDefault {
		t.Fatalf("обзор: %v", data)
	}
	clients := data["clients"].([]any)
	first := clients[0].(map[string]any)
	if first["clientId"] != keyDefault || first["clientName"] != "Дом" {
		t.Fatalf("инстанс в обзоре: %v", first)
	}
	if first["url"] != "/api/proxyrt/instances/freeturn-client:default/captcha/" {
		t.Fatalf("ссылка на страницу капчи = %v", first["url"])
	}
}

func TestServeStatus_MethodGate(t *testing.T) {
	s := serviceWithOneClient(true)
	rec := httptest.NewRecorder()
	s.ServeStatus(rec, httptest.NewRequest(http.MethodPost, "/api/proxyrt/freeturn/captcha/status", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("код = %d, want 405", rec.Code)
	}
}

func TestServe_StatusSubpath(t *testing.T) {
	s := serviceWithOneClient(true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/proxyrt/instances/"+keyDefault+"/captcha/status", nil)
	s.Serve(rec, req, keyDefault, []string{"status"})

	if rec.Code != http.StatusOK {
		t.Fatalf("код = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	data := decodeEnvelope(t, rec.Body.String())["data"].(map[string]any)
	if data["clientId"] != keyDefault || data["canOpen"] != true {
		t.Fatalf("статус инстанса: %v", data)
	}
	if data["url"] != "/api/proxyrt/instances/freeturn-client:default/captcha/" {
		t.Fatalf("ссылка = %v", data["url"])
	}
}

func TestServe_UnknownInstanceIs404(t *testing.T) {
	s := serviceWithOneClient(true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/proxyrt/instances/freeturn-client:nope/captcha/", nil)
	s.Serve(rec, req, "freeturn-client:nope", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("код = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

func TestServe_RejectsNonFreeTurnClientKind(t *testing.T) {
	// Роль решает путь: у wdtt-клиента капча — лишь режим -captcha-mode,
	// локального сервера :8765 он не поднимает, и прокси ушёл бы в чужой порт.
	recs := &fakeRecords{recs: []instancestore.Record{{
		ID: "default", Kind: instancestore.KindWdttClient, Name: "wdtt",
		WdttClient: &roles.WdttClientConfig{Listen: "127.0.0.1:9000"},
	}}}
	lis := &fakeListener{owner: 41, open: true}
	s := New(Deps{Records: recs, Instances: recs, Listener: lis.fn})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/proxyrt/instances/wdtt-client:default/captcha/", nil)
	s.Serve(rec, req, "wdtt-client:default", nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("код = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	env := decodeEnvelope(t, rec.Body.String())
	msg, _ := env["message"].(string)
	if !strings.Contains(msg, "wdtt-client") {
		t.Fatalf("отказ обязан называть роль: %q", msg)
	}
	if len(lis.calls) != 0 {
		t.Fatal("порт капчи не должен зондироваться для чужой роли")
	}
}

func TestServe_FailClosedWithoutRecords(t *testing.T) {
	s := New(Deps{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/proxyrt/instances/"+keyDefault+"/captcha/", nil)
	s.Serve(rec, req, keyDefault, nil)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("код = %d, want 500 (%s)", rec.Code, rec.Body.String())
	}
}

func TestServe_PortClosedIs503(t *testing.T) {
	s := serviceWithOneClient(false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/proxyrt/instances/"+keyDefault+"/captcha/", nil)
	s.Serve(rec, req, keyDefault, nil)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("код = %d, want 503 (%s)", rec.Code, rec.Body.String())
	}
	env := decodeEnvelope(t, rec.Body.String())
	if env["code"] != "CAPTCHA_INACTIVE" || env["message"] != "captcha server is not active" {
		t.Fatalf("отказ: %v", env)
	}
}

func TestServe_MethodGate(t *testing.T) {
	s := serviceWithOneClient(true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/proxyrt/instances/"+keyDefault+"/captcha/", nil)
	s.Serve(rec, req, keyDefault, nil)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("код = %d, want 405", rec.Code)
	}
}

func TestProxyBase_ColonKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/proxyrt/instances/"+keyDefault+"/captcha/", nil)
	req.Host = "192.168.1.1"

	if got := proxyBase(req, keyDefault); got != wantBase {
		t.Fatalf("база = %q, want %q", got, wantBase)
	}
}

func TestProxyBase_ForwardedHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/proxyrt/instances/"+keyDefault+"/captcha/", nil)
	req.Host = "192.168.1.1"
	req.Header.Set("X-Forwarded-Proto", "https, http")
	req.Header.Set("X-Forwarded-Host", "router.example, inner")

	want := "https://router.example/api/proxyrt/instances/freeturn-client:default/captcha"
	if got := proxyBase(req, keyDefault); got != want {
		t.Fatalf("база = %q, want %q", got, want)
	}
}

// База уезжает в <base href> отдаваемой страницы. Двоеточие в ключе делает
// относительную базу непригодной: браузер прочитал бы `freeturn-client:` как
// СХЕМУ (RFC 3986 §4.2), поэтому база обязана быть абсолютной и разрешать
// относительные ссылки страницы обратно в наш префикс.
func TestProxyBase_AbsoluteResolvesRelativeLinks(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/proxyrt/instances/"+keyDefault+"/captcha/", nil)
	req.Host = "192.168.1.1"
	base := proxyBase(req, keyDefault)

	u, err := url.Parse(base + "/")
	if err != nil {
		t.Fatalf("база не разбирается: %v", err)
	}
	if u.Scheme == "" || u.Host == "" {
		t.Fatalf("база обязана быть абсолютной: %q", base)
	}
	if u.EscapedPath() != "/api/proxyrt/instances/freeturn-client:default/captcha/" {
		t.Fatalf("путь базы = %q", u.EscapedPath())
	}

	// Относительная ссылка страницы капчи.
	ref, err := u.Parse("not_robot_captcha?session=1")
	if err != nil {
		t.Fatalf("относительная ссылка не разрешилась: %v", err)
	}
	want := wantBase + "/not_robot_captcha?session=1"
	if ref.String() != want {
		t.Fatalf("разрешённая ссылка = %q, want %q", ref.String(), want)
	}

	// Тот же путь БЕЗ схемы и хоста первым сегментом после базы браузер и
	// url.Parse читают как схему — контроль, ради которого база абсолютна.
	if _, err := url.Parse("freeturn-client:default/captcha/"); err == nil {
		t.Log("предупреждение: относительная ссылка с двоеточием разобралась как схема")
	}
}
