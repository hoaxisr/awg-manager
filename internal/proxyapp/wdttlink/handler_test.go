package wdttlink

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ── фейки ────────────────────────────────────────────────────────

type fakeSource struct {
	recs map[string]instancestore.Record
}

func (f *fakeSource) Get(key string) (instancestore.Record, bool) {
	r, ok := f.recs[key]
	return r, ok
}

type updateCall struct {
	Key string
	Rec instancestore.Record // запись ПОСЛЕ прогона мутатора
}

// fakeMutator прогоняет мутатор по хранимой записи и сохраняет результат
// целиком: тест сверяет СОСТАВ аргумента, а не факт вызова.
type fakeMutator struct {
	src     *fakeSource
	created []instancestore.Record
	updates []updateCall
}

func (f *fakeMutator) Create(_ context.Context, rec instancestore.Record) error {
	f.created = append(f.created, rec)
	if f.src != nil {
		f.src.recs[rec.Key()] = rec
	}
	return nil
}

func (f *fakeMutator) Update(_ context.Context, key string, mutate func(*instancestore.Record) error) error {
	rec, ok := f.src.recs[key]
	if !ok {
		return fmt.Errorf("инстанс %s не найден", key)
	}
	if err := mutate(&rec); err != nil {
		return err
	}
	f.src.recs[key] = rec
	f.updates = append(f.updates, updateCall{Key: key, Rec: rec})
	return nil
}

type fakeCleaner struct {
	gotClientIDs []string
	deleted      []string
	errs         []string
}

func (f *fakeCleaner) DeleteLinked(_ context.Context, clientID string) ([]string, []string) {
	f.gotClientIDs = append(f.gotClientIDs, clientID)
	return f.deleted, f.errs
}

type importCall struct{ Conf, Name string }

type fakeTunnels struct {
	tunnels   []storage.AWGTunnel
	imports   []importCall
	started   []string
	deleted   []string
	saved     []storage.AWGTunnel
	nextID    string
	forgotten []string
	published int
}

func (f *fakeTunnels) List() ([]storage.AWGTunnel, error) {
	out := make([]storage.AWGTunnel, len(f.tunnels))
	copy(out, f.tunnels)
	return out, nil
}

func (f *fakeTunnels) Get(id string) (*storage.AWGTunnel, error) {
	for i := range f.tunnels {
		if f.tunnels[i].ID == id {
			t := f.tunnels[i]
			return &t, nil
		}
	}
	return nil, fmt.Errorf("туннель %s не найден", id)
}

func (f *fakeTunnels) Save(t *storage.AWGTunnel) error {
	f.saved = append(f.saved, *t)
	for i := range f.tunnels {
		if f.tunnels[i].ID == t.ID {
			f.tunnels[i] = *t
			return nil
		}
	}
	f.tunnels = append(f.tunnels, *t)
	return nil
}

func (f *fakeTunnels) Delete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	out := f.tunnels[:0]
	for _, t := range f.tunnels {
		if t.ID != id {
			out = append(out, t)
		}
	}
	f.tunnels = out
	return nil
}

func (f *fakeTunnels) Import(_ context.Context, conf, name string) (string, string, error) {
	f.imports = append(f.imports, importCall{Conf: conf, Name: name})
	id := f.nextID
	if id == "" {
		id = "t-new"
	}
	f.tunnels = append(f.tunnels, storage.AWGTunnel{ID: id, Name: name,
		Peer: storage.AWGPeer{PublicKey: ExtractPeerPublicKey(conf)}})
	return id, name, nil
}

func (f *fakeTunnels) Start(_ context.Context, id string) error {
	f.started = append(f.started, id)
	return nil
}

func (f *fakeTunnels) ForgetTraffic(id string) { f.forgotten = append(f.forgotten, id) }

func (f *fakeTunnels) PublishList(context.Context) { f.published++ }

// fakeVetting — предикат пригодности абонента: та же семантика, что у
// перенесённого passwords_json.go (задача 9 даёт прод-реализацию).
type fakeVetting struct{}

func (fakeVetting) UnusableReason(u instancestore.ServerUser, main string, now time.Time) UnusableReason {
	switch {
	case strings.TrimSpace(u.Password) == "":
		return ReasonEmptyPassword
	case strings.TrimSpace(u.Password) == strings.TrimSpace(main):
		return ReasonMainPassword
	case u.ExpiresAt > 0 && now.Unix() >= u.ExpiresAt:
		return ReasonExpired
	}
	return ReasonUsable
}

func (v fakeVetting) UsableUsers(users []instancestore.ServerUser, main string, now time.Time) []instancestore.ServerUser {
	var out []instancestore.ServerUser
	for _, u := range users {
		if v.UnusableReason(u, main, now) == ReasonUsable {
			out = append(out, u)
		}
	}
	return out
}

// ── помощники ────────────────────────────────────────────────────

var testNow = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }

func decodeEnvelope(t *testing.T, rr *httptest.ResponseRecorder) (map[string]any, string, string) {
	t.Helper()
	var env struct {
		Success bool            `json:"success"`
		Error   bool            `json:"error"`
		Data    json.RawMessage `json:"data"`
		Message string          `json:"message"`
		Code    string          `json:"code"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("тело ответа не JSON: %v (%s)", err, rr.Body.String())
	}
	data := map[string]any{}
	if len(env.Data) > 0 {
		_ = json.Unmarshal(env.Data, &data)
	}
	return data, env.Message, env.Code
}

func post(t *testing.T, body string) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodPost, "/api/proxyrt/x", strings.NewReader(body))
}

// serverRecord — сервер с сохранёнными хешами ссылки: без них генерация
// отвергается (WDTT_LINK_NO_VK_HASHES), и тесты про режим, порт и запись
// падали бы не на своей причине. Запись БЕЗ хешей строит serverRecordNoHashes.
func serverRecord(cfg roles.WdttServerConfig, users ...instancestore.ServerUser) instancestore.Record {
	rec := serverRecordNoHashes(cfg, users...)
	rec.LinkVKHashes = "srv-hash-a,srv-hash-b"
	return rec
}

func serverRecordNoHashes(cfg roles.WdttServerConfig, users ...instancestore.ServerUser) instancestore.Record {
	return instancestore.Record{
		ID: "default", Kind: instancestore.KindWdttServer, Name: "Сервер",
		Enabled: true, Users: users, WdttServer: &cfg,
	}
}

func clientRecord(cfg roles.WdttClientConfig) instancestore.Record {
	return instancestore.Record{
		ID: "default", Kind: instancestore.KindWdttClient, Name: "Германия",
		Enabled: true, WdttClient: &cfg,
	}
}

func newTestHandler(t *testing.T, recs ...instancestore.Record) (*Handler, *fakeSource, *fakeMutator, *fakeCleaner, *fakeTunnels) {
	t.Helper()
	src := &fakeSource{recs: map[string]instancestore.Record{}}
	for _, r := range recs {
		src.recs[r.Key()] = r
	}
	mut := &fakeMutator{src: src}
	cleaner := &fakeCleaner{}
	tunnels := &fakeTunnels{}
	b := NewBuilder(BuilderDeps{
		Vetting: fakeVetting{},
		Mutator: mut,
		ExternalIP: func(context.Context) (string, error) {
			return "203.0.113.7", nil
		},
		Now: testNow,
	})
	h := NewHandler(Deps{
		Records: src, Mutator: mut, Tunnels: tunnels,
		Cleaners: map[instancestore.Kind]LinkedCleaner{instancestore.KindWdttClient: cleaner},
		Builders: map[instancestore.Kind]LinkBuilder{instancestore.KindWdttServer: b},
	})
	return h, src, mut, cleaner, tunnels
}

// ── (а) §11: режим ссылки ────────────────────────────────────────

func TestLink_ModeDecidesPort(t *testing.T) {
	user := instancestore.ServerUser{Password: "abonent"}
	cases := []struct {
		name      string
		relayMode string
		mode      string
		wantPort  string
	}{
		// raw-порт считается от DTLS+1 (EffectiveRawListen), wg-порт — из
		// -listen-direct, если он задан, иначе из -listen.
		{"явный raw поверх wg-сервера", ConnModeWG, ConnModeRaw, "56003"},
		{"явный wg поверх raw-сервера", ConnModeRaw, ConnModeWG, "56004"},
		{"пустой режим — по RelayMode записи (raw)", ConnModeRaw, "", "56003"},
		{"пустой режим — по RelayMode записи (wg)", ConnModeWG, "", "56004"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serverRecord(roles.WdttServerConfig{
				Listen: "0.0.0.0:56002", DirectListen: "0.0.0.0:56004",
				WgPort: 56001, Password: "main", RelayMode: tc.relayMode,
			}, user)
			h, _, _, _, _ := newTestHandler(t, rec)
			rr := httptest.NewRecorder()
			body := fmt.Sprintf(`{"peer":"1.2.3.4","password":"abonent","mode":%q}`, tc.mode)
			h.Link(rr, post(t, body), rec.Key())

			data, msg, code := decodeEnvelope(t, rr)
			if code != "" {
				t.Fatalf("отказ %s: %s", code, msg)
			}
			wantPeer := "1.2.3.4:" + tc.wantPort
			if data["peer"] != wantPeer {
				t.Fatalf("peer=%v want %q", data["peer"], wantPeer)
			}
			link, _ := data["link"].(string)
			if !strings.HasPrefix(link, "wdtt://1.2.3.4:"+tc.wantPort+":") {
				t.Fatalf("wdtt-ссылка не на порт %s: %q", tc.wantPort, link)
			}
			q, _ := data["linkQwdtt"].(string)
			if !strings.Contains(q, "peer=1.2.3.4%3A"+tc.wantPort) {
				t.Fatalf("qwdtt-ссылка не на порт %s: %q", tc.wantPort, q)
			}
			// Режим ссылки уезжает в qwdtt://: raw помечается явно, wg — нет.
			wantMode := normalizeConnMode(tc.mode)
			if tc.mode == "" {
				wantMode = normalizeConnMode(tc.relayMode)
			}
			if hasMode := strings.Contains(q, "mode=raw"); hasMode != (wantMode == ConnModeRaw) {
				t.Fatalf("mode=raw в qwdtt=%v при режиме %q: %q", hasMode, wantMode, q)
			}
		})
	}
}

func TestLink_PersistsPeerInPlace(t *testing.T) {
	rec := serverRecord(roles.WdttServerConfig{
		Listen: "0.0.0.0:56002", WgPort: 56001, Password: "main", RelayMode: ConnModeWG,
	}, instancestore.ServerUser{Password: "abonent", Comment: "Абонент 1"})
	rec.Sub = "https://sub.example/x"
	rec.StatsLog = "disk"
	rec.LinkVKHashes = "hh"
	h, _, mut, _, _ := newTestHandler(t, rec)

	rr := httptest.NewRecorder()
	h.Link(rr, post(t, `{"peer":"198.51.100.9","password":"abonent"}`), rec.Key())
	if _, msg, code := decodeEnvelope(t, rr); code != "" {
		t.Fatalf("отказ %s: %s", code, msg)
	}
	if len(mut.updates) != 1 {
		t.Fatalf("ожидалась одна правка записи, получено %d", len(mut.updates))
	}
	want := rec
	want.LinkPeer = "198.51.100.9:56002"
	if !reflect.DeepEqual(mut.updates[0].Rec, want) {
		t.Fatalf("запись после правки:\n%+v\nхотели:\n%+v", mut.updates[0].Rec, want)
	}
}

func TestLink_PeerFallbacks(t *testing.T) {
	base := roles.WdttServerConfig{Listen: "0.0.0.0:56002", WgPort: 56001,
		Password: "main", RelayMode: ConnModeWG}
	user := instancestore.ServerUser{Password: "abonent"}

	t.Run("адрес из записи", func(t *testing.T) {
		rec := serverRecord(base, user)
		rec.LinkPeer = "wan.example:56002"
		h, _, _, _, _ := newTestHandler(t, rec)
		rr := httptest.NewRecorder()
		h.Link(rr, post(t, `{"password":"abonent"}`), rec.Key())
		data, msg, code := decodeEnvelope(t, rr)
		if code != "" {
			t.Fatalf("отказ %s: %s", code, msg)
		}
		if data["peer"] != "wan.example:56002" {
			t.Fatalf("peer=%v", data["peer"])
		}
	})

	t.Run("внешний IP, когда адреса нет нигде", func(t *testing.T) {
		rec := serverRecord(base, user)
		h, _, _, _, _ := newTestHandler(t, rec)
		rr := httptest.NewRecorder()
		h.Link(rr, post(t, `{"password":"abonent"}`), rec.Key())
		data, msg, code := decodeEnvelope(t, rr)
		if code != "" {
			t.Fatalf("отказ %s: %s", code, msg)
		}
		if data["peer"] != "203.0.113.7:56002" {
			t.Fatalf("peer=%v", data["peer"])
		}
	})
}

// Тексты отказов — часть контракта: фронт показывает их пользователю дословно.
func TestLink_PasswordRejections(t *testing.T) {
	base := roles.WdttServerConfig{Listen: "0.0.0.0:56002", WgPort: 56001,
		Password: "main", RelayMode: ConnModeWG}
	expired := instancestore.ServerUser{Password: "old", ExpiresAt: testNow().Unix() - 1}
	good := instancestore.ServerUser{Password: "abonent"}

	cases := []struct {
		name  string
		users []instancestore.ServerUser
		body  string
		want  string
	}{
		{"нет рабочих абонентов", []instancestore.ServerUser{expired}, `{"password":"old"}`,
			"у сервера нет ни одного рабочего абонента: заведите абонента и повторите"},
		{"пароль не выбран", []instancestore.ServerUser{good}, `{}`,
			"выберите абонента: ссылка выдаётся на пароль абонента, а не на главный пароль сервера"},
		{"главный пароль", []instancestore.ServerUser{good}, `{"password":"main"}`,
			"это главный пароль сервера: он остаётся ключом администрирования, ссылка выдаётся на пароль абонента"},
		{"чужой пароль", []instancestore.ServerUser{good}, `{"password":"нетакой"}`,
			"пароль не принадлежит ни одному абоненту сервера"},
		{"просроченный абонент", []instancestore.ServerUser{good, expired}, `{"password":"old"}`,
			"абонент просрочен, ссылка не будет работать: заведите нового абонента"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serverRecord(base, tc.users...)
			h, _, _, _, _ := newTestHandler(t, rec)
			rr := httptest.NewRecorder()
			h.Link(rr, post(t, tc.body), rec.Key())
			_, msg, code := decodeEnvelope(t, rr)
			if code != "WDTT_LINK_NO_CLIENT" {
				t.Fatalf("код=%q сообщение=%q", code, msg)
			}
			if msg != tc.want {
				t.Fatalf("сообщение=%q\nхотели=%q", msg, tc.want)
			}
		})
	}
}

// Пароль владельца убран из UI, задать его негде — а ссылка выдаётся на пароль
// абонента и владельческий в себе не несёт. Стенд 2026-08-28: прежняя проверка
// на его непустоту делала раздачу неработающей — «Ссылка» отвечала 400.
func TestLink_ServerWithoutOwnerPassword(t *testing.T) {
	rec := serverRecord(
		roles.WdttServerConfig{Listen: "0.0.0.0:56002", WgPort: 56001, RelayMode: ConnModeWG},
		instancestore.ServerUser{Password: "abonent"},
	)
	rec.LinkPeer = "198.51.100.9"
	h, _, _, _, _ := newTestHandler(t, rec)
	rr := httptest.NewRecorder()
	h.Link(rr, post(t, `{"password":"abonent"}`), rec.Key())
	data, msg, code := decodeEnvelope(t, rr)
	if code != "" {
		t.Fatalf("отказ %s: %s", code, msg)
	}
	link, _ := data["link"].(string)
	if !strings.Contains(link, "abonent") {
		t.Fatalf("ссылка выдана не на пароль абонента: %q", link)
	}
}

func TestLink_UnknownInstance(t *testing.T) {
	h, _, _, _, _ := newTestHandler(t)
	rr := httptest.NewRecorder()
	h.Link(rr, post(t, `{}`), "wdtt-server:default")
	_, msg, code := decodeEnvelope(t, rr)
	if rr.Code != http.StatusNotFound || code != "NOT_FOUND" {
		t.Fatalf("status=%d код=%q сообщение=%q", rr.Code, code, msg)
	}
}

// ── (б) импорт создаёт запись ────────────────────────────────────

func TestImport_CreatesRecord(t *testing.T) {
	h, _, mut, _, _ := newTestHandler(t)
	rr := httptest.NewRecorder()
	link := "qwdtt://config?name=WL+RUS&peer=203.0.113.1:56000&hashes=h1,h2&workers=18&port=9100&pass=pwd&mode=raw&deviceId=dev1&sub=https://sub.example/w.json"
	h.Import(rr, post(t, `{"link":`+jsonString(link)+`}`))

	data, msg, code := decodeEnvelope(t, rr)
	if code != "" {
		t.Fatalf("отказ %s: %s", code, msg)
	}
	if data["key"] != "wdtt-client:default" {
		t.Fatalf("key=%v", data["key"])
	}
	if len(mut.created) != 1 {
		t.Fatalf("ожидалось одно создание, получено %d", len(mut.created))
	}
	want := instancestore.Record{
		ID:      "default",
		Kind:    instancestore.KindWdttClient,
		Name:    "WL RUS",
		Enabled: false,
		Sub:     "https://sub.example/w.json",
		WdttClient: &roles.WdttClientConfig{
			Mode:     ConnModeRaw,
			Peer:     "203.0.113.1:56000",
			Password: "pwd",
			VKHashes: "h1,h2",
			Workers:  18,
			DeviceID: "dev1",
		},
	}
	if !reflect.DeepEqual(mut.created[0], want) {
		t.Fatalf("создана запись:\n%+v (cfg %+v)\nхотели:\n%+v (cfg %+v)",
			mut.created[0], mut.created[0].WdttClient, want, want.WdttClient)
	}
	// payload отдаётся прежней формой — фронт читает его поля.
	payload, _ := data["payload"].(map[string]any)
	if payload["peer"] != "203.0.113.1:56000" || payload["password"] != "pwd" {
		t.Fatalf("payload=%v", payload)
	}
}

func TestImport_FreeIDWhenDefaultTaken(t *testing.T) {
	taken := clientRecord(roles.WdttClientConfig{Mode: ConnModeWG, Listen: "127.0.0.1:9000",
		Peer: "1.1.1.1:56000", Password: "p", VKHashes: "h"})
	h, _, mut, _, _ := newTestHandler(t, taken)
	rr := httptest.NewRecorder()
	h.Import(rr, post(t, `{"link":"qwdtt://config?peer=10.0.0.1&pass=x&hashes=h"}`))
	if _, msg, code := decodeEnvelope(t, rr); code != "" {
		t.Fatalf("отказ %s: %s", code, msg)
	}
	if len(mut.created) != 1 || mut.created[0].ID != "default2" {
		t.Fatalf("создано %+v", mut.created)
	}
}

func TestImport_BadLink(t *testing.T) {
	h, _, mut, _, _ := newTestHandler(t)
	rr := httptest.NewRecorder()
	h.Import(rr, post(t, `{"link":"не ссылка"}`))
	_, msg, code := decodeEnvelope(t, rr)
	if code != "WDTT_IMPORT_FAILED" || msg == "" {
		t.Fatalf("код=%q сообщение=%q", code, msg)
	}
	if len(mut.created) != 0 {
		t.Fatalf("запись создана на битой ссылке: %+v", mut.created)
	}
}

func TestDecode_ReturnsProfile(t *testing.T) {
	h, _, _, _, _ := newTestHandler(t)
	rr := httptest.NewRecorder()
	h.Decode(rr, post(t, `{"link":"wdtt://1.2.3.4:56000:56001:9000:secret:hash1#MyServer"}`))
	data, msg, code := decodeEnvelope(t, rr)
	if code != "" {
		t.Fatalf("отказ %s: %s", code, msg)
	}
	profile, _ := data["profile"].(map[string]any)
	if profile["peer"] != "1.2.3.4:56000" || profile["password"] != "secret" || profile["name"] != "MyServer" {
		t.Fatalf("profile=%v", profile)
	}
	if _, ok := data["subscription"]; ok {
		t.Fatalf("одиночный профиль не должен нести подписку: %v", data)
	}
}

// ── (в) очистка связей ───────────────────────────────────────────

func TestClearLinkedTunnels_Form(t *testing.T) {
	rec := clientRecord(roles.WdttClientConfig{Mode: ConnModeWG, Listen: "127.0.0.1:9000",
		Peer: "1.1.1.1:56000", Password: "p", VKHashes: "h"})
	h, _, _, cleaner, _ := newTestHandler(t, rec)
	cleaner.deleted = []string{"t1", "t2"}
	cleaner.errs = []string{"Германия wdtt (t3): занят"}

	rr := httptest.NewRecorder()
	h.ClearLinkedTunnels(rr, post(t, ``), rec.Key())
	data, msg, code := decodeEnvelope(t, rr)
	if code != "" {
		t.Fatalf("отказ %s: %s", code, msg)
	}
	if !reflect.DeepEqual(cleaner.gotClientIDs, []string{"default"}) {
		t.Fatalf("уборщик позван с %v (ждали id инстанса, не ключ)", cleaner.gotClientIDs)
	}
	want := map[string]any{
		"deletedTunnels": []any{"t1", "t2"},
		"tunnelErrors":   []any{"Германия wdtt (t3): занят"},
		"message":        "linked AWG tunnels cleared",
	}
	if !reflect.DeepEqual(data, want) {
		t.Fatalf("тело ответа:\n%+v\nхотели:\n%+v", data, want)
	}
}

// ── (г) ensure-wg ────────────────────────────────────────────────

const wgConf = "[Interface]\nPrivateKey = priv\nAddress = 10.66.0.5/32\n\n[Peer]\nPublicKey = srvkey=\nEndpoint = 1.2.3.4:56001\nAllowedIPs = 0.0.0.0/0\n"

func wgClient() instancestore.Record {
	return clientRecord(roles.WdttClientConfig{Mode: ConnModeWG, Listen: "127.0.0.1:9100",
		Peer: "1.1.1.1:56000", Password: "p", VKHashes: "h"})
}

func TestEnsureWG_NoSnapshotIs409(t *testing.T) {
	rec := wgClient()
	h, _, _, _, tunnels := newTestHandler(t, rec)
	rr := httptest.NewRecorder()
	h.EnsureWGTunnel(rr, post(t, ``), rec.Key())
	_, msg, code := decodeEnvelope(t, rr)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d сообщение=%q", rr.Code, msg)
	}
	if code != "WDTT_WG_NOT_READY" ||
		msg != "WireGuard конфиг ещё не получен от wdtt-server — дождитесь успешного подключения клиента" {
		t.Fatalf("код=%q сообщение=%q", code, msg)
	}
	if len(tunnels.imports) != 0 {
		t.Fatalf("импорт при пустом снимке: %+v", tunnels.imports)
	}
}

func TestEnsureWG_EmptyConfigInSnapshotIs409(t *testing.T) {
	rec := wgClient()
	h, _, _, _, tunnels := newTestHandler(t, rec)
	h.deps.Snapshots = func(string) (awgmproto.State, bool) {
		return awgmproto.State{PID: 42, WG: &awgmproto.WGState{Config: "   "}}, true
	}
	rr := httptest.NewRecorder()
	h.EnsureWGTunnel(rr, post(t, ``), rec.Key())
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d тело=%s", rr.Code, rr.Body.String())
	}
	if len(tunnels.imports) != 0 {
		t.Fatalf("импорт при пустом конфиге: %+v", tunnels.imports)
	}
}

func TestEnsureWG_ImportsWithPatchedEndpoint(t *testing.T) {
	rec := wgClient()
	h, _, _, _, tunnels := newTestHandler(t, rec)
	h.deps.Snapshots = func(key string) (awgmproto.State, bool) {
		if key != rec.Key() {
			return awgmproto.State{}, false
		}
		return awgmproto.State{PID: 42, WG: &awgmproto.WGState{Config: wgConf}}, true
	}
	tunnels.nextID = "wg-1"

	rr := httptest.NewRecorder()
	h.EnsureWGTunnel(rr, post(t, ``), rec.Key())
	data, msg, code := decodeEnvelope(t, rr)
	if code != "" {
		t.Fatalf("отказ %s: %s", code, msg)
	}
	if len(tunnels.imports) != 1 {
		t.Fatalf("импортов %d", len(tunnels.imports))
	}
	got := tunnels.imports[0]
	if got.Name != "Германия wdtt" {
		t.Fatalf("имя туннеля %q", got.Name)
	}
	if !strings.Contains(got.Conf, "Endpoint = 127.0.0.1:9100") {
		t.Fatalf("endpoint не подставлен: %q", got.Conf)
	}
	if strings.Contains(got.Conf, "1.2.3.4:56001") {
		t.Fatalf("остался внешний endpoint: %q", got.Conf)
	}
	if len(tunnels.saved) != 1 || tunnels.saved[0].WdttClientID != "default" {
		t.Fatalf("связь не записана: %+v", tunnels.saved)
	}
	if !reflect.DeepEqual(tunnels.started, []string{"wg-1"}) {
		t.Fatalf("живой клиент — туннель обязан подняться: %v", tunnels.started)
	}
	if data["created"] != true || data["tunnelId"] != "wg-1" || data["tunnelName"] != "Германия wdtt" {
		t.Fatalf("тело ответа: %v", data)
	}
	if !strings.Contains(fmt.Sprint(data["message"]), "Создан AWG-туннель «Германия wdtt» (Endpoint 127.0.0.1:9100)") {
		t.Fatalf("сообщение: %v", data["message"])
	}
}

func TestEnsureWG_AdoptsMatchingTunnel(t *testing.T) {
	rec := wgClient()
	h, _, _, _, tunnels := newTestHandler(t, rec)
	h.deps.Snapshots = func(string) (awgmproto.State, bool) {
		return awgmproto.State{PID: 0, WG: &awgmproto.WGState{Config: wgConf}}, true
	}
	tunnels.tunnels = []storage.AWGTunnel{{
		ID: "old", Name: "Старое имя",
		Peer: storage.AWGPeer{PublicKey: "srvkey=", Endpoint: "127.0.0.1:9000"},
	}}

	rr := httptest.NewRecorder()
	h.EnsureWGTunnel(rr, post(t, ``), rec.Key())
	data, msg, code := decodeEnvelope(t, rr)
	if code != "" {
		t.Fatalf("отказ %s: %s", code, msg)
	}
	if len(tunnels.imports) != 0 {
		t.Fatalf("повторный импорт вместо усыновления: %+v", tunnels.imports)
	}
	if len(tunnels.saved) != 1 {
		t.Fatalf("сохранений %d: %+v", len(tunnels.saved), tunnels.saved)
	}
	saved := tunnels.saved[0]
	if saved.WdttClientID != "default" || saved.Name != "Германия wdtt" || saved.Peer.Endpoint != "127.0.0.1:9100" {
		t.Fatalf("усыновлённый туннель: %+v", saved)
	}
	if len(tunnels.started) != 0 {
		t.Fatalf("клиент не запущен — туннель поднимать нечему: %v", tunnels.started)
	}
	if data["created"] != false || data["tunnelId"] != "old" {
		t.Fatalf("тело ответа: %v", data)
	}
	if data["message"] != "AWG-туннель с таким WireGuard-конфигом уже существует" {
		t.Fatalf("сообщение: %v", data["message"])
	}
}

func TestEnsureWG_DropsStaleLinkedTunnel(t *testing.T) {
	rec := wgClient()
	h, _, _, _, tunnels := newTestHandler(t, rec)
	h.deps.Snapshots = func(string) (awgmproto.State, bool) {
		return awgmproto.State{PID: 7, WG: &awgmproto.WGState{Config: wgConf}}, true
	}
	tunnels.tunnels = []storage.AWGTunnel{
		{ID: "stale", Name: "Старый пир", WdttClientID: "default",
			Peer: storage.AWGPeer{PublicKey: "другой="}},
		{ID: "чужой", Name: "Пользовательский", Peer: storage.AWGPeer{PublicKey: "третий="}},
	}
	tunnels.nextID = "wg-2"

	rr := httptest.NewRecorder()
	h.EnsureWGTunnel(rr, post(t, ``), rec.Key())
	if _, msg, code := decodeEnvelope(t, rr); code != "" {
		t.Fatalf("отказ %s: %s", code, msg)
	}
	if !reflect.DeepEqual(tunnels.deleted, []string{"stale"}) {
		t.Fatalf("снесены %v (чужой туннель трогать нельзя)", tunnels.deleted)
	}
	if len(tunnels.imports) != 1 {
		t.Fatalf("импортов %d", len(tunnels.imports))
	}
}

func TestEnsureWG_RawModeIsNoop(t *testing.T) {
	rec := clientRecord(roles.WdttClientConfig{Mode: ConnModeRaw, Listen: "127.0.0.1:9100",
		Peer: "1.1.1.1:56000", Password: "p", VKHashes: "h",
		NdmsIface: "OpkgTun17", RawIface: "opkgtun17"})
	h, _, _, _, tunnels := newTestHandler(t, rec)
	h.deps.Snapshots = func(string) (awgmproto.State, bool) {
		return awgmproto.State{PID: 7, WG: &awgmproto.WGState{Config: wgConf}}, true
	}
	rr := httptest.NewRecorder()
	h.EnsureWGTunnel(rr, post(t, ``), rec.Key())
	data, msg, code := decodeEnvelope(t, rr)
	if code != "" {
		t.Fatalf("отказ %s: %s", code, msg)
	}
	if data["created"] != false ||
		data["message"] != "Режим Raw: AWG-туннель не используется — трафик идёт через OpkgTun (NDMS)" {
		t.Fatalf("тело ответа: %v", data)
	}
	if len(tunnels.imports) != 0 {
		t.Fatalf("импорт в raw-режиме: %+v", tunnels.imports)
	}
}

func TestHandlers_RejectNonPost(t *testing.T) {
	rec := wgClient()
	h, _, _, _, _ := newTestHandler(t, rec)
	get := func() *http.Request { return httptest.NewRequest(http.MethodGet, "/api/proxyrt/x", nil) }
	for name, call := range map[string]func(*httptest.ResponseRecorder){
		"decode": func(rr *httptest.ResponseRecorder) { h.Decode(rr, get()) },
		"import": func(rr *httptest.ResponseRecorder) { h.Import(rr, get()) },
		"link":   func(rr *httptest.ResponseRecorder) { h.Link(rr, get(), rec.Key()) },
		"ensure": func(rr *httptest.ResponseRecorder) { h.EnsureWGTunnel(rr, get(), rec.Key()) },
		"clear":  func(rr *httptest.ResponseRecorder) { h.ClearLinkedTunnels(rr, get(), rec.Key()) },
	} {
		rr := httptest.NewRecorder()
		call(rr)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: status=%d", name, rr.Code)
		}
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ── фикс-раунд 1 ─────────────────────────────────────────────────

// C1: id уникален только внутри роли, «default» есть у всех четырёх. Запрос к
// СЕРВЕРУ не имеет права снести туннели КЛИЕНТА с тем же id.
func TestClearLinkedTunnels_RejectsNonClientKind(t *testing.T) {
	server := serverRecord(roles.WdttServerConfig{Listen: "0.0.0.0:56002",
		Password: "main", RelayMode: ConnModeWG})
	client := clientRecord(roles.WdttClientConfig{Mode: ConnModeWG, Listen: "127.0.0.1:9000",
		Peer: "1.1.1.1:56000", Password: "p", VKHashes: "h"})
	h, _, _, cleaner, _ := newTestHandler(t, server, client)

	rr := httptest.NewRecorder()
	h.ClearLinkedTunnels(rr, post(t, ``), server.Key())
	_, msg, code := decodeEnvelope(t, rr)
	if code != "BAD_REQUEST" {
		t.Fatalf("код=%q сообщение=%q", code, msg)
	}
	if !strings.Contains(msg, "только у клиентов") {
		t.Fatalf("причина отказа невнятна: %q", msg)
	}
	if len(cleaner.gotClientIDs) != 0 {
		t.Fatalf("уборщик позван на серверном ключе: %v", cleaner.gotClientIDs)
	}
}

// C1: поле связи у подсистем разное, поэтому уборщик выбирается ПО РОЛИ.
func TestClearLinkedTunnels_CleanerPickedByKind(t *testing.T) {
	wdttClient := clientRecord(roles.WdttClientConfig{Mode: ConnModeWG, Listen: "127.0.0.1:9000",
		Peer: "1.1.1.1:56000", Password: "p", VKHashes: "h"})
	ftClient := instancestore.Record{ID: "default", Kind: instancestore.KindFreeTurnClient,
		Name: "FT", FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9001", Peer: "1.1.1.1:1"}}
	h, _, _, wdttCleaner, _ := newTestHandler(t, wdttClient, ftClient)
	ftCleaner := &fakeCleaner{deleted: []string{"ft-1"}}
	h.deps.Cleaners[instancestore.KindFreeTurnClient] = ftCleaner

	rr := httptest.NewRecorder()
	h.ClearLinkedTunnels(rr, post(t, ``), ftClient.Key())
	data, msg, code := decodeEnvelope(t, rr)
	if code != "" {
		t.Fatalf("отказ %s: %s", code, msg)
	}
	if !reflect.DeepEqual(ftCleaner.gotClientIDs, []string{"default"}) {
		t.Fatalf("freeturn-уборщик позван с %v", ftCleaner.gotClientIDs)
	}
	if len(wdttCleaner.gotClientIDs) != 0 {
		t.Fatalf("позван чужой уборщик: %v", wdttCleaner.gotClientIDs)
	}
	if !reflect.DeepEqual(data["deletedTunnels"], []any{"ft-1"}) {
		t.Fatalf("ответ от чужого уборщика: %v", data)
	}
}

// I2: штатный повтор (автоэффект открытой страницы) не имеет права
// пересоздавать туннель — иначе на каждом тике теряются id и история трафика.
func TestEnsureWG_SamePeerKeyIsIdempotent(t *testing.T) {
	rec := wgClient()
	h, _, _, _, tunnels := newTestHandler(t, rec)
	h.deps.Snapshots = func(string) (awgmproto.State, bool) {
		return awgmproto.State{PID: 0, WG: &awgmproto.WGState{Config: wgConf}}, true
	}
	// Туннель уже связан, ключ пира ТОТ ЖЕ, имя и endpoint уже верные.
	tunnels.tunnels = []storage.AWGTunnel{{
		ID: "wg-1", Name: "Германия wdtt", WdttClientID: "default",
		Peer: storage.AWGPeer{PublicKey: "srvkey=", Endpoint: "127.0.0.1:9100"},
	}}

	rr := httptest.NewRecorder()
	h.EnsureWGTunnel(rr, post(t, ``), rec.Key())
	data, msg, code := decodeEnvelope(t, rr)
	if code != "" {
		t.Fatalf("отказ %s: %s", code, msg)
	}
	if len(tunnels.deleted) != 0 {
		t.Fatalf("повтор снёс живой туннель: %v", tunnels.deleted)
	}
	if len(tunnels.imports) != 0 {
		t.Fatalf("повтор переимпортировал туннель: %+v", tunnels.imports)
	}
	if len(tunnels.saved) != 0 {
		t.Fatalf("менять было нечего, а запись сохранена: %+v", tunnels.saved)
	}
	if tunnels.published != 0 {
		t.Fatalf("публикация списка без единого изменения: %d", tunnels.published)
	}
	if data["created"] != false || data["tunnelId"] != "wg-1" {
		t.Fatalf("тело ответа: %v", data)
	}
}

// I3: обязанности, которые в старом мире жили побочными эффектами хендлера.
func TestEnsureWG_ForgetsTrafficAndPublishes(t *testing.T) {
	rec := wgClient()
	h, _, _, _, tunnels := newTestHandler(t, rec)
	h.deps.Snapshots = func(string) (awgmproto.State, bool) {
		return awgmproto.State{PID: 7, WG: &awgmproto.WGState{Config: wgConf}}, true
	}
	tunnels.tunnels = []storage.AWGTunnel{{ID: "stale", Name: "Старый пир", WdttClientID: "default",
		Peer: storage.AWGPeer{PublicKey: "другой="}}}
	tunnels.nextID = "wg-2"

	rr := httptest.NewRecorder()
	h.EnsureWGTunnel(rr, post(t, ``), rec.Key())
	if _, msg, code := decodeEnvelope(t, rr); code != "" {
		t.Fatalf("отказ %s: %s", code, msg)
	}
	if !reflect.DeepEqual(tunnels.forgotten, []string{"stale"}) {
		t.Fatalf("история трафика снесённого туннеля осиротела: %v", tunnels.forgotten)
	}
	if tunnels.published == 0 {
		t.Fatal("список туннелей не опубликован — для фронта ничего не произошло")
	}
}

// I4: пять fail-closed веток. Срабатывают ровно тогда, когда проводка
// промахнётся, и обязаны называть причину, а не изображать успех.
func TestFailClosed_MissingWiring(t *testing.T) {
	client := clientRecord(roles.WdttClientConfig{Mode: ConnModeWG, Listen: "127.0.0.1:9000",
		Peer: "1.1.1.1:56000", Password: "p", VKHashes: "h"})
	server := serverRecord(roles.WdttServerConfig{Listen: "0.0.0.0:56002",
		Password: "main", RelayMode: ConnModeWG}, instancestore.ServerUser{Password: "abonent"})

	t.Run("нет проверки абонентов", func(t *testing.T) {
		h, _, _, _, _ := newTestHandler(t, server)
		h.deps.Builders[instancestore.KindWdttServer] = NewBuilder(BuilderDeps{Now: testNow})
		rr := httptest.NewRecorder()
		h.Link(rr, post(t, `{"peer":"1.2.3.4","password":"abonent"}`), server.Key())
		_, msg, code := decodeEnvelope(t, rr)
		if code != "WDTT_LINK_NO_CLIENT" || msg != "проверка абонентов не подключена" {
			t.Fatalf("код=%q сообщение=%q", code, msg)
		}
	})

	t.Run("нет сборщика для роли", func(t *testing.T) {
		h, _, _, _, _ := newTestHandler(t, client)
		rr := httptest.NewRecorder()
		h.Link(rr, post(t, `{}`), client.Key())
		_, msg, code := decodeEnvelope(t, rr)
		if code != "BAD_REQUEST" || !strings.Contains(msg, "wdtt-client") {
			t.Fatalf("код=%q сообщение=%q", code, msg)
		}
	})

	t.Run("нет уборщика для роли", func(t *testing.T) {
		h, _, _, _, _ := newTestHandler(t, client)
		h.deps.Cleaners = nil
		rr := httptest.NewRecorder()
		h.ClearLinkedTunnels(rr, post(t, ``), client.Key())
		_, msg, _ := decodeEnvelope(t, rr)
		if rr.Code != http.StatusInternalServerError || !strings.Contains(msg, "не подключена") {
			t.Fatalf("status=%d сообщение=%q", rr.Code, msg)
		}
	})

	t.Run("нет источника записей", func(t *testing.T) {
		h := NewHandler(Deps{})
		rr := httptest.NewRecorder()
		h.Link(rr, post(t, `{}`), client.Key())
		_, msg, _ := decodeEnvelope(t, rr)
		if rr.Code != http.StatusInternalServerError || !strings.Contains(msg, "источник записей") {
			t.Fatalf("status=%d сообщение=%q", rr.Code, msg)
		}
	})

	t.Run("нет мутатора", func(t *testing.T) {
		h := NewHandler(Deps{Records: &fakeSource{recs: map[string]instancestore.Record{}}})
		rr := httptest.NewRecorder()
		h.Import(rr, post(t, `{"link":"qwdtt://config?peer=10.0.0.1&pass=x&hashes=h"}`))
		_, msg, _ := decodeEnvelope(t, rr)
		if rr.Code != http.StatusInternalServerError || !strings.Contains(msg, "импорт не подключён") {
			t.Fatalf("status=%d сообщение=%q", rr.Code, msg)
		}
	})
}

// Повторная генерация с тем же адресом не имеет права писать диск: запись
// проходит через полный цикл store (нормализация, валидация, объявление
// выходов реестру) на каждый показ панели.
func TestLink_SamePeerNotRewritten(t *testing.T) {
	rec := serverRecord(roles.WdttServerConfig{Listen: "0.0.0.0:56002", WgPort: 56001,
		Password: "main", RelayMode: ConnModeWG}, instancestore.ServerUser{Password: "abonent"})
	rec.LinkPeer = "1.2.3.4:56002"
	h, _, mut, _, _ := newTestHandler(t, rec)

	rr := httptest.NewRecorder()
	h.Link(rr, post(t, `{"peer":"1.2.3.4:56002","password":"abonent"}`), rec.Key())
	if _, msg, code := decodeEnvelope(t, rr); code != "" {
		t.Fatalf("отказ %s: %s", code, msg)
	}
	if len(mut.updates) != 0 {
		t.Fatalf("запись переписана без изменения адреса: %+v", mut.updates)
	}
}

// Хеши из записи — единственный читатель LinkVKHashes.
func TestLink_HashesFromRecord(t *testing.T) {
	rec := serverRecord(roles.WdttServerConfig{Listen: "0.0.0.0:56002", WgPort: 56001,
		Password: "main", RelayMode: ConnModeWG}, instancestore.ServerUser{Password: "abonent"})
	rec.LinkVKHashes = "hash1,hash2"
	h, _, _, _, _ := newTestHandler(t, rec)

	rr := httptest.NewRecorder()
	h.Link(rr, post(t, `{"peer":"1.2.3.4","password":"abonent"}`), rec.Key())
	data, msg, code := decodeEnvelope(t, rr)
	if code != "" {
		t.Fatalf("отказ %s: %s", code, msg)
	}
	link, _ := data["link"].(string)
	if !strings.HasSuffix(link, ":abonent:hash1,hash2#Router WDTT") {
		t.Fatalf("хеши записи не уехали в ссылку: %q", link)
	}
	q, _ := data["linkQwdtt"].(string)
	if !strings.Contains(q, "hashes=hash1%2Chash2") {
		t.Fatalf("хеши записи не уехали в qwdtt: %q", q)
	}
}

// Режим импортируемого профиля нормализуется: пустой connMode — это wg, а не
// пустая строка (её конфиг роли отвергает валидацией).
func TestImport_NormalizesEmptyMode(t *testing.T) {
	h, _, mut, _, _ := newTestHandler(t)
	rr := httptest.NewRecorder()
	h.Import(rr, post(t, `{"link":"qwdtt://config?peer=10.0.0.1&pass=x&hashes=h"}`))
	if _, msg, code := decodeEnvelope(t, rr); code != "" {
		t.Fatalf("отказ %s: %s", code, msg)
	}
	if len(mut.created) != 1 || mut.created[0].WdttClient.Mode != ConnModeWG {
		t.Fatalf("режим импортированной записи: %+v", mut.created)
	}
}

// Ссылка без VK-хешей синтаксически верна, но у абонента не работает:
// транспорт wdtt держится на звонках VK. Отдать такую ссылку значит сообщить
// о поломке последнему, кто может её исправить, — самому абоненту.
func TestLink_NoVKHashesRefused(t *testing.T) {
	rec := serverRecordNoHashes(roles.WdttServerConfig{Listen: "0.0.0.0:56002", WgPort: 56001,
		Password: "main", RelayMode: ConnModeWG}, instancestore.ServerUser{Password: "abonent"})
	h, _, _, _, _ := newTestHandler(t, rec)

	rr := httptest.NewRecorder()
	h.Link(rr, post(t, `{"peer":"1.2.3.4:56002","password":"abonent"}`), rec.Key())
	_, msg, code := decodeEnvelope(t, rr)
	if code != "WDTT_LINK_NO_VK_HASHES" {
		t.Fatalf("code = %q (%s), ждали WDTT_LINK_NO_VK_HASHES", code, msg)
	}

	// Хеши из запроса — достаточное основание: своих у сервера нет, но абонент
	// принёс свои.
	rr = httptest.NewRecorder()
	h.Link(rr, post(t, `{"peer":"1.2.3.4:56002","password":"abonent","vkHashes":["own-hash"]}`), rec.Key())
	if _, msg, code := decodeEnvelope(t, rr); code != "" {
		t.Fatalf("отказ при хешах из запроса: %s (%s)", code, msg)
	}
}
