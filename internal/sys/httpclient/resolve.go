package httpclient

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

const resolveTTL = 5 * time.Minute

// lookupIPv4 is swappable in tests (same pattern as httpprobe.Client).
var lookupIPv4 = LookupIPv4ForBind

type resolveEntry struct {
	ip         string
	resolvedAt time.Time
}

var (
	resolveMu    sync.Mutex
	resolveCache = map[string]resolveEntry{}
)

// ResolveIPv4Resilient resolves host to IPv4 with a failure-tolerant chain:
// fresh lookup (tunnel DNS when dnsServers set, system resolver otherwise),
// then the cached last-known-good answer, then the system resolver as the
// last resort. A flaky resolver must not turn into a false probe failure —
// probing an hour-stale IP still measures the tunnel honestly. Entries are
// keyed by (bindIface, host): different tunnel exits may legitimately
// resolve one name to different IPs (geo-DNS). Stale entries live until
// process restart.
func ResolveIPv4Resilient(ctx context.Context, host string, dnsServers []string, bindIface string) (string, error) {
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

	key := bindIface + "|" + host
	resolveMu.Lock()
	entry, cached := resolveCache[key]
	resolveMu.Unlock()
	if cached && time.Since(entry.resolvedAt) < resolveTTL {
		return entry.ip, nil
	}

	ip, err := lookupIPv4(ctx, host, dnsServers, bindIface)
	if err == nil {
		storeResolved(key, ip)
		return ip, nil
	}
	if cached {
		return entry.ip, nil
	}
	if len(dnsServers) > 0 {
		if sysIP, sysErr := lookupIPv4(ctx, host, nil, ""); sysErr == nil {
			storeResolved(key, sysIP)
			return sysIP, nil
		}
	}
	return "", err
}

// resolveIPv6Resilient — AAAA с той же защитой от дрожащего резолвера, что у
// IPv4: свежий ответ из кэша, при отказе последний удачный (F132). Системного
// резолвера в цепочке нет: запрос через туннель не должен уходить мимо него.
func resolveIPv6Resilient(ctx context.Context, host string, dnsServers []string, bindIface string) (string, error) {
	key := bindIface + "|6|" + host
	resolveMu.Lock()
	entry, cached := resolveCache[key]
	resolveMu.Unlock()
	if cached && time.Since(entry.resolvedAt) < resolveTTL {
		return entry.ip, nil
	}
	ip, err := lookupIPv6(ctx, host, dnsServers, bindIface)
	if err == nil {
		storeResolved(key, ip)
		return ip, nil
	}
	if cached {
		return entry.ip, nil
	}
	return "", err
}

func storeResolved(key, ip string) {
	resolveMu.Lock()
	resolveCache[key] = resolveEntry{ip: ip, resolvedAt: time.Now()}
	resolveMu.Unlock()
}
