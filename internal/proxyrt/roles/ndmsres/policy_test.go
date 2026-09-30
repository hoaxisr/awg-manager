package ndmsres

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

func TestPolicyExitConvergesToUplink(t *testing.T) {
	rt := newFakeRouter()
	rt.ifaces["OpkgTun18"] = &IfaceFacts{SecurityLevel: "private", Address: "10.70.0.5", AdminUp: true}
	res := NewPolicyExit("policy_exit", rt, rt)
	res.SetDesired(PolicyExitDesired{Name: "OpkgTun18", SecurityLevel: "public",
		IPGlobal: true, PermitAllACL: true, DefaultCandidacy: true})

	drive(t, res)

	if rt.ifaces["OpkgTun18"].SecurityLevel != "public" {
		t.Fatal("security-level не приведён к public")
	}
	if !rt.global["OpkgTun18"] || !rt.acl["OpkgTun18"] || !rt.defrt["OpkgTun18"] {
		t.Fatalf("uplink не собран: global=%v acl=%v defrt=%v",
			rt.global["OpkgTun18"], rt.acl["OpkgTun18"], rt.defrt["OpkgTun18"])
	}
}

func TestPolicyExitAbsentIfaceMakesNoSteps(t *testing.T) {
	rt := newFakeRouter()
	res := NewPolicyExit("policy_exit", rt, rt)
	res.SetDesired(PolicyExitDesired{Name: "OpkgTun18", SecurityLevel: "public", IPGlobal: true})
	obs, _ := res.Observe(context.Background())
	if steps := res.Plan(obs); len(steps) != 0 {
		t.Fatalf("uplink на несуществующем интерфейсе: %v", steps)
	}
}

func TestPolicyExitWithoutCandidacySkipsRoute(t *testing.T) {
	// Сервер с ExposeToPolicies: public + ip global + ACL, но БЕЗ кандидатуры
	// (routable_exit сервера убран решением владельца 2026-08-17).
	rt := newFakeRouter()
	rt.ifaces["OpkgTun17"] = &IfaceFacts{SecurityLevel: "private", AdminUp: true}
	res := NewPolicyExit("policy_exit", rt, rt)
	res.SetDesired(PolicyExitDesired{Name: "OpkgTun17", SecurityLevel: "public",
		IPGlobal: true, PermitAllACL: true, DefaultCandidacy: false})
	drive(t, res)
	if rt.defrt["OpkgTun17"] {
		t.Fatal("кандидатура default route не запрошена — ставить нельзя")
	}
}

func policies(ps ...ndms.Policy) *fakePolicies { return &fakePolicies{list: ps} }

type fakePolicies struct {
	list    []ndms.Policy
	permits []string // "policy/iface/order"
	listErr error
}

func (f *fakePolicies) List(context.Context) ([]ndms.Policy, error) {
	return f.list, f.listErr
}

func (f *fakePolicies) PermitInterface(_ context.Context, name, iface string, order int) error {
	f.permits = append(f.permits, name+"/"+iface+"/"+strconv.Itoa(order))
	return nil
}

func pol(name string, ifaces ...ndms.PermittedIface) ndms.Policy {
	return ndms.Policy{Name: name, Interfaces: ifaces}
}

func pi(name string, denied bool) ndms.PermittedIface {
	return ndms.PermittedIface{Name: name, Denied: denied}
}

func TestMembershipPermitsAppendOrder(t *testing.T) {
	// permit добавляется В ХВОСТ разрешённых: order=0 вставил бы нас перед
	// ISP и сломал приоритет (client_policy.go:31-44).
	fp := policies(pol("Policy0", pi("ISP", false), pi("Wireguard0", false)))
	m := NewMembership("policy_membership", fp, fp)
	m.SetDesired("OpkgTun18", []PolicyRef{{Name: "Policy0"}})

	obs, err := m.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	steps := m.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "permit" {
		t.Fatalf("ожидали permit, получили %v", steps)
	}
	if err := m.Apply(context.Background(), steps[0]); err != nil {
		t.Fatal(err)
	}
	if len(fp.permits) != 1 || fp.permits[0] != "Policy0/OpkgTun18/2" {
		t.Fatalf("permit не с append-order: %v", fp.permits)
	}
}

func TestMembershipSettledWhenPermitted(t *testing.T) {
	fp := policies(pol("Policy0", pi("ISP", false), pi("OpkgTun18", false)))
	m := NewMembership("policy_membership", fp, fp)
	m.SetDesired("OpkgTun18", []PolicyRef{{Name: "Policy0"}})
	obs, _ := m.Observe(context.Background())
	if steps := m.Plan(obs); len(steps) != 0 {
		t.Fatalf("уже разрешены — шагов нет: %v", steps)
	}
}

func TestMembershipMissingPolicyIsVerdict(t *testing.T) {
	fp := policies()
	m := NewMembership("policy_membership", fp, fp)
	m.SetDesired("OpkgTun18", []PolicyRef{{Name: "Нет такой"}})
	obs, _ := m.Observe(context.Background())
	steps := m.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "fail" {
		t.Fatalf("несуществующая политика — приговор: %v", steps)
	}
	if err := m.Apply(context.Background(), steps[0]); err == nil ||
		!strings.Contains(err.Error(), "Нет такой") {
		t.Fatalf("причина обязана называть политику: %v", err)
	}
}

func TestMembershipForeignPermitUntouched(t *testing.T) {
	// Интерфейс разрешён в политике, которой нет в намерении, — показываем,
	// НЕ снимаем (спека §4.4): принятие дрейфа — явное действие пользователя.
	fp := policies(
		pol("Наша", pi("OpkgTun18", false)),
		pol("Чужая", pi("OpkgTun18", false)),
	)
	m := NewMembership("policy_membership", fp, fp)
	m.SetDesired("OpkgTun18", []PolicyRef{{Name: "Наша"}})
	obs, _ := m.Observe(context.Background())
	if steps := m.Plan(obs); len(steps) != 0 {
		t.Fatalf("чужое членство трогать нельзя: %v", steps)
	}
	if obs.Attrs["extra"] != "Чужая" {
		t.Fatalf("чужое членство обязано быть видимым: %+v", obs.Attrs)
	}
}

func TestMembershipListErrorIsUnknown(t *testing.T) {
	fp := policies()
	fp.listErr = errors.New("rci недоступен")
	m := NewMembership("policy_membership", fp, fp)
	m.SetDesired("OpkgTun18", []PolicyRef{{Name: "Policy0"}})
	if _, err := m.Observe(context.Background()); err == nil {
		t.Fatal("ошибка списка политик обязана давать unknown, а не «нет членства»")
	}
}

func TestMembershipRecheckIsZero(t *testing.T) {
	// Осознанная потеря §4.6: хука на правки политик в веб-морде нет,
	// периодического опроса не заводим.
	fp := policies()
	m := NewMembership("policy_membership", fp, fp)
	if m.RecheckAfter() != 0 {
		t.Fatal("policy_membership сверяется только по событиям")
	}
}

func TestMembershipRestoresStoredOrder(t *testing.T) {
	// Позиция permit'а — приоритет кандидатуры default route. Пользователь
	// поднял выход выше провайдера; после апгрейда permit обязан вернуться на
	// сохранённое место, а не уехать в хвост.
	fp := policies(pol("Policy0", pi("ISP", false), pi("Wireguard0", false)))
	m := NewMembership("policy_membership", fp, fp)
	m.SetDesired("OpkgTun18", []PolicyRef{{Name: "Policy0", Order: orderPtr(1)}})

	obs, err := m.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	steps := m.Plan(obs)
	if len(steps) != 1 || steps[0].Args["order"] != "1" {
		t.Fatalf("шаг обязан нести сохранённый order=1: %v", steps)
	}
	if err := m.Apply(context.Background(), steps[0]); err != nil {
		t.Fatal(err)
	}
	if len(fp.permits) != 1 || fp.permits[0] != "Policy0/OpkgTun18/1" {
		t.Fatalf("permit не на сохранённой позиции: %v", fp.permits)
	}
}

func orderPtr(v int) *int { return &v }

func TestMembershipRestoresTopPosition(t *testing.T) {
	// Order 0 — САМЫЙ ВЕРХ политики, а не «позиция не закреплена»: NDMS
	// нумерует permit'ы с нуля (ndms/query/policies.go:86), и именно нулём
	// хранится выход, поднятый пользователем ВЫШЕ провайдера. Трактовка нуля
	// как «не задано» уводила бы такой permit в хвост молча.
	fp := policies(pol("Policy0", pi("ISP", false), pi("Wireguard0", false)))
	m := NewMembership("policy_membership", fp, fp)
	m.SetDesired("OpkgTun18", []PolicyRef{{Name: "Policy0", Order: orderPtr(0)}})

	obs, err := m.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	steps := m.Plan(obs)
	if len(steps) != 1 || steps[0].Args["order"] != "0" {
		t.Fatalf("нулевая позиция обязана доехать до шага, а не превратиться в хвост: %v", steps)
	}
	if err := m.Apply(context.Background(), steps[0]); err != nil {
		t.Fatal(err)
	}
	if len(fp.permits) != 1 || fp.permits[0] != "Policy0/OpkgTun18/0" {
		t.Fatalf("permit не на верхней позиции: %v", fp.permits)
	}
}
