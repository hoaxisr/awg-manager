// internal/singbox/migrate_slots_split.go
package singbox

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Имена файлов ПРЕЖНЕЙ раскладки. В реестре слотов их больше нет (подэтап
// 5D0), поэтому литералы здесь — не дубли, а единственное описание того, что
// осталось на диске у существующих установок.
const (
	legacyRouterSlotFile = "20-router.json"
	legacyFakeIPSlotFile = "21-fakeip.json"
	// legacyBackupSuffix помечает резервную копию прежнего слота. Важно, что
	// суффикс ломает имя файла слота: под таким именем файл не подхватит ни
	// оркестратор, ни повторный прогон миграции.
	legacyBackupSuffix = ".pre-5d0"
)

// MigrateSlotsSplit переводит существующую установку с прежней раскладки
// слотов маршрутизации (один файл на режим: 20-router.json для tproxy/policy-tun
// и 21-fakeip.json для fakeip) на новую: общий 21-routing.json с правилами,
// наборами и outbound'ами плюс режимный 20-*.json с одним лишь захватом трафика.
//
// Обязана отработать РАНЬШЕ регистрации слотов и любой записи в них: на
// немигрированной установке общий слот пуст, и первое же включение движка
// собрало бы его с нуля, а генератор режимного слота вычистил бы из старого
// файла пользовательские правила.
//
// Идемпотентна и доводит до конца прерванный прогон. Критерий «мигрировать
// нечего» — отсутствие файлов ПРЕЖНЕЙ раскладки во всех трёх расположениях, а
// не наличие 21-routing.json: при крахе посреди миграции новый файл уже есть, а
// старый ещё лежит рядом, и sing-box сольёт оба в один документ с
// дублирующимися тегами outbound (FATAL при загрузке).
//
// Возвращает changed=true, когда что-то было перенесено или убрано.
func MigrateSlotsSplit(configDir string, activeMode string) (bool, error) {
	return MigrateSlotsSplitWithLog(configDir, activeMode, nil)
}

// MigrateSlotsSplitWithLog — MigrateSlotsSplit с журналированием. logf зовётся
// по одной строке на значимое действие (перенос слота, резервная копия,
// переименование DNS-сервера) и может быть nil.
func MigrateSlotsSplitWithLog(configDir string, activeMode string, logf func(string)) (bool, error) {
	if logf == nil {
		logf = func(string) {}
	}
	activeDir := configDir
	disabledDir := filepath.Join(configDir, "disabled")
	pendingDir := filepath.Join(configDir, "pending")
	dirs := []string{activeDir, disabledDir, pendingDir}
	legacyNames := []string{legacyRouterSlotFile, legacyFakeIPSlotFile}

	if !anyLegacySlotPresent(dirs, legacyNames) {
		return false, nil
	}

	modeSlot := router.ModeSlot(activeMode)
	srcName, otherName := legacyRouterSlotFile, legacyFakeIPSlotFile
	if modeSlot == orchestrator.SlotFakeIP {
		srcName, otherName = legacyFakeIPSlotFile, legacyRouterSlotFile
	}
	sharedFile := knownSlotFilename(orchestrator.SlotRouting)
	modeFile := knownSlotFilename(modeSlot)
	if sharedFile == "" || modeFile == "" {
		return false, fmt.Errorf("миграция слотов: неизвестное имя файла слота (%s/%s)", orchestrator.SlotRouting, modeSlot)
	}
	// Общий слот новой раскладки уже на диске — доказательство, что миграция
	// на этой установке отработала. Дальше разбирается ТОЛЬКО файл активного
	// режима (докат прерванного прогона), а запасной вариант «взять слот
	// второго режима» выключается: иначе возврат резервной копии под исходным
	// именем — то, к чему подталкивает инструкция по откату, — увёл бы
	// актуальный общий слот в резерв и положил поверх правила неактивного
	// режима.
	migrationDone := regularFileExists(filepath.Join(activeDir, sharedFile)) ||
		regularFileExists(filepath.Join(disabledDir, sharedFile))

	// --- Фаза 1: РАЗБОР. Ни одной записи на диск, пока не разобраны все
	// источники. Ошибка здесь оставляет прежнюю раскладку нетронутой и рабочей;
	// ошибка ПОСЛЕ первой записи оставила бы в активном каталоге обе раскладки
	// сразу, а это дублирующиеся теги инбаундов и sing-box, который не
	// поднимается вовсе. ---

	// Порядок перебора и есть правило выбора: слот активного режима важнее
	// слота второго, активное расположение важнее припаркованного (при дрейфе
	// — файл лежит и там, и там — копия из disabled/ уедет в резерв фазой 3).
	// Слот ВТОРОГО режима в хвосте списка нужен для установки, где режим в
	// настройках переключили, но движок в нём ни разу не поднимали: файла
	// активного режима на диске нет, и без запасного варианта все правила
	// пользователя уехали бы в резерв, то есть пропали бы из интерфейса.
	candidates := []struct{ dir, name string }{{activeDir, srcName}, {disabledDir, srcName}}
	if !migrationDone {
		candidates = append(candidates, struct{ dir, name string }{activeDir, otherName},
			struct{ dir, name string }{disabledDir, otherName})
	}
	var applied *router.LegacySlotSplit
	var appliedDir, chosenName string
	for _, cand := range candidates {
		src := filepath.Join(cand.dir, cand.name)
		if !regularFileExists(src) {
			continue
		}
		// Запасной вариант (взят слот НЕ активного режима) пишет только общий
		// слот: режимная часть чужого режима — это инбаунды другого способа
		// захвата, и в файле активного режима им делать нечего. Режимный слот
		// соберёт генератор из настроек при первом же Reconcile.
		fallback := cand.name != srcName
		split, err := splitLegacySlotFile(src, activeMode, fallback)
		if err != nil {
			return false, err
		}
		applied, appliedDir, chosenName = &split, cand.dir, cand.name
		break
	}

	// Черновик staging. Переносится ТОЛЬКО черновик того же слота, что и
	// разобранный применённый файл. На установке в режиме fakeip это значит
	// «не переносится»: SaveDraft всегда звался со слотом 20-router.json, то
	// есть черновика 21-fakeip.json не существует в природе, а лежащий рядом
	// pending/20-router.json — черновик ДРУГОГО набора правил, и класть его
	// поверх данных fakeip нельзя. Такой черновик отбрасывается (копия — в
	// резерве) с отдельной строкой в журнал: пользователь видел баннер «есть
	// несохранённые изменения» и должен узнать, куда они делись.
	var draftShared []byte
	if chosenName != "" {
		if src := filepath.Join(pendingDir, chosenName); regularFileExists(src) {
			split, err := splitLegacySlotFile(src, activeMode, true)
			if err != nil {
				// Битый черновик не повод бросать миграцию применённого
				// конфига: он всё равно уедет в резерв целиком.
				logf(fmt.Sprintf("черновик %s не разобран (%v) — отброшен, копия остаётся в резерве", chosenName, err))
			} else {
				draftShared = split.Shared
			}
		}
	}
	for _, name := range legacyNames {
		if name == chosenName {
			continue
		}
		if regularFileExists(filepath.Join(pendingDir, name)) {
			logf(fmt.Sprintf(
				"несохранённый черновик pending/%s отброшен: он принадлежит другому режиму, чем применённая конфигурация; копия остаётся в резерве", name))
		}
	}

	// --- Фаза 2: ЗАПИСЬ. ---
	wrote := false
	var firstErr error
	fail := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	if applied != nil {
		ok, err := writeMigratedSlot(filepath.Join(appliedDir, sharedFile), applied.Shared, disabledDir)
		wrote = wrote || ok
		if err != nil {
			fail(err)
		} else if applied.Mode != nil {
			ok, err := writeMigratedSlot(filepath.Join(appliedDir, modeFile), applied.Mode, disabledDir)
			wrote = wrote || ok
			if err != nil {
				fail(err)
			}
		}
		if firstErr == nil {
			// Сам исходный файл убирает фаза 3 — в резерв, а не в мусор: после
			// отката версии это единственная копия правил пользователя в форме,
			// которую понимает старый бинарь.
			logf(fmt.Sprintf(
				"раскладка слотов маршрутизации обновлена: правила, наборы и outbound'ы перенесены из %s в %s (режим %q)",
				chosenName, sharedFile, activeMode))
			for _, r := range applied.DNSRenames {
				logf(fmt.Sprintf(
					"DNS-сервер %q переименован в %q: тег зарезервирован движком fakeip-режима, иначе переключение режима заблокировало бы применение конфигурации",
					r.From, r.To))
			}
		}
	}
	if draftShared != nil && firstErr == nil {
		ok, err := writeMigratedSlot(filepath.Join(pendingDir, sharedFile), draftShared, disabledDir)
		wrote = wrote || ok
		if err != nil {
			// Best-effort: потерянный черновик — это неприменённые правки, а
			// оборванная на этом месте миграция — активный каталог с двумя
			// раскладками сразу. Уборка ниже важнее черновика.
			logf(fmt.Sprintf("черновик не перенесён (%v) — копия остаётся в резерве", err))
		} else {
			logf(fmt.Sprintf("несохранённый черновик перенесён в pending/%s", sharedFile))
		}
	}

	// --- Фаза 3: УБОРКА. Выполняется ВСЕГДА, кроме одного случая: запись
	// сорвалась, не создав ни одного файла новой раскладки. Только тогда на
	// диске осталась нетронутая прежняя раскладка, и убирать её нельзя —
	// движок продолжит работать на ней до следующей загрузки.
	//
	// Всё, что осталось от прежней раскладки (разобранный исходный слот, слот
	// неактивного режима, дубль из disabled/, черновики), уходит в резерв под
	// именем, которое новая раскладка уже не подхватит. Удалять нельзя по двум
	// причинам: если у пользователя в двух режимах были РАЗНЫЕ наборы правил,
	// здесь лежит единственная копия набора неактивного режима; а после отката
	// версии резерв — единственный способ вернуть старому бинарю его файл. ---
	if firstErr != nil && !wrote {
		return false, firstErr
	}
	movedAny := false
	for _, dir := range dirs {
		for _, name := range legacyNames {
			path := filepath.Join(dir, name)
			if !regularFileExists(path) {
				continue
			}
			backup, err := backupLegacySlot(path, disabledDir)
			if err != nil {
				fail(err)
				continue
			}
			movedAny = true
			logf(fmt.Sprintf("прежний слот %s сохранён резервной копией: %s", name, backup))
		}
	}
	return wrote || movedAny, firstErr
}

// anyLegacySlotPresent — критерий «есть что мигрировать».
func anyLegacySlotPresent(dirs, names []string) bool {
	for _, dir := range dirs {
		for _, name := range names {
			if regularFileExists(filepath.Join(dir, name)) {
				return true
			}
		}
	}
	return false
}

// splitLegacySlotFile читает файл прежней раскладки и разбирает его на пару
// слотов. sharedOnly — режимный слот не собирается (запасной вариант и
// черновик); принадлежность источника к fakeip определяется его ИМЕНЕМ, а не
// активным режимом: DNS в 21-fakeip.json — механизм режима, в 20-router.json —
// пользовательские серверы, и путать их нельзя даже когда режим сменился.
func splitLegacySlotFile(path string, activeMode string, sharedOnly bool) (router.LegacySlotSplit, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return router.LegacySlotSplit{}, fmt.Errorf("прочитать %s: %w", path, err)
	}
	split, err := router.SplitLegacyRoutingSlot(data, router.LegacySlotSplitParams{
		Mode:           activeMode,
		SourceIsFakeIP: filepath.Base(path) == legacyFakeIPSlotFile,
		SharedOnly:     sharedOnly,
	})
	if err != nil {
		return router.LegacySlotSplit{}, fmt.Errorf("разобрать %s: %w", path, err)
	}
	return split, nil
}

// writeMigratedSlot кладёт содержимое нового слота, предварительно уводя в
// резерв уже лежащий там ДРУГОЙ файл. Такое возможно на двух путях: прогон
// миграции, прерванный между записью и уборкой, и откат версии, при котором
// старый бинарь пересоздал файл прежней раскладки рядом с новым. В обоих
// случаях истина — файл прежней раскладки (он новее), но затирать чужие данные
// молча нельзя.
//
// Первым значением возвращает «файл новой раскладки записан»: по нему
// вызывающий решает, можно ли убирать прежнюю раскладку. Байт-равное
// содержимое записью не считается — файл и так уже на месте, но и уборке это
// не мешает, поэтому возвращается true.
func writeMigratedSlot(path string, data []byte, disabledDir string) (bool, error) {
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, data) {
			return true, nil
		}
		if _, err := backupLegacySlot(path, disabledDir); err != nil {
			return false, err
		}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("прочитать %s: %w", path, err)
	}
	if err := storage.AtomicWrite(path, data); err != nil {
		return false, fmt.Errorf("записать %s: %w", path, err)
	}
	return true, nil
}

// backupLegacySlot переносит файл в disabled/ под именем с суффиксом
// legacyBackupSuffix и возвращает путь резервной копии. Столкновение имён
// (например, слот прежней раскладки лежал сразу в двух каталогах) разводится
// числовым суффиксом — ни одна копия не затирается.
func backupLegacySlot(path, disabledDir string) (string, error) {
	if err := os.MkdirAll(disabledDir, 0755); err != nil {
		return "", fmt.Errorf("создать %s: %w", disabledDir, err)
	}
	base := filepath.Join(disabledDir, filepath.Base(path)+legacyBackupSuffix)
	dst := base
	for n := 2; regularFileExists(dst); n++ {
		dst = fmt.Sprintf("%s.%d", base, n)
	}
	if err := os.Rename(path, dst); err != nil {
		return "", fmt.Errorf("сохранить резервную копию %s: %w", path, err)
	}
	return dst, nil
}

// knownSlotFilename отдаёт имя файла слота из реестра оркестратора: миграция
// обязана писать ровно те имена, которые потом зарегистрирует оркестратор.
func knownSlotFilename(slot orchestrator.Slot) string {
	for _, meta := range orchestrator.KnownSlots() {
		if meta.Slot == slot {
			return meta.Filename
		}
	}
	return ""
}

// regularFileExists — существует и является обычным файлом.
func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
