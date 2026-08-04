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

	// 1. Применённый исходный слот. Порядок перебора и есть правило выбора:
	// слот активного режима важнее слота второго, активное расположение важнее
	// припаркованного (при дрейфе — файл лежит и там, и там — копия из
	// disabled/ уедет в резерв шагом 3). Слот ВТОРОГО режима в хвосте списка
	// нужен для установки, где режим в настройках переключили, но движок в нём
	// ни разу не поднимали: файла активного режима на диске нет, и без этого
	// запасного варианта все правила пользователя уехали бы в резерв, то есть
	// пропали бы из интерфейса.
	chosenName := srcName
	foundApplied := false
	for _, cand := range []struct{ dir, name string }{
		{activeDir, srcName}, {disabledDir, srcName},
		{activeDir, otherName}, {disabledDir, otherName},
	} {
		src := filepath.Join(cand.dir, cand.name)
		if !regularFileExists(src) {
			continue
		}
		split, err := splitLegacySlotFile(src, activeMode)
		if err != nil {
			return false, err
		}
		if err := writeMigratedSlot(filepath.Join(cand.dir, sharedFile), split.Shared, disabledDir); err != nil {
			return false, err
		}
		if err := writeMigratedSlot(filepath.Join(cand.dir, modeFile), split.Mode, disabledDir); err != nil {
			return false, err
		}
		chosenName, foundApplied = cand.name, true
		// Сам исходный файл убирает шаг 3 — в резерв, а не в мусор: после
		// отката версии это единственная копия правил пользователя в форме,
		// которую понимает старый бинарь.
		logf(fmt.Sprintf(
			"раскладка слотов маршрутизации обновлена: правила, наборы и outbound'ы режима %q перенесены из %s в %s, захват трафика — в %s",
			activeMode, cand.name, sharedFile, modeFile))
		for _, r := range split.DNSRenames {
			logf(fmt.Sprintf(
				"DNS-сервер %q переименован в %q: тег зарезервирован движком fakeip-режима, иначе переключение режима заблокировало бы применение конфигурации",
				r.From, r.To))
		}
		break
	}

	// 2. Несохранённый черновик. Режимные слоты собираются из настроек и
	// черновиков не имеют — из разбора берём только общую часть. Черновик
	// берём ТОГО ЖЕ слота, что и применённый файл; когда применённого не было
	// вовсе — по тому же приоритету, что и на шаге 1.
	draftNames := []string{chosenName}
	if !foundApplied {
		draftNames = []string{srcName, otherName}
	}
	for _, name := range draftNames {
		src := filepath.Join(pendingDir, name)
		if !regularFileExists(src) {
			continue
		}
		split, err := splitLegacySlotFile(src, activeMode)
		if err != nil {
			return false, err
		}
		if err := writeMigratedSlot(filepath.Join(pendingDir, sharedFile), split.Shared, disabledDir); err != nil {
			return false, err
		}
		logf(fmt.Sprintf("несохранённый черновик перенесён в pending/%s", sharedFile))
		break
	}

	// 3. Всё, что осталось от прежней раскладки (разобранный исходный слот,
	// слот неактивного режима, дубль из disabled/, черновики), — в резерв под
	// именем, которое новая раскладка уже не подхватит. Удалять нельзя по двум
	// причинам: если у пользователя в двух режимах были РАЗНЫЕ наборы правил,
	// здесь лежит единственная копия набора неактивного режима; а после отката
	// версии резерв — единственный способ вернуть старому бинарю его файл.
	for _, dir := range dirs {
		for _, name := range legacyNames {
			path := filepath.Join(dir, name)
			if !regularFileExists(path) {
				continue
			}
			backup, err := backupLegacySlot(path, disabledDir)
			if err != nil {
				return false, err
			}
			logf(fmt.Sprintf("прежний слот %s сохранён резервной копией: %s", name, backup))
		}
	}
	return true, nil
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
// слотов.
func splitLegacySlotFile(path string, activeMode string) (router.LegacySlotSplit, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return router.LegacySlotSplit{}, fmt.Errorf("прочитать %s: %w", path, err)
	}
	split, err := router.SplitLegacyRoutingSlot(data, activeMode)
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
func writeMigratedSlot(path string, data []byte, disabledDir string) error {
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		if _, err := backupLegacySlot(path, disabledDir); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("прочитать %s: %w", path, err)
	}
	if err := storage.AtomicWrite(path, data); err != nil {
		return fmt.Errorf("записать %s: %w", path, err)
	}
	return nil
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
