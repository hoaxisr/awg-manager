package events_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// walkGoFiles зовёт fn для каждого прод-файла .go в репозитории, передавая путь
// относительно корня и содержимое. Тестовые файлы, вендор, фронт и рабочие
// копии агентов пропускаются.
//
// Общий для сторожей, которые ищут запрещённые конструкции по всему дереву
// (TestResourceKeys_NoLiteralPublishers, TestSubscribeClient_OnlySSEHandler):
// раньше каждый нёс свою копию обхода вместе со списком пропусков, и список
// пришлось бы править в двух местах.
func walkGoFiles(t *testing.T, fn func(rel string, data []byte)) {
	t.Helper()
	root := repoRoot(t)
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
			// ронял сторожа ложно — на своей же копии.
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
		fn(rel, data)
		return nil
	})
	if err != nil {
		t.Fatalf("обход дерева: %v", err)
	}
}
