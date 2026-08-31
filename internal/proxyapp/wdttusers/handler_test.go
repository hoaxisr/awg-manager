package wdttusers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

func decodeStatus(t *testing.T, rr *httptest.ResponseRecorder) (UsersStatus, string, string) {
	t.Helper()
	var env struct {
		Data    json.RawMessage `json:"data"`
		Message string          `json:"message"`
		Code    string          `json:"code"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("тело ответа не JSON: %v (%s)", err, rr.Body.String())
	}
	var st UsersStatus
	if len(env.Data) > 0 {
		_ = json.Unmarshal(env.Data, &st)
	}
	return st, env.Message, env.Code
}

func req(method, body string) *http.Request {
	return httptest.NewRequest(method, "/api/proxyrt/instances/x/users", strings.NewReader(body))
}

func (s *stand) serve(t *testing.T, method, body string, sub ...string) (UsersStatus, string, string) {
	t.Helper()
	rr := httptest.NewRecorder()
	s.svc.Serve(rr, req(method, body), testKey, sub)
	// Если тест поставил «параллельный запрос», он обязан был прийтись на
	// заданный вызов Update. Иначе тест гонки молча ничего не проверяет.
	if s.mut.hookAt != 0 {
		s.assertHookFired(t)
	}
	return decodeStatus(t, rr)
}

// ── список ───────────────────────────────────────────────────────

// TestServe_ListForm сторожит ФОРМУ ответа целиком: фронт ветвится по каждому
// признаку записи.
func TestServe_ListForm(t *testing.T) {
	st := newStand(t, baseCfg(),
		instancestore.ServerUser{Password: "client1", Comment: "Иван", VkHash: "vk1"},
		instancestore.ServerUser{Password: "auto1", Comment: defaultUserName, Auto: true},
		instancestore.ServerUser{Password: "mainpass", Comment: "Он же главный"},
	)
	writePasswordsFixture(t, st.dir, passwordsJSON{
		MainPassword: "mainpass",
		Passwords: map[string]passwordsJSONUser{
			"client1": {Label: "имя из файла", VkHash: "vk-из-файла"},
		},
	})

	got, msg, code := st.serve(t, http.MethodGet, "")
	if code != "" {
		t.Fatalf("ответ = %s / %s", code, msg)
	}
	want := UsersStatus{
		Available: true,
		Users: []UserEntry{
			{Password: "client1", Comment: "Иван", VkHash: "vk1"},
			{Password: "auto1", Comment: defaultUserName, IsAuto: true},
			{Password: "mainpass", Comment: "Он же главный"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("список:\n получено %#v\n ожидалось %#v", got, want)
	}
	if got.Reload != "" {
		t.Fatalf("чтение заполнило reload = %q", got.Reload)
	}
}

// Список не пустеет и без файла: единственный источник состава — запись.
func TestServe_ListWithoutFile(t *testing.T) {
	st := newStand(t, baseCfg(), instancestore.ServerUser{Password: "client1"})
	got, _, code := st.serve(t, http.MethodGet, "")
	if code != "" {
		t.Fatalf("код = %s", code)
	}
	if got.Available {
		t.Fatal("available = true без passwords.json")
	}
	if len(got.Users) != 1 {
		t.Fatalf("абоненты = %#v", got.Users)
	}
}

// GET усыновляет: абонент бота обязан быть виден в списке ДО первой мутации.
func TestServe_ListAdopts(t *testing.T) {
	st := newStand(t, baseCfg(), instancestore.ServerUser{Password: "client1"})
	writePasswordsFixture(t, st.dir, passwordsJSON{
		MainPassword: "mainpass",
		Passwords:    map[string]passwordsJSONUser{"botuser": {Label: "Из бота"}},
	})
	got, _, _ := st.serve(t, http.MethodGet, "")
	if len(got.Users) != 2 || got.Users[1].Password != "botuser" {
		t.Fatalf("абонент бота не усыновлён при чтении: %#v", got.Users)
	}
}

// ── (д) добавление: SIGHUP и поле reload ─────────────────────────

func TestServe_AddSignalsReload(t *testing.T) {
	cases := []struct {
		name      string
		delivered bool
		err       error
		want      Reload
	}{
		{"доставлен", true, nil, ReloadDelivered},
		{"сервер не запущен", false, nil, ReloadServerStopped},
		{"сигнал не прошёл", false, errStub, ReloadFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newStand(t, baseCfg(), instancestore.ServerUser{Password: "client1"})
			st.sig.delivered, st.sig.err = tc.delivered, tc.err

			got, msg, code := st.serve(t, http.MethodPost, `{"password":"client2","comment":"Пётр"}`)
			if code != "" {
				t.Fatalf("ответ = %s / %s", code, msg)
			}
			if got.Reload != tc.want {
				t.Fatalf("reload = %q, ожидался %q", got.Reload, tc.want)
			}
			// Сигнал зовётся с КЛЮЧОМ инстанса, а не с id.
			if !reflect.DeepEqual(st.sig.keys, []string{testKey}) {
				t.Fatalf("SignalReload позван с %#v, ожидался ровно один вызов с %q", st.sig.keys, testKey)
			}
			// Абонент уехал и в запись, и в файл.
			if u := st.rec(t).Users; len(u) != 2 || u[1].Password != "client2" || u[1].Comment != "Пётр" {
				t.Fatalf("запись = %#v", u)
			}
			if e, ok := st.file(t).Passwords["client2"]; !ok || e.Label != "Пётр" {
				t.Fatalf("passwords.json = %#v", st.file(t).Passwords)
			}
		})
	}
}

// Пустой пароль в запросе — заведи сам.
func TestServe_AddGeneratesPassword(t *testing.T) {
	st := newStand(t, baseCfg(), instancestore.ServerUser{Password: "client1"})
	got, _, code := st.serve(t, http.MethodPost, `{"comment":"Без пароля"}`)
	if code != "" {
		t.Fatalf("код = %s", code)
	}
	if len(got.Users) != 2 || len(got.Users[1].Password) != 32 {
		t.Fatalf("пароль не сгенерирован: %#v", got.Users)
	}
}

// ── (е) тексты отказов — часть контракта фронта ──────────────────

func TestServe_AddRejectionTexts(t *testing.T) {
	cases := []struct {
		name  string
		users []instancestore.ServerUser
		body  string
		want  string
	}{
		{
			name:  "пароль занят живым абонентом",
			users: []instancestore.ServerUser{{Password: "client1"}},
			body:  `{"password":"client1"}`,
			want:  "занят живым абонентом",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newStand(t, baseCfg(), tc.users...)
			_, msg, code := st.serve(t, http.MethodPost, tc.body)
			if code != "WDTT_SERVER_CLIENT_ADD_FAILED" {
				t.Fatalf("код = %q (сообщение %q)", code, msg)
			}
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("сообщение = %q, ожидалась подстрока %q", msg, tc.want)
			}
		})
	}
}

// Трим входа — ПЕРВЫМ делом: " client1 " обязан наткнуться на занятый пароль.
func TestServe_AddTrimsInput(t *testing.T) {
	st := newStand(t, baseCfg(), instancestore.ServerUser{Password: "client1"})
	_, msg, _ := st.serve(t, http.MethodPost, `{"password":"  client1  "}`)
	if !strings.Contains(msg, "занят живым абонентом") {
		t.Fatalf("сообщение = %q: пароль с пробелами обошёл проверку", msg)
	}
}

// ── удаление ─────────────────────────────────────────────────────

func TestServe_RemoveOne(t *testing.T) {
	st := newStand(t, baseCfg(),
		instancestore.ServerUser{Password: "client1"},
		instancestore.ServerUser{Password: "client2"},
	)
	got, msg, code := st.serve(t, http.MethodDelete, "", "client2")
	if code != "" {
		t.Fatalf("ответ = %s / %s", code, msg)
	}
	if got.Reload != ReloadDelivered {
		t.Fatalf("reload = %q", got.Reload)
	}
	if u := st.rec(t).Users; len(u) != 1 || u[0].Password != "client1" {
		t.Fatalf("абоненты = %#v", u)
	}
	if _, ok := st.file(t).Passwords["client2"]; ok {
		t.Fatalf("удалённый абонент остался в файле: %#v", st.file(t).Passwords)
	}
}

func TestServe_RemoveRejections(t *testing.T) {
	cases := []struct {
		name  string
		users []instancestore.ServerUser
		pass  string
		want  string
	}{
		{"пустой пароль", []instancestore.ServerUser{{Password: "client1"}}, "  ", "пароль абонента не задан"},
		{
			"последний рабочий",
			[]instancestore.ServerUser{{Password: "client1"}, {Password: "  "}},
			"client1",
			"нельзя удалить последнего рабочего абонента",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newStand(t, baseCfg(), tc.users...)
			_, msg, code := st.serve(t, http.MethodDelete, "", tc.pass)
			if code != "WDTT_SERVER_CLIENT_DELETE_FAILED" {
				t.Fatalf("код = %q (сообщение %q)", code, msg)
			}
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("сообщение = %q, ожидалась подстрока %q", msg, tc.want)
			}
		})
	}
}

// Удаление НЕизвестного пароля отвечает успехом, ничего не удалив, — паритет
// старого RemoveServerClient (api/wdtt_server.go → svc.RemoveServerClient): там
// дроп по отсутствующему паролю был no-op, и ручка отвечала 200. Асимметрия с
// переименованием («абонент с таким паролем не найден») перенесена как есть;
// менять контракт в задаче переноса нельзя.
func TestServe_RemoveUnknownIsNoop(t *testing.T) {
	st := newStand(t, baseCfg(),
		instancestore.ServerUser{Password: "client1"},
		instancestore.ServerUser{Password: "client2"},
	)
	got, msg, code := st.serve(t, http.MethodDelete, "", "чужой")
	if code != "" {
		t.Fatalf("ответ = %s / %s", code, msg)
	}
	if len(got.Users) != 2 {
		t.Fatalf("состав изменился: %#v", got.Users)
	}
}

// Снос ВСЕХ проходит, только когда рабочих и так нет: непригодность после
// снятия срока — это пустой пароль либо совпадение с главным.
func TestServe_RemoveAll(t *testing.T) {
	st := newStand(t, baseCfg(),
		instancestore.ServerUser{Password: "  "},
		instancestore.ServerUser{Password: "   "},
	)
	got, msg, code := st.serve(t, http.MethodDelete, "")
	if code != "" {
		t.Fatalf("ответ = %s / %s", code, msg)
	}
	if len(got.Users) != 0 || got.Reload != ReloadDelivered {
		t.Fatalf("ответ = %#v", got)
	}
	if u := st.rec(t).Users; len(u) != 0 {
		t.Fatalf("абоненты = %#v", u)
	}
}

// Тот же инвариант: снести всех, когда рабочие есть, нельзя.
func TestServe_RemoveAllRefusesUsable(t *testing.T) {
	st := newStand(t, baseCfg(), instancestore.ServerUser{Password: "client1"})
	_, msg, code := st.serve(t, http.MethodDelete, "")
	if code != "WDTT_SERVER_CLIENT_DELETE_FAILED" {
		t.Fatalf("код = %q", code)
	}
	if !strings.Contains(msg, "нельзя удалить последнего рабочего абонента") {
		t.Fatalf("сообщение = %q", msg)
	}
	if u := st.rec(t).Users; len(u) != 1 {
		t.Fatalf("абоненты снесены вопреки отказу: %#v", u)
	}
}

// ── переименование ───────────────────────────────────────────────

func TestServe_Rename(t *testing.T) {
	st := newStand(t, baseCfg(), instancestore.ServerUser{Password: "client1", Comment: "Иван"})
	got, msg, code := st.serve(t, http.MethodPatch, `{"name":"Пётр"}`, "client1")
	if code != "" {
		t.Fatalf("ответ = %s / %s", code, msg)
	}
	if got.Users[0].Comment != "Пётр" {
		t.Fatalf("ответ = %#v", got.Users)
	}
	// Паритет: passwords.json тут не переписывается, SIGHUP не шлётся.
	if got.Reload != "" {
		t.Fatalf("reload = %q, у переименования его быть не должно", got.Reload)
	}
	if len(st.sig.keys) != 0 {
		t.Fatalf("SignalReload позван при переименовании: %#v", st.sig.keys)
	}
	if st.rec(t).Users[0].Comment != "Пётр" {
		t.Fatalf("имя не сохранено: %#v", st.rec(t).Users)
	}
}

// Переименование правит РОВНО имя: хеш и признак авто остаются на месте.
func TestServe_RenameKeepsOtherFields(t *testing.T) {
	st := newStand(t, baseCfg(), instancestore.ServerUser{
		Password: "client1", Comment: "Иван", VkHash: "vk1", Auto: true,
	})
	if _, _, code := st.serve(t, http.MethodPatch, `{"name":"Пётр"}`, "client1"); code != "" {
		t.Fatalf("код = %q", code)
	}
	want := instancestore.ServerUser{
		Password: "client1", Comment: "Пётр", VkHash: "vk1", Auto: true,
	}
	if got := st.rec(t).Users[0]; !reflect.DeepEqual(got, want) {
		t.Fatalf("абонент:\n получено %#v\n ожидалось %#v", got, want)
	}
}

func TestServe_RenameRejections(t *testing.T) {
	cases := []struct{ body, pass, want string }{
		{`{"name":"Пётр"}`, "  ", "пароль абонента не задан"},
		{`{"name":"  "}`, "client1", "имя абонента не задано"},
		{`{"name":"Пётр"}`, "чужой", "абонент с таким паролем не найден"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			st := newStand(t, baseCfg(), instancestore.ServerUser{Password: "client1"})
			_, msg, code := st.serve(t, http.MethodPatch, tc.body, tc.pass)
			if code != "WDTT_SERVER_CLIENT_RENAME_FAILED" {
				t.Fatalf("код = %q (сообщение %q)", code, msg)
			}
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("сообщение = %q, ожидалась подстрока %q", msg, tc.want)
			}
		})
	}
}

// ── маршрутизация и негативные пути ──────────────────────────────

func TestServe_Routing(t *testing.T) {
	st := newStand(t, baseCfg(), instancestore.ServerUser{Password: "client1"})
	cases := []struct {
		method string
		sub    []string
		code   string
	}{
		{http.MethodPut, nil, "METHOD_NOT_ALLOWED"},
		{http.MethodPost, []string{"client1"}, "METHOD_NOT_ALLOWED"},
		{http.MethodGet, []string{"client1", "лишний"}, "NOT_FOUND"},
	}
	for _, tc := range cases {
		rr := httptest.NewRecorder()
		st.svc.Serve(rr, req(tc.method, ""), testKey, tc.sub)
		if _, _, code := decodeStatus(t, rr); code != tc.code {
			t.Fatalf("%s %v: код = %q, ожидался %q", tc.method, tc.sub, code, tc.code)
		}
	}
}

func TestServe_UnknownInstance(t *testing.T) {
	st := newStand(t, baseCfg())
	rr := httptest.NewRecorder()
	st.svc.Serve(rr, req(http.MethodGet, ""), "wdtt-server:нет-такого", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("HTTP = %d, ожидался 404", rr.Code)
	}
	if _, _, code := decodeStatus(t, rr); code != "NOT_FOUND" {
		t.Fatalf("код = %q", code)
	}
}

// Роль сверяется: у клиента абонентов нет, а «default» есть у всех ролей.
func TestServe_RejectsNonServerKind(t *testing.T) {
	st := newStand(t, baseCfg())
	client := instancestore.Record{
		ID: "srv1", Kind: instancestore.KindWdttClient, Name: "Клиент",
		WdttClient: &roles.WdttClientConfig{Mode: "wg", Peer: "1.2.3.4:56000", Password: "p"},
	}
	st.src.recs[client.Key()] = client
	rr := httptest.NewRecorder()
	st.svc.Serve(rr, req(http.MethodGet, ""), client.Key(), nil)
	_, msg, code := decodeStatus(t, rr)
	if code == "" {
		t.Fatalf("клиентский ключ принят: %s", rr.Body.String())
	}
	if !strings.Contains(msg, "wdtt-client") {
		t.Fatalf("сообщение = %q: роль не названа", msg)
	}
}

func TestServe_BadBody(t *testing.T) {
	st := newStand(t, baseCfg())
	if _, _, code := st.serve(t, http.MethodPost, "{не json"); code != "BAD_REQUEST" {
		t.Fatalf("код = %q", code)
	}
}
