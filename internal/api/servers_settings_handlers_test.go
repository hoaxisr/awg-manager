package api

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func postServerSetting(t *testing.T, fn func(http.ResponseWriter, *http.Request, string), path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/servers/Wireguard0/"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	fn(rr, req, "Wireguard0")
	return rr
}

// Endpoint из тела уходит в мету интерфейса (её читает генератор клиентского
// .conf) и объявляется подписчикам.
func TestServersHandler_SetEndpoint_StoresHostAndPublishes(t *testing.T) {
	h, store, _, p := newServersNATHarness(t)

	rr := postServerSetting(t, h.SetEndpoint, "endpoint", `{"endpoint":"vpn.example.org"}`)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	meta, _ := store.GetServerInterfaceMeta("Wireguard0")
	if meta.Endpoint != "vpn.example.org" {
		t.Fatalf("Endpoint = %q, want vpn.example.org", meta.Endpoint)
	}
	if got, want := p.invalidated(), []string{"servers/server-endpoint-changed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("публикации = %v, want %v", got, want)
	}
}

// Невалидный host отвергается до записи: иначе в .conf клиента уехала бы
// строка Endpoint, по которой никто не подключится.
func TestServersHandler_SetEndpoint_RejectsBadHost(t *testing.T) {
	h, store, _, p := newServersNATHarness(t)

	rr := postServerSetting(t, h.SetEndpoint, "endpoint", `{"endpoint":"not a host"}`)
	if rr.Code != 400 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if meta, _ := store.GetServerInterfaceMeta("Wireguard0"); meta.Endpoint != "" {
		t.Fatalf("Endpoint обязан остаться пустым: %q", meta.Endpoint)
	}
	if pub := p.invalidated(); len(pub) != 0 {
		t.Fatalf("на отказе публикаций быть не должно: %v", pub)
	}
}

// Политика, совпадающая с текущей (в фикстуре у Wireguard0 её нет → "none"),
// отдаёт снимок без похода в NDMS и без публикации.
func TestServersHandler_SetPolicy_NoChangeSkipsRCIAndPublish(t *testing.T) {
	h, _, poster, p := newServersNATHarness(t)

	rr := postServerSetting(t, h.SetPolicy, "policy", `{"policy":"none"}`)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if posts, pub := poster.snapshot(), p.invalidated(); len(posts) != 0 || len(pub) != 0 {
		t.Fatalf("совпадающая политика: posts=%v pub=%v", posts, pub)
	}
}

// Смена политики идёт через managedSvc, и на его отказе (в фикстуре списка
// политик нет — Policy0 неизвестна) ни RCI, ни публикации.
func TestServersHandler_SetPolicy_ChangeFailureIsNotPublished(t *testing.T) {
	h, _, poster, p := newServersNATHarness(t)

	rr := postServerSetting(t, h.SetPolicy, "policy", `{"policy":"Policy0"}`)
	if got := decodeJSONBody(t, rr)["code"]; got != "POLICY_FAILED" {
		t.Fatalf("code = %v, want POLICY_FAILED (body=%s)", got, rr.Body.String())
	}
	joined := strings.Join(poster.snapshot(), "\n")
	if strings.Contains(joined, "hotspot") {
		t.Fatalf("отказавшая смена не должна доходить до RCI:\n%s", joined)
	}
	if pub := p.invalidated(); len(pub) != 0 {
		t.Fatalf("на отказе публикаций быть не должно: %v", pub)
	}
}
