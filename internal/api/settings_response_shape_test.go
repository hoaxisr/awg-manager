package api

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// settingsResponseKeys — ПОЛНЫЙ состав тела ответа настроек.
//
// Откуда взялся: рукописный тип фронта frontend/src/lib/types/system.ts
// (export interface Settings) — он и есть контракт потребителя — плюс apiKey,
// который страница настроек показывает и даёт скопировать.
//
// В списке НЕТ полей managedServers, serverPeerSecrets, serverInterfaceMeta,
// amneziaPremiumKeyCipher, singboxRouter, opkgTun, dnsChainPreset и прочей
// backend-managed записи владения: фронт их не читает, а половина несёт
// ключевой материал.
//
// Нет и amneziaPremiumMirrorUrl: адрес зеркала Amnezia принадлежит мастеру
// premium — его читают и пишут ручки premium, а общая страница настроек его
// не показывает и никогда не читала.
//
// Отсутствует и hiddenSystemTunnels, объявленный в рукописном типе фронта:
// поля с таким именем нет ни в storage.Settings, ни в SettingsPatch, и ни
// один компонент фронта его не читает — объявление мёртвое, отдавать нечего.
var settingsResponseKeys = []string{
	"schemaVersion",
	"authEnabled",
	"sessionTtlHours",
	"entwareAuthEnabled",
	"mcpEnabled",
	"apiKey",
	"server",
	"pingCheck",
	"logging",
	"monitoringExcludedTunnels",
	"disableMemorySaving",
	"updates",
	"download",
	"dnsRoute",
	"geoFile",
	"connectivityCheckUrl",
	"usageLevel",
	"singboxBootstrapDNS",
	"singboxClashPort",
}

// seedAllResponseFields наполняет КАЖДОЕ поле белого списка непустым
// значением. Без этого поля с omitempty исчезли бы из тела, и проверка
// состава зеленела бы на неполном ответе — то есть стерегла бы сама себя.
func seedAllResponseFields(t *testing.T, store *storage.SettingsStore) {
	t.Helper()
	if err := store.Update(func(cur *storage.Settings) error {
		cur.ApiKey = "apikey-test-0000-0000"
		cur.MonitoringExcludedTunnels = []string{"tn-1"}
		cur.SingboxBootstrapDNS = "8.8.8.8"
		cur.SingboxClashPort = 9099
		cur.Server.Interfaces = []string{"br0"}
		cur.Download.RouteKind = "direct"
		cur.DNSRoute.RefreshMode = "interval"
		cur.DNSRoute.RefreshDailyTime = "03:00"
		cur.GeoFile.RefreshMode = "interval"
		cur.GeoFile.RefreshDailyTime = "03:00"
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// Состав тела ответа зафиксирован: ни одного ключа сверх белого списка и ни
// одного пропавшего. Поле, добавленное в SettingsData (или убранное из неё),
// роняет этот тест — иначе состав уехал бы незаметно, как уехал в своё время
// serverPeerSecrets.
func TestSettingsResponse_TopLevelKeysAreWhitelisted(t *testing.T) {
	cases := []struct {
		name string
		call func(*SettingsHandler) *httptest.ResponseRecorder
	}{
		{"Get", func(h *SettingsHandler) *httptest.ResponseRecorder {
			return perform(h.Get, http.MethodGet, "/settings/get", "")
		}},
		{"Update", func(h *SettingsHandler) *httptest.ResponseRecorder {
			return perform(h.Update, http.MethodPost, "/settings/update", `{"usageLevel":"expert"}`)
		}},
		{"RegenerateApiKey", func(h *SettingsHandler) *httptest.ResponseRecorder {
			return perform(h.RegenerateApiKey, http.MethodPost, "/settings/regenerate-api-key", "")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, store := newSettingsHandlerForTest(t)
			seedSettingsSecrets(t, store)
			seedAllResponseFields(t, store)

			rr := tc.call(h)
			if rr.Code != http.StatusOK {
				t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
			}
			data, _ := decodeJSONBody(t, rr)["data"].(map[string]any)
			if len(data) == 0 {
				t.Fatalf("пустое тело: %s", rr.Body.String())
			}

			got := make([]string, 0, len(data))
			for k := range data {
				got = append(got, k)
			}
			want := slices.Clone(settingsResponseKeys)
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("состав тела ответа = %v,\nwant %v", got, want)
			}
		})
	}
}

// Тот же список — по ОБЪЯВЛЕНИЮ SettingsData. Проверка по телу не видит поле
// с omitempty, оставшееся пустым на фикстуре, а в бою такое поле уедет
// наружу: новое поле в DTO обязано быть замечено независимо от значения.
func TestSettingsData_DeclaredFieldsMatchWhitelist(t *testing.T) {
	dto := reflect.TypeOf(SettingsData{})
	got := make([]string, 0, dto.NumField())
	for i := 0; i < dto.NumField(); i++ {
		tag := strings.Split(dto.Field(i).Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			t.Fatalf("поле %s объявлено без json-тега — в теле оно окажется под "+
				"именем поля Go, мимо белого списка", dto.Field(i).Name)
		}
		got = append(got, tag)
	}
	want := slices.Clone(settingsResponseKeys)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("поля SettingsData = %v,\nwant %v", got, want)
	}
}

// Фронт не сломан: каждое поле рукописного типа Settings
// (frontend/src/lib/types/system.ts) в теле ответа есть. Список ниже —
// ручная копия этого типа; расхождение с ним фронт увидит у себя в
// `npm run check`, а здесь ловится обратная сторона — поле, забытое в DTO.
func TestSettingsResponse_CoversHandwrittenFrontendType(t *testing.T) {
	// Точная копия полей `export interface Settings` за вычетом
	// hiddenSystemTunnels: у него нет ни хранилища, ни читателя (см. шапку
	// settingsResponseKeys).
	frontendFields := []string{
		"schemaVersion", "authEnabled", "sessionTtlHours", "entwareAuthEnabled",
		"mcpEnabled", "apiKey", "server", "pingCheck", "logging",
		"disableMemorySaving", "updates", "download", "dnsRoute", "geoFile",
		"connectivityCheckUrl", "usageLevel", "monitoringExcludedTunnels",
		"singboxBootstrapDNS", "singboxClashPort",
	}

	h, store := newSettingsHandlerForTest(t)
	seedAllResponseFields(t, store)

	rr := perform(h.Get, http.MethodGet, "/settings/get", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	data, _ := decodeJSONBody(t, rr)["data"].(map[string]any)
	for _, f := range frontendFields {
		if _, ok := data[f]; !ok {
			t.Errorf("поле %s читает фронт, а в теле ответа его нет: %s", f, rr.Body.String())
		}
	}
}
