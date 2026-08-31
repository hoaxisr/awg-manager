package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/manager"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// ── фейки ────────────────────────────────────────────────────────

type proxyPostCall struct {
	Key  string
	Kind proxyrt.EventKind
}

type proxyEnabledCall struct {
	Key string
	On  bool
}

// fakeProxyManager повторяет КОМПОЗИЦИЮ настоящего manager, а не только его
// сигнатуры: Update гоняет мутатор по хранимой записи, возвращает его ошибку
// БЕЗ обёртки (manager.mutateStore отдаёт её как есть — на этом стоит разбор
// гейтов через errors.As) и будит воркер; SetEnabled ходит через тот же
// Update. Иначе тест «PATCH будит воркер» проверял бы факт вызова, а не то,
// что после него инстанс действительно разбужен.
type fakeProxyManager struct {
	records []instancestore.Record
	seed    manager.SeedInfo

	createErr error
	updateErr error
	deleteErr error
	ackErr    error
	postOK    bool
	acked     int

	created []instancestore.Record
	mutated []instancestore.Record
	enabled []proxyEnabledCall
	deleted []string
	posts   []proxyPostCall
}

func (f *fakeProxyManager) Records() []instancestore.Record {
	return append([]instancestore.Record(nil), f.records...)
}

func (f *fakeProxyManager) SeedInfo() manager.SeedInfo { return f.seed }

func (f *fakeProxyManager) AckListenMoves() error {
	if f.ackErr != nil {
		return f.ackErr
	}
	f.acked++
	f.seed.MovedListen = nil
	return nil
}

func (f *fakeProxyManager) Create(_ context.Context, rec instancestore.Record) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.created = append(f.created, rec)
	f.records = append(f.records, rec)
	return nil
}

func (f *fakeProxyManager) Update(_ context.Context, key string, mutate func(*instancestore.Record) error) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	for i := range f.records {
		if f.records[i].Key() != key {
			continue
		}
		rec := f.records[i]
		if err := mutate(&rec); err != nil {
			return err
		}
		f.records[i] = rec
		f.mutated = append(f.mutated, rec)
		f.posts = append(f.posts, proxyPostCall{Key: key, Kind: proxyrt.EventIntentChanged})
		return nil
	}
	return fmt.Errorf("инстанс %s не найден", key)
}

func (f *fakeProxyManager) SetEnabled(ctx context.Context, key string, on bool) error {
	f.enabled = append(f.enabled, proxyEnabledCall{Key: key, On: on})
	return f.Update(ctx, key, func(r *instancestore.Record) error {
		r.Enabled = on
		return nil
	})
}

func (f *fakeProxyManager) Delete(_ context.Context, key string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, key)
	out := f.records[:0]
	for _, r := range f.records {
		if r.Key() != key {
			out = append(out, r)
		}
	}
	f.records = out
	return nil
}

func (f *fakeProxyManager) Post(key string, k proxyrt.EventKind) bool {
	f.posts = append(f.posts, proxyPostCall{Key: key, Kind: k})
	return f.postOK
}

type fakeProxyStates struct {
	m map[string]proxyrt.InstanceState
}

func (f fakeProxyStates) List() []proxyrt.InstanceState {
	out := make([]proxyrt.InstanceState, 0, len(f.m))
	for _, st := range f.m {
		out = append(out, st)
	}
	return out
}

func (f fakeProxyStates) Get(id string) (proxyrt.InstanceState, bool) {
	st, ok := f.m[id]
	return st, ok
}

// ── фикстуры ─────────────────────────────────────────────────────

func fullServerRecord() instancestore.Record {
	return instancestore.Record{
		ID:        "default",
		Kind:      instancestore.KindWdttServer,
		Name:      "Раздача",
		Enabled:   true,
		CreatedAt: "2026-08-01T10:00:00Z",
		Sub:       "https://sub.example/s1",
		PeerWg:    "wg.example:56000",
		PeerRaw:   "raw.example:56001",
		Users: []instancestore.ServerUser{{
			Password: "u-secret", Comment: "Ноут", VkHash: "vh1", Auto: true,
		}},
		LinkPeer:     "link.example",
		LinkVKHashes: "vk1,vk2",
		StatsLog:     "/opt/var/stat.log",
		WdttServer: &roles.WdttServerConfig{
			Listen:       "0.0.0.0:56000",
			WgPort:       51820,
			ConfigDir:    "/opt/etc/wdtt",
			Password:     "s-secret",
			WgIface:      "opkgtun18",
			RawIface:     "opkgtun19",
			NdmsIface:    "OpkgTun18",
			RawNdmsIface: "OpkgTun19",
			RawListen:    "0.0.0.0:56001",
			DirectListen: "0.0.0.0:56002",
			RelayMode:    "wg",
			NatMode:      "full",
			NatStaticWAN: "ISP",
			Policy:       "Policy1",
			LanSegments:  []string{"192.168.1.0/24"},
			Debug:        true,
			OpenFirewall: true,
		},
	}
}

func fullClientRecord() instancestore.Record {
	order := 0
	return instancestore.Record{
		ID:        "nl",
		Kind:      instancestore.KindWdttClient,
		Name:      "Нидерланды",
		Enabled:   true,
		CreatedAt: "2026-08-02T10:00:00Z",
		Sub:       "https://sub.example/c1",
		PeerWg:    "wg.example:56000",
		PeerRaw:   "raw.example:56001",
		WdttClient: &roles.WdttClientConfig{
			Mode: "wg", Listen: "127.0.0.1:9000", Peer: "wg.example:56000",
			Password: "c-secret", VKHashes: "vk", Workers: 9,
			Policies: []roles.PolicyPermit{{Name: "A", Order: &order}},
		},
	}
}

func newProxyHandler(t *testing.T, mgr *fakeProxyManager, states fakeProxyStates) *ProxyInstancesHandler {
	t.Helper()
	return NewProxyInstancesHandler(ProxyInstancesDeps{
		Manager: mgr,
		States:  states,
		Snapshot: func(key string) (awgmproto.State, bool) {
			if key != "wdtt-server:default" {
				return awgmproto.State{}, false
			}
			clients := 3
			return awgmproto.State{
				Role: "server", Instance: "default", PID: 4321,
				ConfigHash: "cfg", BinarySHA256: "sha",
				UptimeS: 120, LastError: "нет связи", Mode: "wg",
				Address: "10.66.0.2/32", MTU: 1420,
				WG:      &awgmproto.WGState{Config: "[Interface]"},
				Clients: &clients,
			}, true
		},
		Log:              func(key string) string { return "хвост журнала " + key },
		BinaryInfo:       func(k instancestore.Kind) (string, bool) { return "/opt/bin/" + string(k), true },
		OpkgTunSupported: func() bool { return true },
	})
}

func doProxy(t *testing.T, h *ProxyInstancesHandler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	rr := httptest.NewRecorder()
	h.Handle(rr, httptest.NewRequest(method, path, rd))
	return rr
}

func decodeProxyData(t *testing.T, rr *httptest.ResponseRecorder, dst any) {
	t.Helper()
	var env struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("разбор конверта: %v\n%s", err, rr.Body.String())
	}
	if !env.Success {
		t.Fatalf("ожидали success, получили: %s", rr.Body.String())
	}
	if err := json.Unmarshal(env.Data, dst); err != nil {
		t.Fatalf("разбор data: %v\n%s", err, env.Data)
	}
}

func decodeProxyErr(t *testing.T, rr *httptest.ResponseRecorder) (code, message string) {
	t.Helper()
	var env struct {
		Error   bool   `json:"error"`
		Message string `json:"message"`
		Code    string `json:"code"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("разбор ошибки: %v\n%s", err, rr.Body.String())
	}
	if !env.Error {
		t.Fatalf("ожидали конверт ошибки, получили: %s", rr.Body.String())
	}
	return env.Code, env.Message
}

// ── список ───────────────────────────────────────────────────────

func TestProxyInstancesList_RecordStateAndProcess(t *testing.T) {
	rec := fullServerRecord()
	mgr := &fakeProxyManager{
		records: []instancestore.Record{rec},
		seed:    manager.SeedInfo{Booted: true, Certified: true},
	}
	states := fakeProxyStates{m: map[string]proxyrt.InstanceState{
		"wdtt-server:default": {
			ID:     "wdtt-server:default",
			Intent: proxyrt.IntentEnabled,
			Phase:  proxyrt.PhaseWaiting,
			Resources: []proxyrt.ResourceState{
				{ID: "process", Status: proxyrt.StatusOK, Detail: "pid 4321"},
				{ID: "ndms_iface", Status: proxyrt.StatusDrift, Detail: "нет", Error: "занят"},
			},
			LastPlan: []proxyrt.Step{
				{Resource: "ndms_iface", Op: "create", Args: map[string]string{"name": "OpkgTun18"}, Reason: "нет интерфейса"},
			},
			UpdatedAt: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
		},
	}}
	h := newProxyHandler(t, mgr, states)

	rr := doProxy(t, h, http.MethodGet, "/api/proxyrt/instances", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("код = %d, ждали 200: %s", rr.Code, rr.Body.String())
	}
	var data ProxyRtListData
	decodeProxyData(t, rr, &data)

	wantSeed := ProxyRtSeedView{Seeded: true, Certified: true}
	if !reflect.DeepEqual(data.Seed, wantSeed) {
		t.Fatalf("seed = %+v, ждали %+v", data.Seed, wantSeed)
	}
	if len(data.Instances) != 1 {
		t.Fatalf("инстансов %d, ждали 1", len(data.Instances))
	}
	got := data.Instances[0]

	if got.State == nil {
		t.Fatalf("state потерян в списке")
	}
	wantState := ProxyRtStateView{
		Intent: "enabled",
		Phase:  "waiting",
		Resources: []ProxyRtResourceView{
			{ID: "process", Status: "ok", Detail: "pid 4321"},
			{ID: "ndms_iface", Status: "drift", Detail: "нет", Error: "занят"},
		},
		LastPlan: []ProxyRtStepView{
			{Resource: "ndms_iface", Op: "create", Args: map[string]string{"name": "OpkgTun18"}, Reason: "нет интерфейса"},
		},
		UpdatedAt: "2026-08-24T12:00:00Z",
	}
	if !reflect.DeepEqual(*got.State, wantState) {
		t.Fatalf("state = %+v,\nждали %+v", *got.State, wantState)
	}

	clients := 3
	wantProcess := ProcessView{
		Running:       true,
		PID:           4321,
		Address:       "10.66.0.2/32",
		UptimeS:       120,
		LastError:     "нет связи",
		Mode:          "wg",
		WgConfig:      "[Interface]",
		Clients:       &clients,
		Log:           "хвост журнала wdtt-server:default",
		Binary:        "/opt/bin/wdtt-server",
		BinaryPresent: true,
	}
	if !reflect.DeepEqual(got.Process, wantProcess) {
		t.Fatalf("process = %+v,\nждали %+v", got.Process, wantProcess)
	}

	// Запись целиком: полная фикстура ловит потерю любого поля.
	wantRec := ProxyRtInstanceView{
		Key:          "wdtt-server:default",
		ID:           "default",
		Kind:         string(instancestore.KindWdttServer),
		Name:         "Раздача",
		Enabled:      true,
		CreatedAt:    "2026-08-01T10:00:00Z",
		Sub:          "https://sub.example/s1",
		PeerWg:       "wg.example:56000",
		PeerRaw:      "raw.example:56001",
		LinkPeer:     "link.example",
		LinkVKHashes: "vk1,vk2",
		StatsLog:     "/opt/var/stat.log",
		Config:       got.Config, // конфиг сверяется отдельно ниже
		State:        got.State,
		Process:      got.Process,
	}
	if !reflect.DeepEqual(got, wantRec) {
		t.Fatalf("запись = %+v,\nждали %+v", got, wantRec)
	}
}

func TestProxyInstancesList_MasksSecrets(t *testing.T) {
	mgr := &fakeProxyManager{
		records: []instancestore.Record{fullServerRecord()},
		seed:    manager.SeedInfo{Booted: true, Certified: true},
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})

	rr := doProxy(t, h, http.MethodGet, "/api/proxyrt/instances", "")
	if strings.Contains(rr.Body.String(), "s-secret") || strings.Contains(rr.Body.String(), "bot-secret") ||
		strings.Contains(rr.Body.String(), "u-secret") {
		t.Fatalf("секрет уехал наружу: %s", rr.Body.String())
	}
	var data ProxyRtListData
	decodeProxyData(t, rr, &data)
	cfg := data.Instances[0].Config
	if _, ok := cfg["password"]; ok {
		t.Errorf("password присутствует в конфиге ответа")
	}
	if cfg["passwordSet"] != true {
		t.Errorf("passwordSet = %v, ждали true", cfg["passwordSet"])
	}
	// Прочие поля конфига доезжают как есть.
	if cfg["natMode"] != "full" || cfg["relayMode"] != "wg" {
		t.Errorf("конфиг потерял поля: %+v", cfg)
	}
}

func TestProxyInstancesList_SecretFlagFalseWhenEmpty(t *testing.T) {
	rec := fullServerRecord()
	rec.WdttServer.Password = ""
	mgr := &fakeProxyManager{
		records: []instancestore.Record{rec},
		seed:    manager.SeedInfo{Booted: true, Certified: true},
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodGet, "/api/proxyrt/instances", "")
	var data ProxyRtListData
	decodeProxyData(t, rr, &data)
	v, ok := data.Instances[0].Config["passwordSet"]
	if !ok {
		t.Fatalf("passwordSet отсутствует: пустой секрет неотличим от неизвестного")
	}
	if v != false {
		t.Fatalf("passwordSet = %v, ждали false", v)
	}
}

func TestProxyInstancesList_SeedGateLocked(t *testing.T) {
	mgr := &fakeProxyManager{
		records: []instancestore.Record{fullServerRecord()},
		seed:    manager.SeedInfo{Booted: true, Certified: false, Err: "реестр не сертифицирован"},
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodGet, "/api/proxyrt/instances", "")
	var data ProxyRtListData
	decodeProxyData(t, rr, &data)
	want := ProxyRtSeedView{Seeded: true, Certified: false, Error: "реестр не сертифицирован"}
	if !reflect.DeepEqual(data.Seed, want) {
		t.Fatalf("seed = %+v, ждали %+v (запертый гейт обязан быть отличим)", data.Seed, want)
	}
	if len(data.Instances) != 1 {
		t.Fatalf("инстансов %d: при seeded=true список отдаётся", len(data.Instances))
	}
}

func TestProxyInstancesList_SkippedOldConfigIsVisible(t *testing.T) {
	// Пропущенный старый конфиг — отдельный признак: по нему интерфейс
	// называет пользователю, ЧЬИ инстансы не перенеслись.
	mgr := &fakeProxyManager{
		records: []instancestore.Record{fullServerRecord()},
		seed: manager.SeedInfo{Booted: true, Certified: false, Err: "пропущен неразобранный старый конфиг",
			Skipped: []instancestore.SkippedSource{{File: "wdtt.json", Reason: "поле не того типа"}}},
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodGet, "/api/proxyrt/instances", "")
	var data ProxyRtListData
	decodeProxyData(t, rr, &data)
	want := []ProxyRtSkippedSourceView{{File: "wdtt.json", Reason: "поле не того типа"}}
	if !reflect.DeepEqual(data.Seed.Skipped, want) {
		t.Fatalf("skipped = %+v, ждали %+v", data.Seed.Skipped, want)
	}
}

func TestProxyInstancesList_ListenMoveIsVisible(t *testing.T) {
	// Амендмент G3: переезд listen-порта обязан доехать до интерфейса — у
	// человека снаружи мог быть настроен клиент на прежний порт.
	mgr := &fakeProxyManager{
		records: []instancestore.Record{fullServerRecord()},
		seed: manager.SeedInfo{Booted: true, Certified: true,
			MovedListen: []instancestore.ListenMove{{Instance: "freeturn-client:default",
				Name: "Клиент", From: "127.0.0.1:9000", To: "127.0.0.1:9002"}}},
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodGet, "/api/proxyrt/instances", "")
	var data ProxyRtListData
	decodeProxyData(t, rr, &data)
	want := []ProxyRtListenMoveView{{Instance: "freeturn-client:default", Name: "Клиент",
		From: "127.0.0.1:9000", To: "127.0.0.1:9002"}}
	if !reflect.DeepEqual(data.Seed.MovedListen, want) {
		t.Fatalf("movedListen = %+v, ждали %+v", data.Seed.MovedListen, want)
	}
}

func TestProxyInstancesList_NotSeeded(t *testing.T) {
	mgr := &fakeProxyManager{
		records: []instancestore.Record{fullServerRecord()},
		seed:    manager.SeedInfo{Booted: false, Err: "RCI недоступен"},
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodGet, "/api/proxyrt/instances", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("код = %d, ждали 200 (список с причиной)", rr.Code)
	}
	var data ProxyRtListData
	decodeProxyData(t, rr, &data)
	want := ProxyRtSeedView{Seeded: false, Certified: false, Error: "RCI недоступен"}
	if !reflect.DeepEqual(data.Seed, want) {
		t.Fatalf("seed = %+v, ждали %+v", data.Seed, want)
	}
	if len(data.Instances) != 0 {
		t.Fatalf("инстансов %d, ждали 0", len(data.Instances))
	}
}

func TestProxyInstancesGet_OneAndNotFound(t *testing.T) {
	mgr := &fakeProxyManager{
		records: []instancestore.Record{fullServerRecord()},
		seed:    manager.SeedInfo{Booted: true, Certified: true},
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})

	rr := doProxy(t, h, http.MethodGet, "/api/proxyrt/instances/wdtt-server:default", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("код = %d, ждали 200: %s", rr.Code, rr.Body.String())
	}
	var got ProxyRtInstanceView
	decodeProxyData(t, rr, &got)
	if got.Key != "wdtt-server:default" || got.Name != "Раздача" {
		t.Fatalf("запись = %+v", got)
	}

	rr = doProxy(t, h, http.MethodGet, "/api/proxyrt/instances/wdtt-client:none", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("код = %d, ждали 404", rr.Code)
	}
	if code, _ := decodeProxyErr(t, rr); code != "NOT_FOUND" {
		t.Fatalf("код ошибки = %q, ждали NOT_FOUND", code)
	}
}

// ── создание ─────────────────────────────────────────────────────

func TestProxyInstancesCreate_PassesRecordToManager(t *testing.T) {
	mgr := &fakeProxyManager{seed: manager.SeedInfo{Booted: true, Certified: true}}
	h := newProxyHandler(t, mgr, fakeProxyStates{})

	body := `{"id":"nl","kind":"wdtt-client","name":"Нидерланды","enabled":true,
		"config":{"connMode":"raw","peer":"1.2.3.4:56000","password":"pw","vkHashes":"vk","workers":18}}`
	rr := doProxy(t, h, http.MethodPost, "/api/proxyrt/instances", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("код = %d, ждали 200: %s", rr.Code, rr.Body.String())
	}
	if len(mgr.created) != 1 {
		t.Fatalf("Create вызван %d раз", len(mgr.created))
	}
	want := instancestore.Record{
		ID: "nl", Kind: instancestore.KindWdttClient, Name: "Нидерланды", Enabled: true,
		WdttClient: &roles.WdttClientConfig{
			Mode: "raw", Peer: "1.2.3.4:56000", Password: "pw", VKHashes: "vk", Workers: 18,
		},
	}
	if !reflect.DeepEqual(mgr.created[0], want) {
		t.Fatalf("в Create ушло %+v (конфиг %+v),\nждали %+v (конфиг %+v)",
			mgr.created[0], mgr.created[0].WdttClient, want, want.WdttClient)
	}
}

func TestProxyInstancesCreate_DeclareFailed(t *testing.T) {
	mgr := &fakeProxyManager{
		seed:      manager.SeedInfo{Booted: true, Certified: true},
		createErr: errors.New("объявить выходы: NDMS отверг"),
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodPost, "/api/proxyrt/instances",
		`{"kind":"freeturn-client","name":"ft","config":{"peer":"1.2.3.4:5"}}`)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("код = %d, ждали 422: %s", rr.Code, rr.Body.String())
	}
	code, msg := decodeProxyErr(t, rr)
	if code != "PROXY_DECLARE_FAILED" {
		t.Fatalf("код ошибки = %q, ждали PROXY_DECLARE_FAILED", code)
	}
	if !strings.Contains(msg, "NDMS отверг") {
		t.Fatalf("тело без причины: %q", msg)
	}
	if len(mgr.records) != 0 {
		t.Fatalf("запись появилась вопреки отказу: %+v", mgr.records)
	}
}

func TestProxyInstancesCreate_NotSeeded(t *testing.T) {
	mgr := &fakeProxyManager{seed: manager.SeedInfo{Booted: false, Err: "посев отложен: RCI недоступен"}}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodPost, "/api/proxyrt/instances",
		`{"kind":"freeturn-client","name":"ft","config":{}}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("код = %d, ждали 503: %s", rr.Code, rr.Body.String())
	}
	code, msg := decodeProxyErr(t, rr)
	if code != "PROXY_NOT_SEEDED" {
		t.Fatalf("код ошибки = %q, ждали PROXY_NOT_SEEDED", code)
	}
	if !strings.Contains(msg, "RCI недоступен") {
		t.Fatalf("тело без причины: %q", msg)
	}
	if len(mgr.created) != 0 {
		t.Fatalf("Create позван при незавершённом посеве")
	}
}

func TestProxyInstancesCreate_InternetOnlyWithoutWAN(t *testing.T) {
	mgr := &fakeProxyManager{seed: manager.SeedInfo{Booted: true, Certified: true}}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodPost, "/api/proxyrt/instances",
		`{"id":"default","kind":"wdtt-server","name":"s","config":{"listen":"0.0.0.0:56000","natMode":"internet-only"}}`)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("код = %d, ждали 422: %s", rr.Code, rr.Body.String())
	}
	if code, _ := decodeProxyErr(t, rr); code != "PROXY_CONFIG_INVALID" {
		t.Fatalf("код ошибки = %q, ждали PROXY_CONFIG_INVALID", code)
	}
	if len(mgr.created) != 0 {
		t.Fatalf("Create позван вопреки отказу")
	}
}

func TestProxyInstancesCreate_OpkgTunUnsupported(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"сервер", `{"id":"default","kind":"wdtt-server","name":"s","config":{"listen":"0.0.0.0:56000"}}`},
		{"raw-клиент", `{"id":"nl","kind":"wdtt-client","name":"c","config":{"connMode":"raw"}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mgr := &fakeProxyManager{seed: manager.SeedInfo{Booted: true, Certified: true}}
			h := NewProxyInstancesHandler(ProxyInstancesDeps{
				Manager:          mgr,
				States:           fakeProxyStates{},
				OpkgTunSupported: func() bool { return false },
			})
			rr := doProxy(t, h, http.MethodPost, "/api/proxyrt/instances", c.body)
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("код = %d, ждали 422: %s", rr.Code, rr.Body.String())
			}
			if code, _ := decodeProxyErr(t, rr); code != "PROXY_OPKGTUN_UNSUPPORTED" {
				t.Fatalf("код ошибки = %q, ждали PROXY_OPKGTUN_UNSUPPORTED", code)
			}
			if len(mgr.created) != 0 {
				t.Fatalf("Create позван вопреки отказу")
			}
		})
	}
}

func TestProxyInstancesCreate_WgClientPassesOpkgGate(t *testing.T) {
	mgr := &fakeProxyManager{seed: manager.SeedInfo{Booted: true, Certified: true}}
	h := NewProxyInstancesHandler(ProxyInstancesDeps{
		Manager:          mgr,
		States:           fakeProxyStates{},
		OpkgTunSupported: func() bool { return false },
	})
	rr := doProxy(t, h, http.MethodPost, "/api/proxyrt/instances",
		`{"id":"nl","kind":"wdtt-client","name":"c","config":{"connMode":"wg"}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("код = %d, ждали 200: wg-клиент OpkgTun не требует", rr.Code)
	}
}

// ── правка ───────────────────────────────────────────────────────

func TestProxyInstancesPatch_EnabledWakesWorker(t *testing.T) {
	rec := fullServerRecord()
	rec.Enabled = false
	mgr := &fakeProxyManager{
		records: []instancestore.Record{rec},
		seed:    manager.SeedInfo{Booted: true, Certified: true},
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})

	rr := doProxy(t, h, http.MethodPatch, "/api/proxyrt/instances/wdtt-server:default", `{"enabled":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("код = %d, ждали 200: %s", rr.Code, rr.Body.String())
	}
	wantEnabled := []proxyEnabledCall{{Key: "wdtt-server:default", On: true}}
	if !reflect.DeepEqual(mgr.enabled, wantEnabled) {
		t.Fatalf("SetEnabled = %+v, ждали %+v", mgr.enabled, wantEnabled)
	}
	wantPosts := []proxyPostCall{{Key: "wdtt-server:default", Kind: proxyrt.EventIntentChanged}}
	if !reflect.DeepEqual(mgr.posts, wantPosts) {
		t.Fatalf("будильники = %+v, ждали %+v", mgr.posts, wantPosts)
	}
	if len(mgr.mutated) != 1 || !mgr.mutated[0].Enabled {
		t.Fatalf("запись не включена: %+v", mgr.mutated)
	}
}

func TestProxyInstancesPatch_NameKeepsEverythingElse(t *testing.T) {
	mgr := &fakeProxyManager{
		records: []instancestore.Record{fullServerRecord()},
		seed:    manager.SeedInfo{Booted: true, Certified: true},
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})

	rr := doProxy(t, h, http.MethodPatch, "/api/proxyrt/instances/wdtt-server:default", `{"name":"Новое имя"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("код = %d, ждали 200: %s", rr.Code, rr.Body.String())
	}
	if len(mgr.mutated) != 1 {
		t.Fatalf("Update вызван %d раз", len(mgr.mutated))
	}
	want := fullServerRecord()
	want.Name = "Новое имя"
	if !reflect.DeepEqual(mgr.mutated[0], want) {
		t.Fatalf("запись после PATCH = %+v (конфиг %+v),\nждали %+v (конфиг %+v)",
			mgr.mutated[0], mgr.mutated[0].WdttServer, want, want.WdttServer)
	}
}

func TestProxyInstancesPatch_SecretSemantics(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantPass string
	}{
		{"поле не прислали", `{"config":{"natMode":"full"}}`, "s-secret"},
		{"прислали пустое", `{"config":{"password":""}}`, "s-secret"},
		{"прислали пробелы", `{"config":{"password":"   "}}`, "s-secret"},
		{"прислали новое", `{"config":{"password":"новый"}}`, "новый"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mgr := &fakeProxyManager{
				records: []instancestore.Record{fullServerRecord()},
				seed:    manager.SeedInfo{Booted: true, Certified: true},
			}
			h := newProxyHandler(t, mgr, fakeProxyStates{})
			rr := doProxy(t, h, http.MethodPatch, "/api/proxyrt/instances/wdtt-server:default", c.body)
			if rr.Code != http.StatusOK {
				t.Fatalf("код = %d, ждали 200: %s", rr.Code, rr.Body.String())
			}
			if len(mgr.mutated) != 1 {
				t.Fatalf("Update вызван %d раз", len(mgr.mutated))
			}
			got := mgr.mutated[0].WdttServer
			if got.Password != c.wantPass {
				t.Errorf("password = %q, ждали %q", got.Password, c.wantPass)
			}
		})
	}
}

func TestProxyInstancesPatch_ConfigAppliedInPlace(t *testing.T) {
	mgr := &fakeProxyManager{
		records: []instancestore.Record{fullServerRecord()},
		seed:    manager.SeedInfo{Booted: true, Certified: true},
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodPatch, "/api/proxyrt/instances/wdtt-server:default",
		`{"config":{"policy":"Policy2"}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("код = %d, ждали 200: %s", rr.Code, rr.Body.String())
	}
	want := fullServerRecord()
	want.WdttServer.Policy = "Policy2"
	if !reflect.DeepEqual(mgr.mutated[0], want) {
		t.Fatalf("запись = %+v (конфиг %+v),\nждали %+v (конфиг %+v)",
			mgr.mutated[0], mgr.mutated[0].WdttServer, want, want.WdttServer)
	}
}

func TestProxyInstancesPatch_GateRefusesInternetOnly(t *testing.T) {
	mgr := &fakeProxyManager{
		records: []instancestore.Record{fullServerRecord()},
		seed:    manager.SeedInfo{Booted: true, Certified: true},
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodPatch, "/api/proxyrt/instances/wdtt-server:default",
		`{"config":{"natMode":"internet-only","natStaticWan":""}}`)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("код = %d, ждали 422: %s", rr.Code, rr.Body.String())
	}
	if code, _ := decodeProxyErr(t, rr); code != "PROXY_CONFIG_INVALID" {
		t.Fatalf("код ошибки = %q, ждали PROXY_CONFIG_INVALID", code)
	}
}

func TestProxyInstancesPatch_NotFoundAndNotSeeded(t *testing.T) {
	mgr := &fakeProxyManager{
		records: []instancestore.Record{fullServerRecord()},
		seed:    manager.SeedInfo{Booted: true, Certified: true},
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodPatch, "/api/proxyrt/instances/wdtt-client:none", `{"enabled":true}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("код = %d, ждали 404", rr.Code)
	}
	if len(mgr.enabled) != 0 {
		t.Fatalf("SetEnabled позван для несуществующего инстанса")
	}

	mgr.seed = manager.SeedInfo{Booted: false, Err: "посев не прошёл"}
	rr = doProxy(t, h, http.MethodPatch, "/api/proxyrt/instances/wdtt-server:default", `{"enabled":true}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("код = %d, ждали 503", rr.Code)
	}
	if code, _ := decodeProxyErr(t, rr); code != "PROXY_NOT_SEEDED" {
		t.Fatalf("код ошибки = %q, ждали PROXY_NOT_SEEDED", code)
	}
}

func TestProxyInstancesPatch_UpdateFailed(t *testing.T) {
	mgr := &fakeProxyManager{
		records:   []instancestore.Record{fullServerRecord()},
		seed:      manager.SeedInfo{Booted: true, Certified: true},
		updateErr: errors.New("объявить выходы: зеркало не записано"),
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodPatch, "/api/proxyrt/instances/wdtt-server:default", `{"name":"x"}`)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("код = %d, ждали 422: %s", rr.Code, rr.Body.String())
	}
	code, msg := decodeProxyErr(t, rr)
	if code != "PROXY_DECLARE_FAILED" {
		t.Fatalf("код ошибки = %q, ждали PROXY_DECLARE_FAILED", code)
	}
	if !strings.Contains(msg, "зеркало не записано") {
		t.Fatalf("тело без причины: %q", msg)
	}
}

// ── удаление и apply ─────────────────────────────────────────────

func TestProxyInstancesDelete(t *testing.T) {
	mgr := &fakeProxyManager{
		records: []instancestore.Record{fullServerRecord()},
		seed:    manager.SeedInfo{Booted: true, Certified: true},
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodDelete, "/api/proxyrt/instances/wdtt-server:default", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("код = %d, ждали 200: %s", rr.Code, rr.Body.String())
	}
	if !reflect.DeepEqual(mgr.deleted, []string{"wdtt-server:default"}) {
		t.Fatalf("Delete = %+v", mgr.deleted)
	}

	rr = doProxy(t, h, http.MethodDelete, "/api/proxyrt/instances/wdtt-server:default", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("повторное удаление: код = %d, ждали 404", rr.Code)
	}
}

func TestProxyInstancesDelete_Failed(t *testing.T) {
	mgr := &fakeProxyManager{
		records:   []instancestore.Record{fullServerRecord()},
		seed:      manager.SeedInfo{Booted: true, Certified: true},
		deleteErr: errors.New("реестр отверг ведомость"),
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodDelete, "/api/proxyrt/instances/wdtt-server:default", "")
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("код = %d, ждали 422", rr.Code)
	}
	if code, msg := decodeProxyErr(t, rr); code != "PROXY_DECLARE_FAILED" || !strings.Contains(msg, "реестр отверг") {
		t.Fatalf("ошибка = %q / %q", code, msg)
	}
}

func TestProxyInstancesApply(t *testing.T) {
	mgr := &fakeProxyManager{
		records: []instancestore.Record{fullServerRecord()},
		seed:    manager.SeedInfo{Booted: true, Certified: true},
		postOK:  true,
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodPost, "/api/proxyrt/instances/wdtt-server:default/apply", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("код = %d, ждали 200: %s", rr.Code, rr.Body.String())
	}
	want := []proxyPostCall{{Key: "wdtt-server:default", Kind: proxyrt.EventIntentChanged}}
	if !reflect.DeepEqual(mgr.posts, want) {
		t.Fatalf("будильники = %+v, ждали %+v", mgr.posts, want)
	}
}

func TestProxyInstancesApply_NoLiveInstance(t *testing.T) {
	mgr := &fakeProxyManager{
		records: []instancestore.Record{fullServerRecord()},
		seed:    manager.SeedInfo{Booted: true, Certified: true},
		postOK:  false,
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodPost, "/api/proxyrt/instances/wdtt-server:default/apply", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("код = %d, ждали 404 (немого «ок» быть не должно): %s", rr.Code, rr.Body.String())
	}
}

// ── процесс ──────────────────────────────────────────────────────

func TestProxyInstancesProcess_NoSnapshot(t *testing.T) {
	rec := fullServerRecord()
	rec.ID = "other"
	mgr := &fakeProxyManager{
		records: []instancestore.Record{rec},
		seed:    manager.SeedInfo{Booted: true, Certified: true},
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodGet, "/api/proxyrt/instances/wdtt-server:other", "")
	var got ProxyRtInstanceView
	decodeProxyData(t, rr, &got)
	want := ProcessView{
		Running:       false,
		Log:           "хвост журнала wdtt-server:other",
		Binary:        "/opt/bin/wdtt-server",
		BinaryPresent: true,
	}
	if !reflect.DeepEqual(got.Process, want) {
		t.Fatalf("process = %+v, ждали %+v", got.Process, want)
	}
}

func TestProxyInstancesMethodNotAllowed(t *testing.T) {
	mgr := &fakeProxyManager{seed: manager.SeedInfo{Booted: true, Certified: true}}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodPut, "/api/proxyrt/instances", "")
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("код = %d, ждали 405", rr.Code)
	}
}

// ── фикс-раунд 1 ─────────────────────────────────────────────────

// I1: присланный срез структур заменяет старый ЦЕЛИКОМ. Слияние поэлементно
// протащило бы order:0 старого permit'а (САМЫЙ ВЕРХ политики) на новое имя.
func TestProxyInstancesPatch_PolicySliceReplacedWholesale(t *testing.T) {
	mgr := &fakeProxyManager{
		records: []instancestore.Record{fullClientRecord()},
		seed:    manager.SeedInfo{Booted: true, Certified: true},
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodPatch, "/api/proxyrt/instances/wdtt-client:nl",
		`{"config":{"policies":[{"name":"B"}]}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("код = %d, ждали 200: %s", rr.Code, rr.Body.String())
	}
	if len(mgr.mutated) != 1 {
		t.Fatalf("Update вызван %d раз", len(mgr.mutated))
	}
	got := mgr.mutated[0].WdttClient.Policies
	want := []roles.PolicyPermit{{Name: "B"}}
	if !reflect.DeepEqual(got, want) {
		var order any = "nil"
		if len(got) > 0 && got[0].Order != nil {
			order = *got[0].Order
		}
		t.Fatalf("policies = %+v (order %v), ждали %+v (order nil): позиционный пин старого permit'а уехал на новое имя",
			got, order, want)
	}
}

// I1 (контроль): срез строк тоже заменяется целиком, а не дополняется.
func TestProxyInstancesPatch_StringSliceReplacedWholesale(t *testing.T) {
	mgr := &fakeProxyManager{
		records: []instancestore.Record{fullServerRecord()},
		seed:    manager.SeedInfo{Booted: true, Certified: true},
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodPatch, "/api/proxyrt/instances/wdtt-server:default",
		`{"config":{"lanSegments":["10.0.0.0/8","172.16.0.0/12"]}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("код = %d, ждали 200: %s", rr.Code, rr.Body.String())
	}
	want := []string{"10.0.0.0/8", "172.16.0.0/12"}
	if !reflect.DeepEqual(mgr.mutated[0].WdttServer.LanSegments, want) {
		t.Fatalf("lanSegments = %+v, ждали %+v", mgr.mutated[0].WdttServer.LanSegments, want)
	}
}

// I2: ответ POST — отдельный путь кода (respondRecord), и он обязан маскировать
// секреты так же, как список.
func TestProxyInstancesCreate_ResponseMasksSecrets(t *testing.T) {
	mgr := &fakeProxyManager{seed: manager.SeedInfo{Booted: true, Certified: true}}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodPost, "/api/proxyrt/instances",
		`{"id":"nl","kind":"wdtt-client","name":"c","config":{"connMode":"wg","password":"секрет-создания","vkHashes":"vk"}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("код = %d, ждали 200: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "секрет-создания") {
		t.Fatalf("пароль уехал в ответе на создание: %s", rr.Body.String())
	}
	var got ProxyRtInstanceView
	decodeProxyData(t, rr, &got)
	if _, ok := got.Config["password"]; ok {
		t.Errorf("password присутствует в конфиге ответа на создание")
	}
	if got.Config["passwordSet"] != true {
		t.Errorf("passwordSet = %v, ждали true", got.Config["passwordSet"])
	}
}

// I2: то же для ответа PATCH — и заодно проверка, что пароли абонентов не
// уезжают ни одним путём.
func TestProxyInstancesPatch_ResponseMasksSecrets(t *testing.T) {
	mgr := &fakeProxyManager{
		records: []instancestore.Record{fullServerRecord()},
		seed:    manager.SeedInfo{Booted: true, Certified: true},
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodPatch, "/api/proxyrt/instances/wdtt-server:default", `{"name":"Новое"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("код = %d, ждали 200: %s", rr.Code, rr.Body.String())
	}
	for _, secret := range []string{"s-secret", "bot-secret", "u-secret"} {
		if strings.Contains(rr.Body.String(), secret) {
			t.Fatalf("секрет %q уехал в ответе на правку: %s", secret, rr.Body.String())
		}
	}
	var got ProxyRtInstanceView
	decodeProxyData(t, rr, &got)
	if _, ok := got.Config["password"]; ok {
		t.Errorf("password присутствует в конфиге ответа на правку")
	}
	if got.Config["passwordSet"] != true {
		t.Errorf("признаки секретов потеряны: %+v", got.Config)
	}
}

// Абоненты сервера из этой поверхности не отдаются вовсе: урезанный блок —
// приманка для сборки таблицы, в которой истёкшие выглядят живыми.
func TestProxyInstances_NoUsersBlock(t *testing.T) {
	mgr := &fakeProxyManager{
		records: []instancestore.Record{fullServerRecord()},
		seed:    manager.SeedInfo{Booted: true, Certified: true},
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	for _, path := range []string{"/api/proxyrt/instances", "/api/proxyrt/instances/wdtt-server:default"} {
		rr := doProxy(t, h, http.MethodGet, path, "")
		if strings.Contains(rr.Body.String(), `"users"`) {
			t.Fatalf("%s отдал блок абонентов: %s", path, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "Ноут") || strings.Contains(rr.Body.String(), "vh1") {
			t.Fatalf("%s отдал данные абонента: %s", path, rr.Body.String())
		}
	}
}

// I3: снимок есть, но процесс не бежит (PID нулевой) — running обязан быть
// ложным. Снимок отдаётся последний известный, поэтому вектор реальный.
func TestProxyInstancesProcess_SnapshotWithoutPID(t *testing.T) {
	mgr := &fakeProxyManager{
		records: []instancestore.Record{fullServerRecord()},
		seed:    manager.SeedInfo{Booted: true, Certified: true},
	}
	h := NewProxyInstancesHandler(ProxyInstancesDeps{
		Manager: mgr,
		States:  fakeProxyStates{},
		Snapshot: func(string) (awgmproto.State, bool) {
			return awgmproto.State{Role: "server", PID: 0, LastError: "процесс вышел"}, true
		},
	})
	rr := doProxy(t, h, http.MethodGet, "/api/proxyrt/instances/wdtt-server:default", "")
	var got ProxyRtInstanceView
	decodeProxyData(t, rr, &got)
	want := ProcessView{Running: false, LastError: "процесс вышел"}
	if !reflect.DeepEqual(got.Process, want) {
		t.Fatalf("process = %+v, ждали %+v (снимок есть, но PID нулевой)", got.Process, want)
	}
}

// Гейт OpkgTun на ПЕРЕКЛЮЧЕНИИ режима: заявленное отступление от брифа
// («создание») закрывается своим кейсом.
func TestProxyInstancesPatch_OpkgTunGateOnModeSwitch(t *testing.T) {
	mgr := &fakeProxyManager{
		records: []instancestore.Record{fullClientRecord()},
		seed:    manager.SeedInfo{Booted: true, Certified: true},
	}
	h := NewProxyInstancesHandler(ProxyInstancesDeps{
		Manager:          mgr,
		States:           fakeProxyStates{},
		OpkgTunSupported: func() bool { return false },
	})
	rr := doProxy(t, h, http.MethodPatch, "/api/proxyrt/instances/wdtt-client:nl",
		`{"config":{"connMode":"raw"}}`)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("код = %d, ждали 422: %s", rr.Code, rr.Body.String())
	}
	if code, _ := decodeProxyErr(t, rr); code != "PROXY_OPKGTUN_UNSUPPORTED" {
		t.Fatalf("код ошибки = %q, ждали PROXY_OPKGTUN_UNSUPPORTED", code)
	}
	if len(mgr.mutated) != 0 {
		t.Fatalf("запись изменена вопреки отказу: %+v", mgr.mutated)
	}
}

// TestProxyInstancesPatch_RecordFieldsSemantics — sub и statsLog живут на
// ЗАПИСИ, а не в конфиге роли, и семантика у них НЕ секретная: пустая строка
// это законное значение («подписки больше нет», «режим журнала по
// умолчанию»), а «не менять» означает только отсутствие поля в теле.
func TestProxyInstancesPatch_RecordFieldsSemantics(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantSub  string
		wantStat string
	}{
		{"поля не прислали", `{"name":"Раздача"}`, "https://sub.example/s1", "/opt/var/stat.log"},
		{"прислали новые", `{"sub":"https://sub.example/s2","statsLog":"disk"}`, "https://sub.example/s2", "disk"},
		{"прислали пустые", `{"sub":"","statsLog":""}`, "", ""},
		{"пробелы обрезаются", `{"sub":"  https://s3  ","statsLog":"  off  "}`, "https://s3", "off"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mgr := &fakeProxyManager{
				records: []instancestore.Record{fullServerRecord()},
				seed:    manager.SeedInfo{Booted: true, Certified: true},
			}
			h := newProxyHandler(t, mgr, fakeProxyStates{})
			rr := doProxy(t, h, http.MethodPatch, "/api/proxyrt/instances/wdtt-server:default", c.body)
			if rr.Code != http.StatusOK {
				t.Fatalf("код = %d, ждали 200: %s", rr.Code, rr.Body.String())
			}
			if len(mgr.mutated) != 1 {
				t.Fatalf("Update вызван %d раз", len(mgr.mutated))
			}
			if got := mgr.mutated[0].Sub; got != c.wantSub {
				t.Errorf("sub = %q, ждали %q", got, c.wantSub)
			}
			if got := mgr.mutated[0].StatsLog; got != c.wantStat {
				t.Errorf("statsLog = %q, ждали %q", got, c.wantStat)
			}
		})
	}
}

// TestProxyInstancesPatch_SubKeepsEverythingElse — правка подписки трогает
// ТОЛЬКО подписку: пересборка записи литералом потеряла бы абонентов, память
// ссылки и слоты адресов.
func TestProxyInstancesPatch_SubKeepsEverythingElse(t *testing.T) {
	mgr := &fakeProxyManager{
		records: []instancestore.Record{fullClientRecord()},
		seed:    manager.SeedInfo{Booted: true, Certified: true},
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})

	rr := doProxy(t, h, http.MethodPatch, "/api/proxyrt/instances/wdtt-client:nl",
		`{"sub":"https://sub.example/c2"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("код = %d, ждали 200: %s", rr.Code, rr.Body.String())
	}
	want := fullClientRecord()
	want.Sub = "https://sub.example/c2"
	if !reflect.DeepEqual(mgr.mutated[0], want) {
		t.Fatalf("запись после PATCH = %+v (конфиг %+v),\nждали %+v (конфиг %+v)",
			mgr.mutated[0], mgr.mutated[0].WdttClient, want, want.WdttClient)
	}
}

// TestProxyInstancesPatch_RecordFieldsWithIntent — тело, где рядом с
// намерением едет поле записи, обязано идти ПОЛНЫМ путём правки. Быстрая
// ветка «только намерение» зовёт SetEnabled и записи не трогает: попади туда
// такое тело — подписка потерялась бы молча.
func TestProxyInstancesPatch_RecordFieldsWithIntent(t *testing.T) {
	for _, c := range []struct {
		name string
		body string
		want instancestore.Record
	}{
		{"подписка рядом с намерением", `{"enabled":false,"sub":"https://sub.example/c2"}`,
			func() instancestore.Record {
				r := fullClientRecord()
				r.Enabled, r.Sub = false, "https://sub.example/c2"
				return r
			}()},
		{"режим журнала рядом с намерением", `{"enabled":false,"statsLog":"off"}`,
			func() instancestore.Record {
				r := fullClientRecord()
				r.Enabled, r.StatsLog = false, "off"
				return r
			}()},
	} {
		t.Run(c.name, func(t *testing.T) {
			mgr := &fakeProxyManager{
				records: []instancestore.Record{fullClientRecord()},
				seed:    manager.SeedInfo{Booted: true, Certified: true},
			}
			h := newProxyHandler(t, mgr, fakeProxyStates{})
			rr := doProxy(t, h, http.MethodPatch, "/api/proxyrt/instances/wdtt-client:nl", c.body)
			if rr.Code != http.StatusOK {
				t.Fatalf("код = %d, ждали 200: %s", rr.Code, rr.Body.String())
			}
			if len(mgr.enabled) != 0 {
				t.Fatalf("тело с полем записи ушло в быструю ветку намерения: %+v", mgr.enabled)
			}
			if len(mgr.mutated) != 1 || !reflect.DeepEqual(mgr.mutated[0], c.want) {
				t.Fatalf("запись после PATCH = %+v,\nждали %+v", mgr.mutated, c.want)
			}
		})
	}
}

// TestProxyInstancesKeyPercentEscaped — фронт адресует инстанс
// encodeURIComponent'ом, и двоеточие ключа уезжает как %3A, а сам бэкенд
// строит свои ссылки через url.PathEscape, который двоеточие НЕ трогает
// (captcha/status.go:151). Формы две, поверхность обязана принимать обе:
// net/http декодирует путь до разбора хвоста.
func TestProxyInstancesKeyPercentEscaped(t *testing.T) {
	for _, path := range []string{
		"/api/proxyrt/instances/wdtt-server:default",
		"/api/proxyrt/instances/wdtt-server%3Adefault",
	} {
		t.Run(path, func(t *testing.T) {
			mgr := &fakeProxyManager{
				records: []instancestore.Record{fullServerRecord()},
				seed:    manager.SeedInfo{Booted: true, Certified: true},
			}
			h := newProxyHandler(t, mgr, fakeProxyStates{})
			rr := doProxy(t, h, http.MethodGet, path, "")
			if rr.Code != http.StatusOK {
				t.Fatalf("код = %d, ждали 200: %s", rr.Code, rr.Body.String())
			}
			var v ProxyRtInstanceView
			decodeProxyData(t, rr, &v)
			if v.Key != "wdtt-server:default" {
				t.Fatalf("key = %q, ждали wdtt-server:default", v.Key)
			}
		})
	}
}

// Гейт считается ДО записи, на сыром конфиге из тела запроса, а режим store
// приводит потом (instancestore/store.go:253-256). Сырое сравнение пропускало
// бы "RAW" и " raw ": гейт молчит, store делает raw, и на прошивке без
// поддержки OpkgTun появляется клиент, которому интерфейс выделить нечем.
func TestProxyInstancesPatch_OpkgTunGateNormalizesMode(t *testing.T) {
	for _, mode := range []string{"RAW", " raw ", "Raw"} {
		t.Run(mode, func(t *testing.T) {
			mgr := &fakeProxyManager{
				records: []instancestore.Record{fullClientRecord()},
				seed:    manager.SeedInfo{Booted: true, Certified: true},
			}
			h := NewProxyInstancesHandler(ProxyInstancesDeps{
				Manager:          mgr,
				States:           fakeProxyStates{},
				OpkgTunSupported: func() bool { return false },
			})
			rr := doProxy(t, h, http.MethodPatch, "/api/proxyrt/instances/wdtt-client:nl",
				`{"config":{"connMode":"`+mode+`"}}`)
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("режим %q обошёл гейт: код = %d, ждали 422: %s", mode, rr.Code, rr.Body.String())
			}
			if code, _ := decodeProxyErr(t, rr); code != "PROXY_OPKGTUN_UNSUPPORTED" {
				t.Fatalf("код ошибки = %q, ждали PROXY_OPKGTUN_UNSUPPORTED", code)
			}
			if len(mgr.mutated) != 0 {
				t.Fatalf("запись изменена вопреки отказу: %+v", mgr.mutated)
			}
		})
	}
}

// Уведомление о переезде listen-порта снимается признанием. Без ручки оно
// висело вечно: посев не повторяется, переписать отметку на диске некому, и
// плашка оставалась на экране навсегда — в том числе про инстанс, которого
// пользователь уже удалил.
func TestProxyListenMoves_Ack(t *testing.T) {
	t.Run("DELETE снимает уведомления", func(t *testing.T) {
		mgr := &fakeProxyManager{seed: manager.SeedInfo{Booted: true, Certified: true,
			MovedListen: []instancestore.ListenMove{{
				Instance: "freeturn-client:default", Name: "Клиент",
				From: "127.0.0.1:9000", To: "127.0.0.1:9001"}}}}
		h := newProxyHandler(t, mgr, fakeProxyStates{})

		rr := httptest.NewRecorder()
		h.AckListenMoves(rr, httptest.NewRequest(http.MethodDelete, proxyrtListenMovesPath, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("код %d, тело %s", rr.Code, rr.Body.String())
		}
		if mgr.acked != 1 {
			t.Errorf("признание не доехало до менеджера: acked=%d", mgr.acked)
		}

		// Список после признания молчит о переездах — иначе плашка вернулась бы
		// на следующем же опросе статуса.
		var data ProxyRtListData
		decodeProxyData(t, doProxy(t, h, http.MethodGet, proxyrtInstancesPath, ""), &data)
		if len(data.Seed.MovedListen) != 0 {
			t.Errorf("после признания переезды всё ещё в выдаче: %+v", data.Seed.MovedListen)
		}
	})

	t.Run("отказ менеджера — не 200", func(t *testing.T) {
		mgr := &fakeProxyManager{ackErr: errors.New("диск только на чтение")}
		h := newProxyHandler(t, mgr, fakeProxyStates{})
		rr := httptest.NewRecorder()
		h.AckListenMoves(rr, httptest.NewRequest(http.MethodDelete, proxyrtListenMovesPath, nil))
		if rr.Code == http.StatusOK {
			t.Errorf("отказ записи выдан за успех: %s", rr.Body.String())
		}
	})

	t.Run("чужой метод отвергается", func(t *testing.T) {
		mgr := &fakeProxyManager{}
		h := newProxyHandler(t, mgr, fakeProxyStates{})
		rr := httptest.NewRecorder()
		h.AckListenMoves(rr, httptest.NewRequest(http.MethodGet, proxyrtListenMovesPath, nil))
		if rr.Code == http.StatusOK {
			t.Errorf("GET принят за признание: %s", rr.Body.String())
		}
		if mgr.acked != 0 {
			t.Error("GET дошёл до менеджера")
		}
	})
}

// F59: старый движок после PR #750 писал СПИСОК выходов static-NAT и явно
// чистил legacy-одиночку («источник правды теперь список», 722cda888), а
// посев переносит обе формы как есть. Такая запись валидна для рантайма
// (roles.Validate читает StaticNATList), но gateCheck смотрел только на
// одиночку — и любой PATCH, включая переименование, отбивался 422.
//
// Краснеет на мутации «вернуть в gateCheck проверку c.NatStaticWAN».
func TestProxyInstancesPatch_GateAcceptsStaticNATList(t *testing.T) {
	rec := fullServerRecord()
	rec.WdttServer.NatMode = "internet-only"
	rec.WdttServer.NatStaticWAN = ""                       // так его оставил старый движок
	rec.WdttServer.NatStaticWANs = []string{"ISP", "ISP2"} // источник правды

	mgr := &fakeProxyManager{
		records: []instancestore.Record{rec},
		seed:    manager.SeedInfo{Booted: true, Certified: true},
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodPatch, "/api/proxyrt/instances/wdtt-server:default",
		`{"name":"Новое имя"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("код = %d, ждали 200: %s", rr.Code, rr.Body.String())
	}
}

// Продолжение F59: фронт говорит только на одиночку natStaticWan. Если при её
// присылке оставить список от старого движка, StaticNATList продолжит
// предпочитать список — выбор WAN в интерфейсе молча не вступит в силу.
//
// Краснеет на мутации «убрать обнуление NatStaticWANs в proxyApplyConfig».
func TestProxyInstancesPatch_SingularStaticWANReplacesList(t *testing.T) {
	rec := fullServerRecord()
	rec.WdttServer.NatMode = "internet-only"
	rec.WdttServer.NatStaticWAN = ""
	rec.WdttServer.NatStaticWANs = []string{"ISP", "ISP2"}

	mgr := &fakeProxyManager{
		records: []instancestore.Record{rec},
		seed:    manager.SeedInfo{Booted: true, Certified: true},
	}
	h := newProxyHandler(t, mgr, fakeProxyStates{})
	rr := doProxy(t, h, http.MethodPatch, "/api/proxyrt/instances/wdtt-server:default",
		`{"config":{"natStaticWan":"ISP3"}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("код = %d, ждали 200: %s", rr.Code, rr.Body.String())
	}
	if len(mgr.mutated) == 0 {
		t.Fatal("запись не сохранена")
	}
	got := mgr.mutated[len(mgr.mutated)-1].WdttServer
	if list := got.StaticNATList(); len(list) != 1 || list[0] != "ISP3" {
		t.Fatalf("выбор WAN не вступил в силу: StaticNATList=%v (NatStaticWANs=%v)", list, got.NatStaticWANs)
	}
}
