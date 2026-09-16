package router

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"encoding/json"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// MigrateDNSRuleSetMatchSource дописывает rule_set_ip_cidr_match_source в уже
// ЗАПИСАННЫЕ dns-правила со ссылкой на rule_set.
//
// Без этого прохода applyDNSRuleSetMatchSource чинит только следующую запись
// конфига, а у пострадавшего конфиг записан прошлой версией панели и
// перезаписывать его некому: reconcileInstalled при живом перехвате уходит в
// heal1140SlotMigration, а тот выходит по гейту «конфиг уже в форме 1.14»
// (service_lifecycle.go:1110). То есть обновление панели цикл FATAL'ов не
// разрывало бы — движок падал бы до первой правки конфига руками. Ровно та же
// причина, по которой заведён MigrateAddressOrRules.
//
// Обход слотов — как у MigrateAddressOrRules: active, disabled/, pending/;
// слот ручного редактора пропускаем — его пишет пользователь, за его спиной
// не правим. pending/ важен отдельно: черновик применяется промоутом байтов
// (ApplyDraft), без материализации, и без этого прохода вернул бы FATAL уже
// после починки.
//
// Правка чисто аддитивная — только добавляем ключ в объект правила, поэтому
// типизированный round-trip (и его риск потерять неизвестное поле) не нужен.
// Идемпотентно: правило с уже выставленным ключом пропускается.
func MigrateDNSRuleSetMatchSource(configDir string) (bool, error) {
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
			fileChanged, err := migrateDNSMatchSourceFile(p)
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

func migrateDNSMatchSourceFile(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	next, changed, err := addDNSRuleSetMatchSourceJSON(data)
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

// addDNSRuleSetMatchSourceJSON проставляет ключ в dns.rules[] слот-файла,
// оставляя остальную структуру байт в байт. Вложенные правила (type=logical)
// обходятся рекурсивно: форк собирает признак legacy-режима и по ним
// (dns/router.go dnsRuleModeRequirementsInRule), так что пропуск вложенного
// правила оставил бы FATAL.
func addDNSRuleSetMatchSourceJSON(data []byte) ([]byte, bool, error) {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, false, fmt.Errorf("parse: %w", err)
	}
	dns, ok := root["dns"].(map[string]any)
	if !ok {
		return nil, false, nil
	}
	rules, ok := dns["rules"].([]any)
	if !ok {
		return nil, false, nil
	}
	changed := false
	for _, entry := range rules {
		obj, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if addMatchSourceToRuleMap(obj) {
			changed = true
		}
	}
	if !changed {
		return nil, false, nil
	}
	dns["rules"] = rules
	root["dns"] = dns
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, false, fmt.Errorf("marshal: %w", err)
	}
	return out, true, nil
}

// addMatchSourceToRuleMap правит одно dns-правило на месте и сообщает, менял ли
// что-нибудь. Правило с match_response не трогаем — там ip_cidr набора
// матчится по ОТВЕТУ, и ключ это сломал бы (см. applyDNSRuleSetMatchSource).
func addMatchSourceToRuleMap(obj map[string]any) bool {
	changed := false
	for _, nested := range nestedDNSRuleMaps(obj) {
		if addMatchSourceToRuleMap(nested) {
			changed = true
		}
	}
	if _, has := obj["match_response"]; has {
		return changed
	}
	set, ok := obj["rule_set"]
	if !ok {
		return changed
	}
	if list, isList := set.([]any); isList && len(list) == 0 {
		return changed
	}
	if cur, has := obj["rule_set_ip_cidr_match_source"]; has {
		if b, isBool := cur.(bool); isBool && b {
			return changed
		}
	}
	obj["rule_set_ip_cidr_match_source"] = true
	return true
}

func nestedDNSRuleMaps(obj map[string]any) []map[string]any {
	raw, ok := obj["rules"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		if m, ok := entry.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
