package httpclient

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"time"
)

// LookupIPv4ForBind resolves host to an IPv4 address. When dnsServers is
// non-empty, queries are sent over UDP bound to bindIface (tunnel DNS).
// Otherwise the system resolver is used (ip4 only).
func LookupIPv4ForBind(ctx context.Context, host string, dnsServers []string, bindIface string) (string, error) {
	host = strings.TrimSuffix(strings.TrimSpace(host), ".")
	if host == "" {
		return "", fmt.Errorf("httpclient: empty host")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return ip4.String(), nil
		}
		return "", fmt.Errorf("httpclient: non-IPv4 literal %q", host)
	}

	if len(dnsServers) > 0 {
		var lastErr error
		for _, srv := range dnsServers {
			srv = strings.TrimSpace(srv)
			if srv == "" {
				continue
			}
			ip, err := lookupAViaDNS(ctx, host, srv, bindIface, 0)
			if err == nil {
				return ip, nil
			}
			lastErr = err
		}
		if lastErr != nil {
			return "", fmt.Errorf("httpclient: tunnel DNS lookup %q: %w", host, lastErr)
		}
		return "", fmt.Errorf("httpclient: no DNS servers configured for %q", host)
	}

	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil {
		return "", fmt.Errorf("httpclient: system DNS lookup %q: %w", host, err)
	}
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			return ip4.String(), nil
		}
	}
	return "", fmt.Errorf("httpclient: no IPv4 address for %q", host)
}

// LookupAllIPv4ForBind resolves host to ALL of its IPv4 addresses. Server and
// bind semantics match LookupIPv4ForBind, but every A record found is returned
// (deduplicated) instead of just the first. Needed wherever every address of a
// host must be covered: a domain may serve several A records (CDN,
// round-robin) and the client can connect to any of them. Returns an error
// only when nothing could be resolved.
func LookupAllIPv4ForBind(ctx context.Context, host string, dnsServers []string, bindIface string) ([]string, error) {
	host = strings.TrimSuffix(strings.TrimSpace(host), ".")
	if host == "" {
		return nil, fmt.Errorf("httpclient: empty host")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return []string{ip4.String()}, nil
		}
		return nil, fmt.Errorf("httpclient: non-IPv4 literal %q", host)
	}

	if len(dnsServers) > 0 {
		var lastErr error
		for _, srv := range dnsServers {
			srv = strings.TrimSpace(srv)
			if srv == "" {
				continue
			}
			ips, err := lookupAllAViaDNS(ctx, host, srv, bindIface, 0)
			if err == nil && len(ips) > 0 {
				return ips, nil
			}
			if err != nil {
				lastErr = err
			}
		}
		if lastErr != nil {
			return nil, fmt.Errorf("httpclient: tunnel DNS lookup %q: %w", host, lastErr)
		}
		return nil, fmt.Errorf("httpclient: no A record in DNS response for %q", host)
	}

	addrs, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil {
		return nil, fmt.Errorf("httpclient: system DNS lookup %q: %w", host, err)
	}
	out := dedupeIPv4(addrs)
	if len(out) == 0 {
		return nil, fmt.Errorf("httpclient: no IPv4 address for %q", host)
	}
	return out, nil
}

func dedupeIPv4(ips []net.IP) []string {
	seen := make(map[string]struct{}, len(ips))
	var out []string
	for _, ip := range ips {
		ip4 := ip.To4()
		if ip4 == nil {
			continue
		}
		s := ip4.String()
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func lookupAViaDNS(ctx context.Context, host, dnsServer, bindIface string, depth int) (string, error) {
	if depth > 5 {
		return "", fmt.Errorf("CNAME chain too deep for %q", host)
	}
	query, err := encodeDNSQuery(host)
	if err != nil {
		return "", err
	}

	d := bindDialer(bindIface, 5*time.Second)
	conn, err := d.DialContext(ctx, "udp4", net.JoinHostPort(dnsServer, "53"))
	if err != nil {
		return "", fmt.Errorf("dial DNS %s: %w", dnsServer, err)
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	}

	if _, err := conn.Write(query); err != nil {
		return "", fmt.Errorf("write DNS query: %w", err)
	}

	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	if err != nil {
		return "", fmt.Errorf("read DNS response: %w", err)
	}
	return parseDNSARecord(ctx, buf[:n], dnsServer, bindIface, depth)
}

func encodeDNSQuery(host string) ([]byte, error) {
	host = strings.TrimSuffix(strings.TrimSpace(host), ".")
	if host == "" {
		return nil, fmt.Errorf("empty DNS name")
	}

	var q []byte
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 {
			return nil, fmt.Errorf("invalid DNS name %q", host)
		}
		q = append(q, byte(len(label)))
		q = append(q, label...)
	}
	q = append(q, 0)

	out := make([]byte, 12+len(q)+4)
	out[0] = 0x12
	out[1] = 0x34
	out[2] = 0x01 // RD=1
	// QDCOUNT=1 — the packet carries one question. Without it, strict resolvers
	// reject the query with FORMERR/rcode 1 (#239: download-via-AWG-tunnel DNS).
	binary.BigEndian.PutUint16(out[4:], 1)
	copy(out[12:], q)
	off := 12 + len(q)
	binary.BigEndian.PutUint16(out[off:], 1)   // A
	binary.BigEndian.PutUint16(out[off+2:], 1) // IN
	return out, nil
}

func parseDNSARecord(ctx context.Context, pkt []byte, dnsServer, bindIface string, depth int) (string, error) {
	if len(pkt) < 12 {
		return "", fmt.Errorf("short DNS packet")
	}
	if pkt[3]&0x0f != 0 {
		return "", fmt.Errorf("DNS error rcode %d", pkt[3]&0x0f)
	}
	ancount := int(binary.BigEndian.Uint16(pkt[6:8]))
	off := 12
	off, err := skipDNSName(pkt, off)
	if err != nil {
		return "", err
	}
	if off+4 > len(pkt) {
		return "", fmt.Errorf("truncated DNS question")
	}
	off += 4

	var cname string
	for i := 0; i < ancount; i++ {
		off, err = skipDNSName(pkt, off)
		if err != nil {
			return "", err
		}
		if off+10 > len(pkt) {
			return "", fmt.Errorf("truncated DNS answer header")
		}
		rrType := binary.BigEndian.Uint16(pkt[off : off+2])
		rdLen := int(binary.BigEndian.Uint16(pkt[off+8 : off+10]))
		rdataOff := off + 10
		off = rdataOff + rdLen
		if off > len(pkt) {
			return "", fmt.Errorf("truncated DNS rdata")
		}
		switch rrType {
		case 1: // A
			if rdLen == 4 {
				return net.IP(pkt[rdataOff : rdataOff+4]).String(), nil
			}
		case 5: // CNAME
			if cname == "" {
				cname, err = decodeDNSName(pkt, rdataOff)
			}
		}
	}
	if cname != "" {
		return lookupAViaDNS(ctx, cname, dnsServer, bindIface, depth+1)
	}
	return "", fmt.Errorf("no A record in DNS response")
}

// lookupAllAViaDNS is the multi-record sibling of lookupAViaDNS: it returns
// every A record in the response (following a single CNAME chain) rather than
// stopping at the first.
// LookupCNAMETargetsViaDNS returns CNAME target hostnames from the answer
// section for host (not followed). Useful for discovering CDN origin names
// that should be resolved into ipset entries.
func LookupCNAMETargetsViaDNS(ctx context.Context, host, dnsServer, bindIface string) ([]string, error) {
	pkt, err := exchangeDNS(ctx, host, dnsServer, bindIface)
	if err != nil {
		return nil, err
	}
	return parseDNSCNAMETargets(pkt)
}

func lookupAllAViaDNS(ctx context.Context, host, dnsServer, bindIface string, depth int) ([]string, error) {
	if depth > 5 {
		return nil, fmt.Errorf("CNAME chain too deep for %q", host)
	}
	pkt, err := exchangeDNS(ctx, host, dnsServer, bindIface)
	if err != nil {
		return nil, err
	}
	return parseAllDNSARecords(ctx, pkt, dnsServer, bindIface, depth)
}

func exchangeDNS(ctx context.Context, host, dnsServer, bindIface string) ([]byte, error) {
	query, err := encodeDNSQuery(host)
	if err != nil {
		return nil, err
	}

	d := bindDialer(bindIface, 5*time.Second)
	conn, err := d.DialContext(ctx, "udp4", net.JoinHostPort(dnsServer, "53"))
	if err != nil {
		return nil, fmt.Errorf("dial DNS %s: %w", dnsServer, err)
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	}

	if _, err := conn.Write(query); err != nil {
		return nil, fmt.Errorf("write DNS query: %w", err)
	}

	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("read DNS response: %w", err)
	}
	return buf[:n], nil
}

// parseDNSCNAMETargets collects every CNAME rdata hostname in the answer section.
func parseDNSCNAMETargets(pkt []byte) ([]string, error) {
	if len(pkt) < 12 {
		return nil, fmt.Errorf("short DNS packet")
	}
	if pkt[3]&0x0f != 0 {
		return nil, fmt.Errorf("DNS error rcode %d", pkt[3]&0x0f)
	}
	ancount := int(binary.BigEndian.Uint16(pkt[6:8]))
	off := 12
	off, err := skipDNSName(pkt, off)
	if err != nil {
		return nil, err
	}
	if off+4 > len(pkt) {
		return nil, fmt.Errorf("truncated DNS question")
	}
	off += 4

	seen := make(map[string]struct{})
	var out []string
	for i := 0; i < ancount; i++ {
		off, err = skipDNSName(pkt, off)
		if err != nil {
			return nil, err
		}
		if off+10 > len(pkt) {
			return nil, fmt.Errorf("truncated DNS answer header")
		}
		rrType := binary.BigEndian.Uint16(pkt[off : off+2])
		rdLen := int(binary.BigEndian.Uint16(pkt[off+8 : off+10]))
		rdataOff := off + 10
		off = rdataOff + rdLen
		if off > len(pkt) {
			return nil, fmt.Errorf("truncated DNS rdata")
		}
		if rrType != 5 { // CNAME
			continue
		}
		target, err := decodeDNSName(pkt, rdataOff)
		if err != nil || target == "" {
			continue
		}
		target = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(target)), ".")
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		out = append(out, target)
	}
	return out, nil
}

// parseAllDNSARecords collects every A record in the answer section. If the
// answer carries only CNAMEs, it follows the first one (bounded by depth).
func parseAllDNSARecords(ctx context.Context, pkt []byte, dnsServer, bindIface string, depth int) ([]string, error) {
	if len(pkt) < 12 {
		return nil, fmt.Errorf("short DNS packet")
	}
	if pkt[3]&0x0f != 0 {
		return nil, fmt.Errorf("DNS error rcode %d", pkt[3]&0x0f)
	}
	ancount := int(binary.BigEndian.Uint16(pkt[6:8]))
	off := 12
	off, err := skipDNSName(pkt, off)
	if err != nil {
		return nil, err
	}
	if off+4 > len(pkt) {
		return nil, fmt.Errorf("truncated DNS question")
	}
	off += 4

	var ips []string
	var cname string
	for i := 0; i < ancount; i++ {
		off, err = skipDNSName(pkt, off)
		if err != nil {
			return nil, err
		}
		if off+10 > len(pkt) {
			return nil, fmt.Errorf("truncated DNS answer header")
		}
		rrType := binary.BigEndian.Uint16(pkt[off : off+2])
		rdLen := int(binary.BigEndian.Uint16(pkt[off+8 : off+10]))
		rdataOff := off + 10
		off = rdataOff + rdLen
		if off > len(pkt) {
			return nil, fmt.Errorf("truncated DNS rdata")
		}
		switch rrType {
		case 1: // A
			if rdLen == 4 {
				ips = append(ips, net.IP(pkt[rdataOff:rdataOff+4]).String())
			}
		case 5: // CNAME
			if cname == "" {
				cname, _ = decodeDNSName(pkt, rdataOff)
			}
		}
	}
	if len(ips) > 0 {
		seen := make(map[string]struct{}, len(ips))
		out := ips[:0]
		for _, ip := range ips {
			if _, ok := seen[ip]; ok {
				continue
			}
			seen[ip] = struct{}{}
			out = append(out, ip)
		}
		return out, nil
	}
	if cname != "" {
		return lookupAllAViaDNS(ctx, cname, dnsServer, bindIface, depth+1)
	}
	return nil, fmt.Errorf("no A record in DNS response")
}

func skipDNSName(pkt []byte, off int) (int, error) {
	for jumps := 0; jumps < 128; jumps++ {
		if off >= len(pkt) {
			return 0, fmt.Errorf("truncated DNS name")
		}
		l := int(pkt[off])
		if l == 0 {
			return off + 1, nil
		}
		if l&0xc0 == 0xc0 {
			if off+1 >= len(pkt) {
				return 0, fmt.Errorf("truncated DNS compression pointer")
			}
			return off + 2, nil
		}
		off++
		if off+l > len(pkt) {
			return 0, fmt.Errorf("truncated DNS label")
		}
		off += l
	}
	return 0, fmt.Errorf("DNS name too long")
}

func decodeDNSName(pkt []byte, off int) (string, error) {
	return decodeDNSNameFromWire(pkt, pkt, off)
}

func decodeDNSNameFromWire(pkt, wire []byte, off int) (string, error) {
	var labels []string
	jumps := 0
	for {
		if off >= len(wire) || jumps > 128 {
			return "", fmt.Errorf("invalid DNS name")
		}
		l := int(wire[off])
		if l == 0 {
			break
		}
		if l&0xc0 == 0xc0 {
			if off+1 >= len(wire) {
				return "", fmt.Errorf("truncated DNS pointer")
			}
			ptr := int(binary.BigEndian.Uint16(wire[off:off+2]) & 0x3fff)
			off = ptr
			jumps++
			continue
		}
		off++
		if off+l > len(wire) {
			return "", fmt.Errorf("truncated DNS label")
		}
		labels = append(labels, string(wire[off:off+l]))
		off += l
	}
	if len(labels) == 0 {
		return "", fmt.Errorf("empty DNS name")
	}
	return strings.Join(labels, "."), nil
}
