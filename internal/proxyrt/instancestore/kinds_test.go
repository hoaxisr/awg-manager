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

// TestAllKinds_Inventory — новая константа Kind обязана попасть в AllKinds,
// иначе тесты полноты её не увидят и молча пропустят. Читаем ВЕСЬ пакет:
// константу можно объявить и в соседнем файле.
func TestAllKinds_Inventory(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("читаем пакет: %v", err)
	}
	declRe := regexp.MustCompile(`\b(Kind[A-Za-z0-9]+)\s+Kind\s*=`)
	listRe := regexp.MustCompile(`(?m)^\t(Kind[A-Za-z0-9]+),$`)
	declared, listed := map[string]bool{}, map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(".", e.Name()))
		if err != nil {
			t.Fatalf("читаем %s: %v", e.Name(), err)
		}
		for _, m := range declRe.FindAllStringSubmatch(string(src), -1) {
			declared[m[1]] = true
		}
		for _, m := range listRe.FindAllStringSubmatch(string(src), -1) {
			listed[m[1]] = true
		}
	}
	if len(declared) == 0 {
		t.Fatal("не разобрано ни одной константы Kind* — сломан разбор пакета")
	}
	for name := range declared {
		if !listed[name] {
			t.Errorf("роль %s объявлена, но не внесена в AllKinds — тесты полноты её не проверят", name)
		}
	}
	if len(AllKinds) != len(declared) {
		t.Errorf("AllKinds содержит %d ролей, объявлено %d", len(AllKinds), len(declared))
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
// новая роль осиротила бы свой каталог молча. Требуем классификации, а не
// конкретных путей.
func TestKinds_DataTargetsClassified(t *testing.T) {
	// Своих данных на диске нет у клиентских ролей (record.go:205).
	hasData := map[Kind]bool{
		KindWdttClient:     false,
		KindWdttServer:     true,
		KindFreeTurnClient: false,
		KindFreeTurnServer: true,
	}
	for _, k := range AllKinds {
		want, ok := hasData[k]
		if !ok {
			t.Errorf("роль %s не классифицирована: есть ли у неё данные на диске? см. DataTargets", k)
			continue
		}
		got := recordOfKind(t, k).DataTargets("/data")
		if (len(got) > 0) != want {
			t.Errorf("%s: DataTargets вернул %d путей, ожидали наличие=%v", k, len(got), want)
		}
	}
}
