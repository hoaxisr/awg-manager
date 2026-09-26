package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// hookCalls собирает вызовы хука выключателя (обработчик зовёт его горутиной).
func hookCalls(h *SettingsHandler) chan bool {
	ch := make(chan bool, 4)
	h.SetOnObfuscatorRelayChanged(func(process bool) { ch <- process })
	return ch
}

// Устаревшее тело настроек с другой вкладки (страница шлёт {...settings})
// не должно снимать срабатывание сторожа: общий update выключатель не пишет
// и хук не зовёт (I1).
func TestUpdate_ObfuscatorRelayProcessIgnored(t *testing.T) {
	h, store := newSettingsHandlerForTest(t)
	if err := store.TripObfuscatorKmod("oops в awgm_relay: x"); err != nil {
		t.Fatal(err)
	}
	calls := hookCalls(h)

	body := []byte(`{"obfuscatorRelayProcess":false,"mcpEnabled":false}`)
	rec := httptest.NewRecorder()
	h.Update(rec, httptest.NewRequest(http.MethodPost, "/settings/update", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !store.IsObfuscatorRelayProcess() {
		t.Fatal("общий update снял выключатель ядра")
	}
	select {
	case p := <-calls:
		t.Fatalf("общий update позвал хук (process=%v)", p)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestSetObfuscatorRelay(t *testing.T) {
	h, store := newSettingsHandlerForTest(t)
	calls := hookCalls(h)
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.SetObfuscatorRelay(rec, httptest.NewRequest(http.MethodPost, "/settings/obfuscator-relay", bytes.NewReader([]byte(body))))
		return rec
	}

	rec := post(`{"process":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !store.IsObfuscatorRelayProcess() {
		t.Fatal("выключатель не записан")
	}
	data, _ := decodeJSONBody(t, rec)["data"].(map[string]any)
	if data["obfuscatorRelayProcess"] != true {
		t.Fatalf("ответ — не актуальные настройки: %s", rec.Body.String())
	}
	select {
	case p := <-calls:
		if !p {
			t.Fatal("хук позван с process=false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("хук не позван")
	}

	// То же значение — ни записи, ни хука.
	if rec := post(`{"process":true}`); rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	select {
	case p := <-calls:
		t.Fatalf("хук на неизменном значении (process=%v)", p)
	case <-time.After(200 * time.Millisecond):
	}

	// Без поля process — отказ, а не молчаливый возврат к ядру.
	if rec := post(`{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("пустое тело: status=%d, ждали 400", rec.Code)
	}
	if !store.IsObfuscatorRelayProcess() {
		t.Fatal("пустое тело сняло выключатель")
	}
}
