package wdttusers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

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
	created []instancestore.Record
	updates []updateCall
	failOn  string // ключ, на котором Update отвечает отказом
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
