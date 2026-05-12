package router

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestParseDefaultIfaces_RealSnapshot(t *testing.T) {
	// Captured from a Keenetic router with PPP WAN + 2 WG tunnels as
	// default-route exits. Synthetic IPs (RFC 5737 + RFC 1918).
	out := `default dev nwg0  table 4096  scope link  metric 1000
default dev ppp0  table 4098  scope link  metric 1000
default dev ppp0  table 16395  scope link  src 203.0.113.207  metric 1000
default dev nwg0  table 16396  scope link  src 10.8.1.3  metric 1000
default dev nwg3  table 16399  scope link  src 172.16.0.2  metric 1000
default dev ppp0  scope link  metric 1000
default dev nwg3  table 16399  metric 1000  pref medium
default dev nwg3  metric 1000  pref medium
`
	got := parseDefaultIfaces(out)
	want := []string{"nwg0", "nwg3", "ppp0"} // sorted, deduped
	if !slices.Equal(got, want) {
		t.Errorf("parseDefaultIfaces:\ngot:  %v\nwant: %v", got, want)
	}
}

func TestParseDefaultIfaces_EmptyOutput(t *testing.T) {
	if got := parseDefaultIfaces(""); len(got) != 0 {
		t.Errorf("empty input expected empty slice, got %v", got)
	}
}

func TestParseDefaultIfaces_NoDefaults(t *testing.T) {
	out := `10.10.10.0/24 dev br0 src 10.10.10.1
172.16.0.0/24 dev nwg2 src 172.16.0.1
`
	if got := parseDefaultIfaces(out); len(got) != 0 {
		t.Errorf("no defaults expected empty slice, got %v", got)
	}
}

func TestParseInetAddrs_PPPWithPeer(t *testing.T) {
	// PPP iface has both inet (our addr) and peer (provider gateway).
	// We want only the inet, normalized to /32.
	out := `5: ppp0: <POINTOPOINT,MULTICAST,NOARP,UP,LOWER_UP> mtu 1500 qdisc pfifo_fast state UNKNOWN group default qlen 100
    link/ppp
    inet 203.0.113.207 peer 198.51.100.1/32 scope global ppp0
       valid_lft forever preferred_lft forever
`
	got := parseInetAddrs(out)
	want := []string{"203.0.113.207/32"}
	if !slices.Equal(got, want) {
		t.Errorf("parseInetAddrs PPP:\ngot:  %v\nwant: %v", got, want)
	}
}

func TestParseInetAddrs_BridgeWithSubnet(t *testing.T) {
	// Bridge has CIDR mask — strip to /32.
	out := `4: br0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc noqueue state UP group default
    link/ether aa:bb:cc:dd:ee:ff brd ff:ff:ff:ff:ff:ff
    inet 10.10.10.1/24 brd 10.10.10.255 scope global br0
       valid_lft forever preferred_lft forever
`
	got := parseInetAddrs(out)
	want := []string{"10.10.10.1/32"}
	if !slices.Equal(got, want) {
		t.Errorf("parseInetAddrs bridge:\ngot:  %v\nwant: %v", got, want)
	}
}

func TestParseInetAddrs_MultipleAddrs(t *testing.T) {
	// Iface with multiple secondary addrs — return all.
	out := `inet 10.0.0.1/24 brd 10.0.0.255 scope global eth0
inet 10.0.0.2/24 scope global secondary eth0
`
	got := parseInetAddrs(out)
	want := []string{"10.0.0.1/32", "10.0.0.2/32"}
	if !slices.Equal(got, want) {
		t.Errorf("parseInetAddrs multi:\ngot:  %v\nwant: %v", got, want)
	}
}

func TestParseInetAddrs_NoAddrs(t *testing.T) {
	out := `7: nwg0: <POINTOPOINT,NOARP,UP,LOWER_UP> mtu 1420 qdisc noqueue state UNKNOWN group default qlen 1000
    link/none
`
	if got := parseInetAddrs(out); len(got) != 0 {
		t.Errorf("empty inet expected empty slice, got %v", got)
	}
}

// fakeWANRunner records calls and returns canned outputs per arg-prefix.
type fakeWANRunner struct {
	routeOut   string
	addrByDev  map[string]string
	addrErr    map[string]error
	routeErr   error
}

func (f *fakeWANRunner) run(_ context.Context, args ...string) (string, error) {
	// "ip route show table all"
	if len(args) >= 3 && args[0] == "route" && args[1] == "show" && args[2] == "table" {
		if f.routeErr != nil {
			return "", f.routeErr
		}
		return f.routeOut, nil
	}
	// "ip -4 addr show dev <iface>"
	if len(args) >= 5 && args[0] == "-4" && args[1] == "addr" && args[2] == "show" && args[3] == "dev" {
		iface := args[4]
		if err, ok := f.addrErr[iface]; ok && err != nil {
			return "", err
		}
		if out, ok := f.addrByDev[iface]; ok {
			return out, nil
		}
		return "", nil
	}
	return "", errors.New("unexpected ip args: " + strings.Join(args, " "))
}

// fakeWANLogger captures warnings so tests assert diagnostic visibility.
type fakeWANLogger struct{ warns []string }

func (l *fakeWANLogger) Warn(msg string) { l.warns = append(l.warns, msg) }
func (l *fakeWANLogger) Info(msg string) {}

func TestWANIPCollector_Collect_HappyPath(t *testing.T) {
	r := &fakeWANRunner{
		routeOut: "default dev ppp0  table 16395  src 203.0.113.207\ndefault dev nwg0  table 16396  src 10.8.1.3\n",
		addrByDev: map[string]string{
			"ppp0": "    inet 203.0.113.207 peer 198.51.100.1/32 scope global ppp0\n",
			"nwg0": "    inet 10.8.1.3/32 scope global nwg0\n",
		},
	}
	c := &wanIPCollectorImpl{run: r.run, log: &fakeWANLogger{}}
	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect err: %v", err)
	}
	want := []string{"10.8.1.3/32", "203.0.113.207/32"} // sorted
	if !slices.Equal(got, want) {
		t.Errorf("Collect:\ngot:  %v\nwant: %v", got, want)
	}
}

func TestWANIPCollector_Collect_NoDefaults_ReturnsEmpty(t *testing.T) {
	r := &fakeWANRunner{routeOut: ""}
	c := &wanIPCollectorImpl{run: r.run, log: &fakeWANLogger{}}
	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect err: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty routes expected empty result, got %v", got)
	}
}

func TestWANIPCollector_Collect_RouteCommandFails_ReturnsError(t *testing.T) {
	r := &fakeWANRunner{routeErr: errors.New("boom")}
	c := &wanIPCollectorImpl{run: r.run, log: &fakeWANLogger{}}
	if _, err := c.Collect(context.Background()); err == nil {
		t.Fatalf("expected error from failed ip route, got nil")
	}
}

func TestWANIPCollector_Collect_IfaceWithoutAddrs_WarnsAndSkips(t *testing.T) {
	log := &fakeWANLogger{}
	r := &fakeWANRunner{
		routeOut:  "default dev ppp0  table 16395\n",
		addrByDev: map[string]string{"ppp0": ""}, // no inet lines
	}
	c := &wanIPCollectorImpl{run: r.run, log: log}
	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect err: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("addr-less iface expected empty result, got %v", got)
	}
	if len(log.warns) == 0 {
		t.Errorf("expected WARN about ppp0 without addresses, got none")
	}
	if !strings.Contains(log.warns[0], "ppp0") {
		t.Errorf("WARN should name the iface, got %q", log.warns[0])
	}
}

func TestWANIPCollector_Collect_AddrCommandFailsForOneIface_WarnsAndContinues(t *testing.T) {
	log := &fakeWANLogger{}
	r := &fakeWANRunner{
		routeOut:  "default dev ppp0\ndefault dev nwg0\n",
		addrByDev: map[string]string{"nwg0": "    inet 10.8.1.3/32 scope global nwg0\n"},
		addrErr:   map[string]error{"ppp0": errors.New("device gone")},
	}
	c := &wanIPCollectorImpl{run: r.run, log: log}
	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect should not fail on per-iface addr error, got %v", err)
	}
	want := []string{"10.8.1.3/32"}
	if !slices.Equal(got, want) {
		t.Errorf("expected nwg0 only:\ngot:  %v\nwant: %v", got, want)
	}
	if len(log.warns) == 0 {
		t.Errorf("expected WARN about ppp0 addr error")
	}
}
