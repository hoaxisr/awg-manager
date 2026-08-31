package events_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// repoRoot — корень репозитория относительно internal/events.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("корень репозитория: %v", err)
	}
	return root
}

// TestResourceKeys_KnownToFrontend — ключ, который бэкенд публикует, а фронт
// не знает, уходит в никуда: invalidateResource на незнакомом ключе молча
// ничего не делает. Сверяем набор с union ResourceKey.
//
// Направление одностороннее (Go ⊆ TS) нарочно: обратное — ключ, объявленный
// на фронте без публикатора — бывает законным ('singbox.proxies' помечен там
// как «no backend publisher yet»).
func TestResourceKeys_KnownToFrontend(t *testing.T) {
	path := filepath.Join(repoRoot(t), "frontend/src/lib/stores/storeRegistry.ts")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("читаем %s: %v", path, err)
	}
	known := map[string]bool{}
	for _, m := range regexp.MustCompile(`\|\s*'([^']+)'`).FindAllStringSubmatch(string(data), -1) {
		known[m[1]] = true
	}
	if len(known) == 0 {
		t.Fatal("в storeRegistry.ts не разобрано ни одного ключа — сломан разбор union ResourceKey")
	}
	for _, res := range events.AllResources {
		if !known[string(res)] {
			t.Errorf("ключ %q публикуется бэкендом, но отсутствует в union ResourceKey (%s) — инвалидация уйдёт в никуда", res, path)
		}
	}
}

// TestResourceKeys_Inventory — новый ключ обязан попасть в AllResources,
// иначе сверка с фронтом его не увидит и публикация уйдёт в никуда.
//
// Сверяются ЗНАЧЕНИЯ, а не имена констант, и берутся они с двух сторон
// по-разному: слева — разбор исходника пакета, справа — сама переменная.
// Прежняя форма сверяла имена в тексте с именами в тексте же и самой
// переменной не касалась, поэтому обходилась тремя способами
// (`const ResourceX = Resource("x")`, имя без префикса `Resource`,
// перечисление в постороннем `[]Resource`) и вдобавок краснела на
// переформатировании списка в одну строку.
func TestResourceKeys_Inventory(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "internal/events")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("читаем %s: %v", dir, err)
	}
	// Обе формы объявления: в const-блоке с типом и через приведение.
	res := []*regexp.Regexp{
		regexp.MustCompile(`(?m)^\s*(\w+)\s+Resource\s*=\s*"([^"]*)"`),
		regexp.MustCompile(`(?m)^\s*(?:const\s+)?(\w+)\s*=\s*Resource\("([^"]*)"\)`),
	}
	declared := map[string]string{} // значение → имя константы
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
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
		t.Fatal("не разобрано ни одной константы типа Resource — сломан разбор пакета")
	}
	listed := map[string]bool{}
	for _, r := range events.AllResources {
		if listed[string(r)] {
			t.Errorf("ключ %q внесён в AllResources дважды", r)
		}
		listed[string(r)] = true
	}
	for val, name := range declared {
		if !listed[val] {
			t.Errorf("константа %s (%q) объявлена, но не внесена в AllResources — сверка с фронтом её не проверит", name, val)
		}
	}
	for _, r := range events.AllResources {
		if _, ok := declared[string(r)]; !ok {
			t.Errorf("AllResources содержит %q, которому не найдено объявления в пакете", r)
		}
	}
}

// TestResourceKeys_NoLiteralPublishers — публиковать resource:invalidated
// можно только через PublishInvalidated/PublishInvalidatedTo. Литерал в
// любом другом пакете означает ключ мимо закрытого набора: именно так
// разъехались deviceproxy, ndms/command и singbox/router.
//
// Ловим три обхода сразу, потому что типа поля мало: нетипизированная
// строковая константа приводится к Resource молча, так что и
// `ResourceInvalidatedEvent{Resource: "опечатка"}`, и
// `events.Resource("опечатка")` компилируются. Событие и ключ имеет право
// конструировать только этот пакет; снаружи допустимо лишь читать их
// (type assertion скобки `{` не содержит).
func TestResourceKeys_NoLiteralPublishers(t *testing.T) {
	root := repoRoot(t)
	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if info.IsDir() {
			switch info.Name() {
			case "vendor", "node_modules", ".git":
				return filepath.SkipDir
			}
			// docs/, frontend/, build/ — не наш код, но только на верхнем
			// уровне: пакет с таким именем внутри internal/ пропускать нельзя.
			// .claude/ — рабочие каталоги агентов: там лежат ПОЛНЫЕ копии
			// дерева (git worktree), и без пропуска любой запущенный агент
			// ронял этот тест ложно — на своей же копии resources.go.
			switch rel {
			case "docs", "frontend", "build", ".claude":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.HasPrefix(rel, "internal/events/") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			for _, bad := range []string{`"resource:invalidated"`, "ResourceInvalidatedEvent{", "events.Resource("} {
				if strings.Contains(line, bad) {
					offenders = append(offenders, fmt.Sprintf("%s:%d: %s", rel, i+1, bad))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("обход дерева: %v", err)
	}
	for _, o := range offenders {
		t.Errorf("%s — публиковать через events.PublishInvalidated(To) с константой events.Resource*; тип события и ключ конструирует только пакет events", o)
	}
}
