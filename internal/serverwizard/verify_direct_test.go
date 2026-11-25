package serverwizard

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/serveringress"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
)

func verifyTgChecks(t *testing.T, svc *WizardService) map[string]VerificationCheck {
	t.Helper()
	svc.SetDialTimeout(func(string, string, time.Duration) (net.Conn, error) { return &mockTestConn{}, nil })
	rep, err := svc.Verify(context.Background(), "tgwebproxy")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]VerificationCheck{}
	for _, c := range rep.Checks {
		out[c.ID] = c
	}
	return out
}

// Мутации: проверять порт и без прямого адреса; не дописывать ошибку файрвола →
// красный.
func TestVerify_WANFirewallFollowsDirectHost(t *testing.T) {
	svc, _, _, tg, _ := setupTestWizard(t)
	svc.SetDirectWANFirewallChecker(func(int) (tgwebproxy.DirectWANFirewallState, error) {
		return tgwebproxy.DirectWANFirewallClosed, nil
	})
	tg.cfg = tgwebproxy.PublicConfig{Enabled: true, Scenario: "direct_fake_tls", DirectPort: 8443, TlsDomain: "www.google.com"}
	tg.st = tgwebproxy.Status{DirectOnline: true, DirectPort: 8443, DirectWANError: "xtables lock"}
	svc.SetKeenDNS(&mockKeenDNSOperator{disc: serveringress.KeenDNSDiscovery{State: "supported", WANAddress: "10.1.1.1", WANPrivate: true}})

	checks := verifyTgChecks(t, svc)
	if c := checks["tg_mtproto_wan_firewall"]; c.Status != CheckStatusNotApplicable || !strings.Contains(c.Message, "прямой адрес не задан") {
		t.Fatalf("без адреса: %+v", c)
	}
	if c, ok := checks["tg_mtproto_wan_ip"]; ok {
		t.Fatalf("порт не открывается, а про WAN предупреждаем: %+v", c)
	}
	tg.cfg.DirectHost = "203.0.113.10"
	checks = verifyTgChecks(t, svc)
	if c := checks["tg_mtproto_wan_firewall"]; c.Status != CheckStatusWarning || !strings.Contains(c.Message, "xtables lock") {
		t.Fatalf("с адресом: %+v", c)
	}
}

// Мутации: не сравнивать A с WAN; сравнивать при сером WAN; не смотреть AAAA;
// блокировать вместо предупреждения → красный.
func TestVerify_DirectHostAddressesAgainstWAN(t *testing.T) {
	svc, _, _, tg, _ := setupTestWizard(t)
	tg.cfg = tgwebproxy.PublicConfig{Enabled: true, Scenario: "direct_fake_tls", DirectHost: "tg.example.com", DirectPort: 8443, TlsDomain: "www.google.com"}
	tg.st = tgwebproxy.Status{DirectOnline: true, DirectPort: 8443}

	for _, tc := range []struct {
		name, host, wan string
		private         bool
		addrs           []string
		wantWAN, wantV6 bool
	}{
		{name: "совпало", wan: "198.51.100.10", addrs: []string{"198.51.100.10"}},
		{name: "совпало вторым", wan: "198.51.100.10", addrs: []string{"192.0.2.1", "198.51.100.10"}},
		{name: "не совпало", wan: "198.51.100.10", addrs: []string{"192.0.2.1"}, wantWAN: true},
		{name: "есть AAAA", wan: "198.51.100.10", addrs: []string{"198.51.100.10", "2001:db8::1"}, wantV6: true},
		{name: "только AAAA", wan: "198.51.100.10", addrs: []string{"2001:db8::1"}, wantV6: true},
		{name: "WAN неизвестен", addrs: []string{"192.0.2.1"}},
		{name: "WAN серый", wan: "10.1.1.1", private: true, addrs: []string{"192.0.2.1"}},
		{name: "имя не разрешилось", wan: "198.51.100.10"},
		{name: "адрес вместо имени", host: "2001:db8::1", wan: "198.51.100.10", wantV6: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tg.cfg.DirectHost = "tg.example.com"
			if tc.host != "" {
				tg.cfg.DirectHost = tc.host
			}
			svc.SetKeenDNS(&mockKeenDNSOperator{disc: serveringress.KeenDNSDiscovery{State: "supported", WANAddress: tc.wan, WANPrivate: tc.private}})
			svc.SetLookupHost(func(context.Context, string) ([]string, error) { return tc.addrs, nil })
			checks := verifyTgChecks(t, svc)
			for id, want := range map[string]bool{"tg_mtproto_dns_wan": tc.wantWAN, "tg_mtproto_dns_ipv6": tc.wantV6} {
				c, ok := checks[id]
				if ok != want {
					t.Fatalf("%s: есть=%v, ждали %v (%+v)", id, ok, want, c)
				}
				if ok && c.Status != CheckStatusWarning {
					t.Fatalf("%s: статус %s", id, c.Status)
				}
			}
			if c := checks["tg_mtproto_dns_wan"]; tc.wantWAN && (!strings.Contains(c.Message, "192.0.2.1") || !strings.Contains(c.Message, tc.wan)) {
				t.Fatalf("сообщение не называет адреса: %s", c.Message)
			}
		})
	}
}
