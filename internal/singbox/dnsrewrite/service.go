package dnsrewrite

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
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

type Service struct {
	store Store
	orch  Orchestrator

	// mu сериализует «прочитать список → изменить → пересобрать слот».
	// Стор атомарен поштучно, но с приходом keendns-пресета у него два
	// писателя: HTTP-CRUD и фоновый Reconcile. Без общего лока пользователь
	// мог удалить не ту запись (индекс посчитан до того, как sync снял
	// managed-набор), а отставший flush — записать слот без свежей записи.
	mu sync.Mutex
	// slotDirty взводится, когда стор уже изменён, а слот пересобрать не
	// удалось. Без него идемпотентный гвард в SetKeenDNSEnabled замыкал бы
	// накоротко навсегда: стор целевой, а слот остался старым.
	slotDirty bool
	// keenDNSOn/keenDNSExtra — состояние пресета keendns: включён ли блок и
	// доп. имя роутера, если /show/ndns отдал FQDN вне keenDNSZones. Живёт
	// в памяти: источник правды — настройки движка, роутер зовёт
	// SetKeenDNSEnabled на старте и каждый Reconcile.
	keenDNSOn    bool
	keenDNSExtra string
}

func NewService(store Store, orch Orchestrator) *Service {
	return &Service{store: store, orch: orch}
}

func (s *Service) List() ([]DNSRewrite, error) { return s.store.List() }

func (s *Service) Add(r DNSRewrite) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
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
func (s *Service) Resync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flush()
}

// SetKeenDNSEnabled включает или снимает блок пресета keendns в слоте:
// DNS-сервер на резолвер роутера и правило, отправляющее туда имена
// KeenDNS/CrazeDNS. extraDomain — FQDN роутера из /show/ndns; учитывается
// только если он не покрыт keenDNSZones (страховка на случай новой зоны).
//
// Идемпотентна: Reconcile зовёт её каждые 30с, а любая запись — это
// пересборка слота, SIGHUP sing-box и SSE-инвалидация у фронта.
func (s *Service) SetKeenDNSEnabled(on bool, extraDomain string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	extra := normalizeDomain(extraDomain)
	if extra != "" && domainCoveredByKeenDNS(extra) {
		extra = ""
	}
	// Разовая уборка: до 2.18.0 пресет клал в стор managed-перезаписи своего
	// FQDN на LAN-адрес роутера, и они ломали доступ к его морде по имени
	// KeenDNS (issue #729). Снимаем их независимо от того, включён пресет
	// сейчас или нет.
	dropped, err := s.dropManagedRewrites()
	if err != nil {
		return err
	}
	if !dropped && !s.slotDirty && s.keenDNSOn == on && s.keenDNSExtra == extra {
		return nil
	}
	s.keenDNSOn, s.keenDNSExtra = on, extra
	return s.flush()
}

// dropManagedRewrites сносит из стора перезаписи прежнего пресета. Отдаёт
// true, если что-то реально удалено.
func (s *Service) dropManagedRewrites() (bool, error) {
	items, err := s.store.List()
	if err != nil {
		return false, err
	}
	found := false
	for _, r := range items {
		if r.Managed == ManagedKeenDNS {
			found = true
			break
		}
	}
	if !found {
		return false, nil
	}
	if err := s.store.ReplaceManaged(ManagedKeenDNS, nil); err != nil {
		return false, err
	}
	return true, nil
}

// keenDNSSlotRules отдаёт (сервер, правило) блока пресета для слота.
func (s *Service) keenDNSSlotRules() (map[string]any, map[string]any) {
	domains := KeenDNSHosts()
	if s.keenDNSExtra != "" {
		domains = append(domains, s.keenDNSExtra)
	}
	// Резолвер роутера — ndnproxy, он слушает 0.0.0.0:53, так что 127.0.0.1
	// это он же (проверено netstat на железе). Адрес-константа, в отличие от
	// IP на мосту, не зависит от того, какую LAN-подсеть выбрал пользователь.
	server := map[string]any{
		"tag":    keenDNSServerTag,
		"type":   "udp",
		"server": "127.0.0.1",
	}
	rule := map[string]any{
		"domain":        domains,
		"domain_suffix": KeenDNSZones(),
		"server":        keenDNSServerTag,
	}
	return server, rule
}

func normalizeDomain(d string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
}

// domainCoveredByKeenDNS сообщает, попадает ли имя под keenDNSHosts/keenDNSZones.
func domainCoveredByKeenDNS(d string) bool {
	if slices.Contains(keenDNSHosts, d) {
		return true
	}
	for _, z := range keenDNSZones {
		if d == z || strings.HasSuffix(d, "."+z) {
			return true
		}
	}
	return false
}

type slotConfig struct {
	DNS slotDNS `json:"dns"`
}
type slotDNS struct {
	Servers []map[string]any `json:"servers,omitempty"`
	Rules   []map[string]any `json:"rules"`
}

// keenDNSServerTag — тег DNS-сервера пресета в объединённом конфиге.
const keenDNSServerTag = "keendns-router"

func (s *Service) flush() error {
	items, err := s.store.List()
	if err != nil {
		return err
	}
	// Блок пресета идёт ПЕРВЫМ: sing-box берёт первое совпавшее правило, и
	// широкий пользовательский паттерн (*.pro) иначе перехватил бы имена
	// KeenDNS. Слот 17-dns-rewrites.json мержится раньше 21-fakeip.json, так
	// что этот же порядок уводит имена роутера мимо fakeip.
	var servers []map[string]any
	rules := make([]map[string]any, 0, len(items)+1)
	if s.keenDNSOn {
		server, rule := s.keenDNSSlotRules()
		servers = append(servers, server)
		rules = append(rules, rule)
	}
	for _, r := range items {
		compiled, err := compileRewrite(r)
		if err != nil {
			return fmt.Errorf("compile %q: %w", r.Pattern, err)
		}
		rules = append(rules, compiled...)
	}
	data, err := json.MarshalIndent(slotConfig{DNS: slotDNS{Servers: servers, Rules: rules}}, "", "  ")
	if err != nil {
		s.slotDirty = true
		return err
	}
	if err := s.orch.Save(SlotName, data); err != nil {
		s.slotDirty = true
		return err
	}
	if err := s.orch.SetEnabled(SlotName, len(rules) > 0); err != nil {
		s.slotDirty = true
		return err
	}
	s.slotDirty = false
	return nil
}
