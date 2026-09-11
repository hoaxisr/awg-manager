package api

import (
	"encoding"
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
// массив байт) секрет унести может, а маркер — нет.
//
// Тип со своим MarshalJSON/MarshalText не покрыт НИКОГДА, какого бы вида он
// ни был: что уедет в тело, решает его собственный код, а обход, спустившись
// внутрь, доказал бы в лучшем случае отсутствие экспортированных полей.
// Секрет в неэкспортированном поле, который такой тип сам выдаёт наружу, при
// спуске остался бы непроверенным — и зелёный тест «доказывал» бы отсутствие
// утечки, которую никто не смотрел.
//
// Непокрытое поле не проваливается молча: обход заносит его в blind, и
// решение о нём принимает человек (guardBlindSpots).
func guardCoversKind(t reflect.Type) bool {
	if selfMarshaling(t) {
		return false
	}
	switch t.Kind() {
	case reflect.String, reflect.Bool, reflect.Struct:
		return true
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		return guardCoversKind(t.Elem())
	default:
		return false
	}
}

var (
	jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

// selfMarshaling — решает ли тип сам, что уедет в JSON. Указатель проверяется
// тоже: поле внутри Settings адресуемо, поэтому encoding/json возьмёт и метод
// с указательным получателем.
func selfMarshaling(t reflect.Type) bool {
	ptr := reflect.PointerTo(t)
	return t.Implements(jsonMarshalerType) || ptr.Implements(jsonMarshalerType) ||
		t.Implements(textMarshalerType) || ptr.Implements(textMarshalerType)
}

// secretFields — что нашёл обход дерева Settings.
type secretFields struct {
	// marks — путь поля -> уникальный маркер, положенный фикстурой в его
	// значение. Такие поля страж проверяет по РЕЗУЛЬТАТУ: виден маркер в теле
	// ответа или нет.
	marks map[string]string
	// blind — путь поля -> вид значения, в которое маркер не положить (см.
	// guardCoversKind). Про такое поле механической правды нет вовсе.
	blind map[string]string
}

func newSecretFields() *secretFields {
	return &secretFields{marks: map[string]string{}, blind: map[string]string{}}
}

// fillSecretMarkers кладёт в каждое строковое поле с секретным именем
// уникальный маркер, создавая по дороге недостающие контейнеры (один элемент
// среза, одна запись карты, все элементы массива), и складывает найденное в
// found. Поля с json:"-" пропускаются — наружу они не сериализуются вовсе.
//
// Поле с секретным именем, в значение которого маркер не положить, попадает
// в found.blind: доказать про него ничего нельзя, и решение о нём принимает
// человек (classifyBlindSpots). Ошибка остаётся за формой, на которой
// ломается сам обход, — картой с нестроковым ключом и циклом в типе.
func fillSecretMarkers(v reflect.Value, path string, found *secretFields, depth int) error {
	// Страховка от самоссылающегося типа: без неё обход повис бы молча.
	if depth > 16 {
		return fmt.Errorf("обход Settings ушёл глубже 16 на %s — в дереве появился цикл", path)
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		return fillSecretMarkers(v.Elem(), path, found, depth+1)
	case reflect.Slice:
		elem := reflect.New(v.Type().Elem()).Elem()
		if err := fillSecretMarkers(elem, path, found, depth+1); err != nil {
			return err
		}
		v.Set(reflect.Append(reflect.MakeSlice(v.Type(), 0, 1), elem))
	case reflect.Array:
		// Место под значения у массива уже есть — маркер кладём в каждый
		// элемент. Массив нулевой длины не несёт значения вовсе, и маркеру в
		// нём взяться неоткуда.
		for i := 0; i < v.Len(); i++ {
			if err := fillSecretMarkers(v.Index(i), path, found, depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		// Ключ фикстуры — строка. Нестроковый ключ ронял Convert ПАНИКОЙ;
		// отказ внятный, а молча пропустить карту нельзя: её значения так и
		// остались бы непроверенными.
		if k := v.Type().Key(); k.Kind() != reflect.String {
			return fmt.Errorf("поле %s — карта с ключом вида %s: страж не умеет завести "+
				"в ней запись и потому не может проверить её значения", path, k)
		}
		elem := reflect.New(v.Type().Elem()).Elem()
		if err := fillSecretMarkers(elem, path, found, depth+1); err != nil {
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
				// Спускаться внутрь нельзя: у самосериализующегося типа обход
				// нашёл бы не то, что уезжает в тело, и зеленел бы на пустом
				// месте.
				found.blind[fieldPath] = f.Type.String()
				continue
			}
			if err := fillSecretMarkers(v.Field(i), fieldPath, found, depth+1); err != nil {
				return err
			}
		}
	case reflect.String:
		name := path[strings.LastIndex(path, ".")+1:]
		if !isSecretFieldName(name) {
			return nil
		}
		marker := "SECRET-" + strings.ReplaceAll(path, ".", "-")
		found.marks[path] = marker
		v.SetString(marker)
	}
	return nil
}

// guardBlindSpots — поля, про которые страж не может сказать НИЧЕГО: имя
// похоже на секретное, а маркер в значение такого вида не положить (число,
// массив байт, тип со своим MarshalJSON). Запись здесь глушит стража сильнее,
// чем intentionallyExposedSecrets: там видимость значения в теле всё-таки
// проверяется, здесь не проверяется ничего. Поэтому сюда попадает либо поле,
// которое секретом не является вовсе (ложное срабатывание по подстроке —
// PingIntervalSec и подобные), либо секрет, снятие которого ревьюер посмотрел
// глазами.
//
// Пусто: сегодня таких полей в Settings нет. Список — дверь вместо стены: без
// него ложное срабатывание по имени роняло бы весь пакет, и выхода не было бы
// никакого, кроме переименования поля или правки самого стража.
var guardBlindSpots = map[string]string{}

// classifyBlindSpots сверяет непроверяемые поля со списком принятых человеком
// решений. Неназванное поле — претензия: отказ по умолчанию закрытый.
func classifyBlindSpots(blind, allowed map[string]string) []string {
	var complaints []string
	for path, kind := range blind {
		if _, ok := allowed[path]; ok {
			continue
		}
		complaints = append(complaints, fmt.Sprintf(
			"поле %s похоже на секрет, а заглянуть в значение вида %s страж не умеет и "+
				"потому не может доказать, что оно не уходит в ответ: сними его в "+
				"settingsForResponse — либо, если решение принято человеком, внеси путь "+
				"в guardBlindSpots с обоснованием", path, kind))
	}
	return complaints
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
	found := newSecretFields()
	if err := fillSecretMarkers(reflect.ValueOf(filled).Elem(), "Settings", found, 0); err != nil {
		t.Fatal(err)
	}
	marks := found.marks
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

	// Поля, которые страж проверить не может, обязаны быть названы человеком.
	for _, complaint := range classifyBlindSpots(found.blind, guardBlindSpots) {
		t.Error(complaint)
	}
	for path := range guardBlindSpots {
		if _, ok := found.blind[path]; !ok {
			t.Errorf("guardBlindSpots: поле %s страж больше не считает непроверяемым — "+
				"запись протухла", path)
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
		found := newSecretFields()
		if err := fillSecretMarkers(reflect.ValueOf(fixture).Elem(), "Fixture", found, 0); err != nil {
			t.Fatal(err)
		}
		if _, ok := found.blind["Fixture.DevicePrivateKey"]; !ok {
			t.Fatalf("обход принял секретное поле вида []byte молча: %+v", found)
		}
		if got := classifyBlindSpots(found.blind, nil); len(got) != 1 {
			t.Fatalf("претензии на неклассифицированное непроверяемое поле = %v", got)
		}
	})

	t.Run("секрет с именем …Cred", func(t *testing.T) {
		fixture := &struct {
			RouterCred string `json:"routerCred"`
		}{}
		found := newSecretFields()
		if err := fillSecretMarkers(reflect.ValueOf(fixture).Elem(), "Fixture", found, 0); err != nil {
			t.Fatal(err)
		}
		marks := found.marks
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

// sealedFixture — тип, который сам решает, что уедет в JSON: экспортированных
// полей у него нет, а в тело он отдаёт секрет. Ровно такой формой ревьюер
// показал молчаливый зелёный: обход спускался внутрь, не находил полей, не
// клал маркеров — и «доказывал» отсутствие утечки, которую никто не смотрел.
type sealedFixture struct{ secret string }

func (s sealedFixture) MarshalJSON() ([]byte, error) { return json.Marshal(s.secret) }

// Дыры стража, найденные вторым ревью.
//
//   - Массив [N]T: guardCoversKind числил его покрытым, а ветки reflect.Array
//     в обходе не было вовсе — поле не метилось и до классификации не
//     доходило.
//   - Тип со своим MarshalJSON считался покрытым по виду struct.
//   - Ложное срабатывание по имени на нестроковом виде роняло ПАКЕТ: отказ
//     возвращался до всякой классификации, и выхода, кроме переименования
//     поля, не оставалось.
//   - Карта с нестроковым ключом роняла стража паникой в Convert.
func TestSecretGuard_ArraysAndBlindSpots(t *testing.T) {
	t.Run("секретное имя при виде [N]T", func(t *testing.T) {
		fixture := &struct {
			RotationKeys [2]string `json:"rotationKeys"`
		}{}
		found := newSecretFields()
		if err := fillSecretMarkers(reflect.ValueOf(fixture).Elem(), "Fixture", found, 0); err != nil {
			t.Fatal(err)
		}
		marker, ok := found.marks["Fixture.RotationKeys"]
		if !ok {
			t.Fatalf("маркер не лёг в массив: %+v", found)
		}
		body, err := json.Marshal(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), marker) {
			t.Fatalf("фикстура не доехала до элементов массива: %s", body)
		}
		// Поле, не снятое из ответа и не классифицированное, обязано быть
		// претензией — иначе секрет в массиве уехал бы непроверенным.
		if got := classifySecretMarks(string(body), found.marks); len(got) != 1 ||
			!strings.Contains(got[0], "не классифицировано") {
			t.Fatalf("претензии на неснятое поле-массив = %v", got)
		}
	})

	t.Run("тип со своим MarshalJSON", func(t *testing.T) {
		fixture := &struct {
			DeviceKey sealedFixture `json:"deviceKey"`
		}{DeviceKey: sealedFixture{secret: "sealed-test-GGGG="}}
		found := newSecretFields()
		if err := fillSecretMarkers(reflect.ValueOf(fixture).Elem(), "Fixture", found, 0); err != nil {
			t.Fatal(err)
		}
		// Секрет и правда уезжает в тело: молчаливый зелёный «доказывал» бы
		// пустоту.
		body, err := json.Marshal(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "sealed-test-GGGG=") {
			t.Fatalf("фикстура не отдала секрет наружу: %s", body)
		}
		if len(found.marks) != 0 {
			t.Fatalf("страж решил, что пометил самосериализующийся тип: %v", found.marks)
		}
		if _, ok := found.blind["Fixture.DeviceKey"]; !ok {
			t.Fatalf("тип со своим MarshalJSON прошёл молча: %+v", found)
		}
		if got := classifyBlindSpots(found.blind, nil); len(got) != 1 {
			t.Fatalf("претензии на неклассифицированный самосериализующийся тип = %v", got)
		}
	})

	t.Run("не-секрет нестрокового вида классифицируется явно", func(t *testing.T) {
		fixture := &struct {
			PingIntervalSec int `json:"pingIntervalSec"`
		}{}
		found := newSecretFields()
		if err := fillSecretMarkers(reflect.ValueOf(fixture).Elem(), "Fixture", found, 0); err != nil {
			t.Fatalf("ложное срабатывание по имени уронило обход: %v", err)
		}
		if _, ok := found.blind["Fixture.PingIntervalSec"]; !ok {
			t.Fatalf("поле не дошло до классификации: %+v", found)
		}
		if got := classifyBlindSpots(found.blind, map[string]string{
			"Fixture.PingIntervalSec": "интервал проверки, в отбор попал по подстроке Pin",
		}); len(got) != 0 {
			t.Fatalf("классифицированное поле всё равно претензия: %v", got)
		}
	})

	t.Run("карта с нестроковым ключом", func(t *testing.T) {
		fixture := &struct {
			Tokens map[int]string `json:"tokens"`
		}{}
		err := fillSecretMarkers(reflect.ValueOf(fixture).Elem(), "Fixture", newSecretFields(), 0)
		if err == nil {
			t.Fatal("карта с ключом int прошла молча — раньше здесь была паника")
		}
		if !strings.Contains(err.Error(), "Fixture.Tokens") {
			t.Fatalf("в отказе не названо поле: %v", err)
		}
	})
}
