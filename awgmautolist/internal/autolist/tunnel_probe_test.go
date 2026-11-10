package autolist

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTunnelProbeUsesSelectedInterfaceWithCurl(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	curl := filepath.Join(dir, "curl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ARGS_PATH\"\nprintf 'ok'\n"
	if err := os.WriteFile(curl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARGS_PATH", argsPath)
	p := &Prober{CurlPath: curl, Interface: "ocserv0", TimeoutSec: 2}
	if got := p.Probe(context.Background(), "example.com"); got.Verdict != VerdictReachable {
		t.Fatalf("tunnel curl verdict = %+v", got)
	}
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Fields(string(data))
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--interface" {
			if args[i+1] != "ocserv0" {
				t.Fatalf("interface = %s", args[i+1])
			}
			return
		}
	}
	t.Fatalf("--interface not passed: %s", data)
}

func TestTunnelProbeControlsAutoAddAndCandidateVerdict(t *testing.T) {
	for _, tc := range []struct {
		name         string
		tunnelScript string
		wantTunnel   Verdict
		wantAdded    bool
	}{
		{name: "reachable tunnel permits auto add", tunnelScript: "printf ok", wantTunnel: VerdictReachable, wantAdded: true},
		{name: "unreachable tunnel blocks auto add", tunnelScript: "printf 'i/o timeout\\n' >&2\nexit 28", wantTunnel: VerdictBlocked},
		{name: "unknown tunnel blocks auto add", tunnelScript: "printf 'DNS unavailable\\n' >&2\nexit 6", wantTunnel: VerdictUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			awgm := newFakeAWGM(t)
			eng := NewEngine(awgm.srv.URL, "")
			eng.prober.CurlPath = fakeCurl(t, "printf 'i/o timeout\\n' >&2\nexit 28")
			eng.tunnelProber = &Prober{CurlPath: fakeCurl(t, tc.tunnelScript)}
			if err := eng.SetConfig(Config{AutoAdd: true, TunnelProbe: true, TargetInterface: "nwg0"}); err != nil {
				t.Fatal(err)
			}
			host := "www.tunnel.example"
			domain := "tunnel.example"
			eng.table.Note(Host{Name: host}, WhyReset, "packet", time.Now())
			if !eng.enqueue(probeTask{host: host, domain: domain, why: WhyReset, source: "packet"}) {
				t.Fatal("probe not queued")
			}
			task, ok := eng.nextProbe()
			if !ok {
				t.Fatal("probe not issued")
			}
			eng.probeOneContext(context.Background(), task)
			row, exists := eng.table.get(domain)
			if tc.wantAdded && exists {
				t.Fatalf("added domain remains in candidates: %+v", row)
			}
			if !tc.wantAdded && (!exists || row.Verdict != VerdictBlocked.String() || row.TunnelVerdict != tc.wantTunnel.String() || row.TunnelProbedAt.IsZero()) {
				t.Fatalf("candidate probe results = %+v, exists=%v", row, exists)
			}
			added := awgm.addedRules()
			if tc.wantAdded && (len(added) != 1 || added[0] != domain) {
				t.Fatalf("auto add = %v", added)
			}
			if !tc.wantAdded && len(added) != 0 {
				t.Fatalf("auto add was not prevented: %v", added)
			}
			if tc.wantAdded {
				if len(awgm.outbounds) != 1 || awgm.outbounds[0]["bind_interface"] != "nwg0" {
					t.Fatalf("interface outbound = %+v", awgm.outbounds)
				}
			}
		})
	}
}

func TestTunnelProbeUsesSameBodyWatchdog(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	eng.prober.CurlPath = fakeCurl(t, "printf 'i/o timeout\\n' >&2\nexit 28")
	eng.tunnelProber = &Prober{CurlPath: fakeCurl(t, fmt.Sprintf("printf '%%s' '%s'\nexec sleep 30\n", strings.Repeat("x", 16*1024)))}
	if err := eng.SetConfig(Config{TunnelProbe: true, TargetInterface: "nwg0"}); err != nil {
		t.Fatal(err)
	}
	eng.table.Note(Host{Name: "www.tunnel.example"}, WhyReset, "packet", time.Now())
	eng.enqueue(probeTask{host: "www.tunnel.example", domain: "tunnel.example", source: "packet"})
	task, _ := eng.nextProbe()
	eng.probeOneContext(context.Background(), task)
	row, exists := eng.table.get("tunnel.example")
	if !exists || row.TunnelVerdict != VerdictBlocked.String() {
		t.Fatalf("body stall through tunnel = %+v, exists=%v", row, exists)
	}
}

func TestAutoAddRequiresFullTunnelProbeAndInterface(t *testing.T) {
	eng := NewEngine("http://127.0.0.1:1", "")
	if err := eng.SetConfig(Config{AutoAdd: true, TargetInterface: "nwg0"}); err == nil {
		t.Fatal("autoAdd without tunnelProbe was accepted")
	}
	if err := eng.SetConfig(Config{AutoAdd: true, TunnelProbe: true}); err == nil {
		t.Fatal("autoAdd without targetInterface was accepted")
	}
}
