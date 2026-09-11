package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Страж «секрет не уходит в ответ настроек». Появился после того, как
// ServerPeerSecrets годами лежало в nonPatchableSettings (то есть было
// классифицировано как секрет по ЗАПИСИ) и всё это время уезжало наружу по
// ЧТЕНИЮ: TestSettingsPatchMirrorsSettings заставляет классифицировать новое
// поле Settings для патча, но про выдачу наружу не говорит ничего.
//
// Проверяется не список, а РЕЗУЛЬТАТ: дерево Settings заполняется
// маркерами по каждому строковому полю с секретным именем, и в теле того,
// что вернула settingsForResponse, маркеров быть не должно.

// secretNameMarkers — признаки секрета в ИМЕНИ поля. Список намеренно грубый:
// его дело — поймать поле, добавленное завтра, а не описать сегодняшние. На
// ложное срабатывание есть intentionallyExposedSecrets.
var secretNameMarkers = []string{"Key", "Secret", "Cipher", "Password", "Token"}

// strippedSecrets — поля, которые settingsForResponse ОБЯЗАНА снять. Ключ —
// путь по именам полей от корня Settings (индексы срезов и ключи карт в путь
// не входят).
var strippedSecrets = map[string]string{
	// Шифротекст ключа подписки Amnezia Premium: читает только
	// premium-линия, расшифровывая DeviceCipher.
	"Settings.AmneziaPremiumKeyCipher": "ключ подписки Amnezia Premium",
	// Приватные и preshared ключи пиров встроенных/помеченных серверов:
	// NDMS их не хранит, хранит наш settings.json.
	"Settings.ServerPeerSecrets.PrivateKey":   "ключ пира системного сервера",
	"Settings.ServerPeerSecrets.PresharedKey": "psk пира системного сервера",
	// Ключевой материал managed-серверов. Собственная ручка серверов его не
	// отдаёт (у ManagedServerDTO поля PrivateKey нет вовсе); исключение
	// названо ровно на одном пути — бэкапе (ManagedServerBackupDTO).
	"Settings.ManagedServers.PrivateKey":         "ключ самого managed-сервера",
	"Settings.ManagedServers.Peers.PrivateKey":   "ключ пира managed-сервера",
	"Settings.ManagedServers.Peers.PresharedKey": "psk пира managed-сервера",
	// Legacy-поле ManagedServer живо до первой записи после миграции
	// (migrateManagedServers) и до неё несёт тот же ключевой материал.
	"Settings.ManagedServer.PrivateKey":         "ключ managed-сервера (legacy-поле)",
	"Settings.ManagedServer.Peers.PrivateKey":   "ключ пира managed-сервера (legacy-поле)",
	"Settings.ManagedServer.Peers.PresharedKey": "psk пира managed-сервера (legacy-поле)",
}

// intentionallyExposedSecrets — поля с секретным ИМЕНЕМ, которые отдаются
// наружу сознательно. Каждая запись объясняет, почему это не утечка.
var intentionallyExposedSecrets = map[string]string{
	// Панель показывает ключ и даёт его скопировать: он и заводится ради
	// внешних клиентов. Ротация — POST /settings/regenerate-api-key.
	"Settings.ApiKey": "ключ API показывает сама панель",
	// Публичный ключ пира секретом не является, в отбор попал по подстроке
	// «Key»: по нему UI сопоставляет пира с записью NDMS.
	"Settings.ManagedServers.Peers.PublicKey": "публичный ключ, не секрет",
	"Settings.ManagedServer.Peers.PublicKey":  "публичный ключ, не секрет (legacy-поле)",
}

func isSecretFieldName(name string) bool {
	for _, m := range secretNameMarkers {
		if strings.Contains(name, m) {
			return true
		}
	}
	return false
}

// fillSecretMarkers кладёт в каждое строковое поле с секретным именем
// уникальный маркер, создавая по дороге недостающие контейнеры (один элемент
// среза, одна запись карты), и возвращает их в marks: путь -> маркер.
// Поля с json:"-" пропускаются — наружу они не сериализуются вовсе.
func fillSecretMarkers(t *testing.T, v reflect.Value, path string, marks map[string]string, depth int) {
	t.Helper()
	// Страховка от самоссылающегося типа: без неё обход повис бы молча.
	if depth > 16 {
		t.Fatalf("обход Settings ушёл глубже 16 на %s — в дереве появился цикл", path)
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		fillSecretMarkers(t, v.Elem(), path, marks, depth+1)
	case reflect.Slice:
		elem := reflect.New(v.Type().Elem()).Elem()
		fillSecretMarkers(t, elem, path, marks, depth+1)
		v.Set(reflect.Append(reflect.MakeSlice(v.Type(), 0, 1), elem))
	case reflect.Map:
		elem := reflect.New(v.Type().Elem()).Elem()
		fillSecretMarkers(t, elem, path, marks, depth+1)
		m := reflect.MakeMap(v.Type())
		m.SetMapIndex(reflect.ValueOf("fixture").Convert(v.Type().Key()), elem)
		v.Set(m)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() || strings.Split(f.Tag.Get("json"), ",")[0] == "-" {
				continue
			}
			fillSecretMarkers(t, v.Field(i), path+"."+f.Name, marks, depth+1)
		}
	case reflect.String:
		name := path[strings.LastIndex(path, ".")+1:]
		if !isSecretFieldName(name) {
			return
		}
		marker := "SECRET-" + strings.ReplaceAll(path, ".", "-")
		marks[path] = marker
		v.SetString(marker)
	}
}

// Каждое поле Settings с секретным именем либо снимается в
// settingsForResponse, либо числится в списке «отдаём намеренно». Новое поле,
// не попавшее ни в один список, роняет тест — отказ по умолчанию закрытый.
func TestSettingsResponse_SecretFieldsAreClassified(t *testing.T) {
	filled := &storage.Settings{}
	marks := map[string]string{}
	fillSecretMarkers(t, reflect.ValueOf(filled).Elem(), "Settings", marks, 0)
	if len(marks) == 0 {
		t.Fatal("в дереве Settings не нашлось ни одного секретного поля — обход сломан")
	}

	// Сначала убеждаемся, что фикстура ДОЕХАЛА до каждого поля: иначе
	// проверка ниже зеленела бы на пустом месте — страж стерёг бы сам себя.
	before, err := json.Marshal(filled)
	if err != nil {
		t.Fatalf("маршал фикстуры: %v", err)
	}
	for path, marker := range marks {
		if !strings.Contains(string(before), marker) {
			t.Fatalf("фикстура не доехала до %s (маркер %q не виден в JSON)", path, marker)
		}
	}

	after, err := json.Marshal(settingsForResponse(filled))
	if err != nil {
		t.Fatalf("маршал ответа: %v", err)
	}

	for path, marker := range marks {
		_, stripped := strippedSecrets[path]
		_, exposed := intentionallyExposedSecrets[path]
		switch {
		case stripped && exposed:
			t.Errorf("%s числится сразу в обоих списках — решение не принято", path)
		case stripped:
			if strings.Contains(string(after), marker) {
				t.Errorf("%s обязано сниматься в settingsForResponse, но его значение в теле ответа: %s", path, after)
			}
		case exposed:
			// Отдаётся намеренно — обоснование в intentionallyExposedSecrets.
		default:
			t.Errorf("поле %s похоже на секрет и не классифицировано: сними его в "+
				"settingsForResponse и внеси в strippedSecrets — либо, если оно отдаётся "+
				"намеренно, внеси в intentionallyExposedSecrets с обоснованием", path)
		}
	}

	// Запись про исчезнувшее поле вводит в заблуждение следующего читателя.
	for _, list := range []struct {
		name    string
		entries map[string]string
	}{
		{"strippedSecrets", strippedSecrets},
		{"intentionallyExposedSecrets", intentionallyExposedSecrets},
	} {
		for path := range list.entries {
			if _, ok := marks[path]; !ok {
				t.Errorf("%s: поля %s в Settings больше нет — запись протухла", list.name, path)
			}
		}
	}
}
