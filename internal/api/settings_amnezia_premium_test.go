package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Секреты фикстур: репозиторий публичный, поэтому значения заведомо
// нерабочие, а домены — из зарезервированного .test (RFC 2606).
const (
	testPremiumCipher = "cipher-test-AAAA=="
	testPeerPrivKey   = "privkey-test-BBBB="
	testMirrorURL     = "https://mirror.test/cp?m-path=/ru"
)

// seedSettingsSecrets кладёт в стор оба секрета, которые ответы настроек
// обязаны снимать: шифротекст ключа подписки и приватный ключ пира сервера.
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
}

// Все ручки, отдающие настройки целиком, проходят одну вычистку: в теле нет
// ни шифротекста ключа подписки, ни приватных ключей пиров (проверка по
// ЗНАЧЕНИЮ — переименование поля мимо неё не проскочит), а пустой адрес
// зеркала подменён действующим дефолтом. Сохранённое при этом цело.
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
			body := rr.Body.String()
			if strings.Contains(body, testPremiumCipher) {
				t.Errorf("шифротекст ключа подписки в теле ответа: %s", body)
			}
			if strings.Contains(body, testPeerPrivKey) {
				t.Errorf("приватный ключ пира в теле ответа: %s", body)
			}

			data, _ := decodeJSONBody(t, rr)["data"].(map[string]any)
			if got, _ := data["amneziaPremiumMirrorUrl"].(string); got != storage.DefaultAmneziaMirrorURL {
				t.Errorf("адрес зеркала в ответе = %q, want дефолт %q", got, storage.DefaultAmneziaMirrorURL)
			}

			assertSecretsStillStored(t, store)
		})
	}
}

// Запасная ветка Update отдаёт ЧЕРНОВИК — поверхностную копию живого кэша
// стора, делящую с ним карты. Вычистка обязана вернуть копию, а не править
// черновик по месту: иначе ответ на /settings/update стирал бы приватные
// ключи пиров из памяти демона. Через HTTP эту ветку не достать (Snapshot
// после успешной записи не отказывает), поэтому черновик собирается здесь
// ровно так же, как его собирает Update.
func TestSettingsForResponse_DraftSharesMapsWithStore(t *testing.T) {
	_, store := newSettingsHandlerForTest(t)
	seedSettingsSecrets(t, store)

	live, err := store.Get()
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	want := *live // так Update строит черновик

	out := settingsForResponse(&want)
	if out.AmneziaPremiumKeyCipher != "" || out.ServerPeerSecrets != nil {
		t.Errorf("черновик отдан наружу с секретами: cipher=%q peers=%v",
			out.AmneziaPremiumKeyCipher, out.ServerPeerSecrets)
	}
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
// должен запирать сохранение остальных настроек: валидируется только
// присланное поле.
func TestUpdate_StoredBrokenMirrorURL_DoesNotBlockOtherSettings(t *testing.T) {
	h, store := newSettingsHandlerForTest(t)
	if err := store.Update(func(cur *storage.Settings) error {
		cur.AmneziaPremiumMirrorURL = "не адрес вовсе"
		return nil
	}); err != nil {
		t.Fatalf("seed broken mirror: %v", err)
	}

	rr := perform(h.Update, http.MethodPost, "/settings/update", `{"usageLevel":"expert"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	snap, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.UsageLevel != "expert" {
		t.Fatalf("usageLevel = %q, want expert", snap.UsageLevel)
	}
}
