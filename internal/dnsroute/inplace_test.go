package dnsroute

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
)

func TestPlanInPlace(t *testing.T) {
	line := func(iface string, reject bool) ndms.DNSRouteRule {
		idx := "idx-" + iface
		if reject {
			idx += "-r"
		}
		return ndms.DNSRouteRule{Group: "g", Interface: iface, Auto: true, Reject: reject, Index: idx}
	}
	tgt := func(iface string, reject bool) rciRouteOp {
		return rciRouteOp{Group: "g", Iface: iface, Auto: true, Reject: reject}
	}
	cases := []struct {
		name    string
		cur     []ndms.DNSRouteRule
		tgts    []rciRouteOp
		deletes []string
		updates []inPlaceUpdate
		adds    []rciRouteOp
	}{
		{
			name:    "смена интерфейса (#1023)",
			cur:     []ndms.DNSRouteRule{line("A", false)},
			tgts:    []rciRouteOp{tgt("B", false)},
			updates: []inPlaceUpdate{{index: "idx-A", op: tgt("B", false)}},
		},
		{
			name: "перестановка без правок",
			cur:  []ndms.DNSRouteRule{line("A", false), line("B", false)},
			tgts: []rciRouteOp{tgt("B", false), tgt("A", false)},
		},
		{
			name:    "снос лишней строки",
			cur:     []ndms.DNSRouteRule{line("A", false), line("B", true)},
			tgts:    []rciRouteOp{tgt("B", true)},
			deletes: []string{"idx-A"},
		},
		{
			name: "добавление новых",
			cur:  []ndms.DNSRouteRule{line("A", false)},
			tgts: []rciRouteOp{tgt("A", false), tgt("B", false), tgt("C", true)},
			adds: []rciRouteOp{tgt("B", false), tgt("C", true)},
		},
		{
			name:    "смена reject у оставшейся",
			cur:     []ndms.DNSRouteRule{line("A", false), line("B", true)},
			tgts:    []rciRouteOp{tgt("A", true)},
			deletes: []string{"idx-B-r"},
			updates: []inPlaceUpdate{{index: "idx-A", op: tgt("A", true)}},
		},
		{
			name:    "повторное вхождение освобождается",
			cur:     []ndms.DNSRouteRule{line("A", false), {Group: "g", Interface: "A", Auto: true, Index: "idx-A2"}},
			tgts:    []rciRouteOp{tgt("A", false), tgt("B", false)},
			updates: []inPlaceUpdate{{index: "idx-A2", op: tgt("B", false)}},
		},
		{
			name: "правка интерфейса раньше смены reject",
			cur:  []ndms.DNSRouteRule{line("A", false), line("A", true)},
			tgts: []rciRouteOp{tgt("A", true), tgt("B", false)},
			updates: []inPlaceUpdate{
				{index: "idx-A-r", op: tgt("B", false)},
				{index: "idx-A", op: tgt("A", true)},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deletes, updates, adds := planInPlace(tc.cur, tc.tgts)
			if !reflect.DeepEqual(deletes, tc.deletes) {
				t.Errorf("deletes = %v, want %v", deletes, tc.deletes)
			}
			if !reflect.DeepEqual(updates, tc.updates) {
				t.Errorf("updates = %+v, want %+v", updates, tc.updates)
			}
			if !reflect.DeepEqual(adds, tc.adds) {
				t.Errorf("adds = %+v, want %+v", adds, tc.adds)
			}
			// Правка на чужой интерфейс, уже стоящий в группе, дала бы дубль.
			curIface := map[string]string{}
			present := map[string]bool{}
			for _, r := range tc.cur {
				if _, ok := curIface[r.Index]; !ok {
					curIface[r.Index] = r.Interface
				}
				present[r.Interface] = true
			}
			for _, u := range updates {
				if u.op.Iface != curIface[u.index] && present[u.op.Iface] {
					t.Errorf("правка %s ставит интерфейс %s, уже стоящий в группе", u.index, u.op.Iface)
				}
			}
		})
	}
}

func TestPlanMoves(t *testing.T) {
	r := func(group, iface string, disabled bool) ndms.DNSRouteRule {
		return ndms.DNSRouteRule{Group: group, Interface: iface, Auto: true, Index: group + "/" + iface, Disabled: disabled}
	}
	tgt := func(iface string, disabled bool) rciRouteOp {
		return rciRouteOp{Group: "g", Iface: iface, Auto: true, Disabled: disabled}
	}

	t.Run("перестановка внутри группы", func(t *testing.T) {
		ordered := []ndms.DNSRouteRule{r("x", "F", false), r("g", "A", false), r("y", "F", false), r("g", "B", false)}
		moves, disables, err := planMoves(ordered, "g", []rciRouteOp{tgt("B", false), tgt("A", false)})
		if err != nil {
			t.Fatal(err)
		}
		want := []command.DNSRouteMove{{Index: "g/B", Position: 1}}
		if !reflect.DeepEqual(moves, want) {
			t.Errorf("moves = %+v, want %+v", moves, want)
		}
		if len(disables) != 0 {
			t.Errorf("disables = %+v, want none", disables)
		}
	})

	t.Run("уже на месте", func(t *testing.T) {
		ordered := []ndms.DNSRouteRule{r("x", "F", false), r("g", "B", false)}
		moves, disables, err := planMoves(ordered, "g", []rciRouteOp{tgt("B", false)})
		if err != nil {
			t.Fatal(err)
		}
		if len(moves) != 0 || len(disables) != 0 {
			t.Errorf("moves = %+v, disables = %+v, want none", moves, disables)
		}
	})

	t.Run("цели нет", func(t *testing.T) {
		ordered := []ndms.DNSRouteRule{r("g", "A", false)}
		if _, _, err := planMoves(ordered, "g", []rciRouteOp{tgt("B", false)}); err == nil {
			t.Error("want error for missing target")
		}
	})

	t.Run("строк больше, чем целей", func(t *testing.T) {
		ordered := []ndms.DNSRouteRule{r("g", "A", false), r("g", "B", false)}
		if _, _, err := planMoves(ordered, "g", []rciRouteOp{tgt("A", false)}); err == nil {
			t.Error("want error for extra group lines")
		}
	})

	t.Run("пауза", func(t *testing.T) {
		ordered := []ndms.DNSRouteRule{r("g", "A", false)}
		moves, disables, err := planMoves(ordered, "g", []rciRouteOp{tgt("A", true)})
		if err != nil {
			t.Fatal(err)
		}
		if len(moves) != 0 {
			t.Errorf("moves = %+v, want none", moves)
		}
		want := []rciRouteDisable{{Index: "g/A", Disabled: true, Group: "g", Iface: "A"}}
		if !reflect.DeepEqual(disables, want) {
			t.Errorf("disables = %+v, want %+v", disables, want)
		}
	})
}

func TestPlanPackMoves(t *testing.T) {
	rr := func(spec ...string) []ndms.DNSRouteRule {
		var out []ndms.DNSRouteRule
		for i, g := range spec {
			out = append(out, ndms.DNSRouteRule{Group: g, Interface: "Wireguard1", Index: fmt.Sprintf("i%d", i)})
		}
		return out
	}
	cases := []struct {
		name    string
		ordered []ndms.DNSRouteRule
		group   string
		want    string
	}{
		{"за младшим пакетом", rr("a_p1", "b_p1", "a_p2"), "a_p2", "[{i2 1}]"},
		{"перед старшим пакетом", rr("a_p2", "b_p1", "a_p1"), "a_p1", "[{i2 0}]"},
		{"уже на месте", rr("a_p1", "a_p2", "b_p1"), "a_p2", "[]"},
		{"нет братьев — хвост", rr("a_p1", "b_p1"), "b_p1", "[]"},
		{"строка выше якоря — не трогаем", rr("a_p2", "a_p1", "b_p1"), "a_p2", "[]"},
		{"не пакет", rr("a_p1", "manual"), "manual", "[]"},
	}
	for _, c := range cases {
		got := fmt.Sprint(planPackMoves(c.ordered, c.group))
		if got != c.want {
			t.Errorf("%s: %s, ожидалось %s", c.name, got, c.want)
		}
	}
}
