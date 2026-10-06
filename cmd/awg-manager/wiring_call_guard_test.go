package main

import (
	"os"
	"strings"
	"testing"
)

// Слушатель существования диспетчера — единственный путь, которым UI узнаёт о
// внешнем создании/снятии интерфейса (tunnels, servers). Пропавший вызов
// сборка и тесты диспетчера не заметят.
func TestSetupEventWiring_WiresExistencePublisher(t *testing.T) {
	src, err := os.ReadFile("wiring_routing.go")
	if err != nil {
		t.Fatalf("чтение проводки: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "func (a *app) setupEventWiring()")
	if start < 0 {
		t.Fatal("setupEventWiring не найдена — проверку надо переписать под новое имя")
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		t.Fatal("не видно конца setupEventWiring")
	}
	if !strings.Contains(body[start:start+end], "SetExistenceListed(existencePublisher(") {
		t.Fatal("setupEventWiring не вешает existencePublisher: UI не узнает о внешнем создании/снятии интерфейса")
	}
}

// П22: вердикт «свой хук» выносит точка входа spool по кредитам стора
// интерфейсов. Без claimer'а (nil-безопасен, п.10) каждый свой хук — чужой:
// список и публикация на каждое своё создание/снятие, проба и замок
// оркестратора на свой ifdestroyed, своя грань conf — в settle/окно. Сборка и
// тесты пакетов этого не заметят.
// Мутация: NewHookSink(a.ndmsDispatcher, nil) → красный.
func TestWiring_HookSinkClaimsFromInterfaces(t *testing.T) {
	src, err := os.ReadFile("wiring_core.go")
	if err != nil {
		t.Fatalf("чтение проводки: %v", err)
	}
	if !strings.Contains(string(src), "api.NewHookSink(a.ndmsDispatcher, a.ndmsQueries.Interfaces)") {
		t.Fatal("точка входа spool не гасит кредиты стора интерфейсов: свои хуки станут чужими")
	}
}

// П25: конец нашего сохранения — событие ConfigurationSaved шины ndm. Без
// клиента шины координатор остаётся «без шины»: полёт кончается ответом на
// POST, и `no interface` идёт во время записи (D-N3). Сборка и тесты пакетов
// этого не заметят. Клиент — сразу за координатором (busUp до первого POST).
// Мутация: убрать NewBusReader/Start или адаптер события → красный.
func TestWiring_NDMBusWired(t *testing.T) {
	src, err := os.ReadFile("wiring_tunnels.go")
	if err != nil {
		t.Fatalf("чтение проводки: %v", err)
	}
	body := string(src)
	coord := strings.Index(body, "a.ndmsSaveCoord = ndmscommand.NewSaveCoordinator(")
	bus := strings.Index(body, "ndmsevents.NewBusReader(ndmsevents.DefaultBusPath,")
	cmds := strings.Index(body, "a.ndmsCommands = ndmscommand.NewCommands(")
	if coord < 0 || bus < 0 || cmds < 0 || !(coord < bus && bus < cmds) {
		t.Fatalf("клиент шины не между координатором и командами: coord=%d bus=%d cmds=%d", coord, bus, cmds)
	}
	for _, want := range []string{
		"a.ndmsSaveCoord.OnConfigurationSaved(ev.RaiseTime)",
		"ev.Class == ndmsevents.BusClassConfigurationSaved",
		"}, a.ndmsSaveCoord.OnBusState, ",
		"if err := ndmBus.Start(); err != nil {",
		"a.deferOnExit(ndmBus.Stop)",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("в проводке шины нет %q", want)
		}
	}
}
