package accesspolicy

import (
	"errors"
	"reflect"
	"testing"
)

type fakeHolds struct {
	m   map[string][]string
	err error
}

func (f fakeHolds) HeldInterfaces() (map[string][]string, error) { return f.m, f.err }

// Снятый мониторингом туннель помечается в своей политике; стоящий в политике
// (deny не прошёл, permit вернули руками) не дублируется (F752).
func TestMarkHeld(t *testing.T) {
	s := &ServiceImpl{holds: fakeHolds{m: map[string][]string{
		"HydraRoute": {"OpkgTun10", "OpkgTun11"},
		"Policy1":    {"OpkgTun10"},
	}}}
	policies := []Policy{
		{Name: "HydraRoute", Interfaces: []PermittedIface{{Name: "OpkgTun11"}, {Name: "PPPoE0"}}},
		{Name: "Policy1"},
		{Name: "Policy0", Interfaces: []PermittedIface{{Name: "PPPoE0"}}},
	}
	s.markHeld(policies)

	want := [][]string{{"OpkgTun10"}, {"OpkgTun10"}, nil}
	for i, p := range policies {
		if !reflect.DeepEqual(p.Held, want[i]) {
			t.Errorf("%s: held = %v, want %v", p.Name, p.Held, want[i])
		}
	}
}

// Отказ источника не роняет список политик — пометки просто нет.
func TestMarkHeld_SourceError(t *testing.T) {
	s := &ServiceImpl{holds: fakeHolds{err: errors.New("disk")}}
	policies := []Policy{{Name: "HydraRoute"}}
	s.markHeld(policies)
	if policies[0].Held != nil {
		t.Fatalf("held = %v", policies[0].Held)
	}
}
