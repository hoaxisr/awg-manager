package router

import (
	"context"
	"strings"
	"testing"
)

// The fail-closed blackhole blob must carry the SAME LAN/router/WAN RETURN
// exclusions as the interception chain, then a terminal DROP, entered from
// PREROUTING by the identical policy selector — mangle only, never nat.
func TestBuildBlackholeRestoreInput_PolicyMark(t *testing.T) {
	got := buildBlackholeRestoreInput(RestoreInputSpec{
		PolicyMark:  "0xffffaaa",
		WANIPs:      []string{"203.0.113.5"},
		BypassCIDRs: []string{"192.168.50.0/24"},
	})

	must := []string{
		"*mangle",
		":" + BlackholeChain + " - [0:0]",
		"-A " + BlackholeChain + " -d 192.168.50.0/24 -j RETURN", // user bypass
		"-A " + BlackholeChain + " -d 10.0.0.0/8 -j RETURN",      // LAN
		"-A " + BlackholeChain + " -d 127.0.0.0/8 -j RETURN",     // loopback
		"-A " + BlackholeChain + " -d 203.0.113.5 -j RETURN",     // router WAN IP
		"-A " + BlackholeChain + " -j DROP",                      // terminal fail-closed drop
		"-A PREROUTING -m connmark --mark 0xffffaaa -m conntrack ! --ctstate INVALID -j " + BlackholeChain,
		"COMMIT",
	}
	for _, s := range must {
		if !strings.Contains(got, s) {
			t.Errorf("blackhole blob missing %q\n---\n%s", s, got)
		}
	}

	// mangle-only: the blackhole must never touch nat or emit TPROXY/REDIRECT.
	for _, bad := range []string{"*nat", RedirectChain, "TPROXY", "REDIRECT"} {
		if strings.Contains(got, bad) {
			t.Errorf("blackhole blob unexpectedly contains %q\n%s", bad, got)
		}
	}

	// DROP must come AFTER every RETURN exclusion — otherwise it would swallow
	// LAN/router/WAN traffic the RETURNs are meant to spare.
	if strings.Index(got, "-j DROP") < strings.LastIndex(got, "-j RETURN") {
		t.Error("terminal DROP must be positioned after all RETURN exclusions")
	}
}

// Bypass ports: the blackhole must mirror the interception chain's RETURN set —
// the user's bypass UDP/TCP ports (so deliberately-excluded traffic goes
// direct, not dropped).
func TestBuildBlackholeRestoreInput_BypassPorts(t *testing.T) {
	got := buildBlackholeRestoreInput(RestoreInputSpec{
		PolicyMark:     "0xff",
		BypassUDPPorts: []PortRange{{51820, 51820}},
		BypassTCPPorts: []PortRange{{22, 22}},
	})
	must := []string{
		"-A " + BlackholeChain + " -p udp --dport 51820 -j RETURN",
		"-A " + BlackholeChain + " -p tcp --dport 22 -j RETURN",
	}
	for _, s := range must {
		if !strings.Contains(got, s) {
			t.Errorf("blackhole blob missing %q\n---\n%s", s, got)
		}
	}
	// Every port RETURN must still precede the DROP.
	if strings.Index(got, "-j DROP") < strings.LastIndex(got, "-j RETURN") {
		t.Error("DROP must be after the bypass-port RETURNs")
	}
}

// geoip-bypass: адреса из AWGM-BYPASS уходят прямо и при живом движке, так что
// мёртвый движок их тем более не должен дропать — RETURN рядом с
// пользовательскими bypass-CIDR и строго до терминального DROP.
func TestBuildBlackholeRestoreInput_BypassGeoIPSetReturn(t *testing.T) {
	got := buildBlackholeRestoreInput(RestoreInputSpec{
		PolicyMark:     "0xff",
		BypassCIDRs:    []string{"192.168.50.0/24"},
		BypassGeoIPSet: true,
	})

	setIdx := strings.Index(got, "-A "+BlackholeChain+" -m set --match-set "+bypassSetName+" dst -j RETURN")
	userIdx := strings.Index(got, "-A "+BlackholeChain+" -d 192.168.50.0/24 -j RETURN")
	dropIdx := strings.Index(got, "-A "+BlackholeChain+" -j DROP")
	if setIdx == -1 || userIdx == -1 || dropIdx == -1 {
		t.Fatalf("setIdx=%d userIdx=%d dropIdx=%d\n%s", setIdx, userIdx, dropIdx, got)
	}
	if setIdx < userIdx {
		t.Errorf("set rule (%d) must follow user bypass CIDRs (%d)", setIdx, userIdx)
	}
	if setIdx > dropIdx {
		t.Errorf("set rule (%d) must precede the terminal DROP (%d)", setIdx, dropIdx)
	}
}

// MatchAll (device mode, no policy mark) drops all non-excluded traffic without
// a connmark filter — mirroring the interception jump.
func TestBuildBlackholeRestoreInput_MatchAll(t *testing.T) {
	got := buildBlackholeRestoreInput(RestoreInputSpec{MatchAll: true})
	if !strings.Contains(got, "-A PREROUTING -m conntrack ! --ctstate INVALID -j "+BlackholeChain) {
		t.Errorf("MatchAll jump missing:\n%s", got)
	}
	if strings.Contains(got, "connmark") {
		t.Errorf("MatchAll must not carry a connmark filter:\n%s", got)
	}
}

// InstallBlackhole restores the blackhole blob and persists it (so the
// netfilter.d hook can re-assert it after an NDMS reload while the engine is
// still down).
func TestInstallBlackhole_RestoresAndPersists(t *testing.T) {
	fe := newFakeExec()
	it := newFakeIPTables(fe)
	var persisted string
	it.persistBlackhole = func(in string) error { persisted = in; return nil }

	if err := it.InstallBlackhole(context.Background(), RestoreInputSpec{PolicyMark: "0xff"}); err != nil {
		t.Fatalf("InstallBlackhole: %v", err)
	}

	var restored string
	for _, c := range fe.calls {
		if c.kind == "restore" {
			restored = c.stdin
		}
	}
	if !strings.Contains(restored, "-A "+BlackholeChain+" -j DROP") {
		t.Errorf("restore missing DROP:\n%s", restored)
	}
	if !strings.Contains(persisted, "-A "+BlackholeChain+" -j DROP") {
		t.Errorf("persistBlackhole not given the blackhole blob: %q", persisted)
	}
}

// RemoveBlackhole deletes the rules file and flushes+deletes the chain.
func TestRemoveBlackhole_CleansUp(t *testing.T) {
	fe := newFakeExec()
	it := newFakeIPTables(fe)
	cleaned := false
	it.cleanupBlackhole = func() { cleaned = true }

	it.RemoveBlackhole(context.Background())

	if !cleaned {
		t.Error("cleanupBlackhole (delete rules file) not called")
	}
	var sawFlush, sawDelete bool
	for _, c := range fe.calls {
		if c.kind != "iptables" || len(c.args) < 4 || c.args[3] != BlackholeChain {
			continue
		}
		switch c.args[2] {
		case "-F":
			sawFlush = true
		case "-X":
			sawDelete = true
		}
	}
	if !sawFlush || !sawDelete {
		t.Errorf("expected -F and -X on %s; calls=%+v", BlackholeChain, fe.calls)
	}
}

// The netfilter.d hook must gain a dead-engine branch that re-asserts the
// blackhole, and an alive-engine scrub that removes any stale blackhole.
func TestNetfilterHookScript_BlackholeFailClosed(t *testing.T) {
	s := netfilterHookScript(true)
	if !strings.Contains(s, netfilterBlackholePath) {
		t.Error("hook missing blackhole rules path (dead-engine restore)")
	}
	if !strings.Contains(s, "grep -qE -- '-[jg] "+BlackholeChain+"($| )'") {
		t.Error("hook missing anchored blackhole jump gate")
	}
	if !strings.Contains(s, "-F "+BlackholeChain) || !strings.Contains(s, "-X "+BlackholeChain) {
		t.Error("hook missing blackhole scrub in the alive-engine path")
	}
	if !strings.Contains(s, "if pidof sing-box") || !strings.Contains(s, "\nelse\n") {
		t.Error("hook missing pidof if/else (alive vs dead) structure")
	}
}

// Повторный InstallBlackhole без netfilter-flap задублировал бы джамп: старый
// снимается до restore.
func TestInstallBlackhole_ScrubsStaleJumpBeforeRestore(t *testing.T) {
	fe := newFakeExec()
	it := newFakeIPTables(fe)
	it.runIPTablesOut = func(_ context.Context, _ ...string) (string, error) {
		return "-P PREROUTING ACCEPT\n-N AWGM-BLACKHOLE\n-A PREROUTING -m conntrack ! --ctstate INVALID -j AWGM-BLACKHOLE\n", nil
	}
	if err := it.InstallBlackhole(context.Background(), RestoreInputSpec{PolicyMark: "0xff"}); err != nil {
		t.Fatalf("InstallBlackhole: %v", err)
	}
	want := []string{"-t", "mangle", "-D", "PREROUTING", "-m", "conntrack", "!", "--ctstate", "INVALID", "-j", "AWGM-BLACKHOLE"}
	if !containsArgv(iptablesCallsBeforeFirstRestore(fe.calls), want) {
		t.Fatalf("старый джамп на AWGM-BLACKHOLE не снят до restore: %v", fe.calls)
	}
}
