package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Страж инварианта «не показали — значит не потеряли».
//
// Состав ответа (белый список SettingsData) и запись (storage.SettingsPatch) —
// два независимых решения, и их рассогласование ничем больше не ловится. А
// страница настроек шлёт обратно ВЕСЬ объект ответа
// (api.updateSettings({ ...settings, ... })), поэтому поле, которое ответ не
// показывает, обязано пережить такой круговорот нетронутым. Раньше здесь
// ломались ключи managed-серверов: ответ их вычищал, а патч принимал — и
// первый же щелчок тумблером затирал их пустыми.
//
// Проверка идёт на РЕАЛЬНЫХ ручках и по РЕЗУЛЬТАТУ: сравнивается всё
// хранимое целиком, а не перечень полей. Поле, добавленное завтра в
// storage.Settings мимо белого списка, попадает под проверку само.
func TestSettingsRoundTrip_ResponseBodyPatchedBack_KeepsSecrets(t *testing.T) {
	h, store := newSettingsHandlerForTest(t)

	// Ключевой материал, которого ответ не показывает вовсе.
	seedSettingsSecrets(t, store)
	// Плюс backend-managed запись владения и прочие поля вне белого списка:
	// потерять их так же нельзя, а секретами они не являются.
	if err := store.Update(func(cur *storage.Settings) error {
		cur.ServerInterfaces = []string{testManagedSrvID}
		cur.ManagedPolicies = []string{"Policy0"}
		cur.ServerInterfaceMeta = map[string]storage.ServerInterfaceMeta{
			testManagedSrvID: {NATStaticWAN: "ISP"},
		}
		cur.SingboxManuallyStopped = true
		cur.ManagedPeerAllowIPsMigrated = true
		cur.OpkgTun = &storage.OpkgTunState{
			Mode: storage.OpkgTunModePolicyTun, Provisioned: true, Index: 17,
		}
		cur.DNSChainPreset = &storage.DNSChainPresetState{Mode: "resilient"}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

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
