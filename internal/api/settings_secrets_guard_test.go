package api

import (
	"encoding/json"
	"fmt"
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
var secretNameMarkers = []string{
	"Key", "Secret", "Cipher", "Password", "Token",
	"Cred", "Auth", "Seed", "Salt", "Passphrase", "Pin",
}

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
//
// Что этот список ПРОВЕРЯЕТСЯ: поле с таким путём в Settings существует, имя
// его действительно попало под отбор, и значение действительно видно в теле
// ответа (иначе запись протухла и её место — в strippedSecrets).
//
// Чего он НЕ проверяет и проверить нельзя: «это на самом деле не секрет».
// Такое решение принимает человек, и механической страховки у него нет —
// запись здесь ГЛУШИТ стража. Поэтому каждую новую строку обязан осмотреть
// ревьюер: если обоснование не объясняет, почему значение безопасно
// показывать всякому, кто открыл страницу настроек, — поле не сюда.
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

// guardCoversKind — доедет ли маркер фикстуры до значения такого вида.
// Строку фикстура помечает сама, в контейнер спускается (маркер положат его
// строковые листья), bool значения не несёт вовсе — два его состояния
// публичны оба, секрета в нём быть не может. Всё остальное (число, []byte,
// массив байт) секрет унести может, а маркер — нет: такое поле с секретным
// именем ушло бы в ответ непроверенным, поэтому обход на нём останавливается.
func guardCoversKind(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.String, reflect.Bool, reflect.Struct:
		return true
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		return guardCoversKind(t.Elem())
	default:
		return false
	}
}

// fillSecretMarkers кладёт в каждое строковое поле с секретным именем
// уникальный маркер, создавая по дороге недостающие контейнеры (один элемент
// среза, одна запись карты), и возвращает их в marks: путь -> маркер.
// Поля с json:"-" пропускаются — наружу они не сериализуются вовсе.
//
// Ошибка — отказ стража: обход встретил форму, которую не умеет проверить, и
// молча пропустить её нельзя (см. guardCoversKind).
func fillSecretMarkers(v reflect.Value, path string, marks map[string]string, depth int) error {
	// Страховка от самоссылающегося типа: без неё обход повис бы молча.
	if depth > 16 {
		return fmt.Errorf("обход Settings ушёл глубже 16 на %s — в дереве появился цикл", path)
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		return fillSecretMarkers(v.Elem(), path, marks, depth+1)
	case reflect.Slice:
		elem := reflect.New(v.Type().Elem()).Elem()
		if err := fillSecretMarkers(elem, path, marks, depth+1); err != nil {
			return err
		}
		v.Set(reflect.Append(reflect.MakeSlice(v.Type(), 0, 1), elem))
	case reflect.Map:
		elem := reflect.New(v.Type().Elem()).Elem()
		if err := fillSecretMarkers(elem, path, marks, depth+1); err != nil {
			return err
		}
		m := reflect.MakeMap(v.Type())
		m.SetMapIndex(reflect.ValueOf("fixture").Convert(v.Type().Key()), elem)
		v.Set(m)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() || strings.Split(f.Tag.Get("json"), ",")[0] == "-" {
				continue
			}
			fieldPath := path + "." + f.Name
			if isSecretFieldName(f.Name) && !guardCoversKind(f.Type) {
				return fmt.Errorf("поле %s похоже на секрет, но страж не умеет положить "+
					"маркер в значение вида %s и потому не может доказать, что оно не "+
					"уходит в ответ: научи fillSecretMarkers этому виду — либо, если "+
					"поле не секрет, переименуй его", fieldPath, f.Type)
			}
			if err := fillSecretMarkers(v.Field(i), fieldPath, marks, depth+1); err != nil {
				return err
			}
		}
	case reflect.String:
		name := path[strings.LastIndex(path, ".")+1:]
		if !isSecretFieldName(name) {
			return nil
		}
		marker := "SECRET-" + strings.ReplaceAll(path, ".", "-")
		marks[path] = marker
		v.SetString(marker)
	}
	return nil
}

// classifySecretMarks сверяет каждое помеченное поле с двумя списками и
// возвращает претензии. Снятое обязано исчезнуть из тела, отданное намеренно —
// в нём остаться, неклассифицированное — претензия само по себе (отказ по
// умолчанию закрытый).
func classifySecretMarks(body string, marks map[string]string) []string {
	var complaints []string
	for path, marker := range marks {
		_, stripped := strippedSecrets[path]
		_, exposed := intentionallyExposedSecrets[path]
		switch {
		case stripped && exposed:
			complaints = append(complaints,
				fmt.Sprintf("%s числится сразу в обоих списках — решение не принято", path))
		case stripped:
			if strings.Contains(body, marker) {
				complaints = append(complaints,
					fmt.Sprintf("%s обязано сниматься в settingsForResponse, но его значение в теле ответа", path))
			}
		case exposed:
			// Обоснование — в intentionallyExposedSecrets; проверяем лишь,
			// что поле и правда отдаётся: иначе запись либо протухла, либо
			// поле уже снимают и его место в strippedSecrets.
			if !strings.Contains(body, marker) {
				complaints = append(complaints,
					fmt.Sprintf("%s числится в intentionallyExposedSecrets, но в теле ответа его нет — "+
						"запись протухла либо поле снимается и должно быть в strippedSecrets", path))
			}
		default:
			complaints = append(complaints,
				fmt.Sprintf("поле %s похоже на секрет и не классифицировано: сними его в "+
					"settingsForResponse и внеси в strippedSecrets — либо, если оно отдаётся "+
					"намеренно, внеси в intentionallyExposedSecrets с обоснованием", path))
		}
	}
	return complaints
}

// Каждое поле Settings с секретным именем либо снимается в
// settingsForResponse, либо числится в списке «отдаём намеренно». Новое поле,
// не попавшее ни в один список, роняет тест — отказ по умолчанию закрытый.
func TestSettingsResponse_SecretFieldsAreClassified(t *testing.T) {
	filled := &storage.Settings{}
	marks := map[string]string{}
	if err := fillSecretMarkers(reflect.ValueOf(filled).Elem(), "Settings", marks, 0); err != nil {
		t.Fatal(err)
	}
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
	for _, complaint := range classifySecretMarks(string(after), marks) {
		t.Errorf("%s\nтело ответа: %s", complaint, after)
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

// Дыры самого стража, найденные ревью: поле с секретным именем, в которое
// фикстура не умеет положить маркер (маркеры кладутся только в строки, а
// секрет бывает и []byte), уходило в общую ветку обхода и до проверки не
// доходило — тест оставался ЗЕЛЁНЫМ; секрет с именем, которого не было в
// списке слов, не попадал под отбор вовсе.
func TestSecretGuard_CatchesUnmarkableAndNewlyNamedSecrets(t *testing.T) {
	t.Run("секретное имя при виде []byte", func(t *testing.T) {
		fixture := &struct {
			DevicePrivateKey []byte `json:"devicePrivateKey"`
		}{}
		err := fillSecretMarkers(reflect.ValueOf(fixture).Elem(), "Fixture", map[string]string{}, 0)
		if err == nil {
			t.Fatal("обход принял секретное поле вида []byte молча")
		}
		if !strings.Contains(err.Error(), "Fixture.DevicePrivateKey") {
			t.Fatalf("в отказе не названо поле: %v", err)
		}
	})

	t.Run("секрет с именем …Cred", func(t *testing.T) {
		fixture := &struct {
			RouterCred string `json:"routerCred"`
		}{}
		marks := map[string]string{}
		if err := fillSecretMarkers(reflect.ValueOf(fixture).Elem(), "Fixture", marks, 0); err != nil {
			t.Fatal(err)
		}
		marker, ok := marks["Fixture.RouterCred"]
		if !ok {
			t.Fatalf("поле с именем …Cred не попало под отбор: marks=%v", marks)
		}
		// Неклассифицированное поле, видное в теле ответа, обязано быть
		// претензией — иначе страж пропустил бы утечку.
		body := `{"routerCred":"` + marker + `"}`
		if got := classifySecretMarks(body, marks); len(got) != 1 ||
			!strings.Contains(got[0], "не классифицировано") {
			t.Fatalf("претензии на неклассифицированное поле = %v", got)
		}
	})
}
