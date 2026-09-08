// Package testutil — общие помощники тестов.
package testutil

import (
	"testing"

	"go.uber.org/goleak"
)

// Main — TestMain с goleak: горутина, пережившая свой тест, роняет ПАКЕТ
// детерминированно, а не вероятностно на CI race-job (F147–F150: debounce-
// таймер оркестратора, tail'ы Process, страж nwg, инвалидатор кэша).
// Отчёт называет пакет, не тест: виновника ищите по «created by».
// Общие фильтры (goleak.IgnoreTopFunction и т.п.) добавлять здесь, а не в
// leak_test.go каждого пакета; пакетные опции — через opts.
func Main(m *testing.M, opts ...goleak.Option) {
	goleak.VerifyTestMain(m, opts...)
}
