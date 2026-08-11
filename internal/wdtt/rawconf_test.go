package wdtt

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestParseRawConfLine(t *testing.T) {
	conf, ok := parseRawConfLine("RAWCONF|10.70.0.2|1.1.1.1,1.0.0.1|1300")
	if !ok {
		t.Fatal("expected ok")
	}
	if conf.ClientIP != "10.70.0.2" || conf.DNS != "1.1.1.1,1.0.0.1" || conf.MTU != 1300 {
		t.Fatalf("unexpected conf: %+v", conf)
	}
}

// F2: верхний потолок MTU симметричен нижнему (<576) — абсурдный/битый MTU
// из RAWCONF не должен уходить в персист и в NDMS как есть.
func TestParseRawConfLineClampsOversizedMTU(t *testing.T) {
	conf, ok := parseRawConfLine("RAWCONF|10.70.0.2|1.1.1.1|65535")
	if !ok {
		t.Fatal("expected ok")
	}
	if conf.MTU != 1300 {
		t.Fatalf("MTU=%d, want фолбэк 1300 при значении выше потолка", conf.MTU)
	}
}

func TestExtractRawConfFromLog(t *testing.T) {
	log := "noise\nRAWCONF|10.70.0.3|8.8.8.8|1280\n"
	conf, ok := ExtractRawConfFromLog(log)
	if !ok || conf.ClientIP != "10.70.0.3" || conf.MTU != 1280 {
		t.Fatalf("got %+v ok=%v", conf, ok)
	}
}

func TestBuildClientArgsRawTunName(t *testing.T) {
	args := buildClientArgs(ClientConfig{
		Peer:      "203.0.113.5:56013",
		Password:  "secret",
		VKHashes:  "abc",
		ConnMode:  ConnModeRaw,
		RawIface:  "opkgtun22",
		NdmsIface: "OpkgTun22",
	}, "/tmp/wdtt-tun.sock")
	if !containsArgPair(args, "-tun-name") || !containsArgPair(args, "opkgtun22") {
		t.Fatalf("missing tun-name in %v", args)
	}
	if !containsArgPair(args, "-tun-fd-sock") || !containsArgPair(args, "/tmp/wdtt-tun.sock") {
		t.Fatalf("missing tun-fd-sock in %v", args)
	}
}

// I5: reconcile обязан ставить MTU из персистнутого RawClientMTU (пришёл в
// RAWCONF при бутстрапе), а не хардкод 1300 — иначе восстановление после
// падения демона поднимает интерфейс с завышенным MTU (PMTU-блэкхол).
func TestReconcileClientRawNDMSUsesPersistedMTU(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(dir, dir, "/bin/sh", "/bin/sh")
	fake := &fakeOpkgCommands{}
	svc.SetNDMSInterfaceCommands(fake)
	svc.SetInterfaceChecker(fakeIfaceChecker{
		exists: map[string]bool{"opkgtun18": true},
		operUp: map[string]bool{"opkgtun18": false}, // reconcile требует down-интерфейс
	})

	proc := svc.clientProcs.get(DefaultInstanceID)
	proc.startCmd = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", "sleep 30; true")
	}
	if err := proc.Start(nil); err != nil {
		t.Fatal(err)
	}
	defer proc.Stop()

	cfg := ClientConfig{
		ConnMode:     ConnModeRaw,
		NdmsIface:    "OpkgTun18",
		RawIface:     "opkgtun18",
		RawClientIP:  "10.70.0.5",
		RawClientMTU: 1280,
	}
	reconciled, err := svc.reconcileClientRawNDMS(context.Background(), DefaultInstanceID, cfg)
	if err != nil || !reconciled {
		t.Fatalf("reconcile: reconciled=%v err=%v", reconciled, err)
	}
	// prepare безусловно шлёт SetMTU 1300 ДО активации — дискриминирующий
	// сигнал только mtu ПОСЛЕ address (реальная активация с RawClientMTU).
	if got := lastMTUAfterAddress(t, fake.calls); got != "mtu OpkgTun18 1280" {
		t.Fatalf("ожидали mtu 1280 из RawClientMTU после address, got %q calls=%v", got, fake.calls)
	}

	fake.calls = nil
	cfg.RawClientMTU = 0
	reconciled, err = svc.reconcileClientRawNDMS(context.Background(), DefaultInstanceID, cfg)
	if err != nil || !reconciled {
		t.Fatalf("reconcile (fallback): reconciled=%v err=%v", reconciled, err)
	}
	if got := lastMTUAfterAddress(t, fake.calls); got != "mtu OpkgTun18 1300" {
		t.Fatalf("ожидали фолбэк mtu 1300 при RawClientMTU=0 после address, got %q calls=%v", got, fake.calls)
	}
}

// lastMTUAfterAddress возвращает mtu-вызов, сделанный ПОСЛЕ первого address —
// это активация реальным MTU, а не безусловный SetMTU 1300 из prepare.
func lastMTUAfterAddress(t *testing.T, calls []string) string {
	t.Helper()
	addrAt := -1
	for i, c := range calls {
		if strings.HasPrefix(c, "address OpkgTun18") {
			addrAt = i
			break
		}
	}
	if addrAt < 0 {
		t.Fatalf("нет address в calls=%v", calls)
	}
	for _, c := range calls[addrAt:] {
		if strings.HasPrefix(c, "mtu OpkgTun18") {
			return c
		}
	}
	t.Fatalf("нет mtu после address, calls=%v", calls)
	return ""
}

func TestClientConfigUsesNDMSOpkgTun(t *testing.T) {
	cfg := ClientConfig{NdmsIface: "OpkgTun18", RawIface: "opkgtun18"}
	if !cfg.usesNDMSOpkgTun() {
		t.Fatal("expected NDMS opkg tun")
	}
	if cfg := (ClientConfig{NdmsIface: "OpkgTun18"}); cfg.usesNDMSOpkgTun() {
		t.Fatal("missing raw iface")
	}
}
