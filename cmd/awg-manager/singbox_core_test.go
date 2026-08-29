package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/awg3endpoint"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// newTestCore собирает ядро над временными каталогами: dir — каталог
// sing-box (config.d), dataDir — awg3.json. queries/commands nil: ядру без
// NDMS они не нужны, ровно как тестам internal/singbox (newOrchedOperator).
func newTestCore(t *testing.T) singboxCore {
	t.Helper()
	dataDir := t.TempDir()
	settingsStore := storage.NewSettingsStore(dataDir)
	loggingService := logging.NewService(settingsStore)
	t.Cleanup(loggingService.Stop)
	return buildSingboxCore(singboxCoreDeps{
		settings: settingsStore,
		appLog:   loggingService,
		bus:      events.NewBus(),
		bootLog:  logging.NewScopedLogger(loggingService, logging.GroupSystem, logging.SubCleanup),
		dataDir:  dataDir,
		dir:      t.TempDir(),
	})
}

// Пин на проводку слотов внутри buildSingboxCore: Register + Bootstrap +
// SetOrch. Запись 10-tunnels.json идёт через оркестратор, поэтому любой
// пропущенный шаг проводки виден отказом ApplyConfig.
// Краснеет на мутации «убрать op.SetOrch(orch)» — ApplyConfig возвращает
// «apply tunnels config: orchestrator not wired».
//
// Честно про границу пина: он пинит ЗАПИСЬ слота, но не валидацию. Без
// бинаря sing-box (dev-машина, CI) валидация падает, оператор считает
// merged-конфиг сломанным не нами и пишет слот через ветку
// mergedAlreadyInvalid (operator_tunnels.go:649-654) — зелёный получается
// именно оттуда.
func TestBuildSingboxCore_ApplyConfigWritesSlot(t *testing.T) {
	core := newTestCore(t)

	cfg := singbox.NewConfig()
	cfg.AddTunnelWithListenPort("A", "vless", "h", 1, 0, json.RawMessage(`{"type":"vless","tag":"A"}`))
	if err := core.op.ApplyConfig(context.Background(), cfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	slot := filepath.Join(core.op.ConfigDir(), "10-tunnels.json")
	if _, err := os.Stat(slot); err != nil {
		t.Fatalf("слот не записан: %v", err)
	}
}

// Пин на подмену meta.HasContent для SlotAwg3: слот AlwaysOn, и без подмены
// он не считается работой вообще — импортированный endpoint не удержал бы
// sing-box запущенным (#456).
// Краснеет на мутации «убрать подмену HasContent для SlotAwg3» — после
// записи endpoint'а HasActiveWork остаётся false.
func TestBuildSingboxCore_Awg3HasContent(t *testing.T) {
	core := newTestCore(t)

	if core.orch.HasActiveWork() {
		t.Fatal("HasActiveWork=true на пустом ядре")
	}
	if err := core.awg3Store.Add(awg3endpoint.Record{
		ID:       "1",
		Tag:      "e1",
		Endpoint: json.RawMessage(`{"type":"awg","tag":"e1"}`),
	}); err != nil {
		t.Fatalf("awg3Store.Add: %v", err)
	}
	if !core.orch.HasActiveWork() {
		t.Fatal("HasActiveWork=false при импортированном AWG3-endpoint")
	}
}

// Пин на конструирующие строки setupSingboxRuntime: потерянное присваивание
// полю *app компилятор не ловит (класс дефекта 0c663c006), а nil-поле
// доезжает до первого обращения уже в рантайме.
// Краснеет на мутации «удалить любую строку постройки» — например
// a.awg3Svc = awg3endpoint.NewService(...).
// singboxInstaller НЕ проверяется: на dev-машине detectArch() уходит в
// warn-ветку и поле остаётся nil — это легальное прод-состояние.
//
// Проверки развёрнуты по полям намеренно: таблица со значениями в any
// была бы вакуумной — типизированный nil-указатель, положенный в
// интерфейс, не равен nil, и мутант проходил бы зелёным.
func TestSetupSingboxRuntime_FieldsConstructed(t *testing.T) {
	dataDir := t.TempDir()
	settingsStore := storage.NewSettingsStore(dataDir)
	loggingService := logging.NewService(settingsStore)
	t.Cleanup(loggingService.Stop)
	a := &app{
		dataDir:        dataDir,
		singboxDir:     t.TempDir(),
		settingsStore:  settingsStore,
		settings:       &storage.Settings{},
		loggingService: loggingService,
		bootLog:        logging.NewScopedLogger(loggingService, logging.GroupSystem, logging.SubCleanup),
		eventBus:       events.NewBus(),
	}

	a.setupSingboxRuntime()

	if a.singboxOp == nil {
		t.Error("singboxOp не собран")
	}
	if a.sbOrch == nil {
		t.Error("sbOrch не собран")
	}
	if a.awg3Store == nil {
		t.Error("awg3Store не собран")
	}
	if a.awg3Svc == nil {
		t.Error("awg3Svc не собран")
	}
	if a.subStore == nil {
		t.Error("subStore не собран")
	}
	if a.subAdapter == nil {
		t.Error("subAdapter не собран")
	}
	if a.subSvc == nil {
		t.Error("subSvc не собран")
	}
	if a.subGroupStore == nil {
		t.Error("subGroupStore не собран")
	}
}
