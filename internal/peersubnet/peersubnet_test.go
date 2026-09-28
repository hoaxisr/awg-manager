package peersubnet

import (
	"encoding/binary"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
)

func cidr(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestValidateClientAllowedIPs(t *testing.T) {
	got, err := ValidateClientAllowedIPs(" 10.0.0.5/24 ,fd00::1/64,192.168.1.0/24 ")
	if err != nil || got != "10.0.0.0/24, fd00::/64, 192.168.1.0/24" {
		t.Fatalf("got %q err %v", got, err)
	}
	if got, err := ValidateClientAllowedIPs("  "); err != nil || got != "" {
		t.Fatalf("empty: %q %v", got, err)
	}
	for _, bad := range []string{"10.0.0.1", "10.0.0.0/24, host", "10.0.0.0/33", "PostUp = rm -rf /"} {
		if _, err := ValidateClientAllowedIPs(bad); !errors.Is(err, ErrInvalidClientAllowedIPs) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
	// Пересечения внутри списка — не ошибка: WireGuard берёт самый длинный префикс.
	if _, err := ValidateClientAllowedIPs("10.0.0.0/8, 10.1.0.0/16"); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRemoteSubnets(t *testing.T) {
	occupied := []Occupied{
		{Net: cidr(t, "192.168.1.0/24"), Label: "LAN Home"},
		{Net: cidr(t, "10.9.0.0/24"), Label: "подсеть сервера Wireguard0"},
	}
	cases := []struct {
		name    string
		in      []string
		wantErr error
		want    []string
		label   string
	}{
		{"ok+canon", []string{"192.168.77.5/24", " 172.16.0.0/12 "}, nil, []string{"192.168.77.0/24", "172.16.0.0/12"}, ""},
		{"empty skipped", []string{"", "192.168.77.0/24"}, nil, []string{"192.168.77.0/24"}, ""},
		{"no prefix", []string{"192.168.77.0"}, ErrInvalidRemoteSubnets, nil, ""},
		{"ipv6", []string{"fd00::/64"}, ErrInvalidRemoteSubnets, nil, ""},
		{"default", []string{"0.0.0.0/0"}, ErrInvalidRemoteSubnets, nil, ""},
		{"inner overlap", []string{"10.20.0.0/16", "10.20.5.0/24"}, ErrInvalidRemoteSubnets, nil, ""},
		{"equal occupied", []string{"192.168.1.0/24"}, ErrRemoteSubnetOverlap, nil, "LAN Home"},
		{"inside occupied", []string{"192.168.1.128/25"}, ErrRemoteSubnetOverlap, nil, "LAN Home"},
		{"occupied inside ours", []string{"10.8.0.0/15"}, ErrRemoteSubnetOverlap, nil, "Wireguard0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ValidateRemoteSubnets(c.in, occupied)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) || (c.label != "" && !strings.Contains(err.Error(), c.label)) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v err %v", got, err)
			}
		})
	}
}

func TestExclude_SingleNetExactCover(t *testing.T) {
	got := Exclude(cidr(t, "0.0.0.0/0"), []*net.IPNet{cidr(t, "10.0.0.0/8")})
	want := []string{"0.0.0.0/5", "8.0.0.0/7", "11.0.0.0/8", "12.0.0.0/6", "16.0.0.0/4", "32.0.0.0/3", "64.0.0.0/2", "128.0.0.0/1"}
	if gs := strs(got); !reflect.DeepEqual(gs, want) {
		t.Fatalf("got %v", gs)
	}
	if got := Exclude(cidr(t, "0.0.0.0/0"), []*net.IPNet{cidr(t, "0.0.0.0/0")}); len(got) != 0 {
		t.Fatalf("вычитание всего: %v", strs(got))
	}
}

// Свойство: результат вместе с вычтенным покрывает базу ровно один раз.
// Проверяется перебором границ, не эталонным списком.
func TestExclude_ComplementProperty(t *testing.T) {
	minus := []*net.IPNet{cidr(t, "10.9.0.0/24"), cidr(t, "10.9.0.128/25"), cidr(t, "192.168.1.0/24"), cidr(t, "172.16.0.0/12")}
	got := Exclude(cidr(t, "0.0.0.0/0"), minus)
	for i := range got {
		for j := i + 1; j < len(got); j++ {
			if Overlaps(got[i], got[j]) {
				t.Fatalf("результат пересекается: %s %s", got[i], got[j])
			}
		}
	}
	var probes []net.IP
	for _, n := range append(append([]*net.IPNet{}, minus...), got...) {
		first := binary.BigEndian.Uint32(n.IP.To4())
		last := first | ^binary.BigEndian.Uint32(net.IP(n.Mask).To4())
		for _, v := range []uint32{first, last, first - 1, last + 1} {
			ip := make(net.IP, 4)
			binary.BigEndian.PutUint32(ip, v)
			probes = append(probes, ip)
		}
	}
	for _, ip := range probes {
		inMinus, hits := false, 0
		for _, m := range minus {
			inMinus = inMinus || m.Contains(ip)
		}
		for _, n := range got {
			if n.Contains(ip) {
				hits++
			}
		}
		if (inMinus && hits != 0) || (!inMinus && hits != 1) {
			t.Fatalf("%s: inMinus=%v hits=%d", ip, inMinus, hits)
		}
	}
}

func TestBuildPresets(t *testing.T) {
	p := BuildPresets(cidr(t, "10.9.0.0/24"),
		[]*net.IPNet{cidr(t, "192.168.1.0/24"), cidr(t, "192.168.1.0/24")},
		[]net.IP{net.ParseIP("192.168.1.1"), net.ParseIP("8.8.8.8"), net.ParseIP("fd00::1")})
	if p.RouterOnly != "10.9.0.0/24, 192.168.1.0/24" {
		t.Fatalf("RouterOnly = %q", p.RouterOnly)
	}
	if !strings.HasSuffix(p.ExceptRouter, ", ::/0") || !strings.Contains(p.ExceptRouter, "192.168.1.1/32") ||
		strings.Contains(p.ExceptRouter, "8.8.8.8/32") || strings.Contains(p.ExceptRouter, "10.9.0.0/24") {
		t.Fatalf("ExceptRouter = %q", p.ExceptRouter)
	}
	if p := BuildPresets(nil, nil, nil); p.RouterOnly != "" || p.ExceptRouter != "0.0.0.0/0, ::/0" {
		t.Fatalf("пусто: %+v", p)
	}
}

// Сети интерфейсов приходят адресом с маской (10.9.0.1/24): пресет обязан
// печатать сеть, а не адрес, и считать дополнение от сети.
func TestBuildPresets_UnmaskedInput(t *testing.T) {
	mask := net.CIDRMask(24, 32)
	p := BuildPresets(&net.IPNet{IP: net.ParseIP("10.9.0.1").To4(), Mask: mask},
		[]*net.IPNet{{IP: net.ParseIP("192.168.1.1").To4(), Mask: mask}}, nil)
	if p.RouterOnly != "10.9.0.0/24, 192.168.1.0/24" {
		t.Fatalf("RouterOnly = %q", p.RouterOnly)
	}
	want := BuildPresets(cidr(t, "10.9.0.0/24"), []*net.IPNet{cidr(t, "192.168.1.0/24")}, nil)
	if p.ExceptRouter != want.ExceptRouter {
		t.Fatalf("ExceptRouter = %q, want %q", p.ExceptRouter, want.ExceptRouter)
	}
}

func TestExclude_UnmaskedBase(t *testing.T) {
	got := Exclude(&net.IPNet{IP: net.ParseIP("10.9.0.1").To4(), Mask: net.CIDRMask(24, 32)}, nil)
	if len(got) != 1 || got[0].String() != "10.9.0.0/24" {
		t.Fatalf("got %v", got)
	}
}

func TestBuildPresets_DuplicateDNS(t *testing.T) {
	p := BuildPresets(cidr(t, "10.9.0.0/24"), nil,
		[]net.IP{net.ParseIP("10.9.0.1"), net.ParseIP("10.9.0.1")})
	if n := strings.Count(p.ExceptRouter, "10.9.0.1/32"); n != 1 {
		t.Fatalf("10.9.0.1/32 встречается %d раз: %q", n, p.ExceptRouter)
	}
}

func TestRouteComment(t *testing.T) {
	if got := RouteComment("5+0I/P0Vaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa="); got != "awgm-peer:5+0I/P0V" {
		t.Fatalf("got %q", got)
	}
	if got := RouteComment("pub-1"); got != "awgm-peer:pub-1" {
		t.Fatalf("short key: %q", got)
	}
}

func strs(nets []*net.IPNet) []string {
	out := make([]string, len(nets))
	for i, n := range nets {
		out[i] = n.String()
	}
	return out
}
