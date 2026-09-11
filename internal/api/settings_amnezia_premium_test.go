package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Секреты фикстур: репозиторий публичный, поэтому значения заведомо
// нерабочие, а домены — из зарезервированного .test (RFC 2606).
const (
	testPremiumCipher  = "cipher-test-AAAA=="
	testPeerPrivKey    = "privkey-test-BBBB="
	testMirrorURL      = "https://mirror.test/cp?m-path=/ru"
	testManagedSrvKey  = "srvkey-test-CCCC="
	testManagedPeerKey = "peerkey-test-DDDD="
	testManagedPeerPSK = "psk-test-EEEE="
	testManagedSrvID   = "Wireguard9"
	testManagedPeerPub = "pubkey-test-FFFF="
)

// seedSettingsSecrets кладёт в стор весь ключевой материал, который ответы
// настроек обязаны снимать: шифротекст ключа подписки, приватный ключ пира
// системного сервера и ключи managed-сервера (своего и пира).
func seedSettingsSecrets(t *testing.T, store *storage.SettingsStore) {
	t.Helper()
	if err := store.Update(func(cur *storage.Settings) error {
		cur.AmneziaPremiumKeyCipher = testPremiumCipher
		return nil
	}); err != nil {
		t.Fatalf("seed cipher: %v", err)
	}
	if err := store.SetServerPeerSecret("srv-1", "pub-1", storage.ServerPeerSecret{
		PrivateKey: testPeerPrivKey,
	}); err != nil {
		t.Fatalf("seed peer secret: %v", err)
	}
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: testManagedSrvID,
		PrivateKey:    testManagedSrvKey,
		Peers: []storage.ManagedPeer{{
			PublicKey:    testManagedPeerPub,
			PrivateKey:   testManagedPeerKey,
			PresharedKey: testManagedPeerPSK,
		}},
	}); err != nil {
		t.Fatalf("seed managed server: %v", err)
	}
}

// assertNoSecretsInBody — проверка по ЗНАЧЕНИЯМ: переименование поля мимо
// неё не проскочит.
func assertNoSecretsInBody(t *testing.T, body string) {
	t.Helper()
	for _, s := range []struct{ name, value string }{
		{"шифротекст ключа подписки", testPremiumCipher},
		{"приватный ключ пира системного сервера", testPeerPrivKey},
		{"приватный ключ managed-сервера", testManagedSrvKey},
		{"приватный ключ пира managed-сервера", testManagedPeerKey},
		{"preshared-ключ пира managed-сервера", testManagedPeerPSK},
	} {
		if strings.Contains(body, s.value) {
			t.Errorf("%s в теле ответа: %s", s.name, body)
		}
	}
}

// assertSecretsStillStored — вычистка ответа не смеет трогать сохранённое.
func assertSecretsStillStored(t *testing.T, store *storage.SettingsStore) {
	t.Helper()
	snap, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.AmneziaPremiumKeyCipher != testPremiumCipher {
		t.Errorf("шифротекст в сторе = %q, want %q", snap.AmneziaPremiumKeyCipher, testPremiumCipher)
	}
	if got := snap.ServerPeerSecrets["srv-1"]["pub-1"].PrivateKey; got != testPeerPrivKey {
		t.Errorf("приватный ключ пира в сторе = %q, want %q", got, testPeerPrivKey)
	}
	if len(snap.ManagedServers) != 1 || len(snap.ManagedServers[0].Peers) != 1 {
		t.Fatalf("managed-серверы в сторе: %+v", snap.ManagedServers)
	}
	srv := snap.ManagedServers[0]
	if srv.PrivateKey != testManagedSrvKey {
		t.Errorf("приватный ключ managed-сервера в сторе = %q, want %q", srv.PrivateKey, testManagedSrvKey)
	}
	if got := srv.Peers[0].PrivateKey; got != testManagedPeerKey {
		t.Errorf("приватный ключ пира managed в сторе = %q, want %q", got, testManagedPeerKey)
	}
	if got := srv.Peers[0].PresharedKey; got != testManagedPeerPSK {
		t.Errorf("preshared-ключ пира managed в сторе = %q, want %q", got, testManagedPeerPSK)
	}
}

// Все ручки, отдающие настройки целиком, проходят одну вычистку: в теле нет
// ни шифротекста ключа подписки, ни ключевого материала пиров и
// managed-серверов, а пустой адрес зеркала подменён действующим дефолтом.
// Сохранённое при этом цело.
func TestSettingsResponses_StripSecretsAndFillMirror(t *testing.T) {
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

			rr := tc.call(h)
			if rr.Code != http.StatusOK {
				t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
			}
			assertNoSecretsInBody(t, rr.Body.String())

			data, _ := decodeJSONBody(t, rr)["data"].(map[string]any)
			if got, _ := data["amneziaPremiumMirrorUrl"].(string); got != storage.DefaultAmneziaMirrorURL {
				t.Errorf("адрес зеркала в ответе = %q, want дефолт %q", got, storage.DefaultAmneziaMirrorURL)
			}

			assertSecretsStillStored(t, store)
		})
	}
}

// Вычистка обязана вернуть КОПИЮ, а не править аргумент по месту: сегодня
// все три ручки подают ей снапшот, но ничто не мешает будущему вызывающему
// подать store.Get() — ЖИВОЙ объект кэша демона (так же делится памятью и
// черновик want из Update: он поверхностно скопирован с живого объекта и
// делит с ним карты и backing-массив ManagedServers). Правка по месту на
// этом пути стёрла бы ключи из памяти демона, а следующая запись настроек
// унесла бы пропажу на диск.
func TestSettingsForResponse_DoesNotMutateLiveStoreCache(t *testing.T) {
	_, store := newSettingsHandlerForTest(t)
	seedSettingsSecrets(t, store)

	live, err := store.Get()
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	out := settingsForResponse(live)
	if out.AmneziaPremiumKeyCipher != "" || out.ServerPeerSecrets != nil {
		t.Errorf("наружу отданы секреты: cipher=%q peers=%v",
			out.AmneziaPremiumKeyCipher, out.ServerPeerSecrets)
	}
	if len(out.ManagedServers) != 1 || len(out.ManagedServers[0].Peers) != 1 {
		t.Fatalf("managed-серверы в ответе: %+v", out.ManagedServers)
	}
	if srv := out.ManagedServers[0]; srv.PrivateKey != "" ||
		srv.Peers[0].PrivateKey != "" || srv.Peers[0].PresharedKey != "" {
		t.Errorf("наружу отданы ключи managed-сервера: %+v", srv)
	}

	// Живой кэш при этом не пострадал — иначе секреты пропали бы и из стора.
	assertSecretsStillStored(t, store)
}

// Шифротекст ключа подписки пишут только ручки premium: общий патч его
// игнорирует, на диске остаётся прежнее значение.
func TestUpdate_AmneziaPremiumKeyCipherNotPatchable(t *testing.T) {
	h, store := newSettingsHandlerForTest(t)
	seedSettingsSecrets(t, store)

	rr := perform(h.Update, http.MethodPost, "/settings/update",
		`{"amneziaPremiumKeyCipher":"cipher-test-ATTACKER=="}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	assertSecretsStillStored(t, store)
}

// Адрес зеркала: не-https отвергается и НЕ записывается, годный сохраняется
// с подрезанными пробелами.
func TestUpdate_AmneziaMirrorURLValidation(t *testing.T) {
	h, store := newSettingsHandlerForTest(t)

	rr := perform(h.Update, http.MethodPost, "/settings/update",
		`{"amneziaPremiumMirrorUrl":"http://mirror.test/cp"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("не-https: code=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "INVALID_AMNEZIA_MIRROR_URL") {
		t.Fatalf("нет кода INVALID_AMNEZIA_MIRROR_URL: %s", rr.Body.String())
	}
	snap, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.AmneziaPremiumMirrorURL != "" {
		t.Fatalf("отвергнутый адрес записан: %q", snap.AmneziaPremiumMirrorURL)
	}

	rr = perform(h.Update, http.MethodPost, "/settings/update",
		`{"amneziaPremiumMirrorUrl":"  `+testMirrorURL+`  "}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("годный адрес: code=%d body=%s", rr.Code, rr.Body.String())
	}
	snap, err = store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.AmneziaPremiumMirrorURL != testMirrorURL {
		t.Fatalf("сохранён %q, want %q", snap.AmneziaPremiumMirrorURL, testMirrorURL)
	}
	// Непустое значение отдаётся как есть, дефолт его не подменяет.
	data, _ := decodeJSONBody(t, rr)["data"].(map[string]any)
	if got, _ := data["amneziaPremiumMirrorUrl"].(string); got != testMirrorURL {
		t.Fatalf("в ответе %q, want %q", got, testMirrorURL)
	}
}

// Испорченный адрес, УЖЕ лежащий в хранилище (downgrade, ручная правка), не
// должен запирать страницу настроек — и уходит из файла сам.
//
// Проверяется тот круговорот, который единственно и бывает у реального
// фронта: страница шлёт обратно тело ответа ЦЕЛИКОМ
// (api.updateSettings({ ...settings, ... })), так что поле адреса в PATCH
// есть ВСЕГДА и всегда проходит валидацию. Эхо хранимого мусора давало бы
// 400 на каждое сохранение; наружу вместо него уходит дефолт
// (storage.EffectiveAmneziaMirrorURL), возвращается тем же PATCH-ем и
// схлопывается в пустое — мусор из settings.json вычищается сам.
func TestUpdate_StoredBrokenMirrorURL_RoundTripHeals(t *testing.T) {
	h, store := newSettingsHandlerForTest(t)
	if err := store.Update(func(cur *storage.Settings) error {
		cur.AmneziaPremiumMirrorURL = "не адрес вовсе"
		return nil
	}); err != nil {
		t.Fatalf("seed broken mirror: %v", err)
	}

	rr := perform(h.Get, http.MethodGet, "/settings/get", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("get: code=%d body=%s", rr.Code, rr.Body.String())
	}
	data, _ := decodeJSONBody(t, rr)["data"].(map[string]any)
	if got, _ := data["amneziaPremiumMirrorUrl"].(string); got != storage.DefaultAmneziaMirrorURL {
		t.Fatalf("в ответе эхо хранимого %q, want дефолт %q", got, storage.DefaultAmneziaMirrorURL)
	}

	// Тело ответа целиком обратно, как на любом щелчке тумблером.
	data["usageLevel"] = "expert"
	body, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rr = perform(h.Update, http.MethodPost, "/settings/update", string(body))
	if rr.Code != http.StatusOK {
		t.Fatalf("update: code=%d body=%s", rr.Code, rr.Body.String())
	}

	snap, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.UsageLevel != "expert" {
		t.Fatalf("usageLevel = %q, want expert", snap.UsageLevel)
	}
	if snap.AmneziaPremiumMirrorURL != "" {
		t.Fatalf("мусор остался в хранилище: %q", snap.AmneziaPremiumMirrorURL)
	}
}

// Круговорот «ответ → PATCH». Фронт сохраняет настройки ЦЕЛИКОМ
// (api.updateSettings({ ...settings, ... })), а ответ несёт ПОДСТАВЛЕННЫЙ
// действующий адрес зеркала. Без схлопывания на записи первый же щелчок
// любым тумблером прибил бы дефолтный литерал в settings.json, и смена
// зеркала в новом релизе не доехала бы до такого пользователя.
func TestUpdate_MirrorRoundTrip_KeepsStorageEmpty(t *testing.T) {
	h, store := newSettingsHandlerForTest(t)

	rr := perform(h.Get, http.MethodGet, "/settings/get", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("get: code=%d body=%s", rr.Code, rr.Body.String())
	}
	data, _ := decodeJSONBody(t, rr)["data"].(map[string]any)
	if got, _ := data["amneziaPremiumMirrorUrl"].(string); got != storage.DefaultAmneziaMirrorURL {
		t.Fatalf("в ответе %q, want дефолт %q", got, storage.DefaultAmneziaMirrorURL)
	}

	// Ровно то, что шлёт страница настроек: весь объект ответа обратно.
	body, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rr = perform(h.Update, http.MethodPost, "/settings/update", string(body))
	if rr.Code != http.StatusOK {
		t.Fatalf("update: code=%d body=%s", rr.Code, rr.Body.String())
	}

	snap, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.AmneziaPremiumMirrorURL != "" {
		t.Fatalf("дефолт прибит в хранилище: %q", snap.AmneziaPremiumMirrorURL)
	}
}

// Негодный адрес отвергается и НЕ записывается.
func TestUpdate_AmneziaMirrorURL_Rejected(t *testing.T) {
	cases := []struct {
		name string
		sent string
		// wantMsg — кусок текста отказа, когда он важен сам по себе.
		wantMsg string
	}{
		// user:pass@ лёг бы в settings.json (бэкап, поддержка), а начало
		// строки показывало бы знакомое имя вместо настоящего хоста.
		{name: "userinfo", sent: "https://u-test:p-test@mirror.test/cp"},
		{name: "пустой хост", sent: "https:///cp"},
		{name: "фрагмент", sent: "https://mirror.test/cp#anchor"},
		// Пустой фрагмент url.Parse не отличает от его отсутствия (признака
		// «решётка была» у url.URL нет), а решётка сохранилась бы в файле.
		{name: "пустой фрагмент", sent: "https://mirror.test/cp#"},
		{
			name: "длиннее предела",
			sent: "https://mirror.test/cp?m-path=/" + strings.Repeat("a", storage.MaxAmneziaMirrorURLLen),
		},
		// Предел считается в БАЙТАХ — ровно в них адрес уезжает в запрос и на
		// флеш. Символов здесь вдвое меньше предела, байт — больше; текст
		// отказа обязан называть ту же единицу, что считает код.
		{
			name:    "длиннее предела в байтах, но не в символах",
			sent:    "https://mirror.test/cp?m-path=/" + strings.Repeat("я", storage.MaxAmneziaMirrorURLLen/2),
			wantMsg: fmt.Sprintf("длиннее %d байт", storage.MaxAmneziaMirrorURLLen),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, store := newSettingsHandlerForTest(t)
			body, err := json.Marshal(map[string]string{"amneziaPremiumMirrorUrl": tc.sent})
			if err != nil {
				t.Fatal(err)
			}
			rr := perform(h.Update, http.MethodPost, "/settings/update", string(body))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "INVALID_AMNEZIA_MIRROR_URL") {
				t.Fatalf("нет кода INVALID_AMNEZIA_MIRROR_URL: %s", rr.Body.String())
			}
			if tc.wantMsg != "" && !strings.Contains(rr.Body.String(), tc.wantMsg) {
				t.Fatalf("в отказе нет %q: %s", tc.wantMsg, rr.Body.String())
			}
			snap, err := store.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if snap.AmneziaPremiumMirrorURL != "" {
				t.Fatalf("отвергнутый адрес записан: %q", snap.AmneziaPremiumMirrorURL)
			}
		})
	}
}

// Годный адрес принимается и приводится к хранимому виду: визуально пустое
// поле и присланный дефолт значат «зеркало по умолчанию» и хранятся пустыми
// (только пустое продолжает ротироваться с релизом), свой адрес — дословно.
// Путь и запрос законны: их содержит сам дефолт.
func TestUpdate_AmneziaMirrorURL_Accepted(t *testing.T) {
	cases := []struct {
		name string
		sent string
		want string
	}{
		{"одни пробелы", "   ", ""},
		{"путь и запрос", testMirrorURL, testMirrorURL},
		{"дефолтный адрес", storage.DefaultAmneziaMirrorURL, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, store := newSettingsHandlerForTest(t)
			body, err := json.Marshal(map[string]string{"amneziaPremiumMirrorUrl": tc.sent})
			if err != nil {
				t.Fatal(err)
			}
			rr := perform(h.Update, http.MethodPost, "/settings/update", string(body))
			if rr.Code != http.StatusOK {
				t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
			}
			snap, err := store.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if snap.AmneziaPremiumMirrorURL != tc.want {
				t.Fatalf("в хранилище %q, want %q", snap.AmneziaPremiumMirrorURL, tc.want)
			}
		})
	}
}
