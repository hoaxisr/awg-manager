package autolist

import (
	"errors"
	"net"
	"sort"
	"strings"
)

// ExcludeDomains навсегда добавляет домены в ручные исключения и сразу
// убирает их из текущих кандидатов. Существующие правила маршрутизации не
// меняются. Повторное исключение является успешным no-op.
func (e *Engine) ExcludeDomains(domains []string) error {
	if len(domains) == 0 {
		return errors.New("domains required")
	}
	normalized := make([]string, 0, len(domains))
	normalizedSeen := make(map[string]bool, len(domains))
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
		if d == "" || net.ParseIP(d) != nil || strings.ContainsAny(d, " \t\n/") {
			return errors.New("некорректный домен в исключениях")
		}
		if !normalizedSeen[d] {
			normalized = append(normalized, d)
			normalizedSeen[d] = true
		}
	}
	if err := e.updateConfig(func(cfg *Config) error {
		seen := make(map[string]bool, len(cfg.ExcludeDomains)+len(normalized))
		for _, d := range cfg.ExcludeDomains {
			seen[d] = true
		}
		for _, d := range normalized {
			seen[d] = true
		}
		cfg.ExcludeDomains = make([]string, 0, len(seen))
		for d := range seen {
			cfg.ExcludeDomains = append(cfg.ExcludeDomains, d)
		}
		sort.Strings(cfg.ExcludeDomains)
		return nil
	}); err != nil {
		return err
	}

	// Снимки всех строк берутся до любого Forget: один массовый exclude может
	// затрагивать и точный хост, и его регистрируемый домен.
	historyAt := e.now()
	history := make([]HistoryEntry, 0, len(normalized))
	for _, d := range normalized {
		row, exists := e.table.get(d)
		if !exists {
			if domain := registeredDomain(d); domain != "" && domain != d {
				row, exists = e.table.get(domain)
			}
		}
		history = append(history, historyEntryFromCandidate(d, "excluded", "user", historyAt, row, exists))
	}
	for _, d := range normalized {
		e.cancelDomain(d)
		e.table.Forget(d)
		e.releaseDomainRevision(d)
		if domain := registeredDomain(d); domain != "" && domain != d {
			e.cancelDomain(domain)
			e.table.Forget(domain)
			e.releaseDomainRevision(domain)
		}
	}
	// Конфиг уже сохранён: запись всей массовой операции best-effort и одна.
	e.recordHistory(history)
	return nil
}
