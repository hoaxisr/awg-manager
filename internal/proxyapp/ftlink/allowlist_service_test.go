package ftlink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
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
	updates []updateCall
	fail    error
}

func (f *fakeMutator) Create(_ context.Context, rec instancestore.Record) error {
	f.src.recs[rec.Key()] = rec
	return nil
}

func (f *fakeMutator) Update(_ context.Context, key string, mutate func(*instancestore.Record) error) error {
	if f.fail != nil {
		return f.fail
	}
	rec, ok := f.src.recs[key]
	if !ok {
		return fmt.Errorf("инстанс %s не найден", key)
	}
	// Конфиг за указателем: копия записи делит его с хранимой, и правка «по
	// месту» была бы видна даже при отказе. Клонируем, чтобы фейк не маскировал
	// потерю полей.
	if rec.FreeTurnServer != nil {
		cfg := *rec.FreeTurnServer
		rec.FreeTurnServer = &cfg
	}
	if err := mutate(&rec); err != nil {
		return err
	}
	f.src.recs[key] = rec
	f.updates = append(f.updates, updateCall{Key: key, Rec: rec})
	return nil
}

const ftServerKey = "freeturn-server:default"

func ftServerRecord(clientsFile string) instancestore.Record {
	return instancestore.Record{
		ID:      "default",
		Kind:    instancestore.KindFreeTurnServer,
		Name:    "Реле",
		Enabled: true,
		FreeTurnServer: &roles.FreeTurnServerConfig{
			Listen:       "0.0.0.0:56000",
			Connect:      "127.0.0.1:9000",
			Mode:         "udp",
			ObfProfile:   "rtpopus2",
			ObfKey:       "aabb",
			ClientsFile:  clientsFile,
			OpenFirewall: true,
		},
	}
}

func newAllowlistService(t *testing.T, rec instancestore.Record) (*Service, *fakeSource, *fakeMutator, string) {
	t.Helper()
	src := &fakeSource{recs: map[string]instancestore.Record{rec.Key(): rec}}
	mut := &fakeMutator{src: src}
	dir := t.TempDir()
	return New(Deps{Records: src, Mutator: mut, DataDir: dir}), src, mut, dir
}

const okClientID = "aabbccddeeff00112233445566778899"

// ── список ───────────────────────────────────────────────────────

func TestAllowlist_ListDisabled(t *testing.T) {
	s, _, _, _ := newAllowlistService(t, ftServerRecord(""))
	st, err := s.List(ftServerKey)
	if err != nil {
		t.Fatal(err)
	}
	want := AllowlistStatus{Enabled: false, ClientsFile: "", Clients: []AllowlistEntry{}}
	if !reflect.DeepEqual(st, want) {
		t.Fatalf("статус=%+v, want %+v", st, want)
	}
}

func TestAllowlist_ListReadsConfiguredFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clients.json")
	if err := addAllowlistClient(path, okClientID, "Alice"); err != nil {
		t.Fatal(err)
	}
	s, _, _, _ := newAllowlistService(t, ftServerRecord(path))
	st, err := s.List(ftServerKey)
	if err != nil {
		t.Fatal(err)
	}
	want := AllowlistStatus{Enabled: true, ClientsFile: path,
		Clients: []AllowlistEntry{{ClientID: okClientID, Comment: "Alice"}}}
	if !reflect.DeepEqual(st, want) {
		t.Fatalf("статус=%+v, want %+v", st, want)
	}
}

// ── добавление ───────────────────────────────────────────────────

func TestAllowlist_AddEnablesListAndAsksRestart(t *testing.T) {
	rec := ftServerRecord("")
	s, src, mut, dir := newAllowlistService(t, rec)

	res, err := s.Add(context.Background(), ftServerKey, okClientID, "Alice")
	if err != nil {
		t.Fatal(err)
	}
	if !res.NeedsRestart {
		t.Fatal("включение списка обязано просить перезапуск: -clients-file — аргумент старта")
	}
	wantPath := filepath.Join(dir, "freeturn", "allowlist-default.json")
	want := AddAllowlistResult{
		AllowlistStatus: AllowlistStatus{Enabled: true, ClientsFile: wantPath,
			Clients: []AllowlistEntry{{ClientID: okClientID, Comment: "Alice"}}},
		NeedsRestart: true,
	}
	if !reflect.DeepEqual(res, want) {
		t.Fatalf("ответ=%+v, want %+v", res, want)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("файл списка не создан: %v", err)
	}

	// Путь записан ПО МЕСТУ: запись сверяется целиком, пересборка литералом
	// потеряла бы имя, тумблер и остальные поля конфига роли.
	if len(mut.updates) != 1 || mut.updates[0].Key != ftServerKey {
		t.Fatalf("вызовы мутатора: %+v", mut.updates)
	}
	wantRec := ftServerRecord(wantPath)
	if !reflect.DeepEqual(src.recs[ftServerKey], wantRec) {
		t.Fatalf("запись после включения:\n got %+v / %+v\nwant %+v / %+v",
			src.recs[ftServerKey], *src.recs[ftServerKey].FreeTurnServer,
			wantRec, *wantRec.FreeTurnServer)
	}
}

func TestAllowlist_AddToEnabledListKeepsRunning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clients.json")
	s, _, mut, _ := newAllowlistService(t, ftServerRecord(path))

	res, err := s.Add(context.Background(), ftServerKey, okClientID, "Alice")
	if err != nil {
		t.Fatal(err)
	}
	if res.NeedsRestart {
		t.Fatal("список уже включён: перезапускать нечего, сервер перечитает файл сам")
	}
	if len(mut.updates) != 0 {
		t.Fatalf("конфиг не менялся, а мутатор позван: %+v", mut.updates)
	}
	if res.ClientsFile != path || len(res.Clients) != 1 {
		t.Fatalf("ответ=%+v", res)
	}
}

// Путь не сохранился — файл не пишем: сервер стартовал бы БЕЗ -clients-file и
// пропускал бы всех, а пользователь видел бы «список включён».
func TestAllowlist_AddStopsWhenConfigNotSaved(t *testing.T) {
	s, _, mut, dir := newAllowlistService(t, ftServerRecord(""))
	mut.fail = errors.New("диск полон")

	if _, err := s.Add(context.Background(), ftServerKey, okClientID, ""); err == nil {
		t.Fatal("отказ записи конфига обязан доехать до вызывающего")
	}
	if _, err := os.Stat(filepath.Join(dir, "freeturn", "allowlist-default.json")); !os.IsNotExist(err) {
		t.Fatalf("файл списка создан при неудачном включении: %v", err)
	}
}

func TestAllowlist_AddRejectsBadClientID(t *testing.T) {
	s, src, mut, _ := newAllowlistService(t, ftServerRecord(""))
	if _, err := s.Add(context.Background(), ftServerKey, "not-hex", ""); err == nil {
		t.Fatal("нехекс обязан быть отвергнут")
	}
	// Путь при этом уже записан — включение списка идёт ДО проверки id
	// (паритет старого порядка).
	if len(mut.updates) != 1 {
		t.Fatalf("вызовы мутатора: %+v", mut.updates)
	}
	if src.recs[ftServerKey].FreeTurnServer.ClientsFile == "" {
		t.Fatal("путь списка не сохранён")
	}
}

// ── удаление одного ──────────────────────────────────────────────

func TestAllowlist_Remove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clients.json")
	if err := addAllowlistClient(path, okClientID, "Alice"); err != nil {
		t.Fatal(err)
	}
	s, _, _, _ := newAllowlistService(t, ftServerRecord(path))
	if err := s.Remove(ftServerKey, okClientID); err != nil {
		t.Fatal(err)
	}
	st, err := loadAllowlistStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Clients) != 0 {
		t.Fatalf("после удаления: %+v", st.Clients)
	}
}

func TestAllowlist_RemoveWhenDisabled(t *testing.T) {
	s, _, _, _ := newAllowlistService(t, ftServerRecord(""))
	err := s.Remove(ftServerKey, okClientID)
	if err == nil || err.Error() != "allowlist не включён" {
		t.Fatalf("err=%v", err)
	}
}

// ── выключение ───────────────────────────────────────────────────

func TestAllowlist_DisableClearsPathAndAsksRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clients.json")
	s, src, mut, _ := newAllowlistService(t, ftServerRecord(path))

	needsRestart, err := s.Disable(context.Background(), ftServerKey)
	if err != nil {
		t.Fatal(err)
	}
	if !needsRestart {
		t.Fatal("выключение обязано просить перезапуск: живой сервер продолжает проверять id")
	}
	if !reflect.DeepEqual(src.recs[ftServerKey], ftServerRecord("")) {
		t.Fatalf("запись после выключения: %+v / %+v",
			src.recs[ftServerKey], *src.recs[ftServerKey].FreeTurnServer)
	}
	if len(mut.updates) != 1 {
		t.Fatalf("вызовы мутатора: %+v", mut.updates)
	}

	// Повторное выключение ничего не меняет — перезапускать нечего.
	needsRestart, err = s.Disable(context.Background(), ftServerKey)
	if err != nil {
		t.Fatal(err)
	}
	if needsRestart {
		t.Fatal("список уже выключен: needsRestart обязан быть false")
	}
	if len(mut.updates) != 1 {
		t.Fatalf("повтор позвал мутатор: %+v", mut.updates)
	}
}

// ── гейты ────────────────────────────────────────────────────────

func TestAllowlist_UnknownInstance(t *testing.T) {
	s, _, _, _ := newAllowlistService(t, ftServerRecord(""))
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"list", func() error { _, err := s.List("freeturn-server:нет"); return err }},
		{"add", func() error {
			_, err := s.Add(context.Background(), "freeturn-server:нет", okClientID, "")
			return err
		}},
		{"remove", func() error { return s.Remove("freeturn-server:нет", okClientID) }},
		{"disable", func() error { _, err := s.Disable(context.Background(), "freeturn-server:нет"); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), "не найден") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

// Роль сверяется обязательно: список разрешённых есть ТОЛЬКО у freeturn-сервера,
// а id «default» носят все четыре роли.
func TestAllowlist_RejectsForeignKind(t *testing.T) {
	rec := instancestore.Record{
		ID: "default", Kind: instancestore.KindFreeTurnClient, Name: "Клиент",
		FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9000", Peer: "1.2.3.4:56000"},
	}
	s, _, mut, _ := newAllowlistService(t, rec)
	key := rec.Key()
	if _, err := s.List(key); err == nil {
		t.Fatal("список чужой роли обязан быть отвергнут")
	}
	if _, err := s.Add(context.Background(), key, okClientID, ""); err == nil {
		t.Fatal("добавление в чужую роль обязано быть отвергнуто")
	}
	if err := s.Remove(key, okClientID); err == nil {
		t.Fatal("удаление у чужой роли обязано быть отвергнуто")
	}
	if _, err := s.Disable(context.Background(), key); err == nil {
		t.Fatal("выключение у чужой роли обязано быть отвергнуто")
	}
	if len(mut.updates) != 0 {
		t.Fatalf("чужая роль дошла до мутатора: %+v", mut.updates)
	}
}

func TestAllowlist_FailClosedWithoutWiring(t *testing.T) {
	s := New(Deps{})
	if _, err := s.List(ftServerKey); err == nil {
		t.Fatal("без источника записей операция обязана отказать")
	}

	src := &fakeSource{recs: map[string]instancestore.Record{ftServerKey: ftServerRecord("")}}
	noMut := New(Deps{Records: src, DataDir: t.TempDir()})
	if _, err := noMut.Add(context.Background(), ftServerKey, okClientID, ""); err == nil {
		t.Fatal("без мутатора включение списка обязано отказать, а не записать файл втихую")
	}

	// Без каталога данных путь получился бы относительным, и сервер искал бы
	// список относительно своего рабочего каталога — проверка пропускала бы всех.
	noDir := New(Deps{Records: src, Mutator: &fakeMutator{src: src}})
	if _, err := noDir.Add(context.Background(), ftServerKey, okClientID, ""); err == nil {
		t.Fatal("без каталога данных включение списка обязано отказать")
	}
	if src.recs[ftServerKey].FreeTurnServer.ClientsFile != "" {
		t.Fatalf("отказ оставил путь в конфиге: %q", src.recs[ftServerKey].FreeTurnServer.ClientsFile)
	}
}

// ── HTTP-формы ───────────────────────────────────────────────────

func serveAllowlist(t *testing.T, s *Service, method, key string, sub []string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("")
	} else {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/api/proxyrt/instances/"+key+"/allowlist", rdr)
	rr := httptest.NewRecorder()
	s.Serve(rr, req, key, sub)
	return rr
}

func decodeEnvelope(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("тело %q: %v", rr.Body.String(), err)
	}
	return env
}

func TestAllowlistHandler_Forms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clients.json")
	s, _, _, _ := newAllowlistService(t, ftServerRecord(path))

	// GET
	rr := serveAllowlist(t, s, http.MethodGet, ftServerKey, nil, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET code=%d body=%s", rr.Code, rr.Body)
	}
	env := decodeEnvelope(t, rr)
	data, _ := env["data"].(map[string]any)
	if env["success"] != true || data["enabled"] != true || data["clientsFile"] != path {
		t.Fatalf("GET data=%+v", data)
	}

	// POST
	rr = serveAllowlist(t, s, http.MethodPost, ftServerKey, nil,
		`{"clientId":"`+okClientID+`","comment":"Alice"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST code=%d body=%s", rr.Code, rr.Body)
	}
	data, _ = decodeEnvelope(t, rr)["data"].(map[string]any)
	if data["needsRestart"] != false {
		t.Fatalf("POST needsRestart=%v (ключ обязан быть в теле)", data["needsRestart"])
	}
	clients, _ := data["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("POST clients=%+v", data["clients"])
	}

	// DELETE одного
	rr = serveAllowlist(t, s, http.MethodDelete, ftServerKey, []string{okClientID}, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("DELETE one code=%d body=%s", rr.Code, rr.Body)
	}
	data, _ = decodeEnvelope(t, rr)["data"].(map[string]any)
	if !reflect.DeepEqual(data, map[string]any{"message": "removed"}) {
		t.Fatalf("DELETE one data=%+v", data)
	}

	// DELETE коллекции
	rr = serveAllowlist(t, s, http.MethodDelete, ftServerKey, nil, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("DELETE code=%d body=%s", rr.Code, rr.Body)
	}
	data, _ = decodeEnvelope(t, rr)["data"].(map[string]any)
	if !reflect.DeepEqual(data, map[string]any{"message": "allowlist disabled", "needsRestart": true}) {
		t.Fatalf("DELETE data=%+v", data)
	}
}

func TestAllowlistHandler_Rejections(t *testing.T) {
	s, _, _, _ := newAllowlistService(t, ftServerRecord(""))

	if rr := serveAllowlist(t, s, http.MethodPut, ftServerKey, nil, ""); rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT code=%d", rr.Code)
	}
	if rr := serveAllowlist(t, s, http.MethodGet, ftServerKey, []string{"a", "b"}, ""); rr.Code != http.StatusNotFound {
		t.Fatalf("глубокий путь code=%d", rr.Code)
	}
	if rr := serveAllowlist(t, s, http.MethodPost, ftServerKey, nil, "{"); rr.Code != http.StatusBadRequest {
		t.Fatalf("битое тело code=%d", rr.Code)
	}
	rr := serveAllowlist(t, s, http.MethodGet, "freeturn-server:нет", nil, "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("несуществующий инстанс code=%d body=%s", rr.Code, rr.Body)
	}
	if code, _ := decodeEnvelope(t, rr)["code"].(string); code != "NOT_FOUND" {
		t.Fatalf("код отказа=%v", decodeEnvelope(t, rr)["code"])
	}
}

// Коды отказов — вербатим старые: фронт и пользователь видят прежние.
func TestAllowlistHandler_ErrorCodes(t *testing.T) {
	s, _, _, _ := newAllowlistService(t, ftServerRecord(""))

	rr := serveAllowlist(t, s, http.MethodPost, ftServerKey, nil, `{"clientId":"not-hex"}`)
	if code, _ := decodeEnvelope(t, rr)["code"].(string); code != "FREETURN_ALLOWLIST_ADD_FAILED" {
		t.Fatalf("код добавления=%v", decodeEnvelope(t, rr)["code"])
	}
	// Свежий сервис: неудачное добавление выше уже включило список, а отказ
	// удаления нужен именно на ВЫКЛЮЧЕННОМ.
	off, _, _, _ := newAllowlistService(t, ftServerRecord(""))
	rr = serveAllowlist(t, off, http.MethodDelete, ftServerKey, []string{okClientID}, "")
	if code, _ := decodeEnvelope(t, rr)["code"].(string); code != "FREETURN_ALLOWLIST_REMOVE_FAILED" {
		t.Fatalf("код удаления=%v", decodeEnvelope(t, rr)["code"])
	}
}
