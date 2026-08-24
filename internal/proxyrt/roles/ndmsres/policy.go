package ndmsres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
)

// PolicyExitDesired — «интерфейс виден роутеру как выход». Ставится ПОСЛЕ
// адреса и up (стендовое правило managed AWG: ip global не до адреса —
// ndms_iface.go:341-343); позицию в цепочке гарантирует декларация роли.
type PolicyExitDesired struct {
	Name          string
	SecurityLevel string // public
	IPGlobal      bool
	PermitAllACL  bool
	// DefaultCandidacy — запись «кандидат в default route». Только клиент;
	// у сервера routable_exit убран решением владельца. Допущение §13 —
	// стендовый гейт волны.
	DefaultCandidacy bool
}

// PolicyExit — ресурс policy_exit.
type PolicyExit struct {
	id   proxyrt.ResourceID
	cmds Commands
	q    Query
	d    PolicyExitDesired
}

func NewPolicyExit(id proxyrt.ResourceID, cmds Commands, q Query) *PolicyExit {
	return &PolicyExit{id: id, cmds: cmds, q: q}
}

func (r *PolicyExit) SetDesired(d PolicyExitDesired) { r.d = d }

func (r *PolicyExit) ID() proxyrt.ResourceID { return r.id }

func (r *PolicyExit) Observe(ctx context.Context) (proxyrt.Observation, error) {
	facts, ok, err := r.q.Iface(ctx, r.d.Name)
	if err != nil {
		return proxyrt.Observation{}, err
	}
	if !ok {
		return proxyrt.Observation{Known: true, Exists: false, Detail: "интерфейса нет"}, nil
	}
	global, err := r.q.HasIPGlobal(ctx, r.d.Name)
	if err != nil {
		return proxyrt.Observation{}, err
	}
	acl, err := r.q.HasPermitAllACL(ctx, r.d.Name)
	if err != nil {
		return proxyrt.Observation{}, err
	}
	defrt, err := r.q.HasDefaultRoute(ctx, r.d.Name)
	if err != nil {
		return proxyrt.Observation{}, err
	}
	return proxyrt.Observation{Known: true, Exists: true, Attrs: map[string]string{
		"security_level": facts.SecurityLevel,
		"ip_global":      strconv.FormatBool(global),
		"acl":            strconv.FormatBool(acl),
		"default_route":  strconv.FormatBool(defrt),
	}}, nil
}

func (r *PolicyExit) Plan(obs proxyrt.Observation) []proxyrt.Step {
	if !obs.Exists {
		return nil // create-on-reference: не адресуем несуществующий
	}
	var steps []proxyrt.Step
	add := func(op, reason string) {
		steps = append(steps, proxyrt.Step{Resource: r.id, Op: op,
			Args: map[string]string{"name": r.d.Name}, Reason: reason})
	}
	if r.d.SecurityLevel != "" && obs.Attrs["security_level"] != r.d.SecurityLevel {
		add("set-security-level", "уровень не "+r.d.SecurityLevel)
	}
	if r.d.IPGlobal && obs.Attrs["ip_global"] != "true" {
		// Обратной команды нет: снятый ip global убирается только
		// пересозданием интерфейса (ndms_iface.go:340-346) — поэтому
		// желаемое здесь только аддитивно.
		add("ip-global", "не uplink: без ip global не попадает в политики/HydraRoute")
	}
	if r.d.PermitAllACL && obs.Attrs["acl"] != "true" {
		add("acl", "permit-all ACL отсутствует")
	}
	if r.d.DefaultCandidacy && obs.Attrs["default_route"] != "true" {
		add("default-candidacy", "нет кандидатуры default route")
	}
	return steps
}

func (r *PolicyExit) Apply(ctx context.Context, s proxyrt.Step) error {
	switch s.Op {
	case "set-security-level":
		return r.cmds.SetSecurityLevel(ctx, r.d.Name, r.d.SecurityLevel)
	case "ip-global":
		return r.cmds.SetIPGlobal(ctx, r.d.Name)
	case "acl":
		return r.cmds.SetPermitAllACL(ctx, r.d.Name)
	case "default-candidacy":
		return r.cmds.EnsureDefaultRouteCandidacy(ctx, r.d.Name)
	default:
		return fmt.Errorf("неизвестный шаг %q", s.Op)
	}
}

func (r *PolicyExit) RecheckAfter() time.Duration { return 0 }

// PolicyLister — срез query.PolicyStore.
type PolicyLister interface {
	List(ctx context.Context) ([]ndms.Policy, error)
}

// Permitter — срез command.PolicyCommands.
type Permitter interface {
	PermitInterface(ctx context.Context, name, iface string, order int) error
}

// Membership — ресурс policy_membership: намерение членства с единственным
// писателем-пользователем. RecheckAfter = 0 — осознанная потеря §4.6 (хука на
// правки политик в веб-морде нет; периодический опрос не возвращаем).
type Membership struct {
	id       proxyrt.ResourceID
	list     PolicyLister
	permit   Permitter
	iface    string
	policies []PolicyRef
}

// PolicyRef — политика из намерения и позиция, на которую ставить наш permit
// ПРИ СОЗДАНИИ. Локальный тип: ndmsres не импортирует roles. Order 0 — в
// хвост (appendOrder); существующую позицию не двигаем (§4.4).
type PolicyRef struct {
	Name  string
	Order int
}

func NewMembership(id proxyrt.ResourceID, list PolicyLister, permit Permitter) *Membership {
	return &Membership{id: id, list: list, permit: permit}
}

func (m *Membership) SetDesired(iface string, policies []PolicyRef) {
	m.iface, m.policies = iface, policies
}

func (m *Membership) ID() proxyrt.ResourceID { return m.id }

func (m *Membership) Observe(ctx context.Context) (proxyrt.Observation, error) {
	all, err := m.list.List(ctx)
	if err != nil {
		return proxyrt.Observation{}, err
	}
	byName := make(map[string]ndms.Policy, len(all))
	for _, p := range all {
		byName[p.Name] = p
	}
	attrs := map[string]string{}
	for _, want := range m.policies {
		p, ok := byName[want.Name]
		if !ok {
			attrs["policy:"+want.Name] = "missing"
			continue
		}
		already, order := appendOrder(p, m.iface)
		if already {
			attrs["policy:"+want.Name] = "permitted"
		} else {
			attrs["policy:"+want.Name] = "order=" + strconv.Itoa(order)
		}
	}
	// Чужой дрейф показываем, но не трогаем (§4.4): политики, где мы
	// разрешены сверх намерения.
	var extra []string
	wanted := make(map[string]bool, len(m.policies))
	for _, w := range m.policies {
		wanted[w.Name] = true
	}
	for _, p := range all {
		if wanted[p.Name] {
			continue
		}
		if already, _ := appendOrder(p, m.iface); already {
			extra = append(extra, p.Name)
		}
	}
	if len(extra) > 0 {
		attrs["extra"] = strings.Join(extra, ",")
	}
	return proxyrt.Observation{Known: true, Exists: true, Attrs: attrs}, nil
}

// appendOrder — паритет с opkgPermitAppendOrder (client_policy.go:31-44):
// order = число уже разрешённых, permit уходит в хвост и не сдвигает ISP/WG.
func appendOrder(policy ndms.Policy, iface string) (already bool, order int) {
	for _, pi := range policy.Interfaces {
		if pi.Name == iface && !pi.Denied {
			return true, order
		}
		if !pi.Denied {
			order++
		}
	}
	return false, order
}

func (m *Membership) Plan(obs proxyrt.Observation) []proxyrt.Step {
	var steps []proxyrt.Step
	for _, want := range m.policies {
		switch v := obs.Attrs["policy:"+want.Name]; {
		case v == "permitted":
		case v == "missing":
			return []proxyrt.Step{{Resource: m.id, Op: "fail",
				Reason: fmt.Sprintf("политика %q не существует", want.Name)}}
		case strings.HasPrefix(v, "order="):
			// Позиция из намерения главнее хвоста: пользователь поднял выход
			// выше провайдера, и после апгрейда её надо восстановить (паритет
			// ensureOpkgPermittedAtOrder старого мира). Order 0 — в хвост,
			// как наблюдение и посчитало.
			order := strings.TrimPrefix(v, "order=")
			if want.Order > 0 {
				order = strconv.Itoa(want.Order)
			}
			steps = append(steps, proxyrt.Step{Resource: m.id, Op: "permit",
				Args:   map[string]string{"policy": want.Name, "order": order},
				Reason: "нашего permit нет в политике из намерения"})
		}
	}
	return steps
}

func (m *Membership) Apply(ctx context.Context, s proxyrt.Step) error {
	switch s.Op {
	case "fail":
		return errors.New(s.Reason)
	case "permit":
		order, err := strconv.Atoi(s.Args["order"])
		if err != nil {
			return err
		}
		return m.permit.PermitInterface(ctx, s.Args["policy"], m.iface, order)
	default:
		return fmt.Errorf("неизвестный шаг %q", s.Op)
	}
}

func (m *Membership) RecheckAfter() time.Duration { return 0 }
