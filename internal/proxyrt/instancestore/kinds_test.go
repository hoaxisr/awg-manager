package instancestore

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// ftServer — четвёртая фикстура рядом с rawClient/ftClient/wdttServer
// (store_test.go): нужна перечислению по AllKinds.
func ftServer(id string) Record {
	return Record{ID: id, Kind: KindFreeTurnServer, Name: "FT-сервер", Enabled: false,
		FreeTurnServer: &roles.FreeTurnServerConfig{Listen: "0.0.0.0:56000",
			ClientsFile: "/opt/etc/awg-manager/freeturn/clients.json"}}
}

// recordOfKind — запись каждой роли для тестов полноты. Новая роль падает
// здесь первой, до самих проверок диспатчей.
func recordOfKind(t *testing.T, k Kind) Record {
	t.Helper()
	switch k {
	case KindWdttClient:
		return rawClient("c", "C")
	case KindWdttServer:
		r := wdttServer("s")
		// Фикстура store_test.go каталог абонентов не задаёт, а тест данных
		// проверяет именно его: без пути «есть данные» неотличимо от
		// «роль выпала из диспатча».
		r.WdttServer.ConfigDir = "/opt/etc/awg-manager/wdtt/s"
		return r
	case KindFreeTurnClient:
		return ftClient("f")
	case KindFreeTurnServer:
		return ftServer("g")
	}
	t.Fatalf("роль %s не имеет фикстуры — заведите её здесь и классифицируйте во всех TestKinds_*", k)
	return Record{}
}

// TestAllKinds_Inventory — новая роль обязана попасть в AllKinds, иначе тесты
// полноты её не увидят и молча пропустят.
//
// Сверяются ЗНАЧЕНИЯ, а не имена констант, и берутся они с двух сторон
// по-разному: слева — разбор исходника пакета, справа — сама переменная. Обход
// через имя без префикса `Kind`, через форму `const X = Kind("…")` и через
// перечисление в постороннем `[]Kind` того же пакета этим закрыт; сверка по
// именам в тексте закрывала бы только первый попавшийся способ записи и
// краснела бы на переформатировании.
func TestAllKinds_Inventory(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("читаем пакет: %v", err)
	}
	// Обе формы объявления: в const-блоке с типом и через приведение.
	res := []*regexp.Regexp{
		regexp.MustCompile(`(?m)^\s*(\w+)\s+Kind\s*=\s*"([^"]*)"`),
		regexp.MustCompile(`(?m)^\s*(?:const\s+)?(\w+)\s*=\s*Kind\("([^"]*)"\)`),
	}
	declared := map[string]string{} // значение → имя константы
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(".", e.Name()))
		if err != nil {
			t.Fatalf("читаем %s: %v", e.Name(), err)
		}
		for _, re := range res {
			for _, m := range re.FindAllStringSubmatch(string(src), -1) {
				declared[m[2]] = m[1]
			}
		}
	}
	if len(declared) == 0 {
		t.Fatal("не разобрано ни одной константы типа Kind — сломан разбор пакета")
	}
	listed := map[string]bool{}
	for _, k := range AllKinds {
		if listed[string(k)] {
			t.Errorf("роль %q внесена в AllKinds дважды", k)
		}
		listed[string(k)] = true
	}
	for val, name := range declared {
		if !listed[val] {
			t.Errorf("роль %s (%q) объявлена, но не внесена в AllKinds — тесты полноты её не проверят", name, val)
		}
	}
	for _, k := range AllKinds {
		if _, ok := declared[string(k)]; !ok {
			t.Errorf("AllKinds содержит %q, которому не найдено объявления в пакете", k)
		}
	}
}

// TestKinds_RawExiterCoversAll — ведомость выходов обязана отдаваться для
// ЛЮБОЙ роли; отсутствие выхода выражает сам RawExit()==false, а не выпадение
// из ведомости (требование 16). Перечисление идёт по AllKinds: прежний
// вариант перечислял четыре записи руками и пятую роль пропустил бы молча.
func TestKinds_RawExiterCoversAll(t *testing.T) {
	// Выход объявляет только raw-клиент: у сервера publication убрана
	// решением владельца, у FreeTurn зеркальных записей нет в принципе
	// (roles/config.go:172-180).
	declaresExit := map[Kind]bool{KindWdttClient: true}
	for _, k := range AllKinds {
		r := recordOfKind(t, k)
		ex := r.RawExiter()
		if ex == nil {
			t.Fatalf("%s: RawExiter обязан отдаваться для любой роли", k)
		}
		if _, has := ex.RawExit(); has != declaresExit[k] {
			t.Errorf("%s: объявление выхода has=%v, ожидали %v", k, has, declaresExit[k])
		}
	}
}

// TestKinds_NDMSNamedCoversAll — то же для ведомости NDMS-имён уборщика: nil
// означает «роль выпала из ведомости», то есть её живой интерфейс уборщик
// счёл бы ничьим. Имена сверяются с фикстурами recordOfKind.
func TestKinds_NDMSNamedCoversAll(t *testing.T) {
	want := map[Kind][]string{
		KindWdttClient:     {"OpkgTun18"},
		KindWdttServer:     {"OpkgTun20", "OpkgTun21"},
		KindFreeTurnClient: nil,
		KindFreeTurnServer: nil,
	}
	for _, k := range AllKinds {
		exp, ok := want[k]
		if !ok {
			t.Errorf("роль %s не классифицирована: какие NDMS-имена она объявляет уборщику?", k)
			continue
		}
		n := recordOfKind(t, k).NDMSNamed()
		if n == nil {
			t.Fatalf("%s: NDMSNamed обязан отдаваться для любой роли", k)
		}
		got := n.NDMSNames()
		if len(got) != len(exp) {
			t.Fatalf("%s: имена %v, ждали %v", k, got, exp)
		}
		for i := range got {
			if got[i] != exp[i] {
				t.Fatalf("%s: имена %v, ждали %v", k, got, exp)
			}
		}
	}
}

// TestKinds_DataTargetsClassified — данные на диске переживут удаление
// инстанса, если DataTargets про них не знает: ветка «прочее» отдаёт nil, и
// новая роль осиротила бы свой каталог молча.
//
// Сверяется СОСТАВ путей, а не их наличие: у freeturn-сервера путь списка по
// умолчанию (FreeTurnAllowlistPath) добавляется безусловно, поэтому проверка
// «вернулось непусто» держалась бы сама собой и не заметила бы потерю
// ClientsFile — второй половины ветки.
func TestKinds_DataTargetsClassified(t *testing.T) {
	const dataDir = "/data"
	// Пусто у клиентских ролей: своих данных на диске у них нет
	// (record.go, докстринг DataTargets).
	want := map[Kind][]string{
		KindWdttClient:     nil,
		KindWdttServer:     {"/opt/etc/awg-manager/wdtt/s"},
		KindFreeTurnClient: nil,
		KindFreeTurnServer: {
			"/opt/etc/awg-manager/freeturn/clients.json",
			FreeTurnAllowlistPath(dataDir, "g"),
		},
	}
	for _, k := range AllKinds {
		exp, ok := want[k]
		if !ok {
			t.Errorf("роль %s не классифицирована: какие её данные переживут удаление инстанса? см. DataTargets", k)
			continue
		}
		got := map[string]bool{}
		for _, d := range recordOfKind(t, k).DataTargets(dataDir) {
			got[d.Path] = true
		}
		if len(got) != len(exp) {
			t.Errorf("%s: DataTargets вернул %v, ждали %v", k, got, exp)
			continue
		}
		for _, p := range exp {
			if !got[p] {
				t.Errorf("%s: DataTargets не вернул %q — файл переживёт удаление инстанса, а убрать его из UI нечем", k, p)
			}
		}
	}
}
