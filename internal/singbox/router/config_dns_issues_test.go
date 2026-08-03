package router

import (
	"strings"
	"testing"
)

func dialIssueMessages(cfg *RouterConfig) []string {
	var out []string
	for _, i := range computeDNSDialIssues(cfg) {
		out = append(out, i.Message)
	}
	return out
}

func requireIssueContaining(t *testing.T, msgs []string, substr string) {
	t.Helper()
	for _, m := range msgs {
		if strings.Contains(m, substr) {
			return
		}
	}
	t.Fatalf("no issue containing %q, got %v", substr, msgs)
}

// Домен без резолвера НЕ флагается: merged config.d всегда несёт
// route.default_domain_resolver из 00-base.json, sing-box валидирует
// merged-конфиг — предупреждение было бы вечным ложным срабатыванием.
func TestDNSDialIssues_DomainWithoutResolverIsNotFlagged(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.DNS.Servers = []DNSServer{
		{Tag: "dot", Type: "tls", Server: "common.dot.dns.yandex.net"},
		{Tag: "direct", Type: "udp", Server: "77.88.8.8"},
	}
	if msgs := dialIssueMessages(cfg); len(msgs) != 0 {
		t.Fatalf("no warnings expected, got %v", msgs)
	}
}

// Ссылки резолверов на неизвестные слоту теги НЕ флагаются: они резолвятся
// кросс-слотово (90-user, 00-base) и валидируются оркестратором при reload.
func TestDNSDialIssues_CrossSlotResolverRefsAreNotFlagged(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.DNS.Servers = []DNSServer{
		{Tag: "dot", Type: "tls", Server: "dns.example.com", DomainResolver: &DomainResolver{Server: "dns-bootstrap"}},
	}
	cfg.Route.DefaultDomainResolver = &DomainResolver{Server: "dns-bootstrap"}
	if msgs := dialIssueMessages(cfg); len(msgs) != 0 {
		t.Fatalf("no warnings expected, got %v", msgs)
	}
}

// detour + domain_resolver: dial-поля при detour игнорируются. Ровно одно
// предупреждение, IP-сервер не задет.
func TestDNSDialIssues_DetourIgnoresDialFields(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.DNS.Servers = []DNSServer{
		{Tag: "remote", Type: "udp", Server: "1.1.1.1", Detour: "vpn", DomainResolver: &DomainResolver{Server: "direct"}},
		{Tag: "direct", Type: "udp", Server: "77.88.8.8"},
	}
	msgs := dialIssueMessages(cfg)
	if len(msgs) != 1 {
		t.Fatalf("want exactly 1 issue, got %v", msgs)
	}
	requireIssueContaining(t, msgs, "при заданном detour остальные dial-поля игнорируются")
}

// Доменный сервер с detour и без резолвера — НЕ предупреждение: адрес уедет
// на удалённое разрешение через detour-outbound. Закрепляем исключение.
func TestDNSDialIssues_DetouredDomainServerNeedsNoResolver(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.DNS.Servers = []DNSServer{
		{Tag: "dot", Type: "tls", Server: "dot.example.com", Detour: "vpn"},
		{Tag: "direct", Type: "udp", Server: "77.88.8.8"},
	}
	if msgs := dialIssueMessages(cfg); len(msgs) != 0 {
		t.Fatalf("no warnings expected, got %v", msgs)
	}
}

// Цикл резолверов: репортится ровно ОДИН раз на цикл, а не по разу на
// каждого участника.
func TestDNSDialIssues_ResolverCycleReportedOnce(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.DNS.Servers = []DNSServer{
		{Tag: "dot", Type: "tls", Server: "dot.example.com", DomainResolver: &DomainResolver{Server: "doh"}},
		{Tag: "doh", Type: "https", Server: "doh.example.com", DomainResolver: &DomainResolver{Server: "dot"}},
	}
	msgs := dialIssueMessages(cfg)
	if len(msgs) != 1 {
		t.Fatalf("want exactly 1 cycle issue, got %v", msgs)
	}
	requireIssueContaining(t, msgs, "цикл domain_resolver")
}

// Сервер, чья цепочка лишь ВЕДЁТ в чужой цикл (a → b → c → b), — жертва:
// цикл репортится один раз участникам (b, c), без третьего сообщения для a.
func TestDNSDialIssues_CycleVictimNotBlamed(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.DNS.Servers = []DNSServer{
		{Tag: "a", Type: "tls", Server: "a.example.com", DomainResolver: &DomainResolver{Server: "b"}},
		{Tag: "b", Type: "tls", Server: "b.example.com", DomainResolver: &DomainResolver{Server: "c"}},
		{Tag: "c", Type: "tls", Server: "c.example.com", DomainResolver: &DomainResolver{Server: "b"}},
	}
	msgs := dialIssueMessages(cfg)
	if len(msgs) != 1 {
		t.Fatalf("want exactly 1 cycle issue, got %v", msgs)
	}
	requireIssueContaining(t, msgs, "b → c → b")
	for _, m := range msgs {
		if strings.Contains(m, "(a →") {
			t.Fatalf("victim 'a' must not be blamed for the cycle: %v", msgs)
		}
	}
}

// Самозацикливание и цикл через default_domain_resolver.
func TestDNSDialIssues_SelfAndDefaultResolverCycles(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.DNS.Servers = []DNSServer{
		{Tag: "dot", Type: "tls", Server: "dot.example.com", DomainResolver: &DomainResolver{Server: "dot"}},
		{Tag: "direct", Type: "udp", Server: "77.88.8.8"},
	}
	msgs := dialIssueMessages(cfg)
	if len(msgs) != 1 {
		t.Fatalf("self-cycle: want exactly 1 issue, got %v", msgs)
	}
	requireIssueContaining(t, msgs, "dot → dot")

	cfg.DNS.Servers = []DNSServer{
		{Tag: "dot", Type: "tls", Server: "dot.example.com"},
		{Tag: "direct", Type: "udp", Server: "77.88.8.8"},
	}
	cfg.Route.DefaultDomainResolver = &DomainResolver{Server: "dot"}
	msgs = dialIssueMessages(cfg)
	if len(msgs) != 1 {
		t.Fatalf("default-cycle: want exactly 1 issue, got %v", msgs)
	}
	requireIssueContaining(t, msgs, "цикл domain_resolver")
}

// Обрывы цепочки: IP-сервер, detour-сервер и кросс-слотовый тег — не циклы.
func TestDNSDialIssues_ChainTerminators(t *testing.T) {
	// IP-терминированная цепочка.
	cfg := NewEmptyConfig()
	cfg.DNS.Servers = []DNSServer{
		{Tag: "dot", Type: "tls", Server: "dot.example.com", DomainResolver: &DomainResolver{Server: "direct"}},
		{Tag: "direct", Type: "udp", Server: "77.88.8.8"},
	}
	if msgs := dialIssueMessages(cfg); len(msgs) != 0 {
		t.Fatalf("IP-terminated chain: no issues expected, got %v", msgs)
	}

	// Detour-узел обрывает цепочку — default, указывающий на него, не цикл.
	cfg = NewEmptyConfig()
	cfg.DNS.Servers = []DNSServer{
		{Tag: "dot", Type: "tls", Server: "dot.example.com", Detour: "vpn"},
		{Tag: "other", Type: "tls", Server: "other.example.com"},
	}
	cfg.Route.DefaultDomainResolver = &DomainResolver{Server: "dot"}
	if msgs := dialIssueMessages(cfg); len(msgs) != 0 {
		t.Fatalf("detour terminates chain: no issues expected, got %v", msgs)
	}

	// Кросс-слотовый резолвер (тег вне слота) обрывает цепочку.
	cfg = NewEmptyConfig()
	cfg.DNS.Servers = []DNSServer{
		{Tag: "dot", Type: "tls", Server: "dot.example.com", DomainResolver: &DomainResolver{Server: "dns-bootstrap"}},
	}
	if msgs := dialIssueMessages(cfg); len(msgs) != 0 {
		t.Fatalf("cross-slot ref terminates chain: no issues expected, got %v", msgs)
	}
}

// IP:port, IPv6 (в т.ч. с портом) и URL — не домены: ни предупреждений,
// ни участия в цепочках циклов.
func TestDNSServerAddrIsDomain(t *testing.T) {
	domains := []string{"dns.google", "common.dot.dns.yandex.net"}
	notDomains := []string{
		"", "8.8.8.8", "8.8.8.8:53", "[2a02:6b8::feed:ff]", "2a02:6b8::feed:ff",
		"[2a02:6b8::feed:ff]:853", "https://dns.google/dns-query", "1.1.1.1:853",
	}
	for _, d := range domains {
		if !dnsServerAddrIsDomain(d) {
			t.Errorf("%q must be a domain", d)
		}
	}
	for _, n := range notDomains {
		if dnsServerAddrIsDomain(n) {
			t.Errorf("%q must NOT be a domain", n)
		}
	}
}

// Дубликаты тегов — предупреждение (sing-box отвергнет конфиг), и walker
// циклов не маскируется last-wins картой.
func TestDNSDialIssues_DuplicateTags(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.DNS.Servers = []DNSServer{
		{Tag: "dot", Type: "tls", Server: "dot.example.com", DomainResolver: &DomainResolver{Server: "dot"}},
		{Tag: "dot", Type: "udp", Server: "8.8.8.8"},
	}
	msgs := dialIssueMessages(cfg)
	requireIssueContaining(t, msgs, `дублирующийся тег DNS-сервера "dot"`)
}

// computeIssues прокидывает dial-предупреждения в общий список.
func TestComputeIssues_IncludesDNSDialIssues(t *testing.T) {
	svc := &ServiceImpl{deps: Deps{}}
	cfg := NewEmptyConfig()
	cfg.DNS.Servers = []DNSServer{
		{Tag: "dot", Type: "tls", Server: "dot.example.com", DomainResolver: &DomainResolver{Server: "dot"}},
	}
	found := false
	for _, i := range svc.computeIssues(cfg) {
		if i.Kind == "dns-domain-resolver" {
			found = true
		}
	}
	if !found {
		t.Fatal("computeIssues must include dns-domain-resolver warnings")
	}
}

func TestComputeDNSChainIssues(t *testing.T) {
	mk := func(rules ...DNSRule) *RouterConfig {
		cfg := &RouterConfig{}
		cfg.DNS.Servers = []DNSServer{{Tag: "dns-direct", Type: "udp", Server: "1.1.1.1"}}
		cfg.DNS.Rules = rules
		return cfg
	}
	evalRD := DNSRule{Action: "evaluate", Server: "dns-direct", Tag: "rd"}
	mrRD := &DNSMatchResponse{Enabled: true, Tag: "rd"}
	respondRD := DNSRule{MatchResponse: mrRD, ResponseRcode: "NOERROR", Action: "respond"}

	t.Run("здоровая цепочка — без issues", func(t *testing.T) {
		cfg := mk(evalRD, respondRD)
		if got := computeDNSChainIssues(cfg); len(got) != 0 {
			t.Fatalf("unexpected issues: %+v", got)
		}
	})
	t.Run("evaluate с тегом без потребителя НИЖЕ", func(t *testing.T) {
		cfg := mk(evalRD)
		got := computeDNSChainIssues(cfg)
		if len(got) != 1 || got[0].RuleIndex != 0 || got[0].Kind != "dns-chain" || got[0].Tag != "rd" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("потребитель ВЫШЕ не гасит warning (позиционность)", func(t *testing.T) {
		// respondRD стоит выше — цепочка битая (это ловит validateDNSChain),
		// но issues считаются независимо: evaluate ниже остаётся без потребителя.
		cfg := mk(respondRD, evalRD)
		got := computeDNSChainIssues(cfg)
		if len(got) != 1 || got[0].RuleIndex != 1 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("анонимный match_response не потребляет тегированный evaluate", func(t *testing.T) {
		// Фикстура b1 на бинаре beta.1: WARN «evaluate tag is never referenced: rd».
		cfg := mk(
			evalRD,
			DNSRule{Action: "evaluate", Server: "dns-direct"},
			DNSRule{MatchResponse: &DNSMatchResponse{Enabled: true}, ResponseRcode: "NOERROR", Action: "respond"},
		)
		got := computeDNSChainIssues(cfg)
		if len(got) != 1 || got[0].RuleIndex != 0 || got[0].Tag != "rd" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("speculative без race выше", func(t *testing.T) {
		cfg := mk(DNSRule{Domain: []string{"x.com"}, Server: "dns-direct", Speculative: true})
		got := computeDNSChainIssues(cfg)
		if len(got) != 1 || got[0].RuleIndex != 0 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("speculative с race выше — без warning", func(t *testing.T) {
		cfg := mk(
			DNSRule{Action: "evaluate", Server: "dns-direct", Tag: "rd"},
			DNSRule{MatchResponse: mrRD, ResponseRcode: "NOERROR", Race: true, Action: "respond"},
			DNSRule{Domain: []string{"x.com"}, Server: "dns-direct", Speculative: true},
		)
		for _, is := range computeDNSChainIssues(cfg) {
			if is.RuleIndex == 2 {
				t.Fatalf("speculative после race не должен давать warning: %+v", is)
			}
		}
	})
}

func TestComputeIssues_IncludesDNSChainIssues(t *testing.T) {
	svc := &ServiceImpl{deps: Deps{}}
	cfg := NewEmptyConfig()
	cfg.DNS.Servers = []DNSServer{{Tag: "dns-direct", Type: "udp", Server: "1.1.1.1"}}
	cfg.DNS.Rules = []DNSRule{{Action: "evaluate", Server: "dns-direct", Tag: "rd"}}
	found := false
	for _, is := range svc.computeIssues(cfg) {
		if is.Kind == "dns-chain" {
			found = true
		}
	}
	if !found {
		t.Fatal("computeIssues must include dns-chain warnings")
	}
}
