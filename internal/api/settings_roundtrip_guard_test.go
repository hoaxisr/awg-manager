package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Страж инварианта «снято на чтении — значит непатчабельно».
//
// Вычистка ответа (settingsForResponse) и запись (storage.SettingsPatch) —
// два независимых решения, и их рассогласование само по себе ничем не
// ловится. А страница настроек шлёт обратно ВЕСЬ объект ответа
// (api.updateSettings({ ...settings, ... })), поэтому вычищенное поле,
// оставшееся патчабельным, возвращается PATCH-ем пустым и затирает
// настоящее значение: утечка превращается в потерю данных. Ровно это
// случилось с ключами managed-серверов.
//
// Проверка идёт на РЕАЛЬНЫХ ручках и по РЕЗУЛЬТАТУ, а не по спискам полей:
// секретные поля Settings заполняются маркерами через тот же обход, что и в
// TestSettingsResponse_SecretFieldsAreClassified, поэтому поле, добавленное
// завтра в вычистку мимо nonPatchableSettings, роняет этот тест без правки
// каких-либо перечней.
//
// Красным он становится там, где вычищенное значение ВСЁ РАВНО едет в теле:
// у поля внутри сериализуемого контейнера (элемент managedServers, поле
// блока server). Одиночный скаляр верхнего уровня с omitempty защищён сам:
// пустым он из тела исчезает, и патч его не касается.
func TestSettingsRoundTrip_ResponseBodyPatchedBack_KeepsSecrets(t *testing.T) {
	h, store := newSettingsHandlerForTest(t)

	found := newSecretFields()
	if err := store.Update(func(cur *storage.Settings) error {
		return fillSecretMarkers(reflect.ValueOf(cur).Elem(), "Settings", found, 0)
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	marks := found.marks
	if len(marks) == 0 {
		t.Fatal("в дереве Settings не нашлось ни одного секретного поля — обход сломан")
	}
	// Фикстура обязана ДОЕХАТЬ до хранилища: иначе проверка ниже зеленела бы
	// на пустом месте — страж стерёг бы сам себя.
	assertMarkersStored(t, store, marks, "после засева")

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

	assertMarkersStored(t, store, marks, "после круговорота ответ→PATCH")
}

// assertMarkersStored — маркер ищется по ЗНАЧЕНИЮ во всём хранимом JSON, а не
// по пути: миграции стора вправе переложить значение в другое поле (легаси
// managedServer переезжает в managedServers), и это не потеря.
func assertMarkersStored(t *testing.T, store *storage.SettingsStore, marks map[string]string, when string) {
	t.Helper()
	snap, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	stored, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("маршал хранимого: %v", err)
	}
	for path, marker := range marks {
		if !strings.Contains(string(stored), marker) {
			t.Errorf("%s: значение %s пропало из хранилища (маркер %q)", when, path, marker)
		}
	}
}
