package dnsroute

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/hydraroute"
)

// Policy naming constraints for HR Neo.
//
// hrPolicyNameRE: latin letters only, no digits / underscores / hyphens /
// spaces / non-ASCII. Keeps names valid as ipset set names on the router.
//
// systemPolicyNameRE: policies created by Keenetic itself use Policy<N>
// naming (Policy0, Policy1, ...). HR Neo can't attach routes to these —
// reject at the API boundary so broken rules never reach disk.
var (
	hrPolicyNameRE     = regexp.MustCompile(`^[a-zA-Z]+$`)
	systemPolicyNameRE = regexp.MustCompile(`^Policy\d+$`)
)

const hrPolicyNameMaxLen = 32

// validateHRPolicyName enforces naming rules for HR Neo policy targets.
func validateHRPolicyName(name string) error {
	if name == "" {
		return fmt.Errorf("policy name is empty")
	}
	if len(name) > hrPolicyNameMaxLen {
		return fmt.Errorf("policy name too long: %d chars (max %d)", len(name), hrPolicyNameMaxLen)
	}
	if systemPolicyNameRE.MatchString(name) {
		return fmt.Errorf("policy name %q is reserved for system policies (HR Neo cannot attach to those)", name)
	}
	if !hrPolicyNameRE.MatchString(name) {
		return fmt.Errorf("policy name %q must contain only latin letters (a-z, A-Z)", name)
	}
	return nil
}

// policyOrchestrator handles the two-step "wait for HR Neo to create the
// policy, then permit the interfaces" sequence used by the new-policy flow.
// Split out for testability.
type policyOrchestrator interface {
	WaitForPolicy(ctx context.Context, policyName string, timeout time.Duration) error
	EnsurePolicyInterfaces(ctx context.Context, policyName string, ndmsIfaces []string) error
}

// hrReady reports whether the HR backend is available for read/write ops.
func (s *ServiceImpl) hrReady() bool {
	return s.hydra != nil && s.hydra.GetStatus().Installed
}

// listHydraRoute returns HR groups read straight from the HR Neo config
// files, converted into the DomainList shape the API expects.
func (s *ServiceImpl) listHydraRoute(ctx context.Context) ([]DomainList, error) {
	if !s.hrReady() {
		return nil, nil
	}
	rs, err := s.hydra.ListRuleSet()
	if err != nil {
		return nil, fmt.Errorf("list HR rules: %w", err)
	}

	// Classify Target: is it a policy name or a kernel iface? Best-effort —
	// a transient NDMS error just means everything looks like an interface.
	policyNames, _ := s.hydra.ListPolicyNames(ctx)
	policySet := make(map[string]bool, len(policyNames))
	for _, n := range policyNames {
		policySet[n] = true
	}

	icons := map[string]string{}
	// Сводится копия map, а не кэш: при отказе записи правка в кэше не
	// остаётся (F695). Черновик всего store — только когда есть что писать:
	// List вызывается часто. Ответ строится по сведённым иконкам и при
	// отказе — как прежде.
	if cached := s.store.GetCached(); cached != nil {
		if c := maps.Clone(cached.HRRuleIcons); c != nil {
			icons = c
		}
		if reconcileHRIcons(icons, rs) {
			data := s.store.Draft()
			data.HRRuleIcons = icons
			if err := s.store.Save(data); err != nil {
				s.appLog.Warn("hydraroute-icons", "", "save reconciled icons: "+err.Error())
			}
		}
	}

	systemByIface := s.systemTunnelsByIface(ctx)
	var result []DomainList
	for _, t := range rs.Targets {
		for i, g := range t.Groups {
			ref := hydraroute.GroupRef{Revision: rs.Revision, Target: t.Name, Index: i}
			dl := hrGroupToDomainList(ref, g, policySet, systemByIface)
			if g.Description != "" {
				dl.IconURL = strings.TrimSpace(icons[hrIconKey(t.Name, g.Description)])
			}
			result = append(result, dl)
		}
	}
	return result, nil
}

// hrIDPrefix tags HR group identifiers in the API layer. The rest of the id
// is hydraroute.GroupRef: revision, target and position — HR files have no
// place for an id of their own (HRweb would read it as the group name).
const hrIDPrefix = "hr:"

func hrID(ref hydraroute.GroupRef) string { return hrIDPrefix + ref.String() }

func refFromHRID(id string) (hydraroute.GroupRef, error) {
	return hydraroute.ParseGroupRef(strings.TrimPrefix(id, hrIDPrefix))
}

// isHRID reports whether the ID refers to an HR group.
func isHRID(id string) bool { return strings.HasPrefix(id, hrIDPrefix) }

// hrIconKey — ключ иконки группы: цель и описание. У безымянной группы
// иконки нет: позиционный ключ уезжал бы на соседнюю группу.
func hrIconKey(target, description string) string { return target + "\x00" + description }

// reconcileHRIcons приводит ключи иконок к группам файлов: старый ключ
// «имя» (до F629) переносится на каждую цель, где есть группа с таким
// описанием; ключ без группы удаляется — в том числе все, если HR Neo
// установлен, а файлов правил нет. Возвращает true, если map изменилась.
func reconcileHRIcons(icons map[string]string, rs hydraroute.RuleSet) bool {
	live := map[string]bool{}
	byDesc := map[string][]string{}
	for _, t := range rs.Targets {
		for _, g := range t.Groups {
			if g.Description == "" {
				continue
			}
			live[hrIconKey(t.Name, g.Description)] = true
			byDesc[g.Description] = append(byDesc[g.Description], t.Name)
		}
	}
	changed := false
	moved := map[string]string{}
	for k, url := range icons {
		if strings.Contains(k, "\x00") {
			if !live[k] {
				delete(icons, k)
				changed = true
			}
			continue
		}
		delete(icons, k)
		changed = true
		for _, target := range byDesc[k] {
			moved[hrIconKey(target, k)] = url
		}
	}
	for k, url := range moved {
		if _, ok := icons[k]; !ok {
			icons[k] = url
		}
	}
	return changed
}

// hrGroupToDomainList converts an HR group into the DomainList contract
// shared with NDMS. Subscriptions/dedup don't exist at this layer. Enabled
// reflects whether the group's lines are commented with '#'.
//
// systemByIface turns the target of a system interface back into its
// "system:<NDMS id>" tunnel ID (F498); other targets (managed tunnels, WAN)
// keep the kernel name as TunnelID, as before.
func hrGroupToDomainList(ref hydraroute.GroupRef, g hydraroute.Group, policySet map[string]bool, systemByIface map[string]string) DomainList {
	domains := append([]string(nil), g.Domains...)
	domains = append(domains, g.Subnets...)
	dl := DomainList{
		ID:            hrID(ref),
		Name:          g.Description,
		Domains:       g.Domains,
		Subnets:       g.Subnets,
		ManualDomains: domains,
		Backend:       "hydraroute",
		Enabled:       g.Enabled(),
	}
	if policySet[ref.Target] && validateHRPolicyName(ref.Target) == nil {
		dl.HRRouteMode = "policy"
		dl.HRPolicyName = ref.Target
		return dl
	}
	dl.HRRouteMode = "interface"
	tunnelID := ref.Target
	if id, ok := systemByIface[ref.Target]; ok {
		tunnelID = id
	}
	dl.Routes = []RouteTarget{{Interface: ref.Target, TunnelID: tunnelID}}
	return dl
}

// systemTunnelsByIface — best-effort, like currentPolicySet: without the map
// system targets read back as bare interface names.
func (s *ServiceImpl) systemTunnelsByIface(ctx context.Context) map[string]string {
	if s.resolver == nil {
		return nil
	}
	return s.resolver.SystemTunnelsByIface(ctx)
}

func hrGroupFromList(list DomainList) hydraroute.Group {
	domains, subnets := splitDomainsAndSubnets(deduplicateDomains(list.ManualDomains))
	return hydraroute.Group{Description: strings.TrimSpace(list.Name), Domains: domains, Subnets: subnets}
}

// setHRIcon пишет иконку группы с описанием; устаревшие ключи убирает
// reconcileHRIcons при следующем List.
func (s *ServiceImpl) setHRIcon(target, description, iconURL string) error {
	data := s.store.Draft()
	if data == nil || description == "" {
		return nil
	}
	if data.HRRuleIcons == nil {
		data.HRRuleIcons = map[string]string{}
	}
	key := hrIconKey(target, description)
	if iconURL == "" {
		delete(data.HRRuleIcons, key)
	} else {
		data.HRRuleIcons[key] = iconURL
	}
	if err := s.store.Save(data); err != nil {
		return fmt.Errorf("save HR group icon: %w", err)
	}
	return nil
}

// createHydraRoute validates the input, resolves the target tunnel, and
// appends a new group straight into the HR Neo config files. The name is
// optional: HRweb groups may have no description.
func (s *ServiceImpl) createHydraRoute(ctx context.Context, list DomainList) (*DomainList, error) {
	if !s.hrReady() {
		return nil, fmt.Errorf("HydraRoute Neo is not installed")
	}
	if len(list.ManualDomains) == 0 {
		return nil, fmt.Errorf("at least one domain or subnet is required")
	}

	target, err := s.resolveHRTarget(ctx, list)
	if err != nil {
		return nil, err
	}

	g := hrGroupFromList(list)
	ref, err := s.hydra.CreateGroup(target, g)
	if err != nil {
		return nil, err
	}

	// Orchestrate policy flow: wait for HR Neo to create the policy on the
	// router (new-policy only), then permit the user's interfaces in order.
	if err := s.applyPolicyInterfaces(ctx, list); err != nil {
		return nil, err
	}
	iconURL := strings.TrimSpace(list.IconURL)
	if err := s.setHRIcon(target, g.Description, iconURL); err != nil {
		return nil, err
	}
	s.appLog.Info("hydraroute-create", target, fmt.Sprintf("group %d created", ref.Index))

	dl := hrGroupToDomainList(ref, g, s.currentPolicySet(ctx), s.systemTunnelsByIface(ctx))
	if g.Description != "" {
		dl.IconURL = iconURL
	}
	dl.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	dl.UpdatedAt = dl.CreatedAt
	return &dl, nil
}

// updateHydraRoute replaces the HR group addressed by id ("hr:<rev>:<target>:<n>").
// A stale revision fails with hydraroute.ErrRulesChanged.
func (s *ServiceImpl) updateHydraRoute(ctx context.Context, id string, list DomainList) (*DomainList, error) {
	if !s.hrReady() {
		return nil, fmt.Errorf("HydraRoute Neo is not installed")
	}
	ref, err := refFromHRID(id)
	if err != nil {
		return nil, err
	}
	var target string
	if keepsHRTarget(list, ref.Target) {
		// Цель группы вернулась в том виде, в каком её отдал List:
		// hrGroupToDomainList кладёт имя ядра несистемного выхода и в
		// TunnelID, и в Interface, а каталог знает туннели по id, не по имени
		// ядра. Принимается ТОЛЬКО прежняя цель этой же группы — произвольную
		// строку так не записать (F632).
		target = ref.Target
	} else if target, err = s.resolveHRTarget(ctx, list); err != nil {
		return nil, err
	}
	g := hrGroupFromList(list)
	newRef, err := s.hydra.UpdateGroup(ref, target, g)
	if err != nil {
		return nil, err
	}
	if err := s.applyPolicyInterfaces(ctx, list); err != nil {
		return nil, err
	}
	iconURL := strings.TrimSpace(list.IconURL)
	if err := s.setHRIcon(target, g.Description, iconURL); err != nil {
		return nil, err
	}
	s.appLog.Info("hydraroute-update", target, "was "+ref.String())

	// UpdateGroup сохраняет флаги включения; показать группу как записана
	// (если файлы с тех пор не переписали — иначе по newRef уже не она).
	if rs, err := s.hydra.ListRuleSet(); err == nil && rs.Revision == newRef.Revision {
		if t := rs.Target(newRef.Target); t != nil && newRef.Index < len(t.Groups) {
			g = t.Groups[newRef.Index]
		}
	}
	dl := hrGroupToDomainList(newRef, g, s.currentPolicySet(ctx), s.systemTunnelsByIface(ctx))
	if g.Description != "" {
		dl.IconURL = iconURL
	}
	dl.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return &dl, nil
}

// keepsHRTarget — интерфейсная правка, вернувшая цель группы без перевода.
func keepsHRTarget(list DomainList, target string) bool {
	if list.HRRouteMode == "policy" || len(list.Routes) == 0 {
		return false
	}
	r := list.Routes[0]
	return r.Interface == target && r.TunnelID == target
}

// resolveHRTarget turns the user-facing DomainList fields into the opaque
// Target string that lives in the HR config file. Policy mode passes the
// policy name through; interface mode resolves the tunnel ID to a kernel
// interface name and refuses to write if the tunnel doesn't exist.
func (s *ServiceImpl) resolveHRTarget(ctx context.Context, list DomainList) (string, error) {
	if list.HRRouteMode == "policy" {
		name := strings.TrimSpace(list.HRPolicyName)
		if err := validateHRPolicyName(name); err != nil {
			return "", err
		}
		return name, nil
	}
	if len(list.Routes) == 0 {
		return "", fmt.Errorf("interface mode requires a tunnel")
	}
	tunnelID := list.Routes[0].TunnelID
	iface, err := s.resolver.GetKernelIfaceName(ctx, tunnelID)
	if err != nil {
		return "", fmt.Errorf("tunnel %q is not available: %w", tunnelID, err)
	}
	return iface, nil
}

// currentPolicySet is a best-effort snapshot of policy names used only to
// classify Target during DomainList conversion.
func (s *ServiceImpl) currentPolicySet(ctx context.Context) map[string]bool {
	names, _ := s.hydra.ListPolicyNames(ctx)
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// policyWaitTimeout is how long we wait for HR Neo to actually create a new
// policy on the router after writing the rule and restarting the daemon.
// Covers the 2s scheduleRestart debounce + restart + daemon's RCI round-trip
// with a comfortable margin.
const policyWaitTimeout = 15 * time.Second

// applyPolicyInterfaces permits the requested interfaces in the target policy,
// in the order given. Used by the HR policy-mode create/update paths.
//
//   - Existing-policy flow (HRPolicyInterfaces empty): legacy fallback
//     resolves Routes[0].TunnelID to an NDMS name and permits that single
//     interface.
//   - New-policy flow (HRPolicyInterfaces non-empty): the slice already
//     contains NDMS interface names. Wait for HR Neo to create the policy
//     on the router, then permit the names in the given order.
//
// A failure here propagates up to fail the Create/Update so the frontend can
// surface it. The rule itself has already been written to HR files — HR Neo
// keeps them as SoT, so a failed permit leaves the user with a rule until
// they retry via Access Policies.
func (s *ServiceImpl) applyPolicyInterfaces(ctx context.Context, list DomainList) error {
	if list.HRRouteMode != "policy" || list.HRPolicyName == "" {
		return nil
	}
	if s.policyOrchestrator == nil {
		return nil
	}

	var ndmsNames []string
	newPolicyFlow := len(list.HRPolicyInterfaces) > 0
	if newPolicyFlow {
		// Frontend sends NDMS names directly in this flow.
		ndmsNames = append(ndmsNames, list.HRPolicyInterfaces...)
	} else if len(list.Routes) > 0 {
		// Legacy single-interface path: still resolve from a TunnelID.
		name, err := s.resolver.ResolveInterface(ctx, list.Routes[0].TunnelID)
		if err == nil && name != "" {
			ndmsNames = []string{name}
		}
	}
	if len(ndmsNames) == 0 {
		return nil
	}

	if newPolicyFlow {
		if err := s.policyOrchestrator.WaitForPolicy(ctx, list.HRPolicyName, policyWaitTimeout); err != nil {
			return fmt.Errorf("policy %q not created on router: %w", list.HRPolicyName, err)
		}
	}

	return s.policyOrchestrator.EnsurePolicyInterfaces(ctx, list.HRPolicyName, ndmsNames)
}
