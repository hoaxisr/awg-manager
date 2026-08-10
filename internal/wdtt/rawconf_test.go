package wdtt

import "testing"

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

func TestClientConfigUsesNDMSOpkgTun(t *testing.T) {
	cfg := ClientConfig{NdmsIface: "OpkgTun18", RawIface: "opkgtun18"}
	if !cfg.usesNDMSOpkgTun() {
		t.Fatal("expected NDMS opkg tun")
	}
	if cfg := (ClientConfig{NdmsIface: "OpkgTun18"}); cfg.usesNDMSOpkgTun() {
		t.Fatal("missing raw iface")
	}
}
