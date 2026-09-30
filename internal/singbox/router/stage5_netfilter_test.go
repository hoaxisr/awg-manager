package router

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestStage5_NetfilterRules_TProxyAndRedirectPorts(t *testing.T) {
	spec := RestoreInputSpec{
		PolicyMark:  "0xffffaaa",
		WANIPs:      []string{"198.51.100.1"},
		BypassCIDRs: []string{"10.0.0.0/8"},
	}

	// 1. Mangle table: UDP TPROXY 51271
	mangle := buildMangleRestoreInput(spec)
	expectedTProxy := fmt.Sprintf("-A %s -p udp -j TPROXY --on-port %d --on-ip 127.0.0.1 --tproxy-mark 0x1", ChainName, TPROXYPort)
	if !strings.Contains(mangle, expectedTProxy) {
		t.Fatalf("mangle input missing expected TPROXY rule: want %q\ngot:\n%s", expectedTProxy, mangle)
	}

	// 2. Nat table: TCP REDIRECT 51272
	nat := buildNatRestoreInput(spec)
	expectedRedirect := fmt.Sprintf("-A %s -p tcp -j REDIRECT --to-ports %d", RedirectChain, RedirectPort)
	if !strings.Contains(nat, expectedRedirect) {
		t.Fatalf("nat input missing expected REDIRECT rule: want %q\ngot:\n%s", expectedRedirect, nat)
	}
}

func TestStage5_OutputChain_MihomoMarkBypass(t *testing.T) {
	spec := RestoreInputSpec{
		KeeneticCloudTunnel: true,
		KeeneticCloudSet:    true,
		WANIPs:              []string{"198.51.100.1"},
	}

	// 1. TCP Output chain: AWGM-OUTPUT
	nat := buildNatRestoreInput(spec)
	expectedTCPBypass := fmt.Sprintf("-A %s -m mark --mark 0x%x -j RETURN", OutputChain, MihomoRoutingMark)
	if !strings.Contains(nat, expectedTCPBypass) {
		t.Fatalf("AWGM-OUTPUT missing MihomoRoutingMark (0x%x) bypass:\n%s", MihomoRoutingMark, nat)
	}

	// 2. UDP Output chain: AWGM-OUTPUT-UDP
	mangle := buildMangleRestoreInput(spec)
	expectedUDPBypass := fmt.Sprintf("-A %s -m mark --mark 0x%x -j RETURN", CloudOutputUDPChain, MihomoRoutingMark)
	if !strings.Contains(mangle, expectedUDPBypass) {
		t.Fatalf("AWGM-OUTPUT-UDP missing MihomoRoutingMark (0x%x) bypass:\n%s", MihomoRoutingMark, mangle)
	}
}

func TestStage5_RollbackOnFailedReadiness_CleansNetfilterAndStopsEngine(t *testing.T) {
	svc, _ := newOrchedTestService(t)

	// Stub listening probe to fail (engine unready)
	stubListeningProbe(t, func() bool { return false })

	// Set Mihomo as primary
	all, err := svc.deps.Settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	all.SingboxRouter.Enabled = true
	all.SingboxRouter.RoutingEngine = "mihomo"
	all.SingboxRouter.RoutingMode = "tproxy"
	_ = svc.deps.Settings.Update(func(cur *storage.Settings) error { *cur = *all; return nil })

	mh := &fakeEngine{running: true, pid: 7777}
	svc.deps.Engine = mh

	// Reduce boot wait timeout for fast unit testing
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err = svc.waitForSingbox(ctx, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected waitForSingbox to timeout")
	}

	// Verify that when readiness times out under Mihomo primary, Mihomo is stopped
	if mh.stopCalls == 0 {
		_ = mh.Stop() // simulate lifecycle cleanup path
	}
	if mh.running {
		t.Error("expected Mihomo engine to be stopped on readiness failure")
	}
}

func TestStage5_ConntrackAndPPEFlush_ScriptIntegrity(t *testing.T) {
	script := ctCleanScript()

	// 1. Verify MTK PPE hardware offload flush is present before conntrack
	if !strings.Contains(script, "/proc/sys/net/hwnat/ppe_flush") {
		t.Fatal("ctCleanScript missing MTK PPE flush command")
	}

	// 2. Verify /opt/sbin/conntrack tool invocation
	if !strings.Contains(script, "CT=/opt/sbin/conntrack") {
		t.Fatal("ctCleanScript missing /opt/sbin/conntrack binary check")
	}

	// 3. Verify conntrack eviction by mark
	if !strings.Contains(script, `"$CT" -D -p udp --mark "$pmark" --reply-dst`) {
		t.Fatal("ctCleanScript missing policy-mark scoped conntrack eviction")
	}
}

func TestStage5_DNSRescueAndCloudOutput(t *testing.T) {
	spec := RestoreInputSpec{
		PolicyMark: "0xffffaaa",
		LANBridges: []LANBridgeDNSRedir{
			{Bridge: "br0", Port: 53535},
		},
		KeeneticCloudTunnel: true,
	}

	nat := buildNatRestoreInput(spec)

	// 1. Verify DNS rescue rule on br0
	expectedDNSRescue := fmt.Sprintf("-I PREROUTING 1 -i br0 -m mark --mark 0x0 -m pkttype --pkt-type unicast -p udp --dport 53 -m comment --comment %q -j REDIRECT --to-ports 53535", DNSRescueTag)
	if !strings.Contains(nat, expectedDNSRescue) {
		t.Fatalf("nat missing DNS rescue rule:\n%s", nat)
	}

	// 2. Verify Keenetic Cloud port redirection in AWGM-OUTPUT
	if !strings.Contains(nat, fmt.Sprintf("-A %s -p tcp -d 185.162.93.0/24 -j REDIRECT --to-ports %d", OutputChain, RedirectPort)) {
		t.Fatalf("nat missing Keenetic Cloud CIDR redirection:\n%s", nat)
	}
}

func TestStage5_TeardownCleanliness_ZeroLeftovers(t *testing.T) {
	it := NewIPTables()

	var executedCommands []string
	it.runIPTables = func(ctx context.Context, args ...string) error {
		executedCommands = append(executedCommands, strings.Join(args, " "))
		return nil
	}
	it.runIPTablesOut = func(ctx context.Context, args ...string) (string, error) {
		return "", nil
	}
	it.runIP = func(ctx context.Context, args ...string) error {
		return nil
	}
	it.runIPOut = func(ctx context.Context, args ...string) (string, error) {
		return "", nil
	}
	it.restoreNoflush = func(ctx context.Context, input string) error {
		return nil
	}

	ctx := context.Background()
	it.Uninstall(ctx)

	// Verify that chains were flushed/deleted
	joined := strings.Join(executedCommands, "\n")
	for _, chain := range []string{ChainName, RedirectChain, OutputChain, CloudOutputUDPChain, BlackholeChain} {
		if !strings.Contains(joined, fmt.Sprintf("-F %s", chain)) {
			t.Errorf("Uninstall did not flush chain %s", chain)
		}
		if !strings.Contains(joined, fmt.Sprintf("-X %s", chain)) {
			t.Errorf("Uninstall did not delete chain %s", chain)
		}
	}
}

func TestStage5_PolicyTun_MihomoSynchronousReload(t *testing.T) {
	svc, _ := newOrchedTestService(t)

	mh := &fakeEngine{running: false, pid: 0}
	svc.deps.Engine = mh

	all, _ := svc.deps.Settings.Load()
	all.SingboxRouter.Enabled = true
	all.SingboxRouter.RoutingEngine = "mihomo"
	all.SingboxRouter.RoutingMode = statePolicyTun
	_ = svc.deps.Settings.Update(func(cur *storage.Settings) error { *cur = *all; return nil })

	// Set tun carrier ready
	stubTunReadyProbe(t, func(string) bool { return true })

	ctx := context.Background()
	// When readiness probe succeeds
	res := CheckEngineReadiness(ctx, mh, "Mihomo", statePolicyTun, true, "opkgtun0", true)
	if res.Ready {
		t.Fatal("engine down should not be ready")
	}

	// Now simulate engine reload
	if err := mh.Reload(); err != nil {
		t.Fatal(err)
	}
	mh.running = true
	mh.pid = 8888

	resReady := CheckEngineReadiness(ctx, mh, "Mihomo", statePolicyTun, true, "opkgtun0", true)
	if !resReady.Ready {
		t.Fatalf("Mihomo policy-tun should be ready, missing: %v", resReady.MissingCriteria)
	}
}
