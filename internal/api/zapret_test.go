package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/zapret"
)

// zapretServiceStub — заглушка ZapretService. Позволяет проверить
// маппинг ошибок и форму ответов, не трогая файловую систему роутера.
type zapretServiceStub struct {
	status      zapret.Status
	strategies  zapret.Strategies
	stratErr    error
	hostlists   []zapret.HostlistInfo
	content     string
	readErr     error
	writeErr    error
	controlErr  error
	reorderFrom int
	reorderTo   int
}

func (s *zapretServiceStub) GetStatus() zapret.Status { return s.status }
func (s *zapretServiceStub) Recheck() zapret.Status   { return s.status }
func (s *zapretServiceStub) GetStrategies() (zapret.Strategies, error) {
	return s.strategies, s.stratErr
}
func (s *zapretServiceStub) ReorderStrategies(from, to int, _ string) (zapret.Strategies, error) {
	s.reorderFrom, s.reorderTo = from, to
	if s.stratErr != nil {
		return zapret.Strategies{}, s.stratErr
	}
	return s.strategies, nil
}
func (s *zapretServiceStub) GetHostlists() ([]zapret.HostlistInfo, error) {
	return s.hostlists, nil
}
func (s *zapretServiceStub) ReadHostlist(id zapret.HostlistID) (zapret.HostlistID, string, string, error) {
	return id, "/opt/zapret2/ipset/zapret-hosts-user.txt", s.content, s.readErr
}
func (s *zapretServiceStub) WriteHostlist(id zapret.HostlistID, _, _ string) (zapret.HostlistID, string, error) {
	if s.writeErr != nil {
		return "", "", s.writeErr
	}
	return id, "/opt/zapret2/ipset/" + string(id) + ".txt", nil
}
func (s *zapretServiceStub) Control(string, string) error { return s.controlErr }

func newZapretHandlerForTest(svc ZapretService, bus *events.Bus) *ZapretHandler {
	h := NewZapretHandler(svc, nil)
	if bus != nil {
		h.SetEventBus(bus)
	}
	return h
}

func zapretData(t *testing.T, rec *httptest.ResponseRecorder, data any) {
	t.Helper()
	decodeEnvelope(t, rec.Body.Bytes(), data)
}

func zapretSuccess(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var env struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("разбор ответа: %v (body=%s)", err, rec.Body.String())
	}
	if !env.Success {
		t.Fatalf("success=false: %s", rec.Body.String())
	}
}

func TestZapretStatusWhenServiceNil(t *testing.T) {
	h := newZapretHandlerForTest(nil, nil)
	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodGet, "/api/zapret/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("код: %d", rec.Code)
	}
	var got zapretStatusResponse
	zapretData(t, rec, &got)
	if got.ZapretStatus == nil {
		t.Fatal("статус не вернулся")
	}
	if got.ZapretStatus.Installed || got.ZapretStatus.ProcessState != zapret.StateNotInstalled {
		t.Errorf("ожидался not_installed, получено %+v", got.ZapretStatus)
	}
}

func TestZapretStatusRejectsWrongMethod(t *testing.T) {
	h := newZapretHandlerForTest(&zapretServiceStub{}, nil)
	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodPost, "/api/zapret/status", nil))
	if rec.Code == http.StatusOK {
		t.Error("POST на read-only эндпоинт не отклонён")
	}
}

func TestZapretStatusReportsRunning(t *testing.T) {
	svc := &zapretServiceStub{status: zapret.Status{
		Installed: true, Running: true, ProcessState: zapret.StateRunning,
		PIDs: []int{4242}, StrategyCount: 3, Package: "zapret22", PackageVersion: "2.0.1",
	}}
	h := newZapretHandlerForTest(svc, nil)
	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodGet, "/api/zapret/status", nil))

	var got zapretStatusResponse
	zapretData(t, rec, &got)
	if !got.ZapretStatus.Running || len(got.ZapretStatus.PIDs) != 1 {
		t.Errorf("статус: %+v", got.ZapretStatus)
	}
	if got.ZapretStatus.Package != "zapret22" {
		t.Errorf("пакет: %q", got.ZapretStatus.Package)
	}
}

func TestZapretControlPublishesInvalidation(t *testing.T) {
	bus := events.NewBus()
	_, ch, _ := bus.Subscribe()
	svc := &zapretServiceStub{status: zapret.Status{Installed: true, ProcessState: zapret.StateRunning}}
	h := newZapretHandlerForTest(svc, bus)

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"action":"restart"}`)
	h.Control(rec, httptest.NewRequest(http.MethodPost, "/api/zapret/control", body))

	if rec.Code != http.StatusOK {
		t.Fatalf("код: %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case ev := <-ch:
		if !strings.Contains(string(mustJSON(t, ev.Data)), string(events.ResourceRoutingZapretStatus)) {
			t.Errorf("инвалидирован не тот ресурс: %s", mustJSON(t, ev.Data))
		}
	default:
		t.Error("событие инвалидации не опубликовано — фронт не обновит статус")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestZapretControlNotInstalledIs404(t *testing.T) {
	h := newZapretHandlerForTest(nil, nil)
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"action":"start"}`)
	h.Control(rec, httptest.NewRequest(http.MethodPost, "/api/zapret/control", body))

	if rec.Code != http.StatusNotFound {
		t.Errorf("код: %d, ожидался 404 — отсутствие zapret не ошибка", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ZAPRET_NOT_INSTALLED") {
		t.Errorf("код ошибки: %s", rec.Body.String())
	}
}

func TestZapretControlSurfacesInitScriptError(t *testing.T) {
	// Текст ошибки init-скрипта — единственный диагностический
	// инструмент: сам zapret пишет в stderr, куда AWGM иначе не видит.
	svc := &zapretServiceStub{controlErr: zapret.ErrNotInstalled}
	h := newZapretHandlerForTest(svc, nil)
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"action":"restart"}`)
	h.Control(rec, httptest.NewRequest(http.MethodPost, "/api/zapret/control", body))

	if rec.Code != http.StatusNotFound {
		t.Errorf("код: %d", rec.Code)
	}
}

func TestZapretStrategiesSuccess(t *testing.T) {
	svc := &zapretServiceStub{strategies: zapret.Strategies{
		OptVar: "NFQWS2_OPT",
		Items: []zapret.Strategy{
			{Index: 0, Raw: "--filter-tcp=80 --lua-desync=multisplit"},
			{Index: 1, Raw: "--filter-tcp=443 --lua-desync=fake"},
		},
	}}
	h := newZapretHandlerForTest(svc, nil)
	rec := httptest.NewRecorder()
	h.Strategies(rec, httptest.NewRequest(http.MethodGet, "/api/zapret/strategies", nil))

	var got zapret.Strategies
	zapretData(t, rec, &got)
	if len(got.Items) != 2 {
		t.Fatalf("стратегий: %d", len(got.Items))
	}
	if got.Items[0].Index != 0 || got.Items[1].Index != 1 {
		t.Errorf("индексы должны совпадать с порядком: %+v", got.Items)
	}
}

func TestZapretStrategiesNotInstalled(t *testing.T) {
	svc := &zapretServiceStub{stratErr: zapret.ErrNotInstalled}
	h := newZapretHandlerForTest(svc, nil)
	rec := httptest.NewRecorder()
	h.Strategies(rec, httptest.NewRequest(http.MethodGet, "/api/zapret/strategies", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("код: %d, ожидался 404", rec.Code)
	}
}

func TestZapretReorderForwardsIndices(t *testing.T) {
	svc := &zapretServiceStub{}
	h := newZapretHandlerForTest(svc, nil)
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"from":0,"to":2}`)
	h.ReorderStrategy(rec, httptest.NewRequest(http.MethodPost, "/api/zapret/strategies/reorder", body))

	if rec.Code != http.StatusOK {
		t.Fatalf("код: %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.reorderFrom != 0 || svc.reorderTo != 2 {
		t.Errorf("индексы: from=%d to=%d", svc.reorderFrom, svc.reorderTo)
	}
}

func TestZapretHostlistsReturnsMetadata(t *testing.T) {
	svc := &zapretServiceStub{hostlists: []zapret.HostlistInfo{
		{ID: zapret.HostlistUser, Path: "/opt/zapret2/ipset/zapret-hosts-user.txt", Exists: true, Entries: 4, Domains: 4, Editable: true},
		{ID: zapret.HostlistMain, Path: "/opt/zapret2/ipset/zapret-hosts.txt", Exists: true, Entries: 900, Generated: true},
	}}
	h := newZapretHandlerForTest(svc, nil)
	rec := httptest.NewRecorder()
	h.Hostlists(rec, httptest.NewRequest(http.MethodGet, "/api/zapret/hostlists", nil))

	var got zapretHostlistsResponse
	zapretData(t, rec, &got)
	if len(got.Hostlists) != 2 {
		t.Fatalf("списков: %d", len(got.Hostlists))
	}
	if !got.Hostlists[0].Editable {
		t.Error("пользовательский список должен быть редактируемым")
	}
	if got.Hostlists[1].Editable {
		t.Error("генерируемый список не должен быть редактируемым")
	}
}

func TestZapretHostlistReadValidatesID(t *testing.T) {
	h := newZapretHandlerForTest(&zapretServiceStub{}, nil)
	// Произвольный путь в id не должен доходить до файловой системы.
	rec := httptest.NewRecorder()
	h.Hostlist(rec, httptest.NewRequest(http.MethodGet, "/api/zapret/hostlist?id=../../../etc/passwd", nil))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("код: %d, ожидался 400", rec.Code)
	}
}

func TestZapretHostlistReadSuccess(t *testing.T) {
	svc := &zapretServiceStub{content: "youtube.com\nrt.com\n"}
	h := newZapretHandlerForTest(svc, nil)
	rec := httptest.NewRecorder()
	h.Hostlist(rec, httptest.NewRequest(http.MethodGet, "/api/zapret/hostlist?id=user", nil))

	var got zapretHostlistResponse
	zapretData(t, rec, &got)
	if got.ID != zapret.HostlistUser {
		t.Errorf("id: %q", got.ID)
	}
	if got.Content != "youtube.com\nrt.com\n" {
		t.Errorf("содержимое: %q", got.Content)
	}
}

func TestZapretHostlistSaveRejectsUnknownID(t *testing.T) {
	h := newZapretHandlerForTest(&zapretServiceStub{}, nil)
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"id":"../../etc/shadow","content":"x"}`)
	h.SaveHostlist(rec, httptest.NewRequest(http.MethodPost, "/api/zapret/hostlist/save", body))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("код: %d, ожидался 400", rec.Code)
	}
}

func TestZapretHostlistSavePublishedInvalidation(t *testing.T) {
	bus := events.NewBus()
	_, ch, _ := bus.Subscribe()
	svc := &zapretServiceStub{}
	h := newZapretHandlerForTest(svc, bus)

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"id":"user","content":"example.com"}`)
	h.SaveHostlist(rec, httptest.NewRequest(http.MethodPost, "/api/zapret/hostlist/save", body))

	if rec.Code != http.StatusOK {
		t.Fatalf("код: %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-ch:
	default:
		t.Error("инвалидация не опубликована после записи списка")
	}
}

func TestZapretErrorCodesAreDistinct(t *testing.T) {
	// Не установлено и невалидный ввод — разные ситуации: первый рисует
	// карточку «поставьте zapret», второй — подсветку поля.
	status, _, code := zapretErrStatus(zapret.ErrNotInstalled)
	if code != "ZAPRET_NOT_INSTALLED" {
		t.Errorf("код для ErrNotInstalled: %q", code)
	}
	if status != http.StatusNotFound {
		t.Errorf("ErrNotInstalled должен давать 404, получено %d", status)
	}
	if vStatus, _, vCode := zapretErrStatus(errBadHostlistID); vCode == "ZAPRET_NOT_INSTALLED" {
		t.Error("ошибка валидации не должна выглядеть как отсутствие zapret")
	} else if vStatus != http.StatusBadRequest {
		t.Errorf("ошибка валидации должна давать 400, получено %d", vStatus)
	}
}

func TestZapretConflictStatus(t *testing.T) {
	svc := &zapretServiceStub{stratErr: zapret.ErrConflict}
	h := newZapretHandlerForTest(svc, nil)
	rec := httptest.NewRecorder()
	h.ReorderStrategy(rec, httptest.NewRequest(http.MethodPost, "/api/zapret/strategies/reorder", strings.NewReader(`{"revision":"stale","from":0,"to":1}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ZAPRET_CONFLICT") {
		t.Fatalf("missing conflict code: %s", rec.Body.String())
	}
}

func TestZapretHostlistSaveReportsRestartFailureAsSaved(t *testing.T) {
	h := newZapretHandlerForTest(&zapretServiceStub{writeErr: &zapret.HostlistApplyError{Err: fmt.Errorf("unsupported daemon restart")}}, nil)
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"id":"user","content":"example.com","revision":"current"}`)
	h.SaveHostlist(rec, httptest.NewRequest(http.MethodPost, "/api/zapret/hostlist/save", body))
	var got zapretHostlistResponse
	zapretData(t, rec, &got)
	if got.RestartError != "unsupported daemon restart" {
		t.Fatalf("saved-but-not-applied result lost: %+v", got)
	}
}
