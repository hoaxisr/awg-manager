package vlessserver

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/realityserver"
	"github.com/hoaxisr/awg-manager/internal/xhttpserver"
	"github.com/xtls/reality"
)

func preseedRealityRecordCache(t *testing.T, dest, sni string) {
	t.Helper()
	for _, alpn := range []string{"0", "1", "2"} {
		k := dest + " " + sni + " " + alpn
		reality.GlobalPostHandshakeRecordsLens.Store(k, []int{})
		t.Cleanup(func() {
			reality.GlobalPostHandshakeRecordsLens.Delete(k)
		})
	}
}

func newRealityTestService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	s := New(dir, nil)
	s.realityFirewall = nil // never touch iptables from tests
	s.SetTestRuntimeHook(func(cfg xhttpserver.Config) error { return nil })
	s.SetRealityNetworkInfoProvider(func(ctx context.Context) RealityNetworkInfo {
		return RealityNetworkInfo{PublicHost: "203.0.113.10", ExternalIP: "203.0.113.10", WANAddress: "203.0.113.10", WhiteIP: true}
	})
	return s
}

func TestUpdateRealityGeneratesKeysAndPersists(t *testing.T) {
	s := newRealityTestService(t)
	port := 20443
	tr := "xhttp"
	dest := "www.example.com"
	st, err := s.UpdateReality(RealityUpdate{Port: &port, Transport: &tr, Dest: &dest})
	if err != nil {
		t.Fatalf("UpdateReality: %v", err)
	}
	if st.Settings.PrivateKey != "" {
		t.Fatal("private key must be redacted in API state")
	}
	if st.Settings.PublicKey == "" || len(st.Settings.ShortIDs) != 1 || st.Settings.XHTTPPath == "" {
		t.Fatalf("material not generated: %+v", st.Settings)
	}
	if st.Settings.Dest != "www.example.com:443" || st.Settings.ServerNames[0] != "www.example.com" {
		t.Fatalf("dest/sni not normalized: %+v", st.Settings)
	}

	data, err := os.ReadFile(filepath.Join(s.DataDir(), "xray", "reality-settings.json"))
	if err != nil {
		t.Fatalf("settings not persisted: %v", err)
	}
	var disk RealitySettings
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if disk.PrivateKey == "" {
		t.Fatal("private key must be persisted on disk")
	}
	if pub, _ := realityserver.PublicKeyFromPrivate(disk.PrivateKey); pub != st.Settings.PublicKey {
		t.Fatal("public key does not match persisted private key")
	}

	// Reload from disk keeps the same keys.
	s2 := New(s.DataDir(), nil)
	s2.realityFirewall = nil
	if got := s2.GetRealityState(context.Background(), false).Settings.PublicKey; got != st.Settings.PublicKey {
		t.Fatalf("reloaded public key %q != %q", got, st.Settings.PublicKey)
	}

	// Regeneration rotates keys.
	st2, err := s.UpdateReality(RealityUpdate{RegenerateKeys: true})
	if err != nil {
		t.Fatal(err)
	}
	if st2.Settings.PublicKey == st.Settings.PublicKey {
		t.Fatal("keys were not regenerated")
	}
}

func TestUpdateRealityRejectsReservedPorts(t *testing.T) {
	s := newRealityTestService(t)
	for _, p := range []int{443, 1099, 8443, 9008, 9009, 51820, 80, 0, 70000} {
		port := p
		if _, err := s.UpdateReality(RealityUpdate{Port: &port}); err == nil {
			t.Errorf("port %d must be rejected", p)
		}
	}
	bad := "grpc"
	if _, err := s.UpdateReality(RealityUpdate{Transport: &bad}); err == nil {
		t.Error("unsupported transport must be rejected")
	}
}

func TestRealitySettingsSurviveCDNConfigUpdate(t *testing.T) {
	s := newRealityTestService(t)
	port := 2083
	if _, err := s.UpdateReality(RealityUpdate{Port: &port}); err != nil {
		t.Fatal(err)
	}
	before := s.GetRealityState(context.Background(), false).Settings.PublicKey

	cfg := s.GetConfig()
	cfg.PublicDomain = "cdn.example.com"
	cfg.Enabled = false
	if err := s.UpdateConfig(cfg); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	after := s.GetRealityState(context.Background(), false)
	if after.Settings.PublicKey != before || after.Settings.Port != 2083 {
		t.Fatalf("CDN config update must not touch REALITY settings: %+v", after.Settings)
	}
}

func TestGenerateRealityLinksViaEndpointID(t *testing.T) {
	s := newRealityTestService(t)
	c, err := s.AddClient("Phone")
	if err != nil {
		t.Fatalf("AddClient: %v", err)
	}
	if _, err := s.GenerateLinksForEndpoint(c.ID, RealityEndpointID, ""); err == nil {
		t.Fatal("expected error before REALITY is initialised")
	}
	port := 2053
	if _, err := s.UpdateReality(RealityUpdate{Port: &port}); err != nil {
		t.Fatal(err)
	}
	links, err := s.GenerateLinksForEndpoint(c.ID, RealityEndpointID, "")
	if err != nil {
		t.Fatalf("GenerateLinksForEndpoint(reality): %v", err)
	}
	if !strings.HasPrefix(links.VlessURL, "vless://"+c.ID+"@203.0.113.10:2053?") {
		t.Fatalf("unexpected url %s", links.VlessURL)
	}
	u, err := url.Parse(links.VlessURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	st := s.GetRealityState(context.Background(), false).Settings
	if q.Get("security") != "reality" || q.Get("type") != "tcp" || q.Get("pbk") != st.PublicKey || q.Get("sid") != st.ShortIDs[0] || q.Get("sni") == "" || q.Get("fp") != "chrome" {
		t.Fatalf("bad query: %v", q)
	}

	// Manual public host override wins over detection.
	host := "home.example.net"
	if _, err := s.UpdateReality(RealityUpdate{PublicHost: &host}); err != nil {
		t.Fatal(err)
	}
	links, err = s.GenerateRealityLinks(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(links.VlessURL, "@home.example.net:2053?") {
		t.Fatalf("override not applied: %s", links.VlessURL)
	}
}

func TestBuildRealityShareLinksFormats(t *testing.T) {
	st := RealitySettings{
		Port: 2053, Transport: "tcp", Dest: "www.microsoft.com:443",
		ServerNames: []string{"www.microsoft.com"}, PublicKey: "PUBKEY", ShortIDs: []string{"0123456789abcdef"},
		Fingerprint: "chrome",
	}
	cl := Client{ID: "b831381d-6324-4d53-ad4f-8cda48b30811", Remark: "Phone"}
	links, err := BuildRealityShareLinks(st, cl, "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	if links.SingboxJSON == "" || !strings.Contains(links.SingboxJSON, `"reality"`) {
		t.Fatalf("sing-box outbound missing reality: %s", links.SingboxJSON)
	}
	if !strings.Contains(links.MihomoYAML, "reality-opts:") || !strings.Contains(links.MihomoYAML, "network: tcp") {
		t.Fatalf("mihomo yaml: %s", links.MihomoYAML)
	}
	var happ map[string]any
	if err := json.Unmarshal([]byte(links.HappJSON), &happ); err != nil {
		t.Fatalf("happ json invalid: %v", err)
	}

	st.Transport = "xhttp"
	st.XHTTPPath = "/abc"
	links, err = BuildRealityShareLinks(st, cl, "2001:db8::1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(links.VlessURL, "@[2001:db8::1]:2053?") || !strings.Contains(links.VlessURL, "type=xhttp") || !strings.Contains(links.VlessURL, "mode=stream-one") {
		t.Fatalf("xhttp url: %s", links.VlessURL)
	}
	if links.SingboxJSON != "" {
		t.Fatal("sing-box has no XHTTP transport; outbound must be omitted")
	}
	if !strings.Contains(links.MihomoYAML, "network: xhttp") {
		t.Fatalf("mihomo xhttp: %s", links.MihomoYAML)
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("alloc free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func TestUpdateRealityReset(t *testing.T) {
	preseedRealityRecordCache(t, "dest.test:443", "dest.test")
	s := newRealityTestService(t)
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	if _, err := s.AddClient("Phone"); err != nil {
		t.Fatalf("AddClient: %v", err)
	}
	cfg := s.GetConfig()
	cfg.Enabled = true
	cfg.ListenPort = freeTCPPort(t)
	if err := s.UpdateConfig(cfg); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	port := freeTCPPort(t)
	enabled := true
	dest := "dest.test:443"
	st, err := s.UpdateReality(RealityUpdate{Enabled: &enabled, Port: &port, Dest: &dest})
	if err != nil {
		t.Fatalf("UpdateReality enable: %v", err)
	}
	if !st.Settings.Enabled || st.Settings.PublicKey == "" || len(st.Settings.ShortIDs) == 0 || !st.Runtime.Running {
		t.Fatalf("expected active running reality, got %+v", st)
	}

	resetSt, err := s.UpdateReality(RealityUpdate{Reset: true})
	if err != nil {
		t.Fatalf("UpdateReality reset: %v", err)
	}
	if resetSt.Settings.Enabled || resetSt.Settings.PublicKey != "" || len(resetSt.Settings.ShortIDs) != 0 {
		t.Fatalf("expected reset settings to be cleared, got %+v", resetSt.Settings)
	}
	if resetSt.Runtime.Running {
		t.Fatal("expected reality runtime to stop on reset")
	}

	data, err := os.ReadFile(filepath.Join(s.DataDir(), "xray", "reality-settings.json"))
	if err != nil {
		t.Fatalf("read persisted settings: %v", err)
	}
	var disk RealitySettings
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if disk.Enabled || disk.PrivateKey != "" || disk.PublicKey != "" || len(disk.ShortIDs) != 0 {
		t.Fatalf("expected reset settings persisted on disk, got %+v", disk)
	}
}
