package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// MigrateDuplicateGroupMembers сворачивает повторы членов в уже ЗАПИСАННЫХ
// группах selector/urltest/fallback (F731).
//
// sing-box 1.15 отвергает группу, где член указан дважды («duplicate tag»), и
// не стартует целиком. Новые записи повтор не пропускают (validateCompositeOutbound
// отказывает, генераторы сворачивают через UniqueMemberTags), но конфиг,
// записанный прошлой версией панели, перезаписывать некому — без этого прохода
// обновление движка оставило бы его в цикле FATAL'ов до правки руками.
//
// Обход — как у MigrateAddressOrRules: все *.json в active, disabled/ и
// pending/ (группы живут в нескольких слотах: маршрутизатор, fakeip, подписки,
// прокси для устройств); слот ручного редактора не трогаем — он
// пользовательский. Свёртка оставляет первое вхождение, порядок членов
// сохраняется. Идемпотентно.
func MigrateDuplicateGroupMembers(configDir string) (bool, error) {
	changed := false
	var failures []error
	for _, pat := range []string{
		filepath.Join(configDir, "*.json"),
		filepath.Join(configDir, "disabled", "*.json"),
		filepath.Join(configDir, "pending", "*.json"),
	} {
		matches, err := filepath.Glob(pat)
		if err != nil {
			return changed, fmt.Errorf("glob %s: %w", pat, err)
		}
		for _, p := range matches {
			if filepath.Base(p) == userSlotFilename() {
				continue
			}
			fileChanged, err := migrateGroupMembersFile(p)
			if err != nil {
				// Нечитаемый файл не должен оставить остальные слоты
				// падающими до следующего запуска — доходим до конца.
				failures = append(failures, err)
				continue
			}
			changed = changed || fileChanged
		}
	}
	return changed, errors.Join(failures...)
}

func migrateGroupMembersFile(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	next, changed, err := dedupeGroupMembersJSON(data)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	if !changed {
		return false, nil
	}
	if err := storage.AtomicWrite(path, next); err != nil {
		return false, err
	}
	return true, nil
}

// dedupeGroupMembersJSON сворачивает повторы в outbounds[].outbounds групп
// слот-файла через общий map — поля, которых нет в наших структурах,
// переживают перезапись.
func dedupeGroupMembersJSON(data []byte) ([]byte, bool, error) {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, false, fmt.Errorf("parse: %w", err)
	}
	outbounds, ok := root["outbounds"].([]any)
	if !ok {
		return nil, false, nil
	}
	changed := false
	for _, entry := range outbounds {
		obj, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		switch obj["type"] {
		case "selector", "urltest", "fallback":
		default:
			continue
		}
		members, ok := obj["outbounds"].([]any)
		if !ok {
			continue
		}
		seen := make(map[string]bool, len(members))
		kept := make([]any, 0, len(members))
		for _, m := range members {
			if tag, isStr := m.(string); isStr {
				if seen[tag] {
					continue
				}
				seen[tag] = true
			}
			kept = append(kept, m)
		}
		if len(kept) != len(members) {
			obj["outbounds"] = kept
			changed = true
		}
	}
	if !changed {
		return nil, false, nil
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, false, fmt.Errorf("marshal: %w", err)
	}
	return out, true, nil
}
