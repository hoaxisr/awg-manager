package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
)

// importStubSvc — импортёр, отдающий запись, которой НЕТ в сторе: пост-импортная
// дозапись дефолтов упадёт с ErrNotFound.
type importStubSvc struct {
	stubTunnelSvc
	imported *service.TunnelWithStatus
	// link — связь, с которой хендлер позвал Import: она обязана уехать в
	// СОЗДАНИЕ записи, а не дописываться после (PF21).
	link service.ImportLink
}

func (s *importStubSvc) Import(_ context.Context, _, _, _ string, link service.ImportLink) (*service.TunnelWithStatus, error) {
	s.link = link
	return s.imported, nil
}

// Get переопределён, потому что после дозаписи хендлер идёт в
// BuildTunnelResponse: без него ответ стал бы IMPORT_FAILED по чужой причине.
func (s *importStubSvc) Get(context.Context, string) (*service.TunnelWithStatus, error) {
	return s.imported, nil
}

type appLogSpy struct {
	entries []string
}

func (s *appLogSpy) AppLog(level logging.Level, group, subgroup, action, target, message string) {
	s.entries = append(s.entries, string(level)+"|"+action+"|"+message)
}

// F53: провал дозаписи пост-импортных дефолтов больше не глотается —
// импорт остаётся успешным, но отказ виден в журнале.
func TestImportConf_WarnsWhenPostImportDefaultsFail(t *testing.T) {
	store := storage.NewAWGTunnelStore(t.TempDir())
	svc := &importStubSvc{imported: &service.TunnelWithStatus{ID: "awg9", Name: "imported"}}
	spy := &appLogSpy{}
	h := NewImportHandler(svc, store, spy)

	body, _ := json.Marshal(map[string]string{"content": "[Interface]", "name": "imported"})
	req := httptest.NewRequest(http.MethodPost, "/api/tunnels/import", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ImportConf(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (импорт уже успешен, отказ дозаписи его не отменяет)", rec.Code)
	}
	found := false
	for _, e := range spy.entries {
		if bytes.Contains([]byte(e), []byte("persist post-import defaults")) {
			found = true
		}
	}
	if !found {
		t.Errorf("журнал = %v, want Warn о провале дозаписи пост-импортных дефолтов", spy.entries)
	}
}

// PF21: связь клиента прокси уезжает в САМ Import, а не дозаписью после него.
// Дозапись у этой ручки не роняет ответ (профиль F48, тест выше) — значит
// связь, оставленная во втором шаге, терялась бы МОЛЧА, и туннель становился
// сиротой, невидимой для уборки связанных.
func TestImportConf_LinkTravelsWithCreate(t *testing.T) {
	store := storage.NewAWGTunnelStore(t.TempDir())
	svc := &importStubSvc{imported: &service.TunnelWithStatus{ID: "awg9", Name: "imported"}}
	h := NewImportHandler(svc, store, &appLogSpy{})

	body, _ := json.Marshal(map[string]string{
		"content": "[Interface]", "name": "imported", "wdttClientId": "default"})
	req := httptest.NewRequest(http.MethodPost, "/api/tunnels/import", bytes.NewReader(body))
	h.ImportConf(httptest.NewRecorder(), req)

	if svc.link.WdttClientID != "default" {
		t.Fatalf("связь не доехала до Import: %+v", svc.link)
	}
}
