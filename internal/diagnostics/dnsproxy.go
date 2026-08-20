package diagnostics

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// DNSProxy is one ndnproxy profile (System or PolicyN) from /show/dns-proxy.
// DisplayName is left empty by the parser and filled by the API handler.
type DNSProxy struct {
	Name        string            `json:"name"`
	DisplayName string            `json:"displayName"`
	TCPPort     int               `json:"tcpPort"`
	UDPPort     int               `json:"udpPort"`
	Stat        DNSProxyStat      `json:"stat"`
	Upstreams   []DNSUpstream     `json:"upstreams"`
	Static      []DNSStaticRecord `json:"staticRecords"`
	Rebind      DNSRebind         `json:"rebind"`
}

type DNSProxyStat struct {
	TotalRequests     int     `json:"totalRequests"`
	ProxyRequestsSent int     `json:"proxyRequestsSent"`
	CacheHitRatio     float64 `json:"cacheHitRatio"`
	CacheHits         int     `json:"cacheHits"`
	Memory            string  `json:"memory"`
}

type DNSUpstream struct {
	Address    string `json:"address"`
	Port       int    `json:"port"`
	Encryption string `json:"encryption"` // DoT | DoH | plain
	SNI        string `json:"sni"`
	Scope      string `json:"scope"` // "all" or a domain like "ru"
	RSent      int    `json:"rSent"`
	ARcvd      int    `json:"aRcvd"`
	NXRcvd     int    `json:"nxRcvd"`
	MedResp    string `json:"medResp"`
	AvgResp    string `json:"avgResp"`
	Rank       int    `json:"rank"`
	localPort  int    // join key, not serialized
}

type DNSStaticRecord struct {
	Host  string `json:"host"`
	Type  string `json:"type"` // A | AAAA
	Value string `json:"value"`
	Flag  int    `json:"flag"`
}

type DNSRebind struct {
	Enabled  bool     `json:"enabled"`
	Nets     []string `json:"nets"`
	Excludes []string `json:"excludes"`
}

// dnsTLSEntry is one entry from proxy-tls.server-tls.
type dnsTLSEntry struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
	SNI     string `json:"sni"`
	Domain  string `json:"domain"`
}

// dnsHTTPSEntry is one entry from proxy-https.server-https.
// NDMS отдаёт URI (с схемой), не голый адрес, плюс внутренний дискриминатор формата.
type dnsHTTPSEntry struct {
	URI    string `json:"uri"`
	Format string `json:"format"`
}

// dnsProxyWire mirrors the relevant subset of /show/dns-proxy JSON.
type dnsProxyWire struct {
	Status []struct {
		Name   string `json:"proxy-name"`
		Config string `json:"proxy-config"`
		Stat   string `json:"proxy-stat"`
		TLS    struct {
			ServerTLS []dnsTLSEntry `json:"server-tls"`
		} `json:"proxy-tls"`
		HTTPS struct {
			ServerHTTPS []dnsHTTPSEntry `json:"server-https"`
		} `json:"proxy-https"`
	} `json:"proxy-status"`
}

// ParseDNSProxy converts the router's /show/dns-proxy payload into a clean
// slice of DNSProxy. It tolerates empty/unknown blocks and never panics on
// malformed text lines (they are skipped).
func ParseDNSProxy(raw []byte) ([]DNSProxy, error) {
	var w dnsProxyWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("decode dns-proxy: %w", err)
	}
	out := make([]DNSProxy, 0, len(w.Status))
	for _, s := range w.Status {
		p := DNSProxy{Name: s.Name}
		p.Upstreams, p.Static, p.Rebind, p.TCPPort, p.UDPPort = parseConfig(s.Config)
		statSummary, serverStats := parseStat(s.Stat)
		p.Stat = statSummary
		applyEncryption(p.Upstreams, s.TLS.ServerTLS, s.HTTPS.ServerHTTPS)
		joinServerStats(p.Upstreams, serverStats)
		out = append(out, p)
	}
	return out, nil
}

// parseConfig walks the proxy-config text block.
func parseConfig(cfg string) (ups []DNSUpstream, static []DNSStaticRecord, rb DNSRebind, tcp, udp int) {
	for _, raw := range strings.Split(cfg, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "dns_server"):
			if u, ok := parseDNSServer(line); ok {
				ups = append(ups, u)
			}
		case strings.HasPrefix(line, "static_aaaa"):
			if r, ok := parseStatic(line); ok {
				static = append(static, r)
			}
		case strings.HasPrefix(line, "static_a "), strings.HasPrefix(line, "static_a="):
			if r, ok := parseStatic(line); ok {
				static = append(static, r)
			}
		case strings.HasPrefix(line, "norebind_ctl"):
			rb.Enabled = afterEquals(line) == "on"
		case strings.HasPrefix(line, "norebind_ip4net"):
			if v := afterEquals(line); v != "" {
				rb.Nets = append(rb.Nets, v)
			}
		case strings.HasPrefix(line, "norebind_exclude"):
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				rb.Excludes = append(rb.Excludes, parts[1])
			}
		case strings.HasPrefix(line, "dns_tcp_port"):
			tcp = atoiSafe(afterEquals(line))
		case strings.HasPrefix(line, "dns_udp_port"):
			udp = atoiSafe(afterEquals(line))
		}
	}
	return ups, static, rb, tcp, udp
}

// parseDNSServer parses lines like:
//
//	dns_server = 127.0.0.1:40500 . # 8.8.8.8@dns.google
//	dns_server = 127.0.0.1:40501 ru # 77.88.8.8:853@common.dot.dns.yandex.net
//	dns_server = 127.0.0.1:40502 . # 9.9.9.9
//	dns_server = 127.0.0.1:40508 . # https://common.dot.dns.yandex.net@dnsm
func parseDNSServer(line string) (DNSUpstream, bool) {
	val := afterEquals(line)
	if val == "" {
		return DNSUpstream{}, false
	}
	var comment string
	if i := strings.Index(val, "#"); i >= 0 {
		comment = strings.TrimSpace(val[i+1:])
		val = strings.TrimSpace(val[:i])
	}
	fields := strings.Fields(val)
	if len(fields) == 0 {
		return DNSUpstream{}, false
	}
	u := DNSUpstream{Scope: "all"}
	if _, portStr, ok := splitHostPort(fields[0]); ok {
		u.localPort = atoiSafe(portStr)
	}
	if len(fields) >= 2 && fields[1] != "." && fields[1] != "" {
		u.Scope = fields[1]
	}
	if comment != "" {
		if strings.HasPrefix(comment, "https://") || strings.HasPrefix(comment, "http://") {
			parseDoHComment(&u, comment)
		} else {
			parsePlainComment(&u, comment)
		}
	}
	return u, u.Address != "" || u.localPort != 0
}

// parsePlainComment разбирает форму DoT/plain: "IP[:port][@SNI]".
func parsePlainComment(u *DNSUpstream, comment string) {
	addrPart := comment
	if at := strings.Index(comment, "@"); at >= 0 {
		addrPart = comment[:at]
		u.SNI = comment[at+1:]
	}
	if host, portStr, err := net.SplitHostPort(addrPart); err == nil {
		u.Address = host
		u.Port = atoiSafe(portStr)
	} else {
		u.Address = addrPart
	}
}

// parseDoHComment разбирает форму DoH: "<scheme>://<host>[:port]/[path][@format]".
// Хвостовой "@<format>" (например "@dnsm") — NDMS-овский дискриминатор wire-формата;
// его отбрасываем. Address получает hostname, Port — порт из URL или дефолт схемы.
func parseDoHComment(u *DNSUpstream, comment string) {
	urlStr := comment
	if at := strings.LastIndex(comment, "@"); at > 0 && isSimpleToken(comment[at+1:]) {
		urlStr = comment[:at]
	}
	hu, err := url.Parse(urlStr)
	if err != nil || hu.Hostname() == "" {
		u.Address = urlStr
		return
	}
	u.Address = hu.Hostname()
	if p := hu.Port(); p != "" {
		u.Port = atoiSafe(p)
	} else if hu.Scheme == "https" {
		u.Port = 443
	} else {
		u.Port = 80
	}
}

// isSimpleToken: непустая строка ≤32 символа из ASCII-букв/цифр/-/_. Используется
// чтобы отличить хвостовой "@<format>" от настоящей части URL.
func isSimpleToken(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') &&
			!(c >= '0' && c <= '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// StaticIPv4InZones возвращает IPv4 из static_a всех профилей ndnproxy для
// имён, которые равны одному из hosts или лежат в одной из zones (дедуп со
// стабильным порядком, сравнение регистронезависимое, хвостовая точка не
// учитывается).
//
// Нужно пресету keendns: это адреса, к которым роутер направляет свои
// KeenDNS-имена, и трафик к ним обязан идти мимо sing-box. Брать их надо
// именно с роутера — с прошивок 5.1.3/5.2 адрес сервиса переехал с
// 78.47.125.180 в документационный 198.51.100.0/24 и отличается у Keenetic
// и NetCraze. AAAA сознательно не отдаём: перехвата IPv6 у нас нет.
func StaticIPv4InZones(proxies []DNSProxy, hosts, zones []string) []string {
	match := func(name string) bool {
		n := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
		if n == "" {
			return false
		}
		for _, h := range hosts {
			if n == strings.ToLower(h) {
				return true
			}
		}
		for _, z := range zones {
			z = strings.ToLower(z)
			if n == z || strings.HasSuffix(n, "."+z) {
				return true
			}
		}
		return false
	}
	var out []string
	seen := map[string]struct{}{}
	for _, p := range proxies {
		for _, r := range p.Static {
			if r.Type != "A" || !match(r.Host) {
				continue
			}
			ip := net.ParseIP(r.Value)
			if ip == nil || ip.To4() == nil {
				continue
			}
			v := ip.String()
			if _, dup := seen[v]; dup {
				continue
			}
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}

// parseStatic parses "static_a = host ip flag" / "static_aaaa = host ip flag".
func parseStatic(line string) (DNSStaticRecord, bool) {
	typ := "A"
	if strings.HasPrefix(line, "static_aaaa") {
		typ = "AAAA"
	}
	val := afterEquals(line)
	fields := strings.Fields(val)
	if len(fields) < 2 {
		return DNSStaticRecord{}, false
	}
	r := DNSStaticRecord{Host: fields[0], Type: typ, Value: fields[1]}
	if len(fields) >= 3 {
		r.Flag = atoiSafe(fields[2])
	}
	return r, true
}

// serverStat is one row of the proxy-stat "DNS Servers" table, keyed by local port.
type serverStat struct {
	rSent, aRcvd, nxRcvd, rank int
	medResp, avgResp           string
}

func parseStat(stat string) (DNSProxyStat, map[int]serverStat) {
	var summary DNSProxyStat
	servers := map[int]serverStat{}
	inTable := false
	for _, raw := range strings.Split(stat, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "Total incoming requests:"):
			summary.TotalRequests = atoiSafe(afterColon(line))
		case strings.HasPrefix(line, "Proxy requests sent:"):
			summary.ProxyRequestsSent = atoiSafe(afterColon(line))
		case strings.HasPrefix(line, "Cache hits ratio:"):
			rest := afterColon(line)
			f := strings.Fields(rest)
			if len(f) >= 1 {
				summary.CacheHitRatio, _ = strconv.ParseFloat(f[0], 64)
			}
			if i := strings.Index(rest, "("); i >= 0 {
				inner := strings.Trim(rest[i+1:], ") ")
				summary.CacheHits = atoiSafe(inner)
			}
		case strings.HasPrefix(line, "Memory usage:"):
			summary.Memory = strings.TrimSpace(afterColon(line))
		case strings.HasPrefix(line, "Ip") && strings.Contains(line, "Port"):
			inTable = true
		case inTable && line != "":
			f := strings.Fields(line)
			if len(f) >= 8 {
				port := atoiSafe(f[1])
				servers[port] = serverStat{
					rSent:   atoiSafe(f[2]),
					aRcvd:   atoiSafe(f[3]),
					nxRcvd:  atoiSafe(f[4]),
					medResp: f[5],
					avgResp: f[6],
					rank:    atoiSafe(f[7]),
				}
			}
		}
	}
	return summary, servers
}

// applyEncryption sets Encryption + SNI on each upstream from proxy-tls / proxy-https.
func applyEncryption(ups []DNSUpstream, tls []dnsTLSEntry, https []dnsHTTPSEntry) {
	tlsByAddr := map[string]string{}
	for _, t := range tls {
		tlsByAddr[t.Address] = t.SNI
	}
	// proxy-https записи приходят с URI, а апстрим парсится в host без схемы.
	// Индексируем по hostname URI, чтобы сравнение в цикле было прямым.
	httpsByHost := map[string]bool{}
	for _, h := range https {
		if h.URI == "" {
			continue
		}
		if hu, err := url.Parse(h.URI); err == nil {
			if host := hu.Hostname(); host != "" {
				httpsByHost[host] = true
			}
		}
	}
	for i := range ups {
		switch {
		case httpsByHost[ups[i].Address]:
			ups[i].Encryption = "DoH"
		default:
			if sni, ok := tlsByAddr[ups[i].Address]; ok {
				ups[i].Encryption = "DoT"
				if ups[i].SNI == "" {
					ups[i].SNI = sni
				}
			} else {
				ups[i].Encryption = "plain"
			}
		}
	}
}

func joinServerStats(ups []DNSUpstream, servers map[int]serverStat) {
	for i := range ups {
		if s, ok := servers[ups[i].localPort]; ok {
			ups[i].RSent = s.rSent
			ups[i].ARcvd = s.aRcvd
			ups[i].NXRcvd = s.nxRcvd
			ups[i].MedResp = s.medResp
			ups[i].AvgResp = s.avgResp
			ups[i].Rank = s.rank
		}
	}
}

func afterEquals(line string) string {
	if i := strings.Index(line, "="); i >= 0 {
		return strings.TrimSpace(line[i+1:])
	}
	return ""
}

func afterColon(line string) string {
	if i := strings.Index(line, ":"); i >= 0 {
		return strings.TrimSpace(line[i+1:])
	}
	return ""
}

func atoiSafe(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// splitHostPort splits "1.2.3.4:853" -> ("1.2.3.4","853",true). No colon -> false.
func splitHostPort(s string) (host, port string, ok bool) {
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return s[:i], s[i+1:], true
	}
	return s, "", false
}
