package config

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
	"time"
)

const testObfKey = "0000000000000000000000000000000000000000000000000000000000000000"

func validClientArgs() []string {
	return []string{
		"-peer", "1.2.3.4:5000",
		"-link", "https://vk.ru/call/join/abcdef",
	}
}

func TestParseClient_Defaults(t *testing.T) {
	c, err := ParseClient(validClientArgs(), io.Discard)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if c.Provider.Name != ProviderVK {
		t.Errorf("Provider.Name default: %q (expected %q)", c.Provider.Name, ProviderVK)
	}
	if c.Proxy.Listen != "127.0.0.1:9000" {
		t.Errorf("Proxy.Listen default: %q", c.Proxy.Listen)
	}
	if c.TURN.N != 10 {
		t.Errorf("TURN.N default: %d", c.TURN.N)
	}
	if c.DNS.Mode != "auto" {
		t.Errorf("DNS.Mode default: %q", c.DNS.Mode)
	}
	if c.VK.StreamsPerCred != defaultStreamsPerCache {
		t.Errorf("VK.StreamsPerCred default: %d", c.VK.StreamsPerCred)
	}
	if len(c.VK.Links) != 1 || c.VK.Links[0] != "abcdef" {
		t.Errorf("VK.Links: %v (expected [abcdef])", c.VK.Links)
	}
	if c.Obf.Key != nil {
		t.Errorf("Obf.Key should be nil when -obf-profile absent")
	}
	if c.Obf.Profile != ObfProfileNone {
		t.Errorf("Obf.Profile default: %q (expected %q)", c.Obf.Profile, ObfProfileNone)
	}
	if c.Obf.Enabled() {
		t.Errorf("Obf.Enabled() should be false when profile=none")
	}
	if c.Proxy.Mode != ProxyModeUDP {
		t.Errorf("Proxy.Mode default: %q (expected udp)", c.Proxy.Mode)
	}
}

func TestParseClient_VKLinkStrip(t *testing.T) {
	args := []string{
		"-peer", "1.2.3.4:5000",
		"-link", "https://vk.ru/call/join/CODE123?foo=bar",
	}
	c, err := ParseClient(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.VK.Links) != 1 || c.VK.Links[0] != "CODE123" {
		t.Errorf("VK.Links: %v (expected [CODE123])", c.VK.Links)
	}
}

func TestParseClient_MissingPeer(t *testing.T) {
	_, err := ParseClient([]string{"-link", "https://vk.ru/call/join/X"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "peer") {
		t.Errorf("expected peer error, got %v", err)
	}
}

func TestParseClient_MultiLinks(t *testing.T) {
	args := []string{
		"-peer", "1.2.3.4:5000",
		"-links", "https://vk.ru/call/join/LINK1,https://vk.ru/call/join/LINK2?foo=bar,https://vk.ru/call/join/LINK3/extra",
	}
	c, err := ParseClient(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.VK.Links) != 3 {
		t.Fatalf("expected 3 links, got %d: %v", len(c.VK.Links), c.VK.Links)
	}
	if c.VK.Links[0] != "LINK1" {
		t.Errorf("VK.Links[0]: %q (expected LINK1)", c.VK.Links[0])
	}
	if c.VK.Links[1] != "LINK2" {
		t.Errorf("VK.Links[1]: %q (expected LINK2)", c.VK.Links[1])
	}
	if c.VK.Links[2] != "LINK3" {
		t.Errorf("VK.Links[2]: %q (expected LINK3)", c.VK.Links[2])
	}
}

func TestParseClient_MissingVKLink(t *testing.T) {
	_, err := ParseClient([]string{"-peer", "1.2.3.4:5000"}, io.Discard)
	if err == nil || (!strings.Contains(err.Error(), "-links") && !strings.Contains(err.Error(), "-link")) {
		t.Errorf("expected vk-link error, got %v", err)
	}
}

func TestParseClient_InvalidDNS(t *testing.T) {
	args := append(validClientArgs(), "-dns-mode", "garbage")
	_, err := ParseClient(args, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invalid -dns-mode") {
		t.Errorf("expected dns error, got %v", err)
	}
}

func TestParseClient_BrowserSafari(t *testing.T) {
	args := append(validClientArgs(), "-browser", "safari")
	c, err := ParseClient(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.VK.Browser != BrowserSafari {
		t.Errorf("VK.Browser = %q, want %q", c.VK.Browser, BrowserSafari)
	}
}

func TestParseClient_BrowserInvalid(t *testing.T) {
	args := append(validClientArgs(), "-browser", "opera")
	_, err := ParseClient(args, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invalid -browser") {
		t.Errorf("expected browser error, got %v", err)
	}
}

func TestParseClient_PlatformDefaultDesktop(t *testing.T) {
	c, err := ParseClient(validClientArgs(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.VK.Platform != PlatformDesktop {
		t.Errorf("VK.Platform = %q, want %q", c.VK.Platform, PlatformDesktop)
	}
}

func TestParseClient_PlatformMobile(t *testing.T) {
	args := append(validClientArgs(), "-platform", "mobile")
	c, err := ParseClient(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.VK.Platform != PlatformMobile {
		t.Errorf("VK.Platform = %q, want %q", c.VK.Platform, PlatformMobile)
	}
}

func TestParseClient_PlatformInvalid(t *testing.T) {
	args := append(validClientArgs(), "-platform", "tablet")
	_, err := ParseClient(args, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invalid -platform") {
		t.Errorf("expected platform error, got %v", err)
	}
}

func TestParseClient_BondWithoutTCPMode(t *testing.T) {
	args := append(validClientArgs(), "-bond")
	_, err := ParseClient(args, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "-bond requires -mode tcp") {
		t.Errorf("expected bond error, got %v", err)
	}
}

func TestParseClient_ObfMissingKey(t *testing.T) {
	args := append(validClientArgs(), "-obf-profile", "rtpopus")
	_, err := ParseClient(args, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "-obf-key") {
		t.Errorf("expected obf-key error, got %v", err)
	}
}

func TestParseClient_ObfKeyOK(t *testing.T) {
	args := append(validClientArgs(), "-obf-profile", "rtpopus", "-obf-key", strings.Repeat("ab", 32))
	c, err := ParseClient(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Obf.Key) != 32 {
		t.Errorf("Obf.Key len: %d", len(c.Obf.Key))
	}
	if c.Obf.Profile != ObfProfileRTPOpus {
		t.Errorf("Obf.Profile: %q (expected %q)", c.Obf.Profile, ObfProfileRTPOpus)
	}
	if !c.Obf.Enabled() {
		t.Errorf("Obf.Enabled() should be true when profile=rtpopus")
	}
}

func TestParseClient_ObfProfileInvalid(t *testing.T) {
	args := append(validClientArgs(), "-obf-profile", "bogus")
	_, err := ParseClient(args, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invalid -obf-profile") {
		t.Errorf("expected invalid obf-profile error, got %v", err)
	}
}

func TestParseClient_ObfTimingRequiresObf(t *testing.T) {
	args := append(validClientArgs(), "-obf-timing", "20ms")
	_, err := ParseClient(args, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "-obf-timing") {
		t.Errorf("expected obf-timing error without profile, got %v", err)
	}
}

func TestParseClient_ObfTimingRejectsTCP(t *testing.T) {
	args := append(validClientArgs(), "-mode", "tcp", "-obf-profile", "rtpopus3", "-obf-key", testObfKey, "-obf-timing", "20ms")
	_, err := ParseClient(args, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "-obf-timing") {
		t.Errorf("expected obf-timing/mode error, got %v", err)
	}
}

func TestParseClient_ObfTimingValid(t *testing.T) {
	args := append(validClientArgs(), "-obf-profile", "rtpopus3", "-obf-key", testObfKey, "-obf-timing", "20ms")
	c, err := ParseClient(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Obf.Timing != 20*time.Millisecond {
		t.Errorf("Obf.Timing = %v, want 20ms", c.Obf.Timing)
	}
}

func TestParseServer_ObfTimingRejectsTCP(t *testing.T) {
	args := []string{"-connect", "127.0.0.1:51820", "-mode", "tcp", "-obf-profile", "rtpopus3", "-obf-key", testObfKey, "-obf-timing", "10ms"}
	_, err := ParseServer(args, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "-obf-timing") {
		t.Errorf("expected obf-timing/mode error, got %v", err)
	}
}

func TestParseClient_StreamsPerCredNonPositive(t *testing.T) {
	args := append(validClientArgs(), "-streams-per-cred", "0")
	_, err := ParseClient(args, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "streams-per-cred") {
		t.Errorf("expected streams-per-cred error, got %v", err)
	}
}

func TestParseClient_NClampedToTen(t *testing.T) {
	args := append(validClientArgs(), "-n", "-5")
	c, err := ParseClient(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.TURN.N != 10 {
		t.Errorf("TURN.N: %d (expected 10)", c.TURN.N)
	}
}

func TestParseClient_ProviderUnknown(t *testing.T) {
	args := []string{"-peer", "1.2.3.4:5000", "-provider", "bogus"}
	_, err := ParseClient(args, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invalid -provider") {
		t.Errorf("expected invalid provider error, got %v", err)
	}
}

func TestParseClient_DNSServersSplit(t *testing.T) {
	args := append(validClientArgs(), "-dns-servers", "1.1.1.1,8.8.8.8:53")
	c, err := ParseClient(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.DNS.Servers) != 2 || c.DNS.Servers[0] != "1.1.1.1" || c.DNS.Servers[1] != "8.8.8.8:53" {
		t.Errorf("DNS.Servers: %v", c.DNS.Servers)
	}
}

func TestParseClient_GenObfKeySkipsPeerCheck(t *testing.T) {
	c, err := ParseClient([]string{"-gen-obf-key"}, io.Discard)
	if err != nil {
		t.Fatalf("gen-obf-key should not require peer/vk-link: %v", err)
	}
	if !c.Obf.GenKey {
		t.Errorf("Obf.GenKey not set")
	}
}

func TestParseClient_HelpReturnsErrHelp(t *testing.T) {
	var buf bytes.Buffer
	_, err := ParseClient([]string{"-h"}, &buf)
	if !errors.Is(err, flag.ErrHelp) {
		t.Errorf("expected flag.ErrHelp, got %v", err)
	}
}

func TestParseClient_ProxyMode(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want ProxyMode
	}{
		{"default-udp", nil, ProxyModeUDP},
		{"tcp", []string{"-mode", "tcp"}, ProxyModeTCPFwd},
		{"tcp-bond", []string{"-mode", "tcp", "-bond"}, ProxyModeTCPFwdBond},
		{"default-udp-no-bond", nil, ProxyModeUDP},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append(validClientArgs(), tc.args...)
			c, err := ParseClient(args, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if c.Proxy.Mode != tc.want {
				t.Errorf("Proxy.Mode = %q, want %q", c.Proxy.Mode, tc.want)
			}
		})
	}
}

func TestParseServer_Defaults(t *testing.T) {
	s, err := ParseServer([]string{"-connect", "backend:1234"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if s.Proxy.Listen != "0.0.0.0:56000" {
		t.Errorf("Proxy.Listen default: %q", s.Proxy.Listen)
	}
	if s.Proxy.Connect != "backend:1234" {
		t.Errorf("Proxy.Connect: %q", s.Proxy.Connect)
	}
	if s.Proxy.Mode != ProxyModeUDP {
		t.Errorf("Proxy.Mode default: %q", s.Proxy.Mode)
	}
}

func TestParseServer_MissingConnect(t *testing.T) {
	_, err := ParseServer(nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "server address") {
		t.Errorf("expected connect error, got %v", err)
	}
}

func TestParseServer_ObfMissingKey(t *testing.T) {
	_, err := ParseServer([]string{"-connect", "x:1", "-obf-profile", "rtpopus"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "-obf-key") {
		t.Errorf("expected obf-key error, got %v", err)
	}
}

func TestParseServer_ObfKeyBadHex(t *testing.T) {
	_, err := ParseServer([]string{"-connect", "x:1", "-obf-profile", "rtpopus", "-obf-key", "zz"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invalid hex") {
		t.Errorf("expected hex error, got %v", err)
	}
}

func TestParseServer_ObfKeyOK(t *testing.T) {
	s, err := ParseServer([]string{"-connect", "x:1", "-obf-profile", "rtpopus", "-obf-key", strings.Repeat("cd", 32)}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Obf.Key) != 32 {
		t.Errorf("Obf.Key len: %d", len(s.Obf.Key))
	}
	if s.Obf.Profile != ObfProfileRTPOpus {
		t.Errorf("Obf.Profile: %q (expected %q)", s.Obf.Profile, ObfProfileRTPOpus)
	}
}

func TestParseServer_ObfProfileInvalid(t *testing.T) {
	_, err := ParseServer([]string{"-connect", "x:1", "-obf-profile", "bogus"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invalid -obf-profile") {
		t.Errorf("expected invalid obf-profile error, got %v", err)
	}
}

func TestParseServer_GenObfKeySkipsConnectCheck(t *testing.T) {
	s, err := ParseServer([]string{"-gen-obf-key"}, io.Discard)
	if err != nil {
		t.Fatalf("gen-obf-key should not require -connect: %v", err)
	}
	if !s.Obf.GenKey {
		t.Errorf("Obf.GenKey not set")
	}
}

func TestParseServer_ProxyMode(t *testing.T) {
	s, err := ParseServer([]string{"-connect", "x:1", "-mode", "tcp"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if s.Proxy.Mode != ProxyModeTCPFwd {
		t.Errorf("Proxy.Mode = %q, want tcpfwd", s.Proxy.Mode)
	}
}
