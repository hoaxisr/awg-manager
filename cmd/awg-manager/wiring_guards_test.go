package main

import (
	"os"
	"strings"
	"testing"
)

// Стражи проводки. Решения ветки проверены как чистые функции, но каждое из
// них стоит ровно столько, сколько стоит ФАКТ вызова из нужной ветки: именно
// эта связь в прошлый раз молча оказалась не там, и поймал её только стенд.
// Достать её из теста иначе нельзя — фазы проводки требуют живого роутера.

func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// F194: кэш оркестратора наполняется независимо от состояния WAN. Загнанный
// обратно в ветку «шлюз найден», LoadState оставлял карту туннелей пустой на
// загрузке без WAN — и пришедший через минуту WAN-up решал по пустой карте:
// ноль действий, ни строки в журнале, туннели стоят до ручного старта.
func TestBootSequence_LoadStateRunsBeforeGatewayProbe(t *testing.T) {
	src := readSource(t, "boot.go")

	load := strings.Index(src, "a.orch.LoadState(a.shutdownCtx)")
	if load < 0 {
		t.Fatal("LoadState на пути холодной загрузки исчез")
	}
	// Пробу ищем ПОСЛЕ LoadState: раньше в файле есть ещё одна, из фазы
	// ожидания WAN (Phase 1b), и она к решению бута отношения не имеет.
	rest := src[load:]
	probe := strings.Index(rest, "a.ndmsQueries.Routes.GetDefaultGatewayInterface(a.shutdownCtx)")
	send := strings.Index(rest, "HandleEvent(a.shutdownCtx, bootEvent(err))")

	if probe < 0 || send < 0 {
		t.Fatalf("проводка бута изменилась: probe=%d send=%d", probe, send)
	}
	if probe > send {
		t.Error("порядок обязан быть: LoadState → проба шлюза → событие бута")
	}
}

// Восстановление из бэкапа бутит без проверки WAN. Это осознанно (архив и так
// разворачивается холодным стартом), но осознанность должна быть видимой:
// сменив здесь значение на результат пробы, легко получить молчаливый отказ
// восстановления вместо бута.
func TestBootSequence_PostRestoreBootsWithoutWANGate(t *testing.T) {
	src := readSource(t, "boot.go")
	if !strings.Contains(src, "Type:  orchestrator.EventBoot,\n\t\t\t\tWANUp: true,") {
		t.Error("путь post-restore больше не шлёт EventBoot{WANUp: true} — проверьте, что это намеренно")
	}
}

// Главный инвариант F197: не узнали версию — НЕ ДЕЙСТВУЕМ. Половина проводки
// (выбор оператора, режим файрвола, гейт DNS-маршрутов) замерзает снимком
// прямо здесь, поэтому продолжение на умолчании неисправимо без перезапуска.
func TestNDMSWiring_WaitsForVersionOnFailure(t *testing.T) {
	src := readSource(t, "wiring_core.go")

	init := strings.Index(src, "if err := ndmsinfo.Init(context.Background(), a.ndmsQueries.SystemInfo, ndmsTimeout); err != nil {")
	wait := strings.Index(src, "a.waitForNDMSVersion(err)")

	if init < 0 || wait < 0 {
		t.Fatalf("проводка версии изменилась: init=%d wait=%d", init, wait)
	}
	if wait < init || wait-init > 200 {
		t.Error("отказ Init обязан уходить в ожидание версии, а не идти дальше на умолчании")
	}
}

// Отложенный бут исполняется под контекстом жизни демона — но только если
// этот контекст в оркестратор вообще передан.
func TestShutdownWiring_PassesBaseContextToOrchestrator(t *testing.T) {
	src := readSource(t, "wiring_server.go")
	if !strings.Contains(src, "a.orch.SetBaseContext(a.shutdownCtx)") {
		t.Error("контекст жизни демона не передан оркестратору — отложенный бут пойдёт под дедлайном хука")
	}
}
