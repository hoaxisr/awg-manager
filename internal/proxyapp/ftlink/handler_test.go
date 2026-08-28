package ftlink

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Decode — POST /api/proxyrt/freeturn/link/decode. Форма ответа вербатим
// прежняя (api/freeturn.go:338): фронт заполняет поля клиента по этим ключам.
func TestDecodeHandler(t *testing.T) {
	s := New(Deps{})
	link, err := EncodeLink(LinkPayload{V: 1, Provider: "vk", Peer: "1.2.3.4:56000", Obf: "rtpopus2", Key: "aabb"})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/proxyrt/freeturn/link/decode",
		strings.NewReader(`{"link":"`+link+`"}`))
	rr := httptest.NewRecorder()
	s.Decode(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body)
	}
	data, _ := decodeEnvelope(t, rr)["data"].(map[string]any)
	if data["peer"] != "1.2.3.4:56000" || data["obf"] != "rtpopus2" || data["key"] != "aabb" {
		t.Fatalf("data=%+v", data)
	}

	rr = httptest.NewRecorder()
	s.Decode(rr, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"link":"мусор"}`)))
	if code, _ := decodeEnvelope(t, rr)["code"].(string); code != "FREETURN_LINK_DECODE_FAILED" {
		t.Fatalf("код отказа=%v", decodeEnvelope(t, rr)["code"])
	}

	rr = httptest.NewRecorder()
	s.Decode(rr, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET code=%d", rr.Code)
	}
}
