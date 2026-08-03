package dnsrewrite

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// SlotName — имя слота оркестратора (= orchestrator.SlotDNSRewrites как строка).
const SlotName = "dns-rewrites"

type Store interface {
	List() ([]DNSRewrite, error)
	Add(DNSRewrite) error
	Update(int, DNSRewrite) error
	Delete(int) error
	Move(from, to int) error
	ReplaceManaged(id string, items []DNSRewrite) error
}

type Orchestrator interface {
	Save(slot string, data []byte) error
	SetEnabled(slot string, on bool) error
}

type EventBus interface {
	Publish(eventType string, data any)
}

type Service struct {
	store Store
	orch  Orchestrator
	bus   EventBus
}

func NewService(store Store, orch Orchestrator, bus EventBus) *Service {
	return &Service{store: store, orch: orch, bus: bus}
}

func (s *Service) List() ([]DNSRewrite, error) { return s.store.List() }

func (s *Service) Add(r DNSRewrite) error {
	r.Managed = "" // API/user path cannot forge managed ownership
	if _, err := compileRewrite(r); err != nil {
		return err
	}
	if err := s.store.Add(r); err != nil {
		return err
	}
	return s.flush()
}

func (s *Service) Update(index int, r DNSRewrite) error {
	items, err := s.store.List()
	if err != nil {
		return err
	}
	if index < 0 || index >= len(items) {
		return fmt.Errorf("dns rewrite index %d out of range", index)
	}
	if id := items[index].Managed; id != "" {
		return fmt.Errorf("перезапись управляется пресетом %q — снимите пресет в настройках движка", id)
	}
	r.Managed = ""
	if _, err := compileRewrite(r); err != nil {
		return err
	}
	if err := s.store.Update(index, r); err != nil {
		return err
	}
	return s.flush()
}

func (s *Service) Delete(index int) error {
	items, err := s.store.List()
	if err != nil {
		return err
	}
	if index < 0 || index >= len(items) {
		return fmt.Errorf("dns rewrite index %d out of range", index)
	}
	if id := items[index].Managed; id != "" {
		return fmt.Errorf("перезапись управляется пресетом %q — снимите пресет в настройках движка", id)
	}
	if err := s.store.Delete(index); err != nil {
		return err
	}
	return s.flush()
}

func (s *Service) Move(from, to int) error {
	items, err := s.store.List()
	if err != nil {
		return err
	}
	n := len(items)
	if from < 0 || from >= n || to < 0 || to >= n {
		return fmt.Errorf("dns rewrite move index out of range")
	}
	if id := items[from].Managed; id != "" {
		return fmt.Errorf("перезапись управляется пресетом %q — снимите пресет в настройках движка", id)
	}
	if id := items[to].Managed; id != "" {
		return fmt.Errorf("перезапись управляется пресетом %q — снимите пресет в настройках движка", id)
	}
	if err := s.store.Move(from, to); err != nil {
		return err
	}
	return s.flush()
}

// Resync пересобирает слот из текущего содержимого стора.
func (s *Service) Resync() error { return s.flush() }

// SyncManagedKeenDNS приводит managed-набор пресета keendns в соответствие с
// (domain, lanIP): точный FQDN, *.FQDN и порталы локального доступа → lanIP.
// Пустой domain или lanIP = снести managed-записи. Единственный контракт —
// «набор равен аргументам», решение «а надо ли вообще трогать» принимает
// вызывающий (router.syncKeenDNSRewrites бережёт last-good при флапе NDMS).
//
// Идемпотентна и БЕЗ побочных эффектов, если набор уже совпадает: Reconcile
// зовёт её каждые 30с, а любая запись — это writeAtomic двух файлов на флеш,
// пересборка слота и SSE-инвалидация у фронта.
func (s *Service) SyncManagedKeenDNS(domain, lanIP string) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	domain = strings.TrimSuffix(domain, ".")
	lanIP = strings.TrimSpace(lanIP)

	var items []DNSRewrite
	if domain != "" && lanIP != "" {
		patterns := append([]string{domain, "*." + domain}, keenDNSPortalDomains...)
		items = make([]DNSRewrite, 0, len(patterns))
		for _, p := range patterns {
			r := DNSRewrite{Pattern: p, IPs: []string{lanIP}, Managed: ManagedKeenDNS}
			if _, err := compileRewrite(r); err != nil {
				return err
			}
			items = append(items, r)
		}
	}
	if managedEqual(s.currentManaged(), items) {
		return nil
	}
	if err := s.store.ReplaceManaged(ManagedKeenDNS, items); err != nil {
		return err
	}
	return s.flush()
}

// currentManaged возвращает записи пресета keendns в текущем порядке стора.
// Ошибка чтения = "неизвестно" → nil, и вызывающий пойдёт на полную запись.
func (s *Service) currentManaged() []DNSRewrite {
	items, err := s.store.List()
	if err != nil {
		return nil
	}
	out := make([]DNSRewrite, 0, len(items))
	for _, r := range items {
		if r.Managed == ManagedKeenDNS {
			out = append(out, r)
		}
	}
	return out
}

// managedEqual сравнивает наборы по паттернам и IP — Managed у обоих один и
// тот же по построению.
func managedEqual(a, b []DNSRewrite) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Pattern != b[i].Pattern || !slices.Equal(a[i].IPs, b[i].IPs) {
			return false
		}
	}
	return true
}

type slotConfig struct {
	DNS slotDNS `json:"dns"`
}
type slotDNS struct {
	Rules []map[string]any `json:"rules"`
}

func (s *Service) flush() error {
	items, err := s.store.List()
	if err != nil {
		return err
	}
	rules := make([]map[string]any, 0, len(items))
	for _, r := range items {
		compiled, err := compileRewrite(r)
		if err != nil {
			return fmt.Errorf("compile %q: %w", r.Pattern, err)
		}
		rules = append(rules, compiled...)
	}
	data, err := json.MarshalIndent(slotConfig{DNS: slotDNS{Rules: rules}}, "", "  ")
	if err != nil {
		return err
	}
	if err := s.orch.Save(SlotName, data); err != nil {
		return err
	}
	if err := s.orch.SetEnabled(SlotName, len(rules) > 0); err != nil {
		return err
	}
	if s.bus != nil {
		s.bus.Publish("resource:invalidated", map[string]any{"resource": "singbox.dns-rewrites"})
	}
	return nil
}
