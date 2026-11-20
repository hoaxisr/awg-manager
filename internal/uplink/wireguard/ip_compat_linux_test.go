//go:build linux

package wireguard

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestLegacyIPParserPreservesRoutingGuards(t *testing.T) {
	data, err := parseLegacyIP("1: from all fwmark 0x4151 lookup 103\n2: not from all fwmark 0x4100/0xff00 lookup 201 suppress_prefixlength 0\n", true)
	if err != nil {
		t.Fatal(err)
	}
	var rules []map[string]any
	if err = json.Unmarshal(data, &rules); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 || rules[0]["table"] != float64(103) || rules[1]["not"] != true || rules[1]["fwmask"] != "0xff00" || rules[1]["suppress_prefixlength"] != "0" {
		t.Fatal(string(data))
	}
	data, err = parseLegacyIP("unreachable default table 103 proto 196 metric 32767 error -101\ndefault via 192.0.2.1 dev wan1 table 103 proto 196 src 192.0.2.2 metric 10\n", false)
	if err != nil {
		t.Fatal(err)
	}
	var routes []route
	if err = json.Unmarshal(data, &routes); err != nil || len(routes) != 2 || routes[0].Type != "unreachable" || routes[1].Prefsrc != "192.0.2.2" || len(routes[0].Extra) != 0 {
		t.Fatal(string(data), err)
	}
	data, err = parseLegacyIP("unreachable default from 10.0.0.0/8 table 103 proto 196 metric 32767", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &routes); err != nil || len(routes[0].Extra) == 0 {
		t.Fatal("source selector was lost", string(data), err)
	}
	if _, err = parseLegacyIP("default nexthop via 192.0.2.1 dev wan1 weight 1", false); err == nil {
		t.Fatal("unknown multipath syntax silently accepted")
	}
}
func TestLegacyIPFallbackOnlyForUnsupportedJSONReads(t *testing.T) {
	binary := t.TempDir() + "/ip"
	defer legacyIPBinaries.Delete(binary)
	count := 0
	invoke := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		count++
		if strings.Contains(strings.Join(args, " "), "-j") {
			return nil, &exec.ExitError{Stderr: []byte(`Option "-j" is unknown, try "ip -help".`)}
		}
		return []byte("1: from all fwmark 0x4151 lookup 103\n"), nil
	}
	for i := 0; i < 2; i++ {
		if _, err := compatibleIP(context.Background(), binary, []string{"-4", "-j", "rule", "show"}, invoke); err != nil {
			t.Fatal(err)
		}
	}
	if count != 3 {
		t.Fatal("legacy capability was not cached", count)
	}
	blocked := errors.New("permission denied")
	calls := 0
	deny := func(context.Context, string, ...string) ([]byte, error) { calls++; return nil, blocked }
	if _, err := compatibleIP(context.Background(), binary, []string{"-4", "route", "replace", "default"}, deny); !errors.Is(err, blocked) || calls != 1 {
		t.Fatal("mutation was retried", err, calls)
	}
	calls = 0
	if _, err := compatibleIP(context.Background(), binary+"-other", []string{"-4", "-j", "rule", "show"}, deny); !errors.Is(err, blocked) || calls != 1 {
		t.Fatal("routing failure masked as unsupported JSON", err, calls)
	}
}
func TestLegacyIPNetlinkLookupProvesTable(t *testing.T) {
	data, err := lookupMarkedRoute(context.Background(), netip.MustParseAddr("127.0.0.1"), 0)
	if err != nil {
		t.Fatal(err)
	}
	var rows []route
	if err = json.Unmarshal(data, &rows); err != nil || len(rows) != 1 || (rows[0].Table != float64(255) && rows[0].Table != float64(254)) || rows[0].Dev != "lo" || rows[0].Prefsrc != "127.0.0.1" {
		t.Fatal(string(data), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := lookupMarkedRoute(ctx, netip.MustParseAddr("127.0.0.1"), 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestLegacyIPRouterReadCapture(t *testing.T) {
	path := os.Getenv("UPLINK_ROUTER_READ_CAPTURE")
	if path == "" {
		t.Skip("opt-in read-only router capture")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var captures map[string]string
	if err = json.Unmarshal(raw, &captures); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"rules4", "rules6", "routes4", "routes6"} {
		text, ok := captures[key]
		if !ok {
			t.Fatal("missing capture", key)
		}
		if _, err := parseLegacyIP(text, strings.HasPrefix(key, "rules")); err != nil {
			t.Fatal(key, err)
		}
	}
}
