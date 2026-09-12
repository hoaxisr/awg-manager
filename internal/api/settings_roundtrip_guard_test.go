package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Страж инварианта «ответ настроек не теряет хранимое».
//
// Состав ответа (белый список SettingsData) и запись (storage.SettingsPatch) —
// два независимых решения, и их рассогласование ничем больше не ловится. А
// страница настроек шлёт обратно ВЕСЬ объект ответа
// (api.updateSettings({ ...settings, ... })), поэтому цена ошибки в DTO — не
// «поле не видно в интерфейсе», а СТИРАНИЕ хранимого на первом же сохранении:
//   - забыли присвоить поле — ответ отдаёт нулевое значение, круговорот
//     кладёт его в файл (authEnabled:false выключает авторизацию панели);
//   - перепутали источник — в файл уезжает чужое значение;
//   - добавили поле во вложенную структуру storage мимо DTO — блок
//     (server, pingCheck, updates, dnsRoute, geoFile) патчится ЦЕЛИКОМ
//     (applyStructPatch, internal/storage/patch.go), и поле обнуляется.
//
// Проверка идёт на РЕАЛЬНЫХ ручках и по РЕЗУЛЬТАТУ: сравнивается всё
// хранимое целиком, а не перечень полей. Поле, добавленное завтра в
// storage.Settings мимо белого списка, попадает под проверку само — фикстура
// заполняет дерево рефлексией.
func TestSettingsRoundTrip_ResponseBodyPatchedBack_KeepsSecrets(t *testing.T) {
	h, store := newSettingsHandlerForTest(t)
	// Маршрут загрузок в фикстуре не "direct" — иначе routeTag и routeKind
	// оказались бы одинаковыми ("direct" схлопывает deriveSettingsHead) и
	// перепутанный источник в этом блоке остался бы невидимым.
	h.SetDownloadService(downloader.NewService(downloader.Deps{
		Outbounds: testDownloadOutboundsProvider{items: []downloader.Outbound{
			{Tag: testFixtureRouteTag, Kind: testFixtureRouteKind, Label: "AWG 7", Available: true},
		}},
	}))

	// Всё дерево настроек — нетипичными и РАЗНЫМИ значениями.
	seedWholeSettingsTree(t, store)
	// Поверх — ключевой материал под известными именами: отказ должен уметь
	// назвать его прямо, а не только показать расхождение двух JSON.
	seedSettingsSecrets(t, store)

	before := storedSettingsJSON(t, store)

	rr := perform(h.Get, http.MethodGet, "/settings/get", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("get: code=%d body=%s", rr.Code, rr.Body.String())
	}
	data, _ := decodeJSONBody(t, rr)["data"].(map[string]any)
	body, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Ровно то, что делает страница настроек на любом тумблере: весь объект
	// ответа уходит обратно в PATCH.
	rr = perform(h.Update, http.MethodPost, "/settings/update", string(body))
	if rr.Code != http.StatusOK {
		t.Fatalf("update: code=%d body=%s", rr.Code, rr.Body.String())
	}

	if after := storedSettingsJSON(t, store); after != before {
		t.Errorf("круговорот ответ→PATCH изменил хранимое:\nбыло:  %s\nстало: %s", before, after)
	}
	// Отдельно и по именам — чтобы отказ сразу называл ключевой материал.
	assertSecretsStillStored(t, store)
}

func storedSettingsJSON(t *testing.T, store *storage.SettingsStore) string {
	t.Helper()
	snap, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("маршал хранимого: %v", err)
	}
	return string(b)
}

// Маршрут загрузок фикстуры: тег и вид намеренно РАЗНЫЕ.
const (
	testFixtureRouteTag  = "awg-7"
	testFixtureRouteKind = "awg"
)

// seedWholeSettingsTree кладёт в стор настройки, у которых заполнен КАЖДЫЙ
// сериализуемый лист — включая поля, которых ответ не показывает.
//
// Зачем рефлексия, а не литерал: страж выше сравнивает хранимое до и после,
// и слепнет ровно на тех значениях, что фикстура оставила нулевыми или
// одинаковыми. Нулевой bool не отличить от «поле забыли в DTO» (ответ отдаст
// тот же false), а два соседних поля с одинаковым значением не отличить от
// перепутанного источника. Литерал же не покрывает поля, добавленные
// завтра, — а именно они и опасны: поле, появившееся в storage.ServerSettings
// мимо DTO, стирается на первом сохранении, потому что блок патчится целиком.
//
// Правила значений: bool — всегда true (нулевое значение маскировало бы
// забытое присваивание), числа — свои у каждого поля, строки — с именем
// поля внутри. Поля, чей формат проверяется на записи, берут значение из
// settingsFixtureOverrides: там оно тоже нетипичное, но проходит валидацию.
func seedWholeSettingsTree(t *testing.T, store *storage.SettingsStore) {
	t.Helper()
	f := &settingsTreeFiller{t: t, overrides: settingsFixtureOverrides(), used: map[string]bool{}}
	var seeded storage.Settings
	f.fill(reflect.ValueOf(&seeded).Elem(), "Settings")
	for path := range f.overrides {
		if !f.used[path] {
			t.Fatalf("значение фикстуры для %s не применено: поле переименовано или исчезло", path)
		}
	}
	if err := store.Update(func(cur *storage.Settings) error {
		*cur = seeded
		return nil
	}); err != nil {
		t.Fatalf("seed дерева настроек: %v", err)
	}
}

// settingsFixtureOverrides — значения для полей, чей формат проверяется на
// записи (deriveSettingsHead/Tail). Общее правило фикстуры дало бы им мусор,
// и круговорот отвечал бы 400 вместо проверки инварианта. Значения всё равно
// нетипичные — совпадение с дефолтом скрыло бы забытое присваивание.
func settingsFixtureOverrides() map[string]any {
	return map[string]any{
		"Settings.SessionTtlHours":                 137, // 1..720
		"Settings.PingCheck.Defaults.Target":       "9.9.9.9",
		"Settings.ConnectivityCheckURL":            "http://probe.test/generate_204",
		"Settings.UsageLevel":                      "advanced",
		"Settings.Logging.SingboxLogLevel":         "trace",
		"Settings.Updates.AutoInstallIntervalDays": 23, // 1..30
		"Settings.Updates.AutoInstallTime":         "19:41",
		"Settings.SingboxBootstrapDNS":             "172.31.0.7", // только литеральный IP
		"Settings.AmneziaPremiumMirrorURL":         testMirrorURL,
		"Settings.Download.RouteTag":               testFixtureRouteTag,
		"Settings.Download.RouteKind":              testFixtureRouteKind,
	}
}

type settingsTreeFiller struct {
	t         *testing.T
	n         int
	overrides map[string]any
	used      map[string]bool
}

func (f *settingsTreeFiller) next() int {
	f.n++
	return f.n
}

// fill заполняет значение по месту. path — путь от корня Settings, он же
// ключ overrides и он же делает значения различимыми между собой.
func (f *settingsTreeFiller) fill(v reflect.Value, path string) {
	f.t.Helper()
	if ov, ok := f.overrides[path]; ok {
		val := reflect.ValueOf(ov)
		if val.Type() != v.Type() {
			f.t.Fatalf("значение фикстуры для %s имеет тип %s, а поле — %s", path, val.Type(), v.Type())
		}
		v.Set(val)
		f.used[path] = true
		return
	}
	switch v.Kind() {
	case reflect.Bool:
		// Всегда true: false не отличить от забытого в DTO поля.
		v.SetBool(true)
	case reflect.String:
		v.SetString(fmt.Sprintf("fx-%s-%d", path, f.next()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(1000 + f.next()))
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		f.fill(p.Elem(), path)
		v.Set(p)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		f.fill(s.Index(0), path+"[0]")
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		key := reflect.New(v.Type().Key()).Elem()
		f.fill(key, path+".<ключ>")
		val := reflect.New(v.Type().Elem()).Elem()
		f.fill(val, path+".<значение>")
		m.SetMapIndex(key, val)
		v.Set(m)
	case reflect.Struct:
		f.fillStruct(v, path)
	default:
		// Отказ закрытый: поле типа, которого фикстура не умеет заполнять,
		// осталось бы нулевым и молча выключило бы проверку для себя.
		f.t.Fatalf("фикстура не умеет заполнять %s (%s)", path, v.Type())
	}
}

func (f *settingsTreeFiller) fillStruct(v reflect.Value, path string) {
	f.t.Helper()
	typ := v.Type()
	filled := 0
	for i := 0; i < typ.NumField(); i++ {
		ft := typ.Field(i)
		if !ft.IsExported() {
			continue
		}
		// json:"-" в файл не попадает, сравнивать там нечего.
		if strings.Split(ft.Tag.Get("json"), ",")[0] == "-" {
			continue
		}
		f.fill(v.Field(i), path+"."+ft.Name)
		filled++
	}
	if filled == 0 {
		f.t.Fatalf("фикстуре нечего заполнить в %s (%s)", path, typ)
	}
}
