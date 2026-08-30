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
// иначе сверка с фронтом его не увидит. Ловим по исходнику: все константы
// Resource* объявлены в resources.go, перечень — там же.
func TestResourceKeys_Inventory(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "internal/events/resources.go"))
	if err != nil {
		t.Fatalf("читаем resources.go: %v", err)
	}
	declared := regexp.MustCompile(`(?m)^\t(Resource[A-Za-z0-9]+)\s+Resource\s+=`).FindAllStringSubmatch(string(src), -1)
	if len(declared) == 0 {
		t.Fatal("не разобрано ни одной константы Resource* — сломан разбор resources.go")
	}
	listed := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\t(Resource[A-Za-z0-9]+),$`).FindAllStringSubmatch(string(src), -1) {
		listed[m[1]] = true
	}
	for _, m := range declared {
		if !listed[m[1]] {
			t.Errorf("константа %s объявлена, но не внесена в AllResources — сверка с фронтом её не проверит", m[1])
		}
	}
}

// TestResourceKeys_NoLiteralPublishers — публиковать resource:invalidated
// можно только через PublishInvalidated/PublishInvalidatedTo. Литерал в
// любом другом пакете означает ключ мимо закрытого набора: именно так
// разъехались deviceproxy, ndms/command и singbox/router.
func TestResourceKeys_NoLiteralPublishers(t *testing.T) {
	root := repoRoot(t)
	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case "vendor", "node_modules", ".git", "frontend", "docs", "build":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if strings.HasPrefix(rel, "internal/events/") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, `"resource:invalidated"`) {
				offenders = append(offenders, fmt.Sprintf("%s:%d", rel, i+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("обход дерева: %v", err)
	}
	for _, o := range offenders {
		t.Errorf(`%s: литерал "resource:invalidated" — публиковать через events.PublishInvalidated(To), читать через events.EventResourceInvalidated`, o)
	}
}
