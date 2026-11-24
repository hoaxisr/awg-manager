package ipinspect

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxEntries = 256
	defaultTTL        = 30 * time.Minute
	defaultEmptyTTL   = 5 * time.Minute
	defaultASTTL      = 6 * time.Hour
	publicTimeout     = 5 * time.Second
	privatePTRTimeout = 1500 * time.Millisecond
)

var (
	// ErrInvalidIP indicates the supplied string could not be parsed as an IP address.
	ErrInvalidIP = errors.New("invalid IP address")

	nat64WellKnown   = netip.MustParsePrefix("64:ff9b::/96")
	nat64LocalUse    = netip.MustParsePrefix("64:ff9b:1::/48")
	cgnatPrefix      = netip.MustParsePrefix("100.64.0.0/10")
	benchmarkPrefix  = netip.MustParsePrefix("198.18.0.0/15")
	reservedPrefixes = []netip.Prefix{
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("0.0.0.0/8"),    // «этот хост/сеть»
		netip.MustParsePrefix("192.0.0.0/24"), // IETF protocol assignments; anycast .9/.10 целью скана быть незачем
		netip.MustParsePrefix("fec0::/10"),    // site-local (устаревший)
		netip.MustParsePrefix("2001::/32"),    // Teredo
		netip.MustParsePrefix("2002::/16"),    // 6to4: внутри любой IPv4
		netip.MustParsePrefix("::/96"),        // IPv4-compatible (устаревший)
	}
)

// Resolver performs reverse DNS and Team Cymru TXT lookups.
type Resolver interface {
	LookupAddr(ctx context.Context, addr string) ([]string, error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// Result contains classification, PTR, and Team Cymru ASN metadata for an IP address.
type Result struct {
	IP           string   `json:"ip"`
	Family       string   `json:"family"`
	Scope        string   `json:"scope"`
	PTR          string   `json:"ptr,omitempty"`
	PTRs         []string `json:"ptrs,omitempty"`
	ASN          int      `json:"asn,omitempty"`
	Prefix       string   `json:"prefix,omitempty"`
	Country      string   `json:"country,omitempty"`
	Registry     string   `json:"registry,omitempty"`
	Organization string   `json:"organization,omitempty"`
	Cached       bool     `json:"cached,omitempty"`
}

type cacheEntry struct {
	result    Result
	expiresAt time.Time
}

type asEntry struct {
	org       string
	country   string
	registry  string
	expiresAt time.Time
}

// Service performs bounded, cached, on-demand IP inspection without background goroutines.
type Service struct {
	resolver   Resolver
	now        func() time.Time
	maxEntries int
	ttl        time.Duration
	emptyTTL   time.Duration
	asTTL      time.Duration

	mu      sync.Mutex
	ipCache map[string]cacheEntry
	asCache map[int]asEntry
}

// NewService constructs an inspection service backed by the default OS DNS resolver.
func NewService() *Service {
	return NewServiceWithResolver(net.DefaultResolver, time.Now)
}

// NewServiceWithResolver constructs an inspection service with a custom Resolver and clock.
func NewServiceWithResolver(r Resolver, now func() time.Time) *Service {
	if r == nil {
		r = net.DefaultResolver
	}
	if now == nil {
		now = time.Now
	}
	return &Service{
		resolver:   r,
		now:        now,
		maxEntries: defaultMaxEntries,
		ttl:        defaultTTL,
		emptyTTL:   defaultEmptyTTL,
		asTTL:      defaultASTTL,
		ipCache:    make(map[string]cacheEntry),
		asCache:    make(map[int]asEntry),
	}
}

// ClassifyAddr classifies an address into family ("ipv4", "ipv6", "nat64") and routing scope.
func ClassifyAddr(addr netip.Addr) (family, scope string) {
	addr = addr.Unmap()
	if !addr.IsValid() {
		return "", "reserved"
	}

	if addr.Is4() {
		family = "ipv4"
	} else if nat64WellKnown.Contains(addr) || nat64LocalUse.Contains(addr) {
		family = "nat64"
		// Well-known NAT64 ведёт на вложенный IPv4 (последние 32 бита):
		// 64:ff9b::7f00:1 — это loopback, а не публичный адрес. У local-use
		// /48 место IPv4 зависит от длины префикса оператора — не угадываем.
		if nat64WellKnown.Contains(addr) {
			b := addr.As16()
			if _, inner := ClassifyAddr(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})); inner != "public" {
				return family, inner
			}
		}
	} else {
		family = "ipv6"
	}

	switch {
	case addr.IsLoopback():
		scope = "loopback"
	case addr.IsMulticast():
		scope = "multicast"
	case addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast():
		scope = "link-local"
	case addr.IsPrivate():
		scope = "private"
	case addr.IsUnspecified():
		scope = "reserved"
	case cgnatPrefix.Contains(addr):
		scope = "cgnat"
	case benchmarkPrefix.Contains(addr):
		scope = "benchmark"
	case hasPrefix(reservedPrefixes, addr):
		scope = "reserved"
	default:
		scope = "public"
	}
	return family, scope
}

func hasPrefix(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// CymruOriginQuery builds the Team Cymru DNS query name for IP->ASN origin lookup.
func CymruOriginQuery(addr netip.Addr) string {
	addr = addr.Unmap()
	if !addr.IsValid() {
		return ""
	}
	if addr.Is4() {
		b := addr.As4()
		return fmt.Sprintf("%d.%d.%d.%d.origin.asn.cymru.com", b[3], b[2], b[1], b[0])
	}
	b := addr.As16()
	const hexDigits = "0123456789abcdef"
	var sb strings.Builder
	sb.Grow(64 + len(".origin6.asn.cymru.com"))
	for i := len(b) - 1; i >= 0; i-- {
		if sb.Len() > 0 {
			sb.WriteByte('.')
		}
		sb.WriteByte(hexDigits[b[i]&0x0f])
		sb.WriteByte('.')
		sb.WriteByte(hexDigits[b[i]>>4])
	}
	sb.WriteString(".origin6.asn.cymru.com")
	return sb.String()
}

// ParseCymruOriginTXT parses a Team Cymru origin TXT record:
// "<asn> | <prefix> | <cc> | <registry> | <allocated>".
func ParseCymruOriginTXT(txt string) (asn int, prefix, country, registry string, ok bool) {
	parts := strings.Split(txt, "|")
	if len(parts) < 2 {
		return 0, "", "", "", false
	}

	fields := strings.Fields(parts[0])
	if len(fields) == 0 {
		return 0, "", "", "", false
	}
	parsedASN, err := strconv.Atoi(fields[0])
	if err != nil || parsedASN <= 0 {
		return 0, "", "", "", false
	}
	asn = parsedASN

	if p, err := netip.ParsePrefix(strings.TrimSpace(parts[1])); err == nil {
		prefix = p.String()
	}
	if len(parts) > 2 {
		country = strings.ToUpper(strings.TrimSpace(parts[2]))
	}
	if len(parts) > 3 {
		registry = strings.ToLower(strings.TrimSpace(parts[3]))
	}
	return asn, prefix, country, registry, true
}

// ParseCymruASNTXT parses a Team Cymru AS description TXT record:
// "<asn> | <cc> | <registry> | <allocated> | <org>".
func ParseCymruASNTXT(txt string) (org, country, registry string) {
	parts := strings.Split(txt, "|")
	if len(parts) > 1 {
		country = strings.ToUpper(strings.TrimSpace(parts[1]))
	}
	if len(parts) > 2 {
		registry = strings.ToLower(strings.TrimSpace(parts[2]))
	}
	if len(parts) > 4 {
		org = strings.TrimSpace(parts[4])
	}
	return org, country, registry
}

// Inspect resolves PTR and Team Cymru ASN metadata for rawIP on demand.
func (s *Service) Inspect(ctx context.Context, rawIP string) (Result, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(rawIP))
	if err != nil {
		return Result{}, ErrInvalidIP
	}
	addr = addr.Unmap()
	ipStr := addr.String()
	family, scope := ClassifyAddr(addr)

	if cached, ok := s.getCachedIP(ipStr); ok {
		cached.Cached = true
		return cached, nil
	}

	res := Result{
		IP:     ipStr,
		Family: family,
		Scope:  scope,
	}

	if scope != "public" {
		if scope == "private" {
			ptrCtx, cancel := context.WithTimeout(ctx, privatePTRTimeout)
			defer cancel()
			if names, err := s.resolver.LookupAddr(ptrCtx, ipStr); err == nil {
				ptrs := cleanPTRs(names)
				if len(ptrs) > 0 {
					res.PTR = ptrs[0]
					res.PTRs = ptrs
				}
			}
			s.putCachedIP(ipStr, res, s.emptyTTL)
		}
		return cloneResult(res), nil
	}

	lookupCtx, cancel := context.WithTimeout(ctx, publicTimeout)
	defer cancel()

	var (
		ptrs     []string
		asn      int
		prefix   string
		country  string
		registry string
		org      string
	)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		names, err := s.lookupAddrWithRetry(lookupCtx, ipStr)
		if err == nil {
			ptrs = cleanPTRs(names)
		}
	}()

	go func() {
		defer wg.Done()
		originName := CymruOriginQuery(addr)
		if originName == "" {
			return
		}
		txts, err := s.lookupTXTWithRetry(lookupCtx, originName)
		if err != nil {
			return
		}
		for _, txt := range txts {
			a, p, cc, reg, ok := ParseCymruOriginTXT(txt)
			if ok {
				asn, prefix, country, registry = a, p, cc, reg
				break
			}
		}
		if asn <= 0 {
			return
		}
		if entry, ok := s.getCachedAS(asn); ok {
			org = entry.org
			if country == "" {
				country = entry.country
			}
			if registry == "" {
				registry = entry.registry
			}
			return
		}

		asName := fmt.Sprintf("AS%d.asn.cymru.com", asn)
		asTXTs, err := s.lookupTXTWithRetry(lookupCtx, asName)
		if err != nil {
			return
		}
		for _, txt := range asTXTs {
			o, cc, reg := ParseCymruASNTXT(txt)
			if o != "" || cc != "" || reg != "" {
				org = o
				if country == "" {
					country = cc
				}
				if registry == "" {
					registry = reg
				}
				s.putCachedAS(asn, asEntry{
					org:      o,
					country:  cc,
					registry: reg,
				})
				break
			}
		}
	}()

	wg.Wait()

	if len(ptrs) > 0 {
		res.PTR = ptrs[0]
		res.PTRs = ptrs
	}
	res.ASN = asn
	res.Prefix = prefix
	res.Country = country
	res.Registry = registry
	res.Organization = org

	ttl := s.emptyTTL
	if res.PTR != "" || res.ASN > 0 {
		ttl = s.ttl
	}
	s.putCachedIP(ipStr, res, ttl)

	return cloneResult(res), nil
}

func isTimeoutError(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func waitRetryDelay(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Service) lookupTXTWithRetry(ctx context.Context, name string) ([]string, error) {
	txts, err := s.resolver.LookupTXT(ctx, name)
	if err == nil || !isTimeoutError(err) || ctx.Err() != nil {
		return txts, err
	}
	if !waitRetryDelay(ctx, 150*time.Millisecond) {
		return nil, ctx.Err()
	}
	return s.resolver.LookupTXT(ctx, name)
}

func (s *Service) lookupAddrWithRetry(ctx context.Context, addr string) ([]string, error) {
	names, err := s.resolver.LookupAddr(ctx, addr)
	if err == nil || !isTimeoutError(err) || ctx.Err() != nil {
		return names, err
	}
	if !waitRetryDelay(ctx, 150*time.Millisecond) {
		return nil, ctx.Err()
	}
	return s.resolver.LookupAddr(ctx, addr)
}

func cleanPTRs(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		trimmed := strings.TrimSuffix(strings.TrimSpace(n), ".")
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if _, dup := seen[lower]; dup {
			continue
		}
		seen[lower] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func cloneResult(r Result) Result {
	if len(r.PTRs) > 0 {
		cp := make([]string, len(r.PTRs))
		copy(cp, r.PTRs)
		r.PTRs = cp
	}
	return r
}

func (s *Service) getCachedIP(ip string) (Result, bool) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.ipCache[ip]
	if !ok {
		return Result{}, false
	}
	if now.After(entry.expiresAt) {
		delete(s.ipCache, ip)
		return Result{}, false
	}
	return cloneResult(entry.result), true
}

func (s *Service) putCachedIP(ip string, res Result, ttl time.Duration) {
	now := s.now()
	stored := cloneResult(res)
	stored.Cached = false

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.ipCache[ip]; !exists && len(s.ipCache) >= s.maxEntries {
		evictCache(s.ipCache, now, s.maxEntries, func(e cacheEntry) time.Time {
			return e.expiresAt
		})
	}
	s.ipCache[ip] = cacheEntry{
		result:    stored,
		expiresAt: now.Add(ttl),
	}
}

func (s *Service) getCachedAS(asn int) (asEntry, bool) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.asCache[asn]
	if !ok {
		return asEntry{}, false
	}
	if now.After(entry.expiresAt) {
		delete(s.asCache, asn)
		return asEntry{}, false
	}
	return entry, true
}

func (s *Service) putCachedAS(asn int, entry asEntry) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.asCache[asn]; !exists && len(s.asCache) >= s.maxEntries {
		evictCache(s.asCache, now, s.maxEntries, func(e asEntry) time.Time {
			return e.expiresAt
		})
	}
	entry.expiresAt = now.Add(s.asTTL)
	s.asCache[asn] = entry
}

func evictCache[K comparable, V any](m map[K]V, now time.Time, maxEntries int, exp func(V) time.Time) {
	for k, v := range m {
		if now.After(exp(v)) {
			delete(m, k)
		}
	}
	if len(m) < maxEntries {
		return
	}
	type item struct {
		key K
		exp time.Time
	}
	items := make([]item, 0, len(m))
	for k, v := range m {
		items = append(items, item{key: k, exp: exp(v)})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].exp.Before(items[j].exp)
	})
	removeCount := len(m) - maxEntries + 1
	for i := 0; i < removeCount && i < len(items); i++ {
		delete(m, items[i].key)
	}
}
