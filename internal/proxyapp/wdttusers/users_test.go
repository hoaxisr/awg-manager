package wdttusers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// ── фейки ────────────────────────────────────────────────────────

type fakeSource struct {
	recs map[string]instancestore.Record
}

// Get отдаёт КОПИЮ записи вместе с копией среза абонентов — как настоящее
// хранилище, которое сериализует состояние. Общий срез маскировал бы половину
// эффектов гонки: правка «по месту» в колбэке доезжала бы до хранилища даже
// тогда, когда запись отменена отказом колбэка.
func (f *fakeSource) Get(key string) (instancestore.Record, bool) {
	r, ok := f.recs[key]
	if !ok {
		return instancestore.Record{}, false
	}
	return copyRecord(r), true
}

func copyRecord(r instancestore.Record) instancestore.Record {
	r.Users = slices.Clone(r.Users)
	if r.WdttServer != nil {
		cfg := *r.WdttServer
		r.WdttServer = &cfg
	}
	return r
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
	failOn  string // ключ, на котором Update отвечает отказом
	// beforeUpdate имитирует ПАРАЛЛЕЛЬНУЮ ручку, успевшую изменить запись
	// между чтением вызывающего и записью. Детерминированно, без горутин.
	//
	// hookAt — НОМЕР вызова Update, перед которым хук срабатывает (1 — первый).
	// Номер обязателен: у путей с усыновлением первый Update принадлежит ему,
	// и «хук на первом вызове» пришёлся бы не на ту мутацию — тест тогда
	// молча перестаёт что-либо обнаруживать. hookFired фиксирует, на каком
	// вызове хук сработал ФАКТИЧЕСКИ, и тест это сверяет.
	beforeUpdate func(*fakeSource)
	hookAt       int
	hookFired    int
	calls        int
}

func (f *fakeMutator) Create(_ context.Context, rec instancestore.Record) error {
	f.created = append(f.created, rec)
	f.src.recs[rec.Key()] = rec
	return nil
}

func (f *fakeMutator) Update(_ context.Context, key string, mutate func(*instancestore.Record) error) error {
	if f.failOn != "" && f.failOn == key {
		return fmt.Errorf("хранилище недоступно")
	}
	f.calls++
	if f.beforeUpdate != nil && f.calls == f.hookAt {
		f.hookFired = f.calls
		f.beforeUpdate(f.src)
	}
	rec, ok := f.src.recs[key]
	if !ok {
		return fmt.Errorf("инстанс %s не найден", key)
	}
	rec = copyRecord(rec)
	if err := mutate(&rec); err != nil {
		// Отказ колбэка отменяет запись целиком — как ReplaceChecked
		// менеджера. Копия выше нужна именно для этого: правка «по месту» не
		// должна доехать до хранилища.
		return err
	}
	f.src.recs[key] = rec
	f.updates = append(f.updates, updateCall{Key: key, Rec: rec})
	return nil
}

// fakeSignal запоминает КЛЮЧИ, с которыми его позвали.
type fakeSignal struct {
	keys      []string
	delivered bool
	err       error
}

func (f *fakeSignal) reload(key string) (bool, error) {
	f.keys = append(f.keys, key)
	return f.delivered, f.err
}

// ── стенд ────────────────────────────────────────────────────────

var fixedNow = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }

const testKey = "wdtt-server:srv1"

type stand struct {
	svc    *Service
	src    *fakeSource
	mut    *fakeMutator
	sig    *fakeSignal
	dir    string
	warns  []string
	tuning func(*instancestore.Record)
}

func newStand(t *testing.T, cfg roles.WdttServerConfig, users ...instancestore.ServerUser) *stand {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "cfg")
	if cfg.ConfigDir == "" {
		cfg.ConfigDir = dir
	}
	rec := instancestore.Record{
		ID: "srv1", Kind: instancestore.KindWdttServer, Name: "Сервер",
		Enabled: true, Users: users, StatsLog: StatsLogModeDisk, WdttServer: &cfg,
	}
	src := &fakeSource{recs: map[string]instancestore.Record{rec.Key(): rec}}
	mut := &fakeMutator{src: src}
	sig := &fakeSignal{delivered: true}
	st := &stand{src: src, mut: mut, sig: sig, dir: cfg.ConfigDir}
	st.svc = New(Deps{
		Records: src, Mutator: mut, SignalReload: sig.reload,
		Warn: func(msg string) { st.warns = append(st.warns, msg) },
		Now:  fixedNow,
	})
	return st
}

// hookOn ставит «параллельный запрос» ПЕРЕД n-м вызовом Update.
func (s *stand) hookOn(n int, fn func(*fakeSource)) {
	s.mut.hookAt = n
	s.mut.beforeUpdate = fn
}

// assertHookFired требует, чтобы хук сработал ИМЕННО на заданном вызове.
// Без этой сверки тест гонки деградирует молча: усыновление пишет первым,
// «съедает» номер, и параллельный запрос приходится не на ту мутацию.
func (s *stand) assertHookFired(t *testing.T) {
	t.Helper()
	if s.mut.hookAt == 0 {
		t.Fatal("хук не задан")
	}
	if s.mut.hookFired != s.mut.hookAt {
		t.Fatalf("хук сработал на вызове %d, а целился в %d (всего вызовов Update: %d) — тест гонки не проверяет то, что обещает",
			s.mut.hookFired, s.mut.hookAt, s.mut.calls)
	}
}

func (s *stand) rec(t *testing.T) instancestore.Record {
	t.Helper()
	r, ok := s.src.recs[testKey]
	if !ok {
		t.Fatalf("запись %s пропала", testKey)
	}
	return r
}

func (s *stand) file(t *testing.T) passwordsJSON {
	t.Helper()
	doc, err := loadPasswordsJSON(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func baseCfg() roles.WdttServerConfig {
	return roles.WdttServerConfig{
		Listen: "0.0.0.0:56000", WgPort: 56001, Password: "mainpass",
		AdminID: "42", BotToken: "bot:token",
		WgIface: "opkgtun17", NdmsIface: "OpkgTun17",
		RawIface: "opkgtun18", RawNdmsIface: "OpkgTun18",
		RelayMode: "wg", NatMode: "full",
	}
}

// ── (а) B5: Materialize — СЛИЯНИЕ, не сборка ─────────────────────

// TestMaterialize_MergesServerOwnedFields сторожит блокирующее требование Г-2:
// поля, принадлежащие форку, обязаны пережить материализацию. Фикстура —
// ПОЛНАЯ: каждое поле passwordsJSONUser в своём различимом значении, сравнение
// целых структур. Мутация «собрать файл из Record.Users, не читая
// существующий» роняет тест целиком.
func TestMaterialize_MergesServerOwnedFields(t *testing.T) {
	st := newStand(t, baseCfg(), instancestore.ServerUser{
		Password: "client1", Comment: "Новое имя", VkHash: "vk-new", ExpiresAt: 1_700_009_999,
	})
	writePasswordsFixture(t, st.dir, passwordsJSON{
		MainPassword: "старый-главный",
		AdminID:      "старый-админ",
		BotToken:     "старый-бот",
		Passwords: map[string]passwordsJSONUser{
			"client1": {
				Label:         "старое имя",
				DeviceID:      "legacy-dev",
				DeviceIDs:     []string{"d1", "d2"},
				MaxDevices:    5,
				ExpiresAt:     1_700_001_111,
				DownBytes:     123456,
				UpBytes:       654321,
				VkHash:        "vk-old",
				Ports:         "80,443",
				IsDeactivated: true,
			},
		},
		Devices: map[string]any{
			"d1": map[string]any{"ip": "10.66.0.7", "raw_ip": "10.70.0.7", "pub_key": "AAA"},
			"d2": map[string]any{"ip": "10.66.0.8"},
		},
	})

	if err := st.svc.Materialize(st.rec(t)); err != nil {
		t.Fatal(err)
	}
	got := st.file(t)

	want := map[string]passwordsJSONUser{
		"client1": {
			Label:         "Новое имя",   // наше
			VkHash:        "vk-new",      // наше
			ExpiresAt:     1_700_009_999, // наше
			DeviceID:      "legacy-dev",
			DeviceIDs:     []string{"d1", "d2"},
			MaxDevices:    5,
			DownBytes:     123456,
			UpBytes:       654321,
			Ports:         "80,443",
			IsDeactivated: true,
		},
	}
	if !reflect.DeepEqual(got.Passwords, want) {
		t.Fatalf("слияние потеряло серверные поля:\n получено %#v\n ожидалось %#v", got.Passwords, want)
	}
	// Заголовок файла — из записи, целиком.
	if got.MainPassword != "mainpass" || got.AdminID != "42" || got.BotToken != "bot:token" {
		t.Fatalf("заголовок файла = %#v", got)
	}
	// Привязка абонента к адресам 10.66.0.x пережила материализацию.
	for _, id := range []string{"d1", "d2"} {
		if _, ok := got.Devices[id]; !ok {
			t.Fatalf("устройство %s снято: %#v", id, got.Devices)
		}
	}
	if ip := deviceIPFromPasswordsEntry(got.Devices["d1"]); ip != "10.66.0.7" {
		t.Fatalf("адрес устройства подменён: %q", ip)
	}
	// Резерв шлюза на месте: перестановка прополки и резерва его снимает.
	if ip := deviceIPFromPasswordsEntry(got.Devices[gatewayReserveDeviceID]); ip != wdttServerGatewayAddr {
		t.Fatalf("резерв шлюза снят: %#v", got.Devices)
	}
}

// ── (б) память о сроке ───────────────────────────────────────────

func TestMaterialize_ExpiredUserDoesNotResurrect(t *testing.T) {
	now := fixedNow()
	st := newStand(t, baseCfg(),
		instancestore.ServerUser{Password: "dead", ExpiresAt: now.Add(-time.Hour).Unix()},
		instancestore.ServerUser{Password: "alive", ExpiresAt: now.Add(time.Hour).Unix()},
	)
	// Файла нет: истёкшую запись удалил янитор форка.
	if err := st.svc.Materialize(st.rec(t)); err != nil {
		t.Fatal(err)
	}
	got := st.file(t)
	if _, ok := got.Passwords["dead"]; ok {
		t.Fatalf("истёкший абонент воскрес: %#v", got.Passwords)
	}
	if e := got.Passwords["alive"]; e.ExpiresAt != now.Add(time.Hour).Unix() {
		t.Fatalf("срок живого абонента = %d, ожидался запомненный (бессрочность вместо срока)", e.ExpiresAt)
	}
}

// ── (ж) Г-3: журнал статистики ───────────────────────────────────

func TestMaterialize_StatsLogSymlink(t *testing.T) {
	cases := []struct {
		mode string
		want string // "" — симлинка быть не должно
	}{
		{StatsLogModeRAM, "/tmp/awg-wdtt-server-srv1.log"},
		{"", "/tmp/awg-wdtt-server-srv1.log"}, // дефолт — ram
		{StatsLogModeOff, "/dev/null"},
		{StatsLogModeDisk, ""},
	}
	for _, tc := range cases {
		t.Run("режим "+tc.mode, func(t *testing.T) {
			st := newStand(t, baseCfg(), instancestore.ServerUser{Password: "client1"})
			rec := st.rec(t)
			rec.StatsLog = tc.mode
			if err := st.svc.Materialize(rec); err != nil {
				t.Fatal(err)
			}
			link, err := os.Readlink(filepath.Join(st.dir, "server.log"))
			if tc.want == "" {
				if err == nil {
					t.Fatalf("режим disk: журнал уведён на %q, а должен лежать на диске", link)
				}
				return
			}
			if err != nil {
				t.Fatalf("симлинка нет: %v (статистика форка пойдёт на флеш каждые ~2 с)", err)
			}
			if link != tc.want {
				t.Fatalf("симлинк = %q, ожидался %q", link, tc.want)
			}
		})
	}
}

// ── (в) усыновление ──────────────────────────────────────────────

// TestSyncOnStart_AdoptsUsersFromFile: абонента завёл телеграм-бот или
// admin-API форка — он лежит только в passwords.json. Без усыновления
// следующая материализация отобрала бы у него доступ.
func TestSyncOnStart_AdoptsUsersFromFile(t *testing.T) {
	now := fixedNow()
	st := newStand(t, baseCfg(),
		instancestore.ServerUser{Password: "known", Comment: "Наш"},
		instancestore.ServerUser{Password: "renewed", Comment: "Продлённый", ExpiresAt: now.Add(time.Hour).Unix()},
	)
	writePasswordsFixture(t, st.dir, passwordsJSON{
		MainPassword: "mainpass",
		Passwords: map[string]passwordsJSONUser{
			"botuser":  {Label: "Из бота", VkHash: "vk-bot", ExpiresAt: now.Add(48 * time.Hour).Unix()},
			"known":    {Label: "имя из файла"},
			"renewed":  {ExpiresAt: now.Add(72 * time.Hour).Unix()},
			"mainpass": {Label: "главный, заведён admin-API"},
		},
	})

	if err := st.svc.SyncOnStart(context.Background(), testKey); err != nil {
		t.Fatal(err)
	}

	want := []instancestore.ServerUser{
		{Password: "known", Comment: "Наш"},
		{Password: "renewed", Comment: "Продлённый", ExpiresAt: now.Add(72 * time.Hour).Unix()},
		{Password: "botuser", Comment: "Из бота", VkHash: "vk-bot", ExpiresAt: now.Add(48 * time.Hour).Unix()},
	}
	if got := st.rec(t).Users; !reflect.DeepEqual(got, want) {
		t.Fatalf("усыновление:\n получено %#v\n ожидалось %#v", got, want)
	}
	// Абонент бота уехал в файл, а не потерялся при следующей материализации.
	if _, ok := st.file(t).Passwords["botuser"]; !ok {
		t.Fatalf("усыновлённый абонент не попал в passwords.json: %#v", st.file(t).Passwords)
	}
	// Главный пароль усыновлять нельзя: он не абонент.
	for _, u := range st.rec(t).Users {
		if u.Password == "mainpass" {
			t.Fatal("главный пароль усыновлён как абонент")
		}
	}
}

// ── (г) инвариант непустоты ──────────────────────────────────────

func TestSyncOnStart_EnsuresUsableUser(t *testing.T) {
	st := newStand(t, baseCfg()) // абонентов нет вовсе
	if err := st.svc.SyncOnStart(context.Background(), testKey); err != nil {
		t.Fatal(err)
	}
	users := st.rec(t).Users
	if len(users) != 1 {
		t.Fatalf("абоненты = %#v, ожидался ровно один заведённый инвариантом", users)
	}
	u := users[0]
	if !u.Auto {
		t.Fatal("Auto = false: бейдж «заведён автоматически» не появится")
	}
	if u.Comment != defaultUserName {
		t.Fatalf("имя = %q, ожидалось %q", u.Comment, defaultUserName)
	}
	if len(u.Password) != 32 {
		t.Fatalf("пароль = %q, ожидались 32 hex-символа", u.Password)
	}
	if _, ok := st.file(t).Passwords[u.Password]; !ok {
		t.Fatalf("заведённый абонент не попал в файл: %#v", st.file(t).Passwords)
	}
}

// Инвариант молчит, когда рабочий абонент есть, и когда пароля сервера нет.
func TestSyncOnStart_EnsureIsQuietWhenNotNeeded(t *testing.T) {
	st := newStand(t, baseCfg(), instancestore.ServerUser{Password: "client1"})
	if err := st.svc.SyncOnStart(context.Background(), testKey); err != nil {
		t.Fatal(err)
	}
	if got := st.rec(t).Users; len(got) != 1 || got[0].Password != "client1" {
		t.Fatalf("абоненты = %#v: инвариант завёл лишнего", got)
	}
}

// TestSyncOnStart_OrderAdoptThenEnsure: усыновление ДО инварианта. Если
// поменять их местами, инвариант заведёт «Абонент 1» рядом с живым абонентом
// бота, которого он не увидел.
func TestSyncOnStart_OrderAdoptThenEnsure(t *testing.T) {
	st := newStand(t, baseCfg()) // в записи абонентов нет
	writePasswordsFixture(t, st.dir, passwordsJSON{
		MainPassword: "mainpass",
		Passwords:    map[string]passwordsJSONUser{"botuser": {Label: "Из бота"}},
	})
	if err := st.svc.SyncOnStart(context.Background(), testKey); err != nil {
		t.Fatal(err)
	}
	want := []instancestore.ServerUser{{Password: "botuser", Comment: "Из бота"}}
	if got := st.rec(t).Users; !reflect.DeepEqual(got, want) {
		t.Fatalf("абоненты = %#v, ожидался только усыновлённый (порядок усыновление→инвариант)", got)
	}
}

// SyncOnStart обязан ПИСАТЬ файл третьим шагом: без него старт сервера
// прошёл бы по старому составу.
func TestSyncOnStart_MaterializesAfterMutations(t *testing.T) {
	st := newStand(t, baseCfg(), instancestore.ServerUser{Password: "client1", Comment: "Иван"})
	if err := st.svc.SyncOnStart(context.Background(), testKey); err != nil {
		t.Fatal(err)
	}
	doc := st.file(t)
	if doc.Passwords["client1"].Label != "Иван" {
		t.Fatalf("файл не переписан: %#v", doc.Passwords)
	}
	if doc.MainPassword != "mainpass" {
		t.Fatalf("main_password = %q", doc.MainPassword)
	}
}

func TestSyncOnStart_UnknownInstance(t *testing.T) {
	st := newStand(t, baseCfg())
	if err := st.svc.SyncOnStart(context.Background(), "wdtt-server:нет-такого"); err == nil {
		t.Fatal("отсутствующий инстанс принят молча")
	}
}

// errStub — отказ доставки сигнала.
var errStub = fmt.Errorf("процесс есть, сигнал не прошёл")

// failLastMutator пропускает первые failFrom-1 вызовов Update и валит
// остальные: так проверяется ЧАСТИЧНЫЙ успех «абонент есть, пароль сервера
// не сохранён».
type failLastMutator struct {
	inner    *fakeMutator
	calls    int
	failFrom int
}

func (f *failLastMutator) Create(ctx context.Context, rec instancestore.Record) error {
	return f.inner.Create(ctx, rec)
}

func (f *failLastMutator) Update(ctx context.Context, key string, mutate func(*instancestore.Record) error) error {
	f.calls++
	if f.calls >= f.failFrom {
		return fmt.Errorf("хранилище недоступно")
	}
	return f.inner.Update(ctx, key, mutate)
}

// ── фикс-раунд 1: семантика мутаций ──────────────────────────────

// TestMutations_DoNotResurrectFromSnapshot — главный тест раунда.
//
// Сценарий владельца: два удаления подряд. Первое уже завершилось и вычеркнуло
// абонента; второе, ждавшее на замке, писало бы состав из СВОЕГО снимка и
// вернуло бы вычеркнутого — материализация положила бы его обратно в
// passwords.json, а SIGHUP восстановил бы ОТОЗВАННЫЙ доступ.
//
// Фейк источника отдаёт копии, поэтому эффект не маскируется общим срезом.
func TestMutations_DoNotResurrectFromSnapshot(t *testing.T) {
	st := newStand(t, baseCfg(),
		instancestore.ServerUser{Password: "revoked"},
		instancestore.ServerUser{Password: "victim"},
		instancestore.ServerUser{Password: "keeper"},
	)
	// Фикстура файла НЕПУСТАЯ: усыновление пишет первым и «съедает» первый
	// вызов Update. Хук целится во ВТОРОЙ — саму мутацию, — и serve сверяет,
	// что он сработал именно там (иначе тест ничего не проверяет).
	writePasswordsFixture(t, st.dir, passwordsJSON{
		MainPassword: "mainpass",
		Passwords:    map[string]passwordsJSONUser{"botuser": {Label: "Из бота"}},
	})
	// Параллельная ручка успела отозвать доступ "revoked" перед нашей записью.
	st.hookOn(2, func(src *fakeSource) {
		rec := src.recs[testKey]
		rec.Users = dropUser(rec.Users, "revoked")
		src.recs[testKey] = rec
	})

	got, msg, code := st.serve(t, http.MethodDelete, "", "victim")
	if code != "" {
		t.Fatalf("ответ = %s / %s", code, msg)
	}
	want := []instancestore.ServerUser{
		{Password: "keeper"},
		{Password: "botuser", Comment: "Из бота"},
	}
	if u := st.rec(t).Users; !reflect.DeepEqual(u, want) {
		t.Fatalf("запись = %#v, ожидалась %#v — отозванный доступ воскрешён из снимка", u, want)
	}
	if _, ok := st.file(t).Passwords["revoked"]; ok {
		t.Fatalf("отозванный доступ вернулся в passwords.json: %#v", st.file(t).Passwords)
	}
	if len(got.Users) != 2 || got.Users[0].Password != "keeper" {
		t.Fatalf("ответ = %#v", got.Users)
	}
}

// Зеркальный сценарий: конкурентное ДОБАВЛЕНИЕ не должно теряться.
func TestMutations_DoNotDropConcurrentAdd(t *testing.T) {
	st := newStand(t, baseCfg(),
		instancestore.ServerUser{Password: "victim"},
		instancestore.ServerUser{Password: "keeper"},
	)
	st.hookOn(1, func(src *fakeSource) {
		rec := src.recs[testKey]
		rec.Users = append(slices.Clone(rec.Users), instancestore.ServerUser{Password: "newcomer", Comment: "Новичок"})
		src.recs[testKey] = rec
	})
	if _, msg, code := st.serve(t, http.MethodDelete, "", "victim"); code != "" {
		t.Fatalf("ответ = %s / %s", code, msg)
	}
	want := []instancestore.ServerUser{
		{Password: "keeper"},
		{Password: "newcomer", Comment: "Новичок"},
	}
	if u := st.rec(t).Users; !reflect.DeepEqual(u, want) {
		t.Fatalf("запись = %#v, ожидалась %#v — конкурентное добавление стёрто снимком", u, want)
	}
}

// То же для переименования: правка идёт по актуальному списку.
func TestRename_DoesNotResurrectFromSnapshot(t *testing.T) {
	st := newStand(t, baseCfg(),
		instancestore.ServerUser{Password: "revoked"},
		instancestore.ServerUser{Password: "client1", Comment: "Иван"},
	)
	st.hookOn(1, func(src *fakeSource) {
		rec := src.recs[testKey]
		rec.Users = dropUser(rec.Users, "revoked")
		src.recs[testKey] = rec
	})
	if _, msg, code := st.serve(t, http.MethodPatch, `{"name":"Пётр"}`, "client1"); code != "" {
		t.Fatalf("ответ = %s / %s", code, msg)
	}
	want := []instancestore.ServerUser{{Password: "client1", Comment: "Пётр"}}
	if u := st.rec(t).Users; !reflect.DeepEqual(u, want) {
		t.Fatalf("запись = %#v, ожидалась %#v", u, want)
	}
}

// Добавление тоже считает от актуального списка: пароль, занятый параллельным
// запросом, обязан быть отвергнут, а не замещён.
func TestAdd_SeesConcurrentPassword(t *testing.T) {
	st := newStand(t, baseCfg(), instancestore.ServerUser{Password: "client1"})
	st.hookOn(1, func(src *fakeSource) {
		rec := src.recs[testKey]
		rec.Users = append(slices.Clone(rec.Users), instancestore.ServerUser{Password: "client2", Comment: "Успел первым"})
		src.recs[testKey] = rec
	})
	_, msg, code := st.serve(t, http.MethodPost, `{"password":"client2","comment":"Опоздал"}`)
	if code != "WDTT_SERVER_CLIENT_ADD_FAILED" {
		t.Fatalf("код = %q (сообщение %q): пароль, занятый параллельно, замещён", code, msg)
	}
	if !strings.Contains(msg, "занят живым абонентом") {
		t.Fatalf("сообщение = %q", msg)
	}
	if u := st.rec(t).Users; len(u) != 2 || u[1].Comment != "Успел первым" {
		t.Fatalf("запись = %#v: чужой абонент затёрт", u)
	}
}

// TestRemoveAll_RefusalKeepsUsers: после отказа инварианта состав прежний.
//
// Имя честное: колбэк отказывает ДО единого изменения, поэтому про АТОМАРНОСТЬ
// отказа этот тест ничего не доказывает — он проходил бы и на фейке, который
// сохраняет запись при ошибке. Контракт «отказ отменяет запись» приколочен
// отдельно, в TestFakeMutator_RefusalCancelsWrite.
func TestRemoveAll_RefusalKeepsUsers(t *testing.T) {
	st := newStand(t, baseCfg(),
		instancestore.ServerUser{Password: "client1", Comment: "Иван", VkHash: "vk1"},
	)
	if _, _, code := st.serve(t, http.MethodDelete, ""); code == "" {
		t.Fatal("снос всех при живом абоненте прошёл")
	}
	want := []instancestore.ServerUser{{Password: "client1", Comment: "Иван", VkHash: "vk1"}}
	if u := st.rec(t).Users; !reflect.DeepEqual(u, want) {
		t.Fatalf("запись = %#v, ожидалась %#v", u, want)
	}
}

// ── фикс-раунд 1: усыновление на путях мутаций ───────────────────

// Абонент бота живёт только в файле. Без усыновления удаление посчитало бы
// его несуществующим и отказало «последний рабочий», а сам он выпал бы из
// следующей материализации.
func TestServe_RemoveAdoptsFirst(t *testing.T) {
	st := newStand(t, baseCfg(), instancestore.ServerUser{Password: "client1"})
	writePasswordsFixture(t, st.dir, passwordsJSON{
		MainPassword: "mainpass",
		Passwords:    map[string]passwordsJSONUser{"botuser": {Label: "Из бота"}},
	})
	got, msg, code := st.serve(t, http.MethodDelete, "", "client1")
	if code != "" {
		t.Fatalf("ответ = %s / %s (усыновления не было — бот-абонент не сочтён рабочим)", code, msg)
	}
	want := []instancestore.ServerUser{{Password: "botuser", Comment: "Из бота"}}
	if u := st.rec(t).Users; !reflect.DeepEqual(u, want) {
		t.Fatalf("запись = %#v, ожидалась %#v", u, want)
	}
	if _, ok := st.file(t).Passwords["botuser"]; !ok {
		t.Fatalf("бот-абонент выпал из passwords.json: %#v", st.file(t).Passwords)
	}
	if len(got.Users) != 1 || got.Users[0].Password != "botuser" {
		t.Fatalf("ответ = %#v", got.Users)
	}
}

// Снос всех БЕЗ усыновления обесценивает инвариант: живой бот-абонент не
// считается рабочим, и снос проходит, отбирая у него доступ.
func TestServe_RemoveAllAdoptsFirst(t *testing.T) {
	now := fixedNow()
	st := newStand(t, baseCfg(), instancestore.ServerUser{Password: "stale", ExpiresAt: now.Add(-time.Hour).Unix()})
	writePasswordsFixture(t, st.dir, passwordsJSON{
		MainPassword: "mainpass",
		Passwords:    map[string]passwordsJSONUser{"botuser": {Label: "Из бота"}},
	})
	_, msg, code := st.serve(t, http.MethodDelete, "")
	if code != "WDTT_SERVER_CLIENT_DELETE_FAILED" {
		t.Fatalf("код = %q: снос прошёл мимо живого бот-абонента", code)
	}
	if !strings.Contains(msg, "нельзя удалить последнего рабочего абонента") {
		t.Fatalf("сообщение = %q", msg)
	}
	if _, ok := st.file(t).Passwords["botuser"]; !ok {
		t.Fatalf("бот-абонент потерял доступ: %#v", st.file(t).Passwords)
	}
}

// Добавление без усыновления заместило бы бот-абонента вместе с его сроком.
func TestServe_AddAdoptsFirst(t *testing.T) {
	now := fixedNow()
	st := newStand(t, baseCfg(), instancestore.ServerUser{Password: "client1"})
	writePasswordsFixture(t, st.dir, passwordsJSON{
		MainPassword: "mainpass",
		Passwords:    map[string]passwordsJSONUser{"botuser": {Label: "Из бота", ExpiresAt: now.Add(time.Hour).Unix()}},
	})
	_, msg, code := st.serve(t, http.MethodPost, `{"password":"botuser","comment":"Мой"}`)
	if code != "WDTT_SERVER_CLIENT_ADD_FAILED" {
		t.Fatalf("код = %q: пароль бот-абонента перезаведён, его срок обнулён", code)
	}
	if !strings.Contains(msg, "занят живым абонентом") {
		t.Fatalf("сообщение = %q", msg)
	}
}

// Переименование без усыновления отвечает «не найден» на живого абонента бота.
func TestServe_RenameAdoptsFirst(t *testing.T) {
	st := newStand(t, baseCfg(), instancestore.ServerUser{Password: "client1"})
	writePasswordsFixture(t, st.dir, passwordsJSON{
		MainPassword: "mainpass",
		Passwords:    map[string]passwordsJSONUser{"botuser": {Label: "Из бота"}},
	})
	got, msg, code := st.serve(t, http.MethodPatch, `{"name":"Свой"}`, "botuser")
	if code != "" {
		t.Fatalf("ответ = %s / %s", code, msg)
	}
	if len(got.Users) != 2 || got.Users[1].Comment != "Свой" {
		t.Fatalf("ответ = %#v", got.Users)
	}
}

// ── фикс-раунд 1: гейт пустого ConfigDir ─────────────────────────

// Молчаливый пропуск оставил бы сервер без единого пароля (форк падает в
// log.Fatalf), а симлинк журнала лёг бы в рабочий каталог демона.
func TestMaterialize_EmptyConfigDirIsRefused(t *testing.T) {
	cfg := baseCfg()
	cfg.ConfigDir = " "
	st := newStand(t, cfg, instancestore.ServerUser{Password: "client1"})
	err := st.svc.Materialize(st.rec(t))
	if err == nil {
		t.Fatal("пустой configDir принят молча")
	}
	if !strings.Contains(err.Error(), "configDir") {
		t.Fatalf("ошибка = %q: причина не названа", err)
	}
	if _, statErr := os.Lstat("server.log"); statErr == nil {
		_ = os.Remove("server.log")
		t.Fatal("симлинк журнала лёг в рабочий каталог")
	}
}

func TestSyncOnStart_EmptyConfigDirIsRefused(t *testing.T) {
	cfg := baseCfg()
	cfg.ConfigDir = " "
	st := newStand(t, cfg, instancestore.ServerUser{Password: "client1"})
	if err := st.svc.SyncOnStart(context.Background(), testKey); err == nil {
		t.Fatal("старт с пустым configDir принят молча")
	}
}

// ── фикс-раунд 1: мелкие непокрытые ──────────────────────────────

// Порядок усыновления — сортированный: карта отдаёт ключи вразнобой, а список
// уезжает в запись и виден в UI. Трёх новых достаточно, чтобы случайный
// порядок падал почти всегда.
func TestAdoptUsers_SortedOrder(t *testing.T) {
	file := map[string]passwordsJSONUser{
		"zulu":   {Label: "Z"},
		"alpha":  {Label: "A"},
		"mike":   {Label: "M"},
		"bravo":  {Label: "B"},
		"yankee": {Label: "Y"},
	}
	got, changed := adoptUsers([]instancestore.ServerUser{{Password: "known"}}, file, "mainpass")
	if !changed {
		t.Fatal("changed = false")
	}
	want := []string{"known", "alpha", "bravo", "mike", "yankee", "zulu"}
	var order []string
	for _, u := range got {
		order = append(order, u.Password)
	}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("порядок = %v, ожидался %v", order, want)
	}
}

// Слияние берёт имя и хеш из файла, когда в записи их нет: у бот-абонента
// имени в нашей записи может не быть вовсе.
func TestMergeUsers_FallsBackToFile(t *testing.T) {
	now := fixedNow()
	file := map[string]passwordsJSONUser{
		"client1": {Label: "имя из файла", VkHash: "vk-из-файла"},
		"client2": {Label: "имя из файла", VkHash: "vk-из-файла"},
	}
	got := mergeUsers([]instancestore.ServerUser{
		{Password: "client1"}, // своих нет — берём из файла
		{Password: "client2", Comment: "Своё", VkHash: "vk-своё"}, // свои сильнее
		{Password: "client3"}, // файла нет вовсе
		{Password: "   "},     // пустой в список не попадает
	}, file, true, "mainpass", now)

	want := UsersStatus{Available: true, Users: []UserEntry{
		{Password: "client1", Comment: "имя из файла", VkHash: "vk-из-файла"},
		{Password: "client2", Comment: "Своё", VkHash: "vk-своё"},
		{Password: "client3"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("слияние признаков:\n получено %#v\n ожидалось %#v", got, want)
	}
}

// Главный пароль сервера не должен совпадать с паролем АБОНЕНТА: форк берёт
// main_password для WRAP, и совпадение с хешем абонента ломает рукопожатия.
// Путь: пароль сервера ещё не сохранён, форма присылает его равным паролю
// уже существующего абонента.
func TestServe_AddRefusesMainEqualToExistingUser(t *testing.T) {
	cfg := baseCfg()
	cfg.Password = ""
	st := newStand(t, cfg, instancestore.ServerUser{Password: "client1"})
	_, msg, code := st.serve(t, http.MethodPost, `{"password":"client2","mainPassword":"client1"}`)
	if code != "WDTT_SERVER_CLIENT_ADD_FAILED" {
		t.Fatalf("код = %q (сообщение %q)", code, msg)
	}
	if !strings.Contains(msg, "не должен совпадать с паролем клиента") {
		t.Fatalf("сообщение = %q", msg)
	}
	// Ни абонент, ни пароль сервера не сохранены: отказ — до единой записи.
	if u := st.rec(t).Users; len(u) != 1 {
		t.Fatalf("запись = %#v", u)
	}
	c, _ := st.rec(t).WdttServerConfig()
	if c.Password != "" {
		t.Fatalf("пароль сервера = %q, сохранён вопреки отказу", c.Password)
	}
}

// Инвариант последнего рабочего считается по АКТУАЛЬНОМУ составу: если
// параллельный запрос успел завести абонента, удаление обязано пройти.
func TestRemove_InvariantSeesConcurrentState(t *testing.T) {
	st := newStand(t, baseCfg(), instancestore.ServerUser{Password: "client1"})
	st.hookOn(1, func(src *fakeSource) {
		rec := src.recs[testKey]
		rec.Users = append(slices.Clone(rec.Users), instancestore.ServerUser{Password: "newcomer"})
		src.recs[testKey] = rec
	})
	_, msg, code := st.serve(t, http.MethodDelete, "", "client1")
	if code != "" {
		t.Fatalf("ответ = %s / %s: инвариант посчитан по устаревшему снимку", code, msg)
	}
	want := []instancestore.ServerUser{{Password: "newcomer"}}
	if u := st.rec(t).Users; !reflect.DeepEqual(u, want) {
		t.Fatalf("запись = %#v, ожидалась %#v", u, want)
	}
}

// Инвариант сноса ВСЕХ тоже считается по актуальному составу: если
// параллельный запрос успел завести рабочего абонента, снос обязан отказать.
func TestRemoveAll_InvariantSeesConcurrentState(t *testing.T) {
	now := fixedNow()
	st := newStand(t, baseCfg(), instancestore.ServerUser{Password: "stale", ExpiresAt: now.Add(-time.Hour).Unix()})
	st.hookOn(1, func(src *fakeSource) {
		rec := src.recs[testKey]
		rec.Users = append(slices.Clone(rec.Users), instancestore.ServerUser{Password: "newcomer"})
		src.recs[testKey] = rec
	})
	_, msg, code := st.serve(t, http.MethodDelete, "")
	if code != "WDTT_SERVER_CLIENT_DELETE_FAILED" {
		t.Fatalf("код = %q: инвариант посчитан по устаревшему снимку — снесён живой абонент", code)
	}
	if !strings.Contains(msg, "нельзя удалить последнего рабочего абонента") {
		t.Fatalf("сообщение = %q", msg)
	}
	if u := st.rec(t).Users; len(u) != 2 {
		t.Fatalf("запись = %#v: снос прошёл вопреки отказу", u)
	}
}

// TestMutations_DoNotClobberConcurrentRename — тот случай, где ОБЩИЙ срез в
// фейке маскирует дефект.
//
// Параллельный запрос правит ПОЛЕ существующего абонента, не переаллоцируя
// срез. При общем срезе устаревший снимок вызывающего видел бы чужую правку
// «бесплатно» (тот же массив), и тест проходил бы даже на дефектном коде.
// Фейк отдаёт копии — снимок остаётся устаревшим, и запись состава из него
// затирает чужое переименование.
func TestMutations_DoNotClobberConcurrentRename(t *testing.T) {
	st := newStand(t, baseCfg(),
		instancestore.ServerUser{Password: "client1", Comment: "Иван"},
		instancestore.ServerUser{Password: "victim"},
	)
	writePasswordsFixture(t, st.dir, passwordsJSON{
		MainPassword: "mainpass",
		Passwords:    map[string]passwordsJSONUser{"botuser": {Label: "Из бота"}},
	})
	st.hookOn(2, func(src *fakeSource) {
		rec := src.recs[testKey]
		rec.Users[0].Comment = "Пётр" // правка ПО МЕСТУ, срез тот же
		src.recs[testKey] = rec
	})
	if _, msg, code := st.serve(t, http.MethodDelete, "", "victim"); code != "" {
		t.Fatalf("ответ = %s / %s", code, msg)
	}
	want := []instancestore.ServerUser{
		{Password: "client1", Comment: "Пётр"},
		{Password: "botuser", Comment: "Из бота"},
	}
	if u := st.rec(t).Users; !reflect.DeepEqual(u, want) {
		t.Fatalf("запись = %#v, ожидалась %#v — чужое переименование затёрто снимком", u, want)
	}
}

// ── фикс-раунд 2: внутренние пути под гонкой ─────────────────────

// TestAdopt_SeesConcurrentState: усыновление тоже пишет состав ОТ АКТУАЛЬНОГО
// списка. Хук целится в ЕГО Update (первый): параллельный запрос завёл абонента,
// и усыновление обязано его сохранить, а не затереть своим снимком.
func TestAdopt_SeesConcurrentState(t *testing.T) {
	st := newStand(t, baseCfg(), instancestore.ServerUser{Password: "client1"})
	writePasswordsFixture(t, st.dir, passwordsJSON{
		MainPassword: "mainpass",
		Passwords:    map[string]passwordsJSONUser{"botuser": {Label: "Из бота"}},
	})
	st.hookOn(1, func(src *fakeSource) {
		rec := src.recs[testKey]
		rec.Users = append(slices.Clone(rec.Users), instancestore.ServerUser{Password: "newcomer", Comment: "Новичок"})
		src.recs[testKey] = rec
	})
	if err := st.svc.SyncOnStart(context.Background(), testKey); err != nil {
		t.Fatal(err)
	}
	st.assertHookFired(t)
	want := []instancestore.ServerUser{
		{Password: "client1"},
		{Password: "newcomer", Comment: "Новичок"},
		{Password: "botuser", Comment: "Из бота"},
	}
	if u := st.rec(t).Users; !reflect.DeepEqual(u, want) {
		t.Fatalf("запись = %#v, ожидалась %#v — усыновление затёрло конкурентное добавление", u, want)
	}
}

// TestEnsureUsable_SeesConcurrentAdd: инвариант непустоты перепроверяет условие
// ВНУТРИ колбэка. Параллельный запрос успел завести рабочего абонента — второй
// «Абонент 1» рядом с ним лишний, и пользователь увидел бы чужой автоматический
// доступ, которого не заказывал.
//
// Файл пуст, поэтому усыновление не пишет и первый Update принадлежит опоре.
func TestEnsureUsable_SeesConcurrentAdd(t *testing.T) {
	st := newStand(t, baseCfg()) // абонентов нет — опора собирается сработать
	st.hookOn(1, func(src *fakeSource) {
		rec := src.recs[testKey]
		rec.Users = append(slices.Clone(rec.Users), instancestore.ServerUser{Password: "newcomer"})
		src.recs[testKey] = rec
	})
	if err := st.svc.SyncOnStart(context.Background(), testKey); err != nil {
		t.Fatal(err)
	}
	st.assertHookFired(t)
	want := []instancestore.ServerUser{{Password: "newcomer"}}
	if u := st.rec(t).Users; !reflect.DeepEqual(u, want) {
		t.Fatalf("запись = %#v, ожидалась %#v — опора завела лишнего абонента поверх конкурентного", u, want)
	}
}

// ── фикс-раунд 2: пароль сервера под замком ──────────────────────

// TestAdd_MainPasswordRefusedWhenUserTakesIt: между проверкой и записью
// параллельное добавление успело завести абонента с паролем, равным паролю
// сервера. Форк берёт main_password для WRAP — совпадение ломает рукопожатия
// ВСЕМ. Сохранять пароль в этом состоянии нельзя.
//
// Вызовы Update: 1 — абонент, 2 — пароль сервера. Хук целится во второй.
func TestAdd_MainPasswordRefusedWhenUserTakesIt(t *testing.T) {
	cfg := baseCfg()
	cfg.Password = ""
	st := newStand(t, cfg)
	st.hookOn(2, func(src *fakeSource) {
		rec := src.recs[testKey]
		rec.Users = append(slices.Clone(rec.Users), instancestore.ServerUser{Password: "свежий-главный"})
		src.recs[testKey] = rec
	})
	_, msg, code := st.serve(t, http.MethodPost, `{"password":"client1","mainPassword":"свежий-главный"}`)
	if code != "WDTT_SERVER_MAIN_PASSWORD_NOT_SAVED" {
		t.Fatalf("код = %q (сообщение %q): пароль сервера сохранён равным паролю абонента", code, msg)
	}
	c, _ := st.rec(t).WdttServerConfig()
	if c.Password != "" {
		t.Fatalf("пароль сервера = %q — сохранён вопреки совпадению", c.Password)
	}
}

// ── фикс-раунд 2: контракт фейка приколочен ──────────────────────

// TestFakeMutator_RefusalCancelsWrite сторожит САМ ФЕЙК: он обязан вести себя
// как хранилище — отказ колбэка отменяет запись целиком, и правка «по месту»
// внутри колбэка до хранилища не доезжает.
//
// Без этого теста атомарность отказа держалась ДВУМЯ барьерами (копия записи +
// «не сохранять при ошибке»), и ни один из них не был приколочен по
// отдельности: мутации порознь выживали. Тесты ручек этого не ловят — их
// колбэки отказывают ДО изменений.
func TestFakeMutator_RefusalCancelsWrite(t *testing.T) {
	st := newStand(t, baseCfg(), instancestore.ServerUser{Password: "client1", Comment: "Иван"})
	boom := fmt.Errorf("инвариант не выполнен")
	err := st.mut.Update(context.Background(), testKey, func(r *instancestore.Record) error {
		r.Users[0].Comment = "Затёрто"                                                  // правка ПО МЕСТУ
		r.Users = append(r.Users, instancestore.ServerUser{Password: "не-должен-быть"}) // и добавление
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("ошибка = %v, ожидалась %v", err, boom)
	}
	want := []instancestore.ServerUser{{Password: "client1", Comment: "Иван"}}
	if u := st.rec(t).Users; !reflect.DeepEqual(u, want) {
		t.Fatalf("после отказа запись = %#v, ожидалась %#v — фейк не отменяет запись как хранилище", u, want)
	}
}
