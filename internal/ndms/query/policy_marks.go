// internal/ndms/query/policy_marks.go
package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// ErrPolicyMarkNotFound is returned by PolicyMarkStore.Get when the
// requested policy is absent from /show/ip/policy or its mark is empty.
var ErrPolicyMarkNotFound = errors.New("policy mark not found")

// PolicyMarkStore fetches NDMS-assigned fwmarks for access policies
// from the runtime endpoint /show/ip/policy. Distinct from PolicyStore
// (which reads /show/rc/ip/policy — the config view, no marks).
//
// JSON shape (verified on hardware, NDMS 4.x):
//
//	{
//	  "Policy0": {"description":"IoT_VPN","mark":"ffffaaa","table4":4096,...},
//	  "Policy1": {"description":"Only_Letai","mark":"ffffaab","table4":4098,...}
//	}
//
// Top-level map (no "policy" wrapper); mark is bare hex without "0x"
// prefix; we add the prefix when returning so iptables --mark accepts
// it directly.
//
// No caching: marks are read on demand because they're consumed
// rarely (router Enable + Reconcile mark-change check) and stale marks
// would silently route via the wrong tunnel.
type PolicyMarkStore struct {
	getter Getter
	log    Logger
}

func NewPolicyMarkStore(g Getter, log Logger) *PolicyMarkStore {
	// Оба продовых вызова (wiring_routing.go, cleanup.go) передают nil —
	// подменяем на no-op, как NewInterfaceStore, иначе первый же Warnf
	// про невалидную марку упал бы паникой.
	if log == nil {
		log = NopLogger()
	}
	return &PolicyMarkStore{getter: g, log: log}
}

type policyMarkWire struct {
	Mark   string `json:"mark"`
	Route4 struct {
		Route []struct {
			Destination string `json:"destination"`
			Interface   string `json:"interface"`
		} `json:"route"`
	} `json:"route4"`
}

// Get returns the hex-formatted mark (e.g. "0xffffaaa") for policyName.
// Returns ErrPolicyMarkNotFound if the policy is absent or its mark is empty.
func (s *PolicyMarkStore) Get(ctx context.Context, policyName string) (string, error) {
	body, err := s.getter.GetRaw(ctx, "/show/ip/policy")
	if err != nil {
		return "", fmt.Errorf("fetch policy marks: %w", err)
	}
	var doc map[string]policyMarkWire
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("decode policy marks: %w", err)
	}
	p, ok := doc[policyName]
	if !ok || p.Mark == "" {
		return "", ErrPolicyMarkNotFound
	}
	return "0x" + p.Mark, nil
}

// isBareHex — непустая строка из одних шестнадцатеричных цифр, как NDMS отдаёт
// марку (без префикса "0x" — его добавляем мы).
func isBareHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return s != ""
}

// PolicyDefaultExit — политика, чей дефолтный маршрут ведёт в заданный
// интерфейс, вместе с её NDMS-меткой (hex с префиксом "0x").
type PolicyDefaultExit struct {
	Name string
	Mark string
}

// ListByDefaultInterface возвращает политики, у которых в эффективной таблице
// есть маршрут 0.0.0.0/0 через iface.
//
// ТОЛЬКО дефолт: NDMS раскладывает connected-подсети всех интерфейсов по
// таблице КАЖДОЙ политики, поэтому отбор по «есть любой маршрут через iface»
// вернул бы все политики роутера (проверено на живом дампе 2026-08-09).
//
// Результат отсортирован по имени: порядок обхода map в Go случаен, а от него
// зависят и текст netfilter.d-хука, и сравнение желаемого состояния — без
// сортировки они дрейфовали бы на каждом тике.
func (s *PolicyMarkStore) ListByDefaultInterface(ctx context.Context, iface string) ([]PolicyDefaultExit, error) {
	body, err := s.getter.GetRaw(ctx, "/show/ip/policy")
	if err != nil {
		return nil, fmt.Errorf("fetch policies: %w", err)
	}
	var doc map[string]policyMarkWire
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("decode policies: %w", err)
	}
	var out []PolicyDefaultExit
	for name, p := range doc {
		if p.Mark == "" {
			continue
		}
		// Марка уезжает в текст netfilter.d-хука, который ndm исполняет от
		// root, поэтому всё, что не голый hex (пробелы, спецсимволы, чужой
		// префикс "0x"), отбрасываем на входе, а не вклеиваем в скрипт.
		if !isBareHex(p.Mark) {
			s.log.Warnf("policy %s: невалидная марка %q — политика пропущена", name, p.Mark)
			continue
		}
		for _, r := range p.Route4.Route {
			if r.Destination == "0.0.0.0/0" && r.Interface == iface {
				out = append(out, PolicyDefaultExit{Name: name, Mark: "0x" + p.Mark})
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
