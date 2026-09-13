package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
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
	// Ищем по имени, а не по индексу: в сторе могут лежать и другие
	// серверы — например, заполненные фикстурой стража круговорота.
	idx := slices.IndexFunc(snap.ManagedServers, func(s storage.ManagedServer) bool {
		return s.InterfaceName == testManagedSrvID
	})
	if idx < 0 {
		t.Fatalf("managed-сервер %s исчез из стора: %+v", testManagedSrvID, snap.ManagedServers)
	}
	srv := snap.ManagedServers[idx]
	pidx := slices.IndexFunc(srv.Peers, func(p storage.ManagedPeer) bool {
		return p.PublicKey == testManagedPeerPub
	})
	if pidx < 0 {
		t.Fatalf("пир %s исчез из стора: %+v", testManagedPeerPub, srv.Peers)
	}
	if srv.PrivateKey != testManagedSrvKey {
		t.Errorf("приватный ключ managed-сервера в сторе = %q, want %q", srv.PrivateKey, testManagedSrvKey)
	}
	if got := srv.Peers[pidx].PrivateKey; got != testManagedPeerKey {
		t.Errorf("приватный ключ пира managed в сторе = %q, want %q", got, testManagedPeerKey)
	}
	if got := srv.Peers[pidx].PresharedKey; got != testManagedPeerPSK {
		t.Errorf("preshared-ключ пира managed в сторе = %q, want %q", got, testManagedPeerPSK)
	}
}

// Все ручки, отдающие настройки целиком, проходят одну вычистку: в теле нет
// ни шифротекста ключа подписки, ни ключевого материала пиров и
// managed-серверов. Сохранённое при этом цело.
func TestSettingsResponses_StripSecrets(t *testing.T) {
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
			assertSecretsStillStored(t, store)
		})
	}
}

// Сборка ответа только ЧИТАЕТ аргумент. Проверяется на ЖИВОМ объекте кэша
// демона (store.Get(), а не снапшот): сегодня все три ручки подают снапшот,
// но так же делится памятью и черновик want из Update — он поверхностно
// скопирован с живого объекта и делит с ним карты и backing-массив
// ManagedServers. Правка по месту на этом пути стёрла бы ключи из памяти
// демона, а следующая запись настроек унесла бы пропажу на диск.
func TestSettingsResponse_DoesNotMutateLiveStoreCache(t *testing.T) {
	_, store := newSettingsHandlerForTest(t)
	seedSettingsSecrets(t, store)

	live, err := store.Get()
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	body, err := json.Marshal(settingsResponse(live))
	if err != nil {
		t.Fatalf("маршал ответа: %v", err)
	}
	assertNoSecretsInBody(t, string(body))

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

// Адрес зеркала через общий патч настроек НЕ пишется: поле принадлежит
// мастеру premium и в SettingsPatch его нет вовсе (nonPatchableSettings).
// Присланное значение молча игнорируется — patch-семантика, как у
// singboxRouter.routingMode, — а хранимое остаётся прежним.
func TestUpdate_AmneziaMirrorURL_IgnoredByGenericPatch(t *testing.T) {
	h, store := newSettingsHandlerForTest(t)
	if err := store.Update(func(cur *storage.Settings) error {
		cur.AmneziaPremiumMirrorURL = testMirrorURL
		return nil
	}); err != nil {
		t.Fatalf("seed mirror: %v", err)
	}

	rr := perform(h.Update, http.MethodPost, "/settings/update",
		`{"amneziaPremiumMirrorUrl":"https://mirror-attacker.test/cp"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	snap, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.AmneziaPremiumMirrorURL != testMirrorURL {
		t.Fatalf("общий патч переписал адрес зеркала: %q", snap.AmneziaPremiumMirrorURL)
	}
}

// Хранимый адрес зеркала переживает круговорот «ответ → PATCH».
//
// Поле ушло из общего ответа настроек — оно принадлежит мастеру premium, — а
// страница настроек шлёт обратно тело ответа ЦЕЛИКОМ. Раз поля в теле нет,
// патч его не присылает, и хранимое обязано остаться нетронутым: иначе
// первый же щелчок любым тумблером стирал бы адрес, вписанный через мастер.
func TestUpdate_StoredMirrorURL_SurvivesResponseRoundTrip(t *testing.T) {
	h, store := newSettingsHandlerForTest(t)
	if err := store.Update(func(cur *storage.Settings) error {
		cur.AmneziaPremiumMirrorURL = testMirrorURL
		return nil
	}); err != nil {
		t.Fatalf("seed mirror: %v", err)
	}

	rr := perform(h.Get, http.MethodGet, "/settings/get", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("get: code=%d body=%s", rr.Code, rr.Body.String())
	}
	data, _ := decodeJSONBody(t, rr)["data"].(map[string]any)
	if _, ok := data["amneziaPremiumMirrorUrl"]; ok {
		t.Errorf("адрес зеркала в общем ответе настроек: %s", rr.Body.String())
	}

	// Ровно то, что шлёт страница настроек на любом тумблере.
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
	if snap.AmneziaPremiumMirrorURL != testMirrorURL {
		t.Fatalf("адрес после круговорота = %q, want %q", snap.AmneziaPremiumMirrorURL, testMirrorURL)
	}
}

// Частичный патч поверх мусора, уже лежащего в хранилище, обязан проходить:
// валидируем ТОЛЬКО присланное. Это не гипотетический сценарий — страница
// настроек шлёт такие патчи сама: selectDownloadRoute отправляет один блок
// {download:{…}}, savePingTargetsSettings — {pingCheck, connectivityCheckUrl},
// оба без ...settings (frontend/src/routes/settings/+page.svelte). Сделай
// валидацию адреса зеркала безусловной — и любая из этих кнопок начнёт
// отвечать 400 у всякого, у кого в settings.json лежит испорченный адрес.
func TestUpdate_StoredBrokenMirrorURL_DoesNotBlockPartialPatch(t *testing.T) {
	h, store := newSettingsHandlerForTest(t)
	const broken = "не адрес вовсе"
	if err := store.Update(func(cur *storage.Settings) error {
		cur.AmneziaPremiumMirrorURL = broken
		return nil
	}); err != nil {
		t.Fatalf("seed broken mirror: %v", err)
	}

	// Ровно то, что шлёт selectDownloadRoute.
	rr := perform(h.Update, http.MethodPost, "/settings/update",
		`{"download":{"routeTag":"direct","routeKind":"direct"}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("частичный патч отвергнут: code=%d body=%s", rr.Code, rr.Body.String())
	}

	snap, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Download.RouteTag != "direct" {
		t.Fatalf("маршрут загрузок не сохранён: %+v", snap.Download)
	}
	// Неприсланное поле патч не трогает: мусор уйдёт из файла только с
	// явно присланным значением (TestUpdate_StoredBrokenMirrorURL_LogsReplacementOnce).
	if snap.AmneziaPremiumMirrorURL != broken {
		t.Fatalf("неприсланное поле изменено: %q", snap.AmneziaPremiumMirrorURL)
	}
}
