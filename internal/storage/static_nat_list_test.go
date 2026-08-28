package storage

import (
	"reflect"
	"testing"
)

func TestStaticNATList(t *testing.T) {
	cases := []struct {
		name string
		wans []string
		wan  string
		want []string
	}{
		{"новый список", []string{"PPPoE0", "Wireguard2"}, "", []string{"PPPoE0", "Wireguard2"}},
		{"legacy одиночка", nil, "PPPoE0", []string{"PPPoE0"}},
		{"список приоритетнее legacy", []string{"Wireguard2"}, "PPPoE0", []string{"Wireguard2"}},
		{"пусто", nil, "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ms := ManagedServer{NATStaticWANs: c.wans, NATStaticWAN: c.wan}
			if got := ms.StaticNATList(); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("ManagedServer: got %v, want %v", got, c.want)
			}
			meta := ServerInterfaceMeta{NATStaticWANs: c.wans, NATStaticWAN: c.wan}
			if got := meta.StaticNATList(); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("ServerInterfaceMeta: got %v, want %v", got, c.want)
			}
		})
	}
}
