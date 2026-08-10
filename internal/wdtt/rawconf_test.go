package wdtt

import (
	"context"
	"os/exec"
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
	if fake.index("mtu OpkgTun18 1280") < 0 {
		t.Fatalf("ожидали mtu 1280 из RawClientMTU, calls=%v", fake.calls)
	}

	fake.calls = nil
	cfg.RawClientMTU = 0
	reconciled, err = svc.reconcileClientRawNDMS(context.Background(), DefaultInstanceID, cfg)
	if err != nil || !reconciled {
		t.Fatalf("reconcile (fallback): reconciled=%v err=%v", reconciled, err)
	}
	if fake.index("mtu OpkgTun18 1300") < 0 {
		t.Fatalf("ожидали фолбэк mtu 1300 при RawClientMTU=0, calls=%v", fake.calls)
	}
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
