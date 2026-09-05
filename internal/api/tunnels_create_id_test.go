package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func createBody(id string) string {
	idField := ""
	if id != "" {
		idField = `"id":"` + id + `",`
	}
	return `{` + idField + `"name":"t","type":"awg","backend":"kernel",
		"interface":{"privateKey":"CA9lE1yLCcziI8Oq0dXDYr3QtdzFCEsKYw8sxAQ132o=","address":"10.9.0.2/32","mtu":1280},
		"peer":{"publicKey":"hOPHc7ZBk0dGrLLDFrCG7WHYzZ8SS5xBWMzOJ9CkNFo=","endpoint":"192.0.2.1:51820","allowedIPs":["0.0.0.0/0"]}}`
}

func postCreate(t *testing.T, h *TunnelsHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/tunnels/create", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Create(rec, req)
	return rec
}

// Идентификатор, присланный клиентом, задаёт номер интерфейса ровно так же, как
// сгенерированный, поэтому обязан проходить ту же проверку занятости. Иначе
// запись создастся на номере, который держит чужая подсистема, и первое же
// включение усыновит её интерфейс.
func TestCreate_ExplicitIDRejectedWhenIndexTaken(t *testing.T) {
	h, _ := newTunnelsUpdateHarness(t, &stubTunnelSvc{})
	h.SetOpkgTunOccupancy(func(context.Context) (map[int]bool, error) {
		return map[int]bool{12: true}, nil
	})

	rec := postCreate(t, h, createBody("awg12"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("занятый номер обязан давать 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "12") {
		t.Errorf("ответ должен называть занятый номер, got %s", rec.Body.String())
	}
}

func TestCreate_ExplicitIDAcceptedWhenIndexFree(t *testing.T) {
	stub := &stubTunnelSvc{}
	h, _ := newTunnelsUpdateHarness(t, stub)
	h.SetOpkgTunOccupancy(func(context.Context) (map[int]bool, error) {
		return map[int]bool{12: true}, nil
	})

	rec := postCreate(t, h, createBody("awg13"))

	// Код ответа не пинуем: stubTunnelSvc.Get отдаёт ошибку, и BuildTunnelResponse даёт
	// CREATE_FAILED уже ПОСЛЕ передачи записи сервису. Предмет — сама запись.
	if strings.Contains(rec.Body.String(), "INDEX_TAKEN") {
		t.Fatalf("свободный номер отвергнут проверкой занятости: %s", rec.Body.String())
	}
	if stub.createdRecord == nil || stub.createdRecord.ID != "awg13" {
		t.Fatalf("сервису ушла запись %+v, want ID awg13", stub.createdRecord)
	}
}

// Клиентский идентификатор без цифр всё равно получает номер интерфейса —
// extractTunnelNum подставляет ноль. Проверка обязана смотреть на этот номер,
// а не на текст идентификатора.
func TestCreate_ExplicitCustomIDUsesDerivedIndex(t *testing.T) {
	h, _ := newTunnelsUpdateHarness(t, &stubTunnelSvc{})
	h.SetOpkgTunOccupancy(func(context.Context) (map[int]bool, error) {
		return map[int]bool{0: true}, nil
	})

	rec := postCreate(t, h, createBody("myvpn"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("идентификатор без цифр займёт нулевой номер — он занят, ожидался 409, got %d: %s",
			rec.Code, rec.Body.String())
	}
}

// nativewg номеров OpkgTun не занимает: спрашивать занятость незачем, и отказ
// источника не должен мешать созданию.
func TestCreate_ExplicitIDNativeWGSkipsOccupancy(t *testing.T) {
	h, _ := newTunnelsUpdateHarness(t, &stubTunnelSvc{})
	h.SetOpkgTunOccupancy(func(context.Context) (map[int]bool, error) {
		t.Error("занятость не должна спрашиваться для nativewg")
		return nil, nil
	})

	body := strings.Replace(createBody("awg25"), `"backend":"kernel"`, `"backend":"nativewg"`, 1)
	rec := postCreate(t, h, body)

	if rec.Code == http.StatusConflict {
		t.Fatalf("nativewg не должен упираться в занятость: %s", rec.Body.String())
	}
}
