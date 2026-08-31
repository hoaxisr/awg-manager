package instancestore

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// Паритет дефолтов со СТАРЫМ миром.
//
// Ожидания выписаны литералами с ссылкой на источник старого мира, а НЕ через
// импорт internal/wdtt|freeturn: ворота задачи 17 требуют, чтобы ни один файл
// в internal и cmd не импортировал умирающие пакеты, и тест с таким импортом
// пришлось бы там переписывать. Соответствие литерала источнику держится
// ссылкой в комментарии — сверять при ревью.
//
// Каждое поле проверяется В ДВЕ СТОРОНЫ: пустое значение получает дефолт, а
// заданное пользователем ЗНАЧЕНИЕ, отличное от дефолта, переживает запись.
// Одной первой половины мало: подмена «всегда дефолт» прошла бы её целиком.

// storeRoundTrip кладёт запись через Replace и читает её обратно с диска —
// нормализация наблюдается там же, где её видит прод.
func storeRoundTrip(t *testing.T, r Record) Record {
	t.Helper()
	s := newStore(t)
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, r)
		return nil
	}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	st, err := New(s.dir).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(st.Records) != 1 {
		t.Fatalf("записей %d, ждали 1", len(st.Records))
	}
	return st.Records[0]
}

// wgClientBare — wdtt-клиент, у которого ВСЕ поля с дефолтами пусты: ровно то,
// что кладёт ручка создания нового мира (config: {} с фронта) и что приезжает
// из старого конфига, где поля ещё не было.
func wgClientBare() Record {
	return Record{ID: "bare", Kind: KindWdttClient, Name: "Bare", Enabled: false,
		WdttClient: &roles.WdttClientConfig{
			Listen: "127.0.0.1:9001", Peer: "1.2.3.4:56000", Password: "pw", VKHashes: "h",
		}}
}

// TestWdttClientDefaultsMatchOldWorld — wdtt/service.go:899-919
// (normalizeClientConfig), wdtt/types.go:73-82 (DefaultClientConfig),
// wdtt/service.go:962-969 (normalizeCaptchaMode), :953-960 (appendVkAuthArgs).
func TestWdttClientDefaultsMatchOldWorld(t *testing.T) {
	got := storeRoundTrip(t, wgClientBare())
	c, err := got.WdttClientConfig()
	if err != nil {
		t.Fatal(err)
	}

	if c.Obfs != "audio" {
		t.Errorf("obfs = %q, старый мир подставлял audio (DefaultClientConfig)", c.Obfs)
	}
	if c.Fingerprint != "chrome" {
		t.Errorf("fingerprint = %q, старый мир подставлял chrome", c.Fingerprint)
	}
	if c.CaptchaMode != "rjs" {
		t.Errorf("captchaMode = %q, старый мир подставлял rjs (normalizeCaptchaMode)", c.CaptchaMode)
	}
	if c.VKAuthMode != "vkcalls" {
		t.Errorf("vkAuthMode = %q, старый мир подставлял vkcalls (appendVkAuthArgs)", c.VKAuthMode)
	}
	if c.Mode != "wg" {
		t.Errorf("connMode = %q, старый мир подставлял wg (normalizeConnMode)", c.Mode)
	}
	// Число потоков старого мира — 24; новое значение зависит от архитектуры
	// (roles.DefaultWorkers) и это ОСОЗНАННОЕ расхождение, зафиксированное
	// докстрокой DefaultWorkers по замерам. Проверяем то, что не менялось:
	// значение есть, оно кратно девяти (иначе клиент срежет его сам) и равно
	// объявленному дефолту этой архитектуры.
	want := roles.DefaultWorkers(runtime.GOARCH)
	if c.Workers != want {
		t.Errorf("workers = %d, ждали дефолт архитектуры %d — argv не эмитит -n при нуле, "+
			"и число потоков определял бы встроенный дефолт бинаря", c.Workers, want)
	}
	if c.Workers%9 != 0 || c.Workers < 9 {
		t.Errorf("workers = %d: клиент округляет вниз до кратного девяти, дефолт обязан доезжать без урезания", c.Workers)
	}
}

// Заданное пользователем значение дефолтом не затирается — вторая сторона.
func TestWdttClientExplicitValuesSurvive(t *testing.T) {
	r := wgClientBare()
	r.WdttClient.Obfs = "video"
	r.WdttClient.Fingerprint = "firefox"
	r.WdttClient.CaptchaMode = "wv"
	r.WdttClient.VKAuthMode = "vkanon"
	r.WdttClient.Workers = 18
	r.WdttClient.Mode = "raw"
	r.WdttClient.NdmsIface, r.WdttClient.RawIface = "OpkgTun18", "opkgtun18"

	c, err := storeRoundTrip(t, r).WdttClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.Obfs != "video" || c.Fingerprint != "firefox" || c.CaptchaMode != "wv" ||
		c.VKAuthMode != "vkanon" || c.Workers != 18 || c.Mode != "raw" {
		t.Fatalf("заданные значения затёрты дефолтом: %+v", c)
	}
}

// Неизвестный режим капчи приводится к rjs — паритет normalizeCaptchaMode,
// который старый мир звал на КАЖДОЙ сборке argv. Регистр — оттуда же.
func TestWdttClientCaptchaModeCoercesUnknown(t *testing.T) {
	for in, want := range map[string]string{
		"AUTO":     "auto",
		" wv ":     "wv",
		"хрень":    "rjs",
		"rjs2":     "rjs",
		"disabled": "rjs",
	} {
		r := wgClientBare()
		r.WdttClient.CaptchaMode = in
		c, err := storeRoundTrip(t, r).WdttClientConfig()
		if err != nil {
			t.Fatal(err)
		}
		if c.CaptchaMode != want {
			t.Errorf("captchaMode %q → %q, ждали %q", in, c.CaptchaMode, want)
		}
	}
}

// Паритет normalizeConnMode (wdtt/modes.go:14-20): "raw" в любом регистре —
// raw, ВСЁ остальное — wg. Точное равенство в argv (roles/args.go:39) делает
// непривёденный регистр молчаливым переездом на WG-путь.
func TestWdttClientConnModeCoercesUnknown(t *testing.T) {
	for in, want := range map[string]string{
		"RAW":   "raw",
		" raw":  "raw",
		"WG":    "wg",
		"мусор": "wg",
		"":      "wg",
	} {
		r := wgClientBare()
		r.WdttClient.Mode = in
		if want == "raw" {
			r.WdttClient.NdmsIface, r.WdttClient.RawIface = "OpkgTun18", "opkgtun18"
		}
		c, err := storeRoundTrip(t, r).WdttClientConfig()
		if err != nil {
			t.Fatal(err)
		}
		if c.Mode != want {
			t.Errorf("connMode %q → %q, ждали %q", in, c.Mode, want)
		}
	}
}

// TestWdttServerDefaultsMatchOldWorld — wdtt/server.go:464-484
// (normalizeServerConfig), wdtt/types.go:200-208 (DefaultServerConfig),
// wdtt/access.go:39-45 (normalizePolicy).
func TestWdttServerDefaultsMatchOldWorld(t *testing.T) {
	r := Record{ID: "bare", Kind: KindWdttServer, Name: "S",
		WdttServer: &roles.WdttServerConfig{
			NdmsIface: "OpkgTun20", WgIface: "opkgtun20",
			RawNdmsIface: "OpkgTun21", RawIface: "opkgtun21",
		}}
	c, err := storeRoundTrip(t, r).WdttServerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "0.0.0.0:56002" {
		t.Errorf("listen = %q, старый мир подставлял 0.0.0.0:56002", c.Listen)
	}
	if c.WgPort != 56001 {
		t.Errorf("wgPort = %d, старый мир подставлял 56001", c.WgPort)
	}
	if c.NatMode != "full" {
		t.Errorf("natMode = %q, старый мир подставлял full", c.NatMode)
	}
	// Пустая политика — не синоним "none": managed.ApplyPolicyToInterface
	// (service_iface.go:26-28) отвергает её ошибкой, и весь ресурс ndms_access
	// сервера не применился бы вовсе — вместе с NAT, LAN-ACL и permit'ом.
	if c.Policy != "none" {
		t.Errorf("policy = %q, старый мир подставлял none (normalizePolicy)", c.Policy)
	}
	if c.RelayMode != "wg" {
		t.Errorf("relayMode = %q, старый мир подставлял wg", c.RelayMode)
	}
	// Паритет serverConfigDir (wdtt/server.go:362-366). Пустой путь — не
	// «дефолт бинаря», а отказ: писатель passwords.json fail-closed отвечает
	// «писать некуда» (proxyapp/wdttusers/users.go:200-206), и сервер,
	// созданный ручкой нового мира, не поднялся бы вовсе.
	if c.ConfigDir == "" {
		t.Error("configDir пуст: старый мир считал его от каталога данных на каждом старте")
	}
}

// Форма пути обязана совпасть с посевом (seed.go:437-440): иначе на апгрейде
// файл абонентов с уже выданными адресами «переехал» бы, и все клиенты
// получили бы новые IP.
func TestWdttServerConfigDirMatchesSeedForm(t *testing.T) {
	s := newStore(t)
	r := Record{ID: "srv7", Kind: KindWdttServer, Name: "S",
		WdttServer: &roles.WdttServerConfig{
			NdmsIface: "OpkgTun20", WgIface: "opkgtun20",
			RawNdmsIface: "OpkgTun21", RawIface: "opkgtun21",
		}}
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, _ := New(s.dir).Load()
	c, _ := st.Records[0].WdttServerConfig()
	want := filepath.Join(s.dir, "wdtt", "server", "srv7")
	if c.ConfigDir != want {
		t.Fatalf("configDir = %q, ждали %q (форма посева)", c.ConfigDir, want)
	}
}

func TestWdttServerExplicitValuesSurvive(t *testing.T) {
	r := Record{ID: "x", Kind: KindWdttServer, Name: "S",
		WdttServer: &roles.WdttServerConfig{
			Listen: "0.0.0.0:57002", WgPort: 57001,
			ConfigDir: "/opt/etc/awgm/custom",
			NatMode:   "none", Policy: "Policy0", RelayMode: "raw",
			NdmsIface: "OpkgTun20", WgIface: "opkgtun20",
			RawNdmsIface: "OpkgTun21", RawIface: "opkgtun21",
		}}
	c, err := storeRoundTrip(t, r).WdttServerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "0.0.0.0:57002" || c.WgPort != 57001 || c.NatMode != "none" ||
		c.Policy != "Policy0" || c.RelayMode != "raw" || c.ConfigDir != "/opt/etc/awgm/custom" {
		t.Fatalf("заданные значения затёрты дефолтом: %+v", c)
	}
}

// Паритет normalizeConnMode для relayMode сервера: старый мир приводил ЛЮБОЕ
// значение, кроме "raw", к "wg". Одного гарда пустоты мало — непривёденное
// значение роль отвергает валидацией, то есть сервер, приехавший из чужого
// импорта с "WG", встал бы в failed на пустом месте.
func TestWdttServerRelayModeCoercesUnknown(t *testing.T) {
	for in, want := range map[string]string{
		"RAW":   "raw",
		" raw ": "raw",
		"WG":    "wg",
		"мусор": "wg",
		"":      "wg",
	} {
		r := Record{ID: "rm", Kind: KindWdttServer, Name: "S",
			WdttServer: &roles.WdttServerConfig{
				RelayMode: in,
				NdmsIface: "OpkgTun20", WgIface: "opkgtun20",
				RawNdmsIface: "OpkgTun21", RawIface: "opkgtun21",
			}}
		c, err := storeRoundTrip(t, r).WdttServerConfig()
		if err != nil {
			t.Fatal(err)
		}
		if c.RelayMode != want {
			t.Errorf("relayMode %q → %q, ждали %q", in, c.RelayMode, want)
		}
	}
}

// TestFreeTurnClientDefaultsMatchOldWorld — freeturn/types.go:46-58
// (DefaultClientConfig, «mirrors the binary's own flag defaults»),
// freeturn/migrate.go:64-75 (migrateClientConfig — платформа).
func TestFreeTurnClientDefaultsMatchOldWorld(t *testing.T) {
	r := Record{ID: "bare", Kind: KindFreeTurnClient, Name: "FT",
		FreeTurnClient: &roles.FreeTurnClientConfig{
			Listen: "127.0.0.1:9002", Peer: "5.6.7.8:56000",
		}}
	c, err := storeRoundTrip(t, r).FreeTurnClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ field, got, want string }{
		{"provider", c.Provider, "vk"},
		{"transport", c.Transport, "tcp"},
		{"mode", c.Mode, "udp"},
		{"obfProfile", c.ObfProfile, "none"},
		{"platform", c.Platform, "desktop"},
		{"dnsMode", c.DNSMode, "auto"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, старый мир подставлял %q", tc.field, tc.got, tc.want)
		}
	}
	if c.Streams != 10 {
		t.Errorf("streams = %d, старый мир подставлял 10", c.Streams)
	}
	if c.StreamsPerCred != 10 {
		t.Errorf("streamsPerCred = %d, старый мир подставлял 10", c.StreamsPerCred)
	}
}

func TestFreeTurnClientExplicitValuesSurvive(t *testing.T) {
	r := Record{ID: "x", Kind: KindFreeTurnClient, Name: "FT",
		FreeTurnClient: &roles.FreeTurnClientConfig{
			Listen: "127.0.0.1:9002", Peer: "5.6.7.8:56000",
			Provider: "custom", Streams: 4, Transport: "udp", Mode: "tcp",
			ObfProfile: "rtpopus2", StreamsPerCred: 3, Platform: "mobile", DNSMode: "doh",
		}}
	c, err := storeRoundTrip(t, r).FreeTurnClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.Provider != "custom" || c.Streams != 4 || c.Transport != "udp" || c.Mode != "tcp" ||
		c.ObfProfile != "rtpopus2" || c.StreamsPerCred != 3 || c.Platform != "mobile" || c.DNSMode != "doh" {
		t.Fatalf("заданные значения затёрты дефолтом: %+v", c)
	}
}

// Платформа приводится и из пустоты, и из неизвестного — паритет
// migrateClientConfig, который старый мир гонял на КАЖДОЙ загрузке файла.
func TestFreeTurnClientPlatformCoercesUnknown(t *testing.T) {
	for in, want := range map[string]string{
		"MOBILE":  "mobile",
		" mobile": "mobile",
		"browser": "desktop",
		"":        "desktop",
	} {
		r := Record{ID: "p", Kind: KindFreeTurnClient, Name: "FT",
			FreeTurnClient: &roles.FreeTurnClientConfig{
				Listen: "127.0.0.1:9002", Peer: "5.6.7.8:56000", Platform: in}}
		c, err := storeRoundTrip(t, r).FreeTurnClientConfig()
		if err != nil {
			t.Fatal(err)
		}
		if c.Platform != want {
			t.Errorf("platform %q → %q, ждали %q", in, c.Platform, want)
		}
	}
}

// TestFreeTurnServerDefaultsMatchOldWorld — freeturn/types.go:78-84.
func TestFreeTurnServerDefaultsMatchOldWorld(t *testing.T) {
	r := Record{ID: "bare", Kind: KindFreeTurnServer, Name: "S",
		FreeTurnServer: &roles.FreeTurnServerConfig{}}
	c, err := storeRoundTrip(t, r).FreeTurnServerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "0.0.0.0:56000" {
		t.Errorf("listen = %q, старый мир подставлял 0.0.0.0:56000", c.Listen)
	}
	if c.Mode != "udp" {
		t.Errorf("mode = %q, старый мир подставлял udp", c.Mode)
	}
	if c.ObfProfile != "none" {
		t.Errorf("obfProfile = %q, старый мир подставлял none", c.ObfProfile)
	}
}

func TestFreeTurnServerExplicitValuesSurvive(t *testing.T) {
	r := Record{ID: "x", Kind: KindFreeTurnServer, Name: "S",
		FreeTurnServer: &roles.FreeTurnServerConfig{
			Listen: "0.0.0.0:57000", Mode: "tcp", ObfProfile: "rtpopus"}}
	c, err := storeRoundTrip(t, r).FreeTurnServerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "0.0.0.0:57000" || c.Mode != "tcp" || c.ObfProfile != "rtpopus" {
		t.Fatalf("заданные значения затёрты дефолтом: %+v", c)
	}
}

// Сценарий апгрейда: конфиг старого мира, у которого полей с дефолтами НЕТ
// вовсе (их не было в той версии либо пользователь их не трогал). Посев
// обязан отдать новые дефолты — иначе после обновления поведение клиента
// определял бы встроенный дефолт стороннего бинаря, а форма показывала бы
// «не задано» там, где значение было.
//
// Фикстуры нарочно НЕ содержат ни одного из нормализуемых ключей: конфиг с
// заполненными полями прошёл бы тест и без нормализации.
const bareOldWdttJSON = `{
  "version": 2,
  "clients": [{"id": "old", "name": "Старый", "config": {
    "enabled": true, "listen": "127.0.0.1:9007", "peer": "1.2.3.4:56000",
    "password": "pw", "vkHashes": "h"
  }}],
  "servers": [{"id": "olds", "name": "Старый сервер", "config": {
    "enabled": false, "password": "pw"
  }}]
}`

const bareOldFreeturnJSON = `{
  "version": 2,
  "clients": [{"id": "oldft", "name": "Старый FT", "config": {
    "enabled": false, "listen": "127.0.0.1:9008", "peer": "5.6.7.8:56000"
  }}],
  "servers": [{"id": "oldfts", "name": "Старый FT-сервер", "config": {
    "enabled": false, "connect": "127.0.0.1:1080"
  }}]
}`

func TestSeedFillsDefaultsForFieldsAbsentInOldConfig(t *testing.T) {
	e := newSeedEnv(t)
	writeFile(t, e.deps.WdttPath, bareOldWdttJSON)
	writeFile(t, e.deps.FreeturnPath, bareOldFreeturnJSON)

	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	recs := byKey(res)

	wc, err := recs["wdtt-client:old"].WdttClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	if wc.Obfs != "audio" || wc.Fingerprint != "chrome" || wc.CaptchaMode != "rjs" ||
		wc.VKAuthMode != "vkcalls" || wc.Mode != "wg" || wc.Workers <= 0 {
		t.Errorf("wdtt-клиент из старого конфига без полей: %+v", wc)
	}

	ws, err := recs["wdtt-server:olds"].WdttServerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if ws.Listen != "0.0.0.0:56002" || ws.WgPort != 56001 || ws.NatMode != "full" ||
		ws.Policy != "none" || ws.RelayMode != "wg" || ws.ConfigDir == "" {
		t.Errorf("wdtt-сервер из старого конфига без полей: %+v", ws)
	}

	fc, err := recs["freeturn-client:oldft"].FreeTurnClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	if fc.Provider != "vk" || fc.Streams != 10 || fc.Transport != "tcp" || fc.Mode != "udp" ||
		fc.ObfProfile != "none" || fc.StreamsPerCred != 10 || fc.Platform != "desktop" ||
		fc.DNSMode != "auto" {
		t.Errorf("freeturn-клиент из старого конфига без полей: %+v", fc)
	}

	fs, err := recs["freeturn-server:oldfts"].FreeTurnServerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if fs.Listen != "0.0.0.0:56000" || fs.Mode != "udp" || fs.ObfProfile != "none" {
		t.Errorf("freeturn-сервер из старого конфига без полей: %+v", fs)
	}
}
