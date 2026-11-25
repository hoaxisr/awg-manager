package serverwizard

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/serveringress"
	"github.com/hoaxisr/awg-manager/internal/sys/procnet"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

type mockFileInfo struct {
	name  string
	size  int64
	isDir bool
}

func (m mockFileInfo) Name() string       { return m.name }
func (m mockFileInfo) Size() int64        { return m.size }
func (m mockFileInfo) Mode() fs.FileMode  { return 0755 }
func (m mockFileInfo) ModTime() time.Time { return time.Now() }
func (m mockFileInfo) IsDir() bool        { return m.isDir }
func (m mockFileInfo) Sys() interface{}   { return nil }

func TestPreflightEngine_TelegramChecks(t *testing.T) {
	xray := &mockXrayReader{}
	disp := &mockDispReader{}
	tg := &mockTgReader{
		st: tgwebproxy.Status{PID: 1234},
	}
	rec := &mockRecReader{required: false}

	engine := NewPreflightEngine(xray, disp, tg, rec, nil)

	// Mock file stats: all binaries exist
	engine.statFn = func(path string) (os.FileInfo, error) {
		return mockFileInfo{name: path, size: 1000}, nil
	}

	// Mock sockets: 8443 is free, 8085 is owned by tg (PID 1234)
	engine.findProcFn = func(procDir, addr string, port int) (procnet.ListenerLookup, error) {
		if port == 8443 {
			return procnet.ListenerLookup{SocketFound: false}, nil
		}
		if port == 8085 {
			return procnet.ListenerLookup{SocketFound: true, SocketInode: "999", PID: 1234}, nil
		}
		return procnet.ListenerLookup{SocketFound: false}, nil
	}

	ctx := context.Background()
	resp := engine.Run(ctx, WizardPlanRequest{
		Kind:         "tgwebproxy",
		DirectPort:   8443,
		ListenPort:   8085,
		CdnProfileID: "cdn_get",
	})

	if !resp.CanProceed {
		t.Fatalf("expected CanProceed=true, checks: %+v", resp.Checks)
	}

	// Now introduce port conflict on 8443 (PID 9999)
	engine.findProcFn = func(procDir, addr string, port int) (procnet.ListenerLookup, error) {
		if port == 8443 {
			return procnet.ListenerLookup{SocketFound: true, SocketInode: "100", PID: 9999}, nil
		}
		return procnet.ListenerLookup{SocketFound: false}, nil
	}

	resp = engine.Run(ctx, WizardPlanRequest{
		Kind:       "tgwebproxy",
		DirectPort: 8443,
	})

	if resp.CanProceed {
		t.Fatalf("expected CanProceed=false on port conflict")
	}

	engine.findProcFn = func(procDir, addr string, port int) (procnet.ListenerLookup, error) {
		return procnet.ListenerLookup{SocketFound: false}, nil
	}

	// With embedded mtg & tproxy, dual scenario succeeds even without /opt/bin/telemt or /opt/bin/tproxy-server
	tg.st = tgwebproxy.Status{
		Installed:       true,
		InstalledTelemt: true,
		InstalledTproxy: true,
		PID:             1234,
		DirectPID:       1234,
	}
	engine.statFn = func(path string) (os.FileInfo, error) {
		return nil, errors.New("file not found")
	}
	resp = engine.Run(ctx, WizardPlanRequest{
		Kind:         "tgwebproxy",
		Scenario:     "dual",
		DirectHost:   "203.0.113.10",
		DirectPort:   8443,
		ListenPort:   8085,
		CdnProfileID: "cdn_get",
	})
	if !resp.CanProceed || resp.HasWarnings {
		t.Fatalf("expected CanProceed=true and HasWarnings=false for embedded mtg+tproxy, got %+v", resp)
	}
}

func TestPreflightEngine_XrayChecks(t *testing.T) {
	xray := &mockXrayReader{
		st: vlessserver.Status{Installed: true, PID: 5678, Version: "awgm-xhttpd 1.0.0-c"},
	}
	disp := &mockDispReader{}
	tg := &mockTgReader{}
	rec := &mockRecReader{required: false}

	engine := NewPreflightEngine(xray, disp, tg, rec, nil)

	// Stat returns xray exists
	engine.statFn = func(path string) (os.FileInfo, error) {
		return mockFileInfo{name: path, size: 5000000}, nil
	}

	// 1099 is listening (Mihomo active), 9008 is free, 9009 is free
	engine.findProcFn = func(procDir, addr string, port int) (procnet.ListenerLookup, error) {
		if port == 1099 {
			return procnet.ListenerLookup{SocketFound: true, PID: 111}, nil
		}
		return procnet.ListenerLookup{SocketFound: false}, nil
	}

	ctx := context.Background()
	resp := engine.Run(ctx, WizardPlanRequest{
		Kind:         "xray",
		PublicDomain: "cdn.myvpn.net",
		Path:         "/cdn-bridge/",
	})

	if !resp.CanProceed {
		t.Fatalf("expected CanProceed=true, got %+v", resp.Checks)
	}

	// Invalid domain
	resp = engine.Run(ctx, WizardPlanRequest{
		Kind:         "xray",
		PublicDomain: "bad domain name",
		Path:         "/cdn-bridge/",
	})
	if resp.CanProceed {
		t.Fatalf("expected CanProceed=false on invalid domain name")
	}

	// Legacy mihomo:1099 blocked in preflight (A2)
	resp = engine.Run(ctx, WizardPlanRequest{
		Kind:           "xray",
		PublicDomain:   "cdn.myvpn.net",
		Path:           "/cdn-bridge/",
		UpstreamDevice: "mihomo:1099",
	})
	if resp.CanProceed {
		t.Fatalf("expected CanProceed=false when legacy mihomo:1099 is requested")
	}

	// Recovery required
	rec.required = true
	rec.reason = "corrupt journal"
	resp = engine.Run(ctx, WizardPlanRequest{
		Kind:         "xray",
		PublicDomain: "cdn.myvpn.net",
		Path:         "/cdn-bridge/",
	})
	if resp.CanProceed {
		t.Fatalf("expected CanProceed=false when recovery required")
	}
}

func TestPreflightEngine_XHTTPDMandatoryAndNoLegacyXrayBinary(t *testing.T) {
	xray := &mockXrayReader{
		st: vlessserver.Status{Installed: true, PID: 5678, Version: "awgm-xhttpd 1.0.0-c"},
	}
	disp := &mockDispReader{}
	tg := &mockTgReader{}
	rec := &mockRecReader{required: false}

	engine := NewPreflightEngine(xray, disp, tg, rec, nil)
	// /opt/sbin/xray MUST NEVER be probed or required
	engine.statFn = func(path string) (os.FileInfo, error) {
		if path == "/opt/sbin/xray" {
			t.Fatalf("PreflightEngine must never stat legacy /opt/sbin/xray")
		}
		return mockFileInfo{name: path, size: 1000}, nil
	}
	engine.findProcFn = func(procDir, addr string, port int) (procnet.ListenerLookup, error) {
		return procnet.ListenerLookup{SocketFound: false}, nil
	}

	resp := engine.Run(context.Background(), WizardPlanRequest{
		Kind:         "xray",
		PublicDomain: "cdn.example.test",
		Path:         "/cdn-bridge/",
	})
	if !resp.CanProceed {
		t.Fatalf("expected CanProceed=true when awgm-xhttpd is installed without /opt/sbin/xray, got: %+v", resp.Checks)
	}

	// Missing / broken awgm-xhttpd must block preflight
	xray.st = vlessserver.Status{Installed: false, UnavailableReason: "awgm-xhttpd binary missing at /opt/sbin/awgm-xhttpd"}
	respMissing := engine.Run(context.Background(), WizardPlanRequest{
		Kind:         "xray",
		PublicDomain: "cdn.example.test",
		Path:         "/cdn-bridge/",
	})
	if respMissing.CanProceed {
		t.Fatalf("expected CanProceed=false when awgm-xhttpd is unavailable")
	}

	// WebSocket transport and legacy non-xhttp_get modes must block preflight
	xray.st = vlessserver.Status{Installed: true, Version: "awgm-xhttpd 1.0.0-c"}
	respWS := engine.Run(context.Background(), WizardPlanRequest{
		Kind:         "xray",
		PublicDomain: "cdn.example.test",
		Path:         "/cdn-bridge/",
		CdnProfileID: "cdn_ws",
		Mode:         "ws",
	})
	if respWS.CanProceed {
		t.Fatalf("expected CanProceed=false when WebSocket transport is requested for VLESS")
	}
	respLegacyMode := engine.Run(context.Background(), WizardPlanRequest{
		Kind:         "both",
		Scenario:     "dual",
		PublicDomain: "connect.example.netcraze.club",
		DirectHost:   "connect.example.netcraze.club",
		TlsDomain:    "www.google.com",
		CdnProfileID: "cdn_get",
		Mode:         "cdn",
	})
	if respLegacyMode.CanProceed {
		t.Fatalf("expected CanProceed=false when legacy Mode='cdn' is passed without PreserveTransport")
	}
}

func TestPreflightAndDesired_TgWebProxyPortAvoidsXrayLoop(t *testing.T) {
	xray := &mockXrayReader{st: vlessserver.Status{Installed: true, Version: "awgm-xhttpd 1.0.0-c"}}
	disp := &mockDispReader{}
	tg := &mockTgReader{st: tgwebproxy.Status{Installed: true, InstalledTelemt: true, InstalledTproxy: true}}
	rec := &mockRecReader{required: false}

	engine := NewPreflightEngine(xray, disp, tg, rec, nil)
	engine.statFn = func(path string) (os.FileInfo, error) {
		return mockFileInfo{name: path, size: 1000}, nil
	}
	checkedPorts := map[int]bool{}
	engine.findProcFn = func(procDir, addr string, port int) (procnet.ListenerLookup, error) {
		checkedPorts[port] = true
		return procnet.ListenerLookup{SocketFound: false}, nil
	}

	// Simulate cached client sending xray default 9008 in listen_port alongside tg_listen_port 8085
	reqWithTgPort := WizardPlanRequest{
		Kind:         "tgwebproxy",
		Scenario:     "cdn_http",
		PublicDomain: "connect.example.netcraze.club",
		ListenPort:   9008,
		TgListenPort: 8085,
	}
	resp := engine.Run(context.Background(), reqWithTgPort)
	if !resp.CanProceed {
		t.Fatalf("expected CanProceed=true when tg_listen_port=8085 rescues listen_port=9008, got: %+v", resp.Checks)
	}
	if !checkedPorts[8085] || checkedPorts[9008] {
		t.Fatalf("expected preflight to inspect port 8085 instead of 9008, checked=%v", checkedPorts)
	}
	desired, err := BuildDesiredConfig(context.Background(), nil, reqWithTgPort)
	if err != nil {
		t.Fatalf("BuildDesiredConfig unexpected error: %v", err)
	}
	if desired.ListenPort != 8085 {
		t.Fatalf("expected desired ListenPort=8085, got %d", desired.ListenPort)
	}

	// Explicit 9008 without non-9008 tg_listen_port must block instead of looping tproxy->mux
	reqConflict := WizardPlanRequest{
		Kind:         "tgwebproxy",
		Scenario:     "cdn_http",
		PublicDomain: "connect.example.netcraze.club",
		ListenPort:   9008,
	}
	respConflict := engine.Run(context.Background(), reqConflict)
	if respConflict.CanProceed {
		t.Fatalf("expected preflight to block tgwebproxy web proxy on reserved port 9008")
	}
	if _, err := BuildDesiredConfig(context.Background(), nil, reqConflict); err == nil {
		t.Fatalf("expected BuildDesiredConfig to reject tgwebproxy web proxy on reserved port 9008")
	}

	// Unknown CDN profile (e.g. legacy "gcore") must be blocked before apply
	reqUnknownCDN := WizardPlanRequest{
		Kind:          "tgwebproxy",
		Scenario:      "cdn_http",
		ConnectionWay: "cdn",
		PublicDomain:  "connect.example.netcraze.club",
		ListenPort:    8085,
		CdnProfileID:  "gcore",
	}
	respUnknownCDN := engine.Run(context.Background(), reqUnknownCDN)
	if respUnknownCDN.CanProceed {
		t.Fatalf("expected preflight to block unknown CDN profile 'gcore'")
	}
	if _, err := BuildDesiredConfig(context.Background(), nil, reqUnknownCDN); err == nil {
		t.Fatalf("expected BuildDesiredConfig to reject unknown CDN profile 'gcore'")
	}

	// Mode mismatch: "direct" profile under direct_fake_tls vs "cdn_get" under direct_fake_tls
	reqMismatch := WizardPlanRequest{
		Kind:         "tgwebproxy",
		Scenario:     "direct_fake_tls",
		DirectHost:   "1.2.3.4",
		DirectPort:   8443,
		TlsDomain:    "www.google.com",
		CdnProfileID: "cdn_get",
	}
	if respM := engine.Run(context.Background(), reqMismatch); respM.CanProceed {
		t.Fatalf("expected preflight to block cdn_get profile on direct_fake_tls scenario")
	}
	if _, err := BuildDesiredConfig(context.Background(), nil, reqMismatch); err == nil {
		t.Fatalf("expected BuildDesiredConfig to reject cdn_get profile on direct_fake_tls scenario")
	}

	// Dispatcher collision and direct==web collision in dual mode must be rejected before apply
	reqColl := WizardPlanRequest{
		Kind:           "tgwebproxy",
		Scenario:       "dual",
		DirectHost:     "1.2.3.4",
		DirectPort:     8085,
		ListenPort:     8085,
		PublicDomain:   "connect.example.netcraze.club",
		TlsDomain:      "www.google.com",
		DispatcherPort: 9009,
	}
	if respC := engine.Run(context.Background(), reqColl); respC.CanProceed {
		t.Fatalf("expected preflight to block DirectPort == ListenPort collision")
	}
	if _, err := BuildDesiredConfig(context.Background(), nil, reqColl); err == nil {
		t.Fatalf("expected BuildDesiredConfig to reject DirectPort == ListenPort collision")
	}
}

func TestPreflightEngine_InProcessPIDPortIsolationAndCollisions(t *testing.T) {
	xray := &mockXrayReader{}
	disp := &mockDispReader{}
	tg := &mockTgReader{
		cfg: tgwebproxy.PublicConfig{
			Enabled:    true,
			DirectPort: 8443,
			ListenPort: 8085,
		},
		st: tgwebproxy.Status{
			InstalledTelemt: true,
			DirectOnline:    true,
			DirectPID:       os.Getpid(),
			DirectPort:      8443,
		},
	}
	rec := &mockRecReader{required: false}
	engine := NewPreflightEngine(xray, disp, tg, rec, nil)
	engine.statFn = func(path string) (os.FileInfo, error) {
		return mockFileInfo{name: path, size: 1000}, nil
	}
	engine.findProcFn = func(procDir, addr string, port int) (procnet.ListenerLookup, error) {
		switch port {
		case 2053, 2222, 8443:
			return procnet.ListenerLookup{SocketFound: true, SocketInode: "inproc", PID: os.Getpid()}, nil
		default:
			return procnet.ListenerLookup{SocketFound: false}, nil
		}
	}

	ctx := context.Background()

	// 1. Active in-process Reality (:2053) and Web UI (:2222) share os.Getpid() -> must be BLOCKED
	for _, occupiedPort := range []int{2053, 2222} {
		resp := engine.Run(ctx, WizardPlanRequest{
			Kind:       "tgwebproxy",
			Scenario:   "direct_fake_tls",
			DirectHost: "203.0.113.10",
			DirectPort: occupiedPort,
			TlsDomain:  "www.google.com",
		})
		if resp.CanProceed {
			t.Fatalf("expected in-process port %d owned by os.Getpid() to be BLOCKED for MTProto", occupiedPort)
		}
	}

	// 2. Own active MTProto port (8443) -> ALLOWED
	respOwn := engine.Run(ctx, WizardPlanRequest{
		Kind:       "tgwebproxy",
		Scenario:   "direct_fake_tls",
		DirectHost: "203.0.113.10",
		DirectPort: 8443,
		TlsDomain:  "www.google.com",
	})
	if !respOwn.CanProceed {
		t.Fatalf("expected active own DirectPort 8443 to pass, got %+v", respOwn.Checks)
	}

	// 3. Reserved tproxy-server backend port 8084 -> BLOCKED
	resp8084 := engine.Run(ctx, WizardPlanRequest{
		Kind:       "tgwebproxy",
		Scenario:   "direct_fake_tls",
		DirectHost: "203.0.113.10",
		DirectPort: 8084,
		TlsDomain:  "www.google.com",
	})
	if resp8084.CanProceed {
		t.Fatalf("expected reserved DirectPort 8084 to be BLOCKED")
	}

	// 4. Single-service dual mode collision DirectPort == ListenPort (8085) -> BLOCKED
	respColl := engine.Run(ctx, WizardPlanRequest{
		Kind:         "tgwebproxy",
		Scenario:     "dual",
		PublicDomain: "tg.example.com",
		DirectHost:   "203.0.113.10",
		DirectPort:   8085,
		ListenPort:   8085,
		TlsDomain:    "www.google.com",
	})
	if respColl.CanProceed {
		t.Fatalf("expected dual mode DirectPort == ListenPort (8085) to be BLOCKED")
	}
}

func TestPreflightEngine_CdnHTTPAndCloudKeenDNSWarnings(t *testing.T) {
	xray := &mockXrayReader{}
	disp := &mockDispReader{}
	tg := &mockTgReader{
		st: tgwebproxy.Status{PID: 1234},
	}
	rec := &mockRecReader{required: false}
	engine := NewPreflightEngine(xray, disp, tg, rec, nil)
	engine.findProcFn = func(procDir, addr string, port int) (procnet.ListenerLookup, error) {
		return procnet.ListenerLookup{SocketFound: false}, nil
	}

	ctx := context.Background()

	// 1. cdn_http omits bin_telemt and includes bin_tproxy
	respCDN := engine.Run(ctx, WizardPlanRequest{
		Kind:         "tgwebproxy",
		Scenario:     "cdn_http",
		PublicDomain: "tg.example.com",
		ListenPort:   8085,
		CdnProfileID: "cdn_get",
	})
	if !respCDN.CanProceed {
		t.Fatalf("expected cdn_http preflight CanProceed=true, got %+v", respCDN.Checks)
	}
	hasTelemt, hasTproxy := false, false
	for _, c := range respCDN.Checks {
		if c.ID == "bin_telemt" {
			hasTelemt = true
		}
		if c.ID == "bin_tproxy" {
			hasTproxy = true
		}
	}
	if hasTelemt {
		t.Fatalf("expected cdn_http preflight to omit bin_telemt")
	}
	if !hasTproxy {
		t.Fatalf("expected cdn_http preflight to include bin_tproxy")
	}

	// 2. Cloud KeenDNS BaseFQDN triggers direct_host_keendns_cloud BLOCKED for direct_fake_tls & dual
	kdCloud := &mockKeenDNSOperator{
		disc: serveringress.KeenDNSDiscovery{
			State:      "supported",
			BaseFQDN:   "dacha.example.keenetic.pro",
			AccessMode: "cloud",
		},
	}
	engine.SetKeenDNS(kdCloud)

	for _, tc := range []struct {
		scenario   string
		directHost string
		pubDomain  string
		cdnProfile string
	}{
		{
			scenario:   "direct_fake_tls",
			directHost: "dacha.example.keenetic.pro",
			cdnProfile: "direct",
		},
		{
			scenario:   "dual",
			directHost: "connect.dacha.example.keenetic.pro",
			pubDomain:  "connect.dacha.example.keenetic.pro",
			cdnProfile: "cdn_get",
		},
	} {
		resp := engine.Run(ctx, WizardPlanRequest{
			Kind:         "tgwebproxy",
			Scenario:     tc.scenario,
			DirectHost:   tc.directHost,
			DirectPort:   8443,
			ListenPort:   8085,
			PublicDomain: tc.pubDomain,
			TlsDomain:    "www.google.com",
			CdnProfileID: tc.cdnProfile,
		})
		if resp.CanProceed {
			t.Fatalf("expected CanProceed=false for %s (%s), got %+v", tc.scenario, tc.directHost, resp)
		}
		foundBlocked := false
		for _, c := range resp.Checks {
			if c.ID == "direct_host_keendns_cloud" && c.Status == CheckStatusBlocked {
				foundBlocked = true
			}
		}
		if !foundBlocked {
			t.Fatalf("expected direct_host_keendns_cloud blocked for %s (%s), got %+v", tc.scenario, tc.directHost, resp.Checks)
		}
	}

	// 3. cdn_http does NOT trigger direct_host_keendns_cloud even with leftover DirectHost
	respCDNLeftover := engine.Run(ctx, WizardPlanRequest{
		Kind:         "tgwebproxy",
		Scenario:     "cdn_http",
		DirectHost:   "connect.dacha.example.keenetic.pro",
		PublicDomain: "connect.dacha.example.keenetic.pro",
		ListenPort:   8085,
		CdnProfileID: "cdn_get",
	})
	for _, c := range respCDNLeftover.Checks {
		if c.ID == "direct_host_keendns_cloud" {
			t.Fatalf("unexpected direct_host_keendns_cloud check for cdn_http: %+v", c)
		}
	}

	// 4. Direct-mode KeenDNS does NOT trigger direct_host_keendns_cloud
	engine.SetKeenDNS(&mockKeenDNSOperator{
		disc: serveringress.KeenDNSDiscovery{
			State:      "supported",
			BaseFQDN:   "router.example.keenetic.pro",
			AccessMode: "direct",
		},
	})
	respDirectMode := engine.Run(ctx, WizardPlanRequest{
		Kind:         "tgwebproxy",
		Scenario:     "direct_fake_tls",
		DirectHost:   "router.example.keenetic.pro",
		DirectPort:   8443,
		TlsDomain:    "www.google.com",
		CdnProfileID: "direct",
	})
	for _, c := range respDirectMode.Checks {
		if c.ID == "direct_host_keendns_cloud" {
			t.Fatalf("unexpected direct_host_keendns_cloud check for direct KeenDNS: %+v", c)
		}
	}

	// 5. Private/CGNAT WAN emits wan_cgnat_direct_mtproto warning for direct_fake_tls & dual, never for cdn_http
	engine.SetKeenDNS(&mockKeenDNSOperator{
		disc: serveringress.KeenDNSDiscovery{
			State:      "supported",
			BaseFQDN:   "dacha.example.keenetic.pro",
			AccessMode: "cloud",
			WANAddress: "10.20.30.40",
			WANPrivate: true,
		},
	})
	for _, tc := range []struct {
		scenario   string
		directHost string
		pubDomain  string
		cdnProfile string
		wantWarn   bool
	}{
		{"direct_fake_tls", "vps.example.com", "", "direct", true},
		{"dual", "vps.example.com", "connect.dacha.example.keenetic.pro", "cdn_get", true},
		{"cdn_http", "", "connect.dacha.example.keenetic.pro", "cdn_get", false},
	} {
		resp := engine.Run(ctx, WizardPlanRequest{
			Kind:         "tgwebproxy",
			Scenario:     tc.scenario,
			DirectHost:   tc.directHost,
			DirectPort:   8443,
			ListenPort:   8085,
			PublicDomain: tc.pubDomain,
			TlsDomain:    "www.google.com",
			CdnProfileID: tc.cdnProfile,
		})
		foundWanWarn := false
		for _, c := range resp.Checks {
			if c.ID == "wan_cgnat_direct_mtproto" && c.Status == CheckStatusWarning {
				foundWanWarn = true
			}
		}
		if foundWanWarn != tc.wantWarn {
			t.Fatalf("scenario=%s: foundWanWarn=%v, want %v; checks=%+v", tc.scenario, foundWanWarn, tc.wantWarn, resp.Checks)
		}
	}
}

func TestPreflightEngine_InvalidDirectPortSkipsDuplicateConflict(t *testing.T) {
	pe := NewPreflightEngine(nil, nil, nil, nil, nil)
	resp := pe.Run(context.Background(), WizardPlanRequest{
		Kind:         "tgwebproxy",
		Scenario:     "direct_fake_tls",
		DirectHost:   "203.0.113.10",
		DirectPort:   2222,
		TlsDomain:    "www.google.com",
		CdnProfileID: "direct",
	})
	if resp.CanProceed {
		t.Fatalf("expected CanProceed=false for reserved port 2222, checks=%+v", resp.Checks)
	}
	var sawValidBlocked, sawDuplicatePortCheck bool
	for _, c := range resp.Checks {
		if c.ID == "port_direct_valid" && c.Status == CheckStatusBlocked {
			sawValidBlocked = true
		}
		if c.ID == "port_direct" || c.ID == "port_direct_conflict" {
			sawDuplicatePortCheck = true
		}
	}
	if !sawValidBlocked {
		t.Fatalf("expected port_direct_valid blocked check, got %+v", resp.Checks)
	}
	if sawDuplicatePortCheck {
		t.Fatalf("did not expect duplicate port_direct/port_direct_conflict check when ValidateDirectPort fails: %+v", resp.Checks)
	}
}
