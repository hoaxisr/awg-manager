package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// recManagedSvc — записывающий managed.ManagedServerService: мутирующие методы
// пишут в журнал «метод:аргументы» и возвращают err, геттеры отдают нули.
// Пин диспатча Subtree держится именно на журнале: код ответа один и тот же у
// половины веток, а вот ЧТО вызвано — различает их.
type recManagedSvc struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (s *recManagedSvc) record(entry string) {
	s.mu.Lock()
	s.calls = append(s.calls, entry)
	s.mu.Unlock()
}

func (s *recManagedSvc) journal() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *recManagedSvc) Create(context.Context, managed.CreateServerRequest) (*storage.ManagedServer, error) {
	s.record("Create")
	return &storage.ManagedServer{}, s.err
}

func (s *recManagedSvc) List() []storage.ManagedServer { return nil }

func (s *recManagedSvc) Get(id string) (*storage.ManagedServer, error) {
	return &storage.ManagedServer{InterfaceName: id}, nil
}

func (s *recManagedSvc) Update(_ context.Context, id string, _ managed.UpdateServerRequest) error {
	s.record("Update:" + id)
	return s.err
}

func (s *recManagedSvc) Delete(_ context.Context, id string) error {
	s.record("Delete:" + id)
	return s.err
}

func (s *recManagedSvc) SuggestAddress(context.Context) (string, string, error) { return "", "", nil }

func (s *recManagedSvc) SetEnabled(_ context.Context, id string, enabled bool) error {
	s.record("SetEnabled:" + id + ":" + boolText(enabled))
	return s.err
}

func (s *recManagedSvc) RestartOrStart(_ context.Context, id string) error {
	s.record("RestartOrStart:" + id)
	return s.err
}

func (s *recManagedSvc) SetNATMode(_ context.Context, id, mode string) error {
	s.record("SetNATMode:" + id + ":" + mode)
	return s.err
}

func (s *recManagedSvc) SetLANSegments(_ context.Context, id string, segments []string) error {
	s.record("SetLANSegments:" + id + ":" + strings.Join(segments, ","))
	return s.err
}

func (s *recManagedSvc) ListLANSegments(context.Context) ([]managed.LANSegmentDTO, error) {
	return nil, nil
}

func (s *recManagedSvc) SetPolicy(_ context.Context, id, policy string) error {
	s.record("SetPolicy:" + id + ":" + policy)
	return s.err
}

func (s *recManagedSvc) ListPolicies(context.Context) ([]managed.PolicyOption, error) {
	return nil, nil
}

func (s *recManagedSvc) AddPeer(_ context.Context, id string, _ managed.AddPeerRequest) (*storage.ManagedPeer, error) {
	s.record("AddPeer:" + id)
	return &storage.ManagedPeer{}, s.err
}

func (s *recManagedSvc) UpdatePeer(_ context.Context, id, pubkey string, _ managed.UpdatePeerRequest) error {
	s.record("UpdatePeer:" + id + ":" + pubkey)
	return s.err
}

func (s *recManagedSvc) DeletePeer(_ context.Context, id, pubkey string) error {
	s.record("DeletePeer:" + id + ":" + pubkey)
	return s.err
}

func (s *recManagedSvc) TogglePeer(_ context.Context, id, pubkey string, enabled bool) error {
	s.record("TogglePeer:" + id + ":" + pubkey + ":" + boolText(enabled))
	return s.err
}

func (s *recManagedSvc) GenerateConf(context.Context, string, string) (string, error) {
	return "", nil
}

func (s *recManagedSvc) GetStats(context.Context, string) (*managed.ManagedServerStats, error) {
	return nil, nil
}

func (s *recManagedSvc) GetASCParams(context.Context, string) (json.RawMessage, error) {
	return nil, nil
}

func (s *recManagedSvc) SetASCParams(_ context.Context, id string, _ json.RawMessage) error {
	s.record("SetASCParams:" + id)
	return s.err
}

func (s *recManagedSvc) InvalidateCache(id string) { s.record("InvalidateCache:" + id) }

func (s *recManagedSvc) ForeignAccessGroups(context.Context, string) ([]string, error) {
	return nil, nil
}

var _ managed.ManagedServerService = (*recManagedSvc)(nil)

func boolText(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// newManagedSubtreeHarness — ManagedServerHandler с подключённым ServersHandler:
// успешные мутации отвечают через writeServersSnapshot, а без servers это
// INTERNAL_ERROR 400 и любой пин кода ответа стал бы ложноположительным.
func newManagedSubtreeHarness(t *testing.T, svc *recManagedSvc) (*ManagedServerHandler, *busProbe) {
	t.Helper()
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/interface/", `{}`)
	queries := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})
	store := storage.NewSettingsStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	sh := NewServersHandler(queries, store, nil, nil)
	p := newBusProbe(t)
	sh.SetEventBus(p.bus())
	h := NewManagedServerHandler(svc)
	h.SetServersHandler(sh)
	return h, p
}

// Пин таблицы диспатча Subtree: путь+метод → метод службы. Restart в таблицу не
// входит — он отвечает синхронно, а службу зовёт из горутины после паузы.
func TestManagedSubtree_DispatchesByPathAndMethod(t *testing.T) {
	// 44-символьный base64 с '/' и '+' — как у ~45% реальных WG-ключей.
	pk := "AB/CD+EF" + strings.Repeat("a", 35) + "="
	if !isValidWGKey(pk) {
		t.Fatalf("настройка теста: %q не проходит isValidWGKey", pk)
	}

	cases := []struct {
		name         string
		method, path string
		body         string
		want         []string
		code         int
	}{
		{"enabled", http.MethodPost, "/api/managed-servers/Wireguard3/enabled", `{"enabled":true}`,
			[]string{"SetEnabled:Wireguard3:true", "InvalidateCache:Wireguard3"}, http.StatusOK},
		{"delete", http.MethodDelete, "/api/managed-servers/Wireguard3", "",
			[]string{"Delete:Wireguard3", "InvalidateCache:Wireguard3"}, http.StatusOK},
		{"wrong method", http.MethodPatch, "/api/managed-servers/Wireguard3", "",
			nil, http.StatusMethodNotAllowed},
		{"delete peer", http.MethodDelete, "/api/managed-servers/Wireguard3/peers/" + url.PathEscape(pk), "",
			[]string{"DeletePeer:Wireguard3:" + pk, "InvalidateCache:Wireguard3"}, http.StatusOK},
		{"toggle peer", http.MethodPost, "/api/managed-servers/Wireguard3/peers/" + url.PathEscape(pk) + "/toggle", `{"enabled":false}`,
			[]string{"TogglePeer:Wireguard3:" + pk + ":false", "InvalidateCache:Wireguard3"}, http.StatusOK},
		{"unknown leaf", http.MethodGet, "/api/managed-servers/Wireguard3/nothing", "",
			nil, http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Фейк свой на каждый случай: общий журнал склеил бы вызовы соседей.
			svc := &recManagedSvc{}
			h, p := newManagedSubtreeHarness(t, svc)

			rr := perform(h.Subtree, tc.method, tc.path, tc.body)
			if rr.Code != tc.code {
				t.Fatalf("код=%d, ожидался %d (тело=%s)", rr.Code, tc.code, rr.Body.String())
			}
			if got := svc.journal(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("журнал службы = %v, ожидался %v", got, tc.want)
			}
			// Успех мутации публикует ровно одну инвалидацию; отвергнутый путь — ни одной.
			wantPub := []string{}
			if len(tc.want) > 0 {
				wantPub = []string{"servers/managed-mutation"}
			}
			if pub := p.invalidated(); !reflect.DeepEqual(pub, wantPub) {
				t.Fatalf("публикации = %v, ожидались %v", pub, wantPub)
			}
		})
	}
}

// Отказ службы обязан оборвать конвейер до InvalidateCache и до публикации:
// иначе фронт перечитал бы состояние как «изменилось», хотя записи не было.
func TestManagedSubtree_ServiceFailureStopsPipeline(t *testing.T) {
	cases := []struct {
		name         string
		method, path string
		body         string
		want         []string
		code         string
	}{
		{"enabled", http.MethodPost, "/api/managed-servers/Wireguard3/enabled", `{"enabled":true}`,
			[]string{"SetEnabled:Wireguard3:true"}, "SET_ENABLED_FAILED"},
		{"delete", http.MethodDelete, "/api/managed-servers/Wireguard3", "",
			[]string{"Delete:Wireguard3"}, "DELETE_FAILED"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &recManagedSvc{err: errors.New("rci down")}
			h, p := newManagedSubtreeHarness(t, svc)

			rr := perform(h.Subtree, tc.method, tc.path, tc.body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("код=%d, ожидался 400 (тело=%s)", rr.Code, rr.Body.String())
			}
			if got := svc.journal(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("журнал службы = %v, ожидался %v (конвейер не оборван)", got, tc.want)
			}
			if pub := p.invalidated(); len(pub) != 0 {
				t.Fatalf("публикации при отказе = %v, ожидался ноль", pub)
			}
			body := decodeJSONBody(t, rr)
			if body["code"] != tc.code {
				t.Fatalf("code=%v, ожидался %q (тело=%s)", body["code"], tc.code, rr.Body.String())
			}
		})
	}
}

// Отказ RestartOrStart больше не голый return: Warn в журнал и инвалидация servers,
// чтобы карточка не зависла в «перезапускается». Пауза — через шов, без сна.
func TestManagedRestart_FailurePublishesAndWarns(t *testing.T) {
	old := managedRestartDelay
	managedRestartDelay = 0
	t.Cleanup(func() { managedRestartDelay = old })

	svc := &recManagedSvc{err: errors.New("iface busy")}
	p := newBusProbe(t)
	spy := &appLogSpy{}
	servers := &ServersHandler{log: logging.NewScopedLogger(spy, logging.GroupServer, logging.SubWan)}
	servers.SetEventBus(p.bus())
	h := &ManagedServerHandler{svc: svc}
	h.SetServersHandler(servers)

	rr := httptest.NewRecorder()
	h.Restart(rr, httptest.NewRequest(http.MethodPost, "/api/managed-servers/srv1/restart", nil), "srv1")
	if rr.Code != http.StatusOK {
		t.Fatalf("ответ %d: %s", rr.Code, rr.Body.String())
	}
	if got := p.waitInvalidated(t, 2*time.Second); !slices.Contains(got, "servers/managed-mutation") {
		t.Fatalf("инвалидация не пришла: %v", got)
	}
	if len(spy.entries) != 1 || spy.entries[0] != "warn|restart|restart failed: iface busy" {
		t.Fatalf("журнал = %v", spy.entries)
	}
}
