package autolist

import (
	"errors"
	"log"
	"net"
	"strings"
)

// RestoreDomain отменяет временное или постоянное скрытие домена. Записи
// dismissed/excluded удаляются после успешного сохранения конфига.
// Повторное восстановление — успешный no-op.
func (e *Engine) RestoreDomain(domain string) error {
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if domain == "" || net.ParseIP(domain) != nil || strings.ContainsAny(domain, " \t\n/") {
		return errors.New("некорректный домен")
	}
	if err := e.updateConfig(func(cfg *Config) error {
		kept := make([]string, 0, len(cfg.ExcludeDomains))
		for _, excluded := range cfg.ExcludeDomains {
			if excluded != domain {
				kept = append(kept, excluded)
			}
		}
		cfg.ExcludeDomains = kept
		return nil
	}); err != nil {
		return err
	}
	e.table.RestoreDismiss(domain)
	if err := e.history.removeHidden(domain); err != nil {
		log.Printf("[autolist] история после восстановления не сохранена: %v", err)
	}
	return nil
}
