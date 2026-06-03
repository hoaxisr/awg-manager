package dnsrewrite

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

// DNSRewrite — каноническая запись перезаписи: glob-паттерн домена → IP.
type DNSRewrite struct {
	Pattern string   `json:"pattern"`
	IPs     []string `json:"ips"`
}

// compileRewrite превращает одну запись в одно или два (dual-stack) sing-box
// predefined-правила. Имена answer-записей всегда абсолютные.
func compileRewrite(r DNSRewrite) ([]map[string]any, error) {
	matcherKey, matcher, answerName, err := parsePattern(r.Pattern)
	if err != nil {
		return nil, err
	}
	v4, v6, err := splitIPs(r.IPs)
	if err != nil {
		return nil, err
	}
	if len(v4) == 0 && len(v6) == 0 {
		return nil, fmt.Errorf("перезапись %q: нужен хотя бы один IP", r.Pattern)
	}

	// Each rule is scoped to one query_type. The family that has IPs answers
	// directly; the opposite family gets a predefined rule WITHOUT an answer,
	// which sing-box returns as NODATA. This stops a single-stack rewrite from
	// being silently bypassed: an IPv4-only rewrite must suppress AAAA (else an
	// IPv6-capable client resolves the real address over IPv6 and never uses
	// the override), and an IPv6-only rewrite must suppress A. Dual-stack just
	// answers both families.
	mk := func(answers []string, qtype string) map[string]any {
		rule := map[string]any{
			matcherKey:   []string{matcher},
			"action":     "predefined",
			"query_type": []string{qtype},
		}
		if len(answers) > 0 {
			rule["answer"] = answers
		}
		return rule
	}

	answersFor := func(ips []string, rrtype string) []string {
		out := make([]string, 0, len(ips))
		for _, ip := range ips {
			out = append(out, fmt.Sprintf("%s IN %s %s", answerName, rrtype, ip))
		}
		return out
	}

	return []map[string]any{
		mk(answersFor(v4, "A"), "A"),
		mk(answersFor(v6, "AAAA"), "AAAA"),
	}, nil
}

// parsePattern разбирает glob-паттерн в (ключ-матчера, значение, абсолютное answer-имя).
func parsePattern(pattern string) (matcherKey, matcher, answerName string, err error) {
	p := strings.ToLower(strings.TrimSpace(pattern))
	p = strings.TrimSuffix(p, ".")
	if p == "" {
		return "", "", "", fmt.Errorf("пустой шаблон")
	}
	if strings.HasPrefix(p, ".") {
		return "", "", "", fmt.Errorf("шаблон %q: лишняя ведущая точка", pattern)
	}
	n := strings.Count(p, "*")
	if n == 0 {
		return "domain", p, p + ".", nil
	}
	if n > 1 {
		return "", "", "", fmt.Errorf("шаблон %q: несколько * не поддерживается", pattern)
	}
	i := strings.IndexByte(p, '*')
	after := p[i+1:]
	dot := strings.IndexByte(after, '.')
	if dot < 0 {
		return "", "", "", fmt.Errorf("шаблон %q: после * нужен доменный хвост (напр. *.example.com)", pattern)
	}
	tail := after[dot+1:]
	if tail == "" {
		return "", "", "", fmt.Errorf("шаблон %q: пустой доменный хвост", pattern)
	}
	answerName = "*." + tail + "."
	// Ведущий "*." → чистый суффикс (домен + поддомены).
	if i == 0 && dot == 0 {
		return "domain_suffix", tail, answerName, nil
	}
	// Иначе — regex по экранированным литералам, * → [^.]*.
	parts := strings.SplitN(p, "*", 2)
	rx := "^" + regexp.QuoteMeta(parts[0]) + "[^.]*" + regexp.QuoteMeta(parts[1]) + "$"
	if _, e := regexp.Compile(rx); e != nil {
		return "", "", "", fmt.Errorf("шаблон %q: не компилируется в regex: %w", pattern, e)
	}
	return "domain_regex", rx, answerName, nil
}

// splitIPs делит и валидирует IP на v4/v6 (в исходном строковом виде).
func splitIPs(ips []string) (v4, v6 []string, err error) {
	for _, raw := range ips {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		addr, e := netip.ParseAddr(s)
		if e != nil {
			return nil, nil, fmt.Errorf("неверный IP %q", raw)
		}
		if addr.Is4() || addr.Is4In6() {
			v4 = append(v4, addr.Unmap().String())
		} else {
			v6 = append(v6, s)
		}
	}
	return v4, v6, nil
}
