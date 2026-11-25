package firc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// FextCloudIpsetName is the dedicated FIRC-owned ipset for KeenDNS / Keenetic Cloud destinations.
	// Using a distinct FIRC-owned name avoids ownership collisions with foreign or legacy AWGM-CLOUD sets.
	FextCloudIpsetName        = "FEXT_CLOUD"
	fextCloudStagingIpsetName = "FEXT_CLOUD_TMP"
	fextCloudIpsetMaxElem     = 4096

	keenCloudSyncInterval  = 60 * time.Second
	keenCloudRetryInterval = 15 * time.Second
	keenCloudSyncTimeout   = 10 * time.Second
	keenCloudRCITimeout    = 3 * time.Second
	keenCloudDNSTimeout    = 3 * time.Second
	keenCloudIpsetTimeout  = 5 * time.Second
	keenCloudDNSWorkers    = 4
	keenCloudStaleTTL      = 30 * time.Minute
	keenCloudMaxLKGTTL     = 2 * time.Hour
	keenCloudMaxCachedIPs  = 512
)

type cloudSourceEntry struct {
	ips          map[string]time.Time
	lkgExpiresAt time.Time
}

type cloudNegBackoffEntry struct {
	step         int
	nextLookupAt time.Time
}

var keenCloudNegBackoffSteps = []time.Duration{
	2 * time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	30 * time.Minute,
}

// keenDNSCloudBaseDomains lists static Keenetic/Netcraze cloud broker and master endpoints
// resolved through the router's standard DNS resolver (never redirected to FakeIP).
var keenDNSCloudBaseDomains = []string{
	"broker.keenetic.cloud",
	"broker.netcraze.cloud",
	"cloud.keenetic.net",
	"master.keenetic.cloud",
	"ea.master.keenetic.cloud",
	"master.netcraze.cloud",
	"ea.master.netcraze.cloud",
}

// disallowedCloudIPv4Prefixes rejects bogon, loopback, RFC1918, CGNAT, link-local,
// FakeIP (198.18.0.0/15), KeenDNS local portal (198.51.100.0/24), and multicast/reserved ranges from entering FEXT_CLOUD.
var disallowedCloudIPv4Prefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("224.0.0.0/3"),
}

// KeenCloudRelaySnapshot is the narrow projection of NDMS /show/ndns state consumed by FIRC.
type KeenCloudRelaySnapshot struct {
	Enabled      bool
	RelayIPs     []string
	RelayDomains []string
}

// KeenCloudRelayProvider queries current active KeenDNS cloud relay IPs and domains.
type KeenCloudRelayProvider func(ctx context.Context) (KeenCloudRelaySnapshot, error)

// ExtIpsetRunner executes ipset subcommands directly without a shell, optionally feeding stdin.
type ExtIpsetRunner func(ctx context.Context, stdin string, args ...string) ([]byte, error)

var (
	extIpsetMu      sync.RWMutex
	extIpsetRunner  ExtIpsetRunner = defaultExtIpsetRunner
	extIpsetCustom  bool
	extCloudDNSMu   sync.RWMutex
	extCloudDNSFn   = defaultExtDNSLookupIPv4
	extCloudClockMu sync.RWMutex
	extCloudNowFn   = time.Now
	pkgCloudIpsetMu sync.Mutex
)

func defaultExtIpsetRunner(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ipset", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	return cmd.CombinedOutput()
}

func defaultExtDNSLookupIPv4(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
}

// SetExtIpsetRunner overrides the ipset command runner used by FIRC cloud sync (for tests).
func SetExtIpsetRunner(fn ExtIpsetRunner) {
	extIpsetMu.Lock()
	if fn == nil {
		extIpsetRunner = defaultExtIpsetRunner
		extIpsetCustom = false
	} else {
		extIpsetRunner = fn
		extIpsetCustom = true
	}
	extIpsetMu.Unlock()
}

func hasCustomExtIpsetRunner() bool {
	extIpsetMu.RLock()
	custom := extIpsetCustom
	extIpsetMu.RUnlock()
	return custom
}

func runExtIpset(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	extIpsetMu.RLock()
	fn := extIpsetRunner
	extIpsetMu.RUnlock()
	return fn(ctx, stdin, args...)
}

// SetExtDNSLookupIPv4 overrides IPv4 DNS resolution used during KeenDNS cloud sync (for tests).
func SetExtDNSLookupIPv4(fn func(ctx context.Context, host string) ([]netip.Addr, error)) {
	extCloudDNSMu.Lock()
	if fn == nil {
		extCloudDNSFn = defaultExtDNSLookupIPv4
	} else {
		extCloudDNSFn = fn
	}
	extCloudDNSMu.Unlock()
}

func lookupCloudIPv4(ctx context.Context, host string) ([]netip.Addr, error) {
	extCloudDNSMu.RLock()
	fn := extCloudDNSFn
	extCloudDNSMu.RUnlock()
	return fn(ctx, host)
}

func setExtCloudNow(fn func() time.Time) {
	extCloudClockMu.Lock()
	if fn == nil {
		extCloudNowFn = time.Now
	} else {
		extCloudNowFn = fn
	}
	extCloudClockMu.Unlock()
}

func cloudNow() time.Time {
	extCloudClockMu.RLock()
	fn := extCloudNowFn
	extCloudClockMu.RUnlock()
	return fn()
}

func isCloudIpsetRuntimeEnabled(op *Operator) bool {
	if hasCustomExtIpsetRunner() {
		return true
	}
	return !hasCustomExtIptablesRunner() && runtime.GOOS == "linux" && (op == nil || op.isSystemConfigDir())
}

func isCloudIpsetDestroyEnabled() bool {
	if hasCustomExtIpsetRunner() {
		return true
	}
	return !hasCustomExtIptablesRunner() && runtime.GOOS == "linux"
}

func isMissingIpsetError(out []byte, err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(strings.TrimSpace(string(out)))
	return strings.Contains(s, "does not exist") ||
		strings.Contains(s, "no such set") ||
		strings.Contains(s, "cannot find")
}

func cloudIpsetExists(ctx context.Context) bool {
	pkgCloudIpsetMu.Lock()
	defer pkgCloudIpsetMu.Unlock()
	return cloudIpsetExistsLocked(ctx)
}

func cloudIpsetExistsLocked(ctx context.Context) bool {
	if !isCloudIpsetDestroyEnabled() {
		return false
	}
	_, err := runExtIpset(ctx, "", "list", FextCloudIpsetName, "-n")
	return err == nil
}

// ValidateCloudIPv4Entry validates and canonicalizes a single public/routable IPv4 host or CIDR
// suitable for insertion into an IPv4 hash:net ipset (FEXT_CLOUD). It rejects IPv6, IPv4-in-IPv6,
// shell characters, overly broad CIDRs (< /8), loopback, RFC1918, CGNAT, FakeIP (198.18.0.0/15),
// link-local, unspecified, and multicast/reserved ranges.
func ValidateCloudIPv4Entry(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > 32 {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && c != '.' && c != '/' {
			return "", false
		}
	}
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil || !p.IsValid() || !p.Addr().Is4() || p.Addr().Is4In6() {
			return "", false
		}
		if p.Bits() < 8 || p.Bits() > 32 {
			return "", false
		}
		p = p.Masked()
		if prefixOverlapsDisallowedIPv4(p) {
			return "", false
		}
		if p.Bits() == 32 {
			return p.Addr().String(), true
		}
		return p.String(), true
	}
	addr, err := netip.ParseAddr(s)
	if err != nil || !addr.IsValid() || !addr.Is4() || addr.Is4In6() {
		return "", false
	}
	p := netip.PrefixFrom(addr, 32)
	if prefixOverlapsDisallowedIPv4(p) {
		return "", false
	}
	return addr.String(), true
}

func prefixOverlapsDisallowedIPv4(p netip.Prefix) bool {
	for _, bad := range disallowedCloudIPv4Prefixes {
		if p.Overlaps(bad) {
			return true
		}
	}
	return false
}

func validateCloudHostname(raw string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(raw, ".")))
	if s == "" || len(s) > 253 || !strings.Contains(s, ".") {
		return "", false
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return "", false
	}
	labels := strings.Split(s, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return "", false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
				return "", false
			}
		}
	}
	tld := labels[len(labels)-1]
	allDigits := true
	for i := 0; i < len(tld); i++ {
		if tld[i] < '0' || tld[i] > '9' {
			allDigits = false
			break
		}
	}
	if allDigits {
		return "", false
	}
	return s, true
}

// SetKeenCloudProvider injects the narrow KeenDNS relay IP/domain provider.
func (o *Operator) SetKeenCloudProvider(fn KeenCloudRelayProvider) {
	o.mu.Lock()
	o.keenCloudProvider = fn
	o.mu.Unlock()
}

// KeenCloudProvider returns the currently registered KeenDNS relay provider (nil if unset).
func (o *Operator) KeenCloudProvider() KeenCloudRelayProvider {
	o.mu.Lock()
	fn := o.keenCloudProvider
	o.mu.Unlock()
	return fn
}

func (o *Operator) keenDNSCloudStatus() (lastErr string, lastSyncAt int64) {
	o.cloudStateMu.RLock()
	defer o.cloudStateMu.RUnlock()
	if !o.cloudLastSyncAt.IsZero() {
		lastSyncAt = o.cloudLastSyncAt.Unix()
	}
	return o.cloudLastSyncErr, lastSyncAt
}

func (o *Operator) clearKeenDNSCloudSyncState() {
	o.cloudStateMu.Lock()
	o.cloudSetReady = false
	o.cloudLastAttemptAt = time.Time{}
	o.cloudLastSyncAt = time.Time{}
	o.cloudLastSyncErr = ""
	o.cloudLastEntries = nil
	o.cloudSourceCache = nil
	o.cloudNegBackoff = nil
	o.keenDNSSyncedActiveIface = ""
	o.cloudStateMu.Unlock()
}

func (o *Operator) cancelActiveCloudSync() {
	if o == nil {
		return
	}
	o.cloudCancelMu.Lock()
	cancel := o.cloudActiveCancel
	o.cloudActiveCancel = nil
	o.cloudActiveID = 0
	o.cloudCancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (o *Operator) abortKeenDNSCloudSync() {
	if o == nil {
		return
	}
	o.cancelActiveCloudSync()
	o.cloudCommitMu.Lock()
	o.cloudEpoch++
	o.clearKeenDNSCloudSyncState()
	o.cloudCommitMu.Unlock()
}

// canCommitCloudSyncLocked must be called with o.cloudCommitMu held whenever o != nil.
func (o *Operator) canCommitCloudSyncLocked(syncCtx context.Context, epoch uint64, expectedCfgGen uint64, checkCfgGen bool, requireEnabled bool) error {
	if syncCtx != nil {
		if err := syncCtx.Err(); err != nil {
			return err
		}
	}
	if o == nil {
		return nil
	}
	if o.ManuallyDisabled() {
		return ErrManuallyDisabled
	}
	if o.cloudEpoch != epoch {
		return context.Canceled
	}
	if checkCfgGen && o.currentExtConfigGen() != expectedCfgGen {
		return context.Canceled
	}
	if requireEnabled || o.hasPersistedExtConfig() {
		ext, curGen := o.loadExtConfigWithGen()
		if (checkCfgGen && curGen != expectedCfgGen) || !extWantsKeenDNS(ext) {
			return context.Canceled
		}
	}
	return nil
}

func normalizeAbortedCloudSyncErr(callerCtx context.Context, o *Operator, err error) error {
	if callerCtx != nil && callerCtx.Err() != nil {
		return callerCtx.Err()
	}
	if o != nil && o.ManuallyDisabled() {
		return ErrManuallyDisabled
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// SyncKeenDNSCloud refreshes the FIRC-owned FEXT_CLOUD IPv4 ipset from static Keenetic cloud CIDRs,
// the injected NDMS /show/ndns relay provider, and DNS resolution of static/relay cloud domains.
//
// Stale-entry and error semantics:
//   - Only one sync runs at a time (single-flight via cloudSyncMu; non-forced periodic ticks skip if busy).
//   - If ManuallyDisabled(), disabled via ExtConfig, or ctx is cancelled, sync aborts before mutating any kernel/status state.
//   - On RCI query failure or total DNS resolution failure on cold start (no prior cached sources),
//     any pre-existing FEXT_CLOUD set is preserved untouched and the error is recorded in lastSyncError.
//   - A confirmed empty RCI result (Enabled=false or empty RelayIPs/RelayDomains) is NOT an error.
//   - Validated dynamic IPv4 entries have independent per-source TTLs: normal stale TTL (30m) on clean/
//     confirmed-empty/negative answers, and bounded LKG TTL (2h) on transient DNS or RCI errors. Every source
//     ("rci", "relay:<host>", "base:<host>") expires on its own deadline even during total DNS or RCI outages.
//   - Updates to FEXT_CLOUD use staging set FEXT_CLOUD_TMP + ipset restore + ipset swap (zero empty gap),
//     serialized under cloudCommitMu + pkgCloudIpsetMu with teardown; during partial/total outages where the
//     active entry set did not change, the kernel ipset is left untouched without redundant swaps.
func (o *Operator) SyncKeenDNSCloud(ctx context.Context, force bool) error {
	return o.syncKeenDNSCloudWithGen(ctx, force, 0, false)
}

func (o *Operator) syncKeenDNSCloudWithGen(ctx context.Context, force bool, expectedCfgGen uint64, requireEnabled bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := o.CheckNotForeign(); err != nil {
		return err
	}
	if o.ManuallyDisabled() {
		return ErrManuallyDisabled
	}
	if !isCloudIpsetRuntimeEnabled(o) {
		return nil
	}

	if force {
		o.cloudSyncMu.Lock()
	} else if !o.cloudSyncMu.TryLock() {
		return nil
	}
	defer o.cloudSyncMu.Unlock()

	now := cloudNow()

	o.cloudCommitMu.Lock()
	cfgGen := expectedCfgGen
	if expectedCfgGen == 0 {
		cfgGen = o.currentExtConfigGen()
	}
	myEpoch := o.cloudEpoch
	if err := o.canCommitCloudSyncLocked(ctx, myEpoch, cfgGen, true, requireEnabled); err != nil {
		o.cloudCommitMu.Unlock()
		return normalizeAbortedCloudSyncErr(ctx, o, err)
	}

	o.cloudStateMu.RLock()
	lastAttempt := o.cloudLastAttemptAt
	lastErr := o.cloudLastSyncErr
	o.cloudStateMu.RUnlock()

	minInterval := keenCloudSyncInterval
	if lastErr != "" {
		minInterval = keenCloudRetryInterval
	}
	if !force && !lastAttempt.IsZero() && now.Sub(lastAttempt) < minInterval {
		o.cloudCommitMu.Unlock()
		return nil
	}

	o.cloudStateMu.Lock()
	o.cloudLastAttemptAt = now
	o.cloudStateMu.Unlock()

	var syncCtx context.Context
	var syncCancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		syncCtx, syncCancel = context.WithTimeout(ctx, keenCloudSyncTimeout)
	} else {
		syncCtx, syncCancel = context.WithCancel(ctx)
	}
	o.cloudCancelMu.Lock()
	o.cloudActiveID++
	myID := o.cloudActiveID
	o.cloudActiveCancel = syncCancel
	o.cloudCancelMu.Unlock()
	o.cloudCommitMu.Unlock()

	defer func() {
		o.cloudCancelMu.Lock()
		if o.cloudActiveID == myID {
			o.cloudActiveID = 0
			o.cloudActiveCancel = nil
		}
		o.cloudCancelMu.Unlock()
		syncCancel()
	}()

	// 1. Query RCI relay snapshot (if provider is wired).
	// On RCI failure, we record the error and continue to per-source DNS lookup
	// and per-source bounded LKG / TTL expiration rather than short-circuiting.
	provider := o.KeenCloudProvider()
	var snap KeenCloudRelaySnapshot
	var rciErr error
	if provider != nil {
		rciCtx, rciCancel := context.WithTimeout(syncCtx, keenCloudRCITimeout)
		snap, rciErr = provider(rciCtx)
		rciCancel()
	}
	if err := syncCtx.Err(); err != nil {
		return normalizeAbortedCloudSyncErr(ctx, o, err)
	}

	// 2. Collect validated static CIDRs + RCI relay IPs + per-source hostnames.
	desiredSet := make(map[string]struct{}, len(keenDNSCloudSubnets)+64)
	for _, rawCIDR := range keenDNSCloudSubnets {
		if entry, ok := ValidateCloudIPv4Entry(rawCIDR); ok {
			desiredSet[entry] = struct{}{}
		}
	}

	validRelayIPs := make([]string, 0, len(snap.RelayIPs))
	if rciErr == nil {
		for _, rawIP := range snap.RelayIPs {
			if entry, ok := ValidateCloudIPv4Entry(rawIP); ok {
				validRelayIPs = append(validRelayIPs, entry)
			}
		}
	}

	domainTargets := make([]cloudDomainTarget, 0, len(keenDNSCloudBaseDomains)+len(snap.RelayDomains))
	domainSeen := make(map[string]struct{}, len(keenDNSCloudBaseDomains)+len(snap.RelayDomains))
	addDomainTarget := func(raw string, isOptional bool) {
		if rciErr == nil {
			if entry, ok := ValidateCloudIPv4Entry(raw); ok {
				validRelayIPs = append(validRelayIPs, entry)
				return
			}
		}
		if host, ok := validateCloudHostname(raw); ok {
			if _, exists := domainSeen[host]; !exists {
				domainSeen[host] = struct{}{}
				prefix := "relay:"
				if isOptional {
					prefix = "base:"
				}
				domainTargets = append(domainTargets, cloudDomainTarget{
					host:       host,
					sourceKey:  prefix + host,
					isOptional: isOptional,
				})
			}
		}
	}
	if rciErr == nil && snap.Enabled {
		for _, d := range snap.RelayDomains {
			addDomainTarget(d, false)
		}
	}
	for _, d := range keenDNSCloudBaseDomains {
		addDomainTarget(d, true)
	}

	o.cloudStateMu.RLock()
	hadPriorState := o.cloudSetReady || len(o.cloudSourceCache) > 0
	nextCache := cloneCloudSourceCache(o.cloudSourceCache)
	nextNeg := make(map[string]cloudNegBackoffEntry, len(o.cloudNegBackoff))
	for k, v := range o.cloudNegBackoff {
		nextNeg[k] = v
	}
	o.cloudStateMu.RUnlock()

	lookupTargets := make([]cloudDomainTarget, 0, len(domainTargets))
	for _, target := range domainTargets {
		if !target.isOptional {
			delete(nextNeg, target.host)
			lookupTargets = append(lookupTargets, target)
			continue
		}
		if neg, exists := nextNeg[target.host]; exists && now.Before(neg.nextLookupAt) {
			continue
		}
		lookupTargets = append(lookupTargets, target)
	}

	// 3. Resolve active cloud and relay hostnames with bounded concurrency (system resolver -> ndnproxy, never FakeIP).
	results := resolveCloudDomainTargetsIPv4(syncCtx, lookupTargets)
	if err := syncCtx.Err(); err != nil {
		return normalizeAbortedCloudSyncErr(ctx, o, err)
	}

	var transientErrs []error
	if rciErr != nil {
		transientErrs = append(transientErrs, fmt.Errorf("ndms keendns query: %w", rciErr))
	}

	dnsSuccessCount := 0
	dnsTransientCount := 0
	touchedSources := make(map[string]struct{}, len(domainTargets)+1)

	for _, res := range results {
		target := res.target
		touchedSources[target.sourceKey] = struct{}{}

		if target.isOptional {
			isNeg := (res.err == nil && len(res.ips) == 0) || isNegativeCloudDNSError(res.err)
			if res.err == nil && len(res.ips) > 0 {
				dnsSuccessCount++
				delete(nextNeg, target.host)
				updateCloudSourceIPsLocked(nextCache, target.sourceKey, res.ips, now)
				continue
			}
			if isNeg {
				prev := nextNeg[target.host]
				idx := prev.step
				if idx < 0 {
					idx = 0
				}
				if idx >= len(keenCloudNegBackoffSteps) {
					idx = len(keenCloudNegBackoffSteps) - 1
				}
				nextNeg[target.host] = cloudNegBackoffEntry{
					step:         prev.step + 1,
					nextLookupAt: now.Add(keenCloudNegBackoffSteps[idx]),
				}
				pruneCloudSourceNormalTTLLocked(nextCache, target.sourceKey, now)
				continue
			}
			dnsTransientCount++
			transientErrs = append(transientErrs, fmt.Errorf("%s: %w", target.host, res.err))
			pruneCloudSourceBoundedLKGLocked(nextCache, target.sourceKey, now)
			continue
		}

		// Explicit RCI relay domains: negative or empty answers still count as transient relay sync failures.
		if res.err == nil && len(res.ips) > 0 {
			dnsSuccessCount++
			updateCloudSourceIPsLocked(nextCache, target.sourceKey, res.ips, now)
			continue
		}
		dnsTransientCount++
		errVal := res.err
		if errVal == nil {
			errVal = errors.New("no valid IPv4 addresses")
		}
		transientErrs = append(transientErrs, fmt.Errorf("%s: %w", target.host, errVal))
		pruneCloudSourceBoundedLKGLocked(nextCache, target.sourceKey, now)
	}

	// 4. Reconcile "rci" source and untouched cached sources per independent source lifetime.
	// Unlike an all-or-nothing short-circuit, each source expires on its own TTL/LKG deadline
	// even when RCI or every DNS upstream is failing simultaneously.
	touchedSources["rci"] = struct{}{}
	if rciErr == nil {
		updateCloudSourceIPsLocked(nextCache, "rci", validRelayIPs, now)
	} else {
		pruneCloudSourceBoundedLKGLocked(nextCache, "rci", now)
	}
	for srcKey := range nextCache {
		if _, ok := touchedSources[srcKey]; ok {
			continue
		}
		if rciErr != nil && strings.HasPrefix(srcKey, "relay:") {
			pruneCloudSourceBoundedLKGLocked(nextCache, srcKey, now)
		} else {
			pruneCloudSourceNormalTTLLocked(nextCache, srcKey, now)
		}
	}

	trimCloudSourceCacheLocked(nextCache, keenCloudMaxCachedIPs)
	for _, entry := range nextCache {
		for ip := range entry.ips {
			desiredSet[ip] = struct{}{}
		}
	}

	partialErr := errors.Join(transientErrs...)
	errMsg := ""
	if partialErr != nil {
		errMsg = formatCloudSyncPartialError(rciErr, partialErr, dnsSuccessCount, dnsTransientCount, len(validRelayIPs))
	}

	// On cold start (no prior synced state or cached dynamic sources), avoid creating or
	// overwriting FEXT_CLOUD with static-only CIDRs if RCI or all DNS queries failed.
	if !hadPriorState && partialErr != nil && len(nextCache) == 0 && (rciErr != nil || (len(lookupTargets) > 0 && dnsSuccessCount == 0 && dnsTransientCount > 0)) {
		o.cloudCommitMu.Lock()
		defer o.cloudCommitMu.Unlock()
		if cErr := o.canCommitCloudSyncLocked(syncCtx, myEpoch, cfgGen, true, requireEnabled); cErr != nil {
			return normalizeAbortedCloudSyncErr(ctx, o, cErr)
		}
		pkgCloudIpsetMu.Lock()
		exists := cloudIpsetExistsLocked(syncCtx)
		pkgCloudIpsetMu.Unlock()
		o.cloudStateMu.Lock()
		o.cloudSetReady = exists
		o.cloudNegBackoff = nextNeg
		o.cloudLastSyncErr = errMsg
		o.cloudStateMu.Unlock()
		return fmt.Errorf("%s", errMsg)
	}

	entries := make([]string, 0, len(desiredSet))
	for entry := range desiredSet {
		entries = append(entries, entry)
	}
	sort.Strings(entries)

	// 5. Serialize generation verification, atomic ipset commit, and status publication with teardown.
	o.cloudCommitMu.Lock()
	defer o.cloudCommitMu.Unlock()

	if cErr := o.canCommitCloudSyncLocked(syncCtx, myEpoch, cfgGen, true, requireEnabled); cErr != nil {
		return normalizeAbortedCloudSyncErr(ctx, o, cErr)
	}

	o.cloudStateMu.RLock()
	sameEntries := o.cloudSetReady && equalStringSlices(entries, o.cloudLastEntries)
	o.cloudStateMu.RUnlock()

	pkgCloudIpsetMu.Lock()
	skipSwap := partialErr != nil && sameEntries && cloudIpsetExistsLocked(syncCtx)
	if !skipSwap {
		aborted, err := applyCloudIpsetAtomicLocked(syncCtx, o, myEpoch, cfgGen, requireEnabled, entries)
		if err != nil {
			if aborted {
				pkgCloudIpsetMu.Unlock()
				return normalizeAbortedCloudSyncErr(ctx, o, err)
			}
			if cErr := o.canCommitCloudSyncLocked(syncCtx, myEpoch, cfgGen, true, requireEnabled); cErr != nil {
				pkgCloudIpsetMu.Unlock()
				return normalizeAbortedCloudSyncErr(ctx, o, cErr)
			}
			swapErrMsg := fmt.Sprintf("cloud ipset sync: %v", err)
			exists := cloudIpsetExistsLocked(syncCtx)
			pkgCloudIpsetMu.Unlock()
			o.cloudStateMu.Lock()
			o.cloudSetReady = exists
			o.cloudLastSyncErr = swapErrMsg
			o.cloudStateMu.Unlock()
			return fmt.Errorf("%s", swapErrMsg)
		}
	}
	pkgCloudIpsetMu.Unlock()

	o.cloudStateMu.Lock()
	o.cloudSourceCache = nextCache
	o.cloudNegBackoff = nextNeg
	o.cloudSetReady = true
	o.cloudLastEntries = append([]string(nil), entries...)
	if !skipSwap || dnsSuccessCount > 0 || (rciErr == nil && len(validRelayIPs) > 0) {
		o.cloudLastSyncAt = now
	}
	o.cloudLastSyncErr = errMsg
	o.cloudStateMu.Unlock()
	if partialErr != nil {
		return fmt.Errorf("%s", errMsg)
	}
	return nil
}

type cloudDomainTarget struct {
	host       string
	sourceKey  string
	isOptional bool
}

type cloudDomainResult struct {
	target cloudDomainTarget
	ips    []string
	err    error
}

func resolveCloudDomainTargetsIPv4(ctx context.Context, targets []cloudDomainTarget) []cloudDomainResult {
	if len(targets) == 0 {
		return nil
	}
	jobs := make(chan cloudDomainTarget, len(targets))
	results := make(chan cloudDomainResult, len(targets))

	workers := keenCloudDNSWorkers
	if len(targets) < workers {
		workers = len(targets)
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for target := range jobs {
				if ctx.Err() != nil {
					results <- cloudDomainResult{target: target, err: ctx.Err()}
					continue
				}
				dCtx, cancel := context.WithTimeout(ctx, keenCloudDNSTimeout)
				addrs, err := lookupCloudIPv4(dCtx, target.host)
				cancel()
				if err != nil {
					results <- cloudDomainResult{target: target, err: err}
					continue
				}
				var valid []string
				for _, addr := range addrs {
					if !addr.IsValid() || !addr.Is4() || addr.Is4In6() {
						continue
					}
					if entry, ok := ValidateCloudIPv4Entry(addr.String()); ok {
						valid = append(valid, entry)
					}
				}
				results <- cloudDomainResult{target: target, ips: valid}
			}
		}()
	}

	for _, t := range targets {
		jobs <- t
	}
	close(jobs)
	wg.Wait()
	close(results)

	out := make([]cloudDomainResult, 0, len(targets))
	for res := range results {
		out = append(out, res)
	}
	return out
}

func resolveCloudDomainsIPv4(ctx context.Context, domains []string) (resolved []string, successCount int, errs []error) {
	targets := make([]cloudDomainTarget, 0, len(domains))
	for _, d := range domains {
		targets = append(targets, cloudDomainTarget{host: d, sourceKey: d})
	}
	results := resolveCloudDomainTargetsIPv4(ctx, targets)
	seen := make(map[string]struct{})
	for _, res := range results {
		if res.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", res.target.host, res.err))
			continue
		}
		successCount++
		for _, ip := range res.ips {
			if _, ok := seen[ip]; !ok {
				seen[ip] = struct{}{}
				resolved = append(resolved, ip)
			}
		}
	}
	return resolved, successCount, errs
}

func isNegativeCloudDNSError(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return true
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "no such host") ||
		strings.Contains(low, "nxdomain") ||
		strings.Contains(low, "nodata")
}

func cloneCloudSourceCache(src map[string]*cloudSourceEntry) map[string]*cloudSourceEntry {
	out := make(map[string]*cloudSourceEntry, len(src)+8)
	for srcKey, entry := range src {
		if entry == nil {
			continue
		}
		ips := make(map[string]time.Time, len(entry.ips))
		for ip, exp := range entry.ips {
			ips[ip] = exp
		}
		out[srcKey] = &cloudSourceEntry{
			ips:          ips,
			lkgExpiresAt: entry.lkgExpiresAt,
		}
	}
	return out
}

func formatCloudSyncPartialError(rciErr error, partialErr error, dnsSuccessCount, dnsTransientCount, validRelayIPCount int) string {
	if rciErr != nil && dnsTransientCount == 0 {
		return fmt.Sprintf("ndms keendns query: %v", rciErr)
	}
	if rciErr == nil && dnsSuccessCount == 0 && validRelayIPCount == 0 && dnsTransientCount > 0 {
		return fmt.Sprintf("cloud dns resolve: %v", partialErr)
	}
	return fmt.Sprintf("cloud dns resolve (partial): %v", partialErr)
}

func updateCloudSourceIPsLocked(cache map[string]*cloudSourceEntry, srcKey string, validIPs []string, now time.Time) {
	entry := cache[srcKey]
	if entry == nil {
		if len(validIPs) == 0 {
			return
		}
		entry = &cloudSourceEntry{ips: make(map[string]time.Time, len(validIPs))}
		cache[srcKey] = entry
	}
	for ip, exp := range entry.ips {
		if !exp.After(now) {
			delete(entry.ips, ip)
		}
	}
	normalExp := now.Add(keenCloudStaleTTL)
	for _, ip := range validIPs {
		entry.ips[ip] = normalExp
	}
	if len(validIPs) > 0 {
		entry.lkgExpiresAt = now.Add(keenCloudMaxLKGTTL)
	}
	if len(entry.ips) == 0 {
		delete(cache, srcKey)
	}
}

func pruneCloudSourceNormalTTLLocked(cache map[string]*cloudSourceEntry, srcKey string, now time.Time) {
	entry := cache[srcKey]
	if entry == nil {
		return
	}
	for ip, exp := range entry.ips {
		if !exp.After(now) {
			delete(entry.ips, ip)
		}
	}
	if len(entry.ips) == 0 {
		delete(cache, srcKey)
	}
}

func pruneCloudSourceBoundedLKGLocked(cache map[string]*cloudSourceEntry, srcKey string, now time.Time) {
	entry := cache[srcKey]
	if entry == nil {
		return
	}
	if !entry.lkgExpiresAt.After(now) {
		delete(cache, srcKey)
	}
}

func trimCloudSourceCacheLocked(cache map[string]*cloudSourceEntry, maxEntries int) {
	type pair struct {
		srcKey string
		ip     string
		exp    time.Time
	}
	var items []pair
	for srcKey, entry := range cache {
		if entry == nil {
			continue
		}
		for ip, exp := range entry.ips {
			items = append(items, pair{srcKey: srcKey, ip: ip, exp: exp})
		}
	}
	if len(items) <= maxEntries {
		return
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].exp.Equal(items[j].exp) {
			if items[i].srcKey == items[j].srcKey {
				return items[i].ip < items[j].ip
			}
			return items[i].srcKey < items[j].srcKey
		}
		return items[i].exp.Before(items[j].exp)
	})
	drop := len(items) - maxEntries
	for i := 0; i < drop; i++ {
		if entry := cache[items[i].srcKey]; entry != nil {
			delete(entry.ips, items[i].ip)
			if len(entry.ips) == 0 {
				delete(cache, items[i].srcKey)
			}
		}
	}
}

// applyCloudIpsetAtomicLocked must be called with pkgCloudIpsetMu (and o.cloudCommitMu whenever o != nil) held.
func applyCloudIpsetAtomicLocked(syncCtx context.Context, o *Operator, epoch uint64, cfgGen uint64, requireEnabled bool, entries []string) (bool, error) {
	if err := o.canCommitCloudSyncLocked(syncCtx, epoch, cfgGen, true, requireEnabled); err != nil {
		return true, err
	}
	ipsetCtx, cancel := context.WithTimeout(syncCtx, keenCloudIpsetTimeout)
	defer cancel()

	var script strings.Builder
	fmt.Fprintf(&script, "create %s hash:net family inet hashsize 1024 maxelem %d -exist\n", fextCloudStagingIpsetName, fextCloudIpsetMaxElem)
	fmt.Fprintf(&script, "flush %s\n", fextCloudStagingIpsetName)
	for _, entry := range entries {
		if valid, ok := ValidateCloudIPv4Entry(entry); ok {
			fmt.Fprintf(&script, "add %s %s -exist\n", fextCloudStagingIpsetName, valid)
		}
	}

	if out, err := runExtIpset(ipsetCtx, script.String(), "restore", "-exist"); err != nil {
		cleanupStagingCloudIpset(ipsetCtx)
		if cErr := o.canCommitCloudSyncLocked(syncCtx, epoch, cfgGen, true, requireEnabled); cErr != nil {
			return true, cErr
		}
		return false, fmt.Errorf("restore %s: %w (%s)", fextCloudStagingIpsetName, err, strings.TrimSpace(string(out)))
	}

	if err := o.canCommitCloudSyncLocked(syncCtx, epoch, cfgGen, true, requireEnabled); err != nil {
		cleanupStagingCloudIpset(syncCtx)
		return true, err
	}

	// Ensure target FIRC-owned set exists right before swap after re-checking generation/enable state.
	if out, err := runExtIpset(ipsetCtx, "", "create", FextCloudIpsetName,
		"hash:net", "family", "inet", "hashsize", "1024", "maxelem", fmt.Sprintf("%d", fextCloudIpsetMaxElem), "-exist"); err != nil {
		cleanupStagingCloudIpset(ipsetCtx)
		if cErr := o.canCommitCloudSyncLocked(syncCtx, epoch, cfgGen, true, requireEnabled); cErr != nil {
			return true, cErr
		}
		return false, fmt.Errorf("create %s: %w (%s)", FextCloudIpsetName, err, strings.TrimSpace(string(out)))
	}

	if err := o.canCommitCloudSyncLocked(syncCtx, epoch, cfgGen, true, requireEnabled); err != nil {
		cleanupStagingCloudIpset(syncCtx)
		if o != nil {
			ext, _ := o.loadExtConfigWithGen()
			if (requireEnabled || o.hasPersistedExtConfig()) && !extWantsKeenDNS(ext) {
				_ = destroyOwnedCloudIpsetsLocked(context.WithoutCancel(syncCtx))
			}
		}
		return true, err
	}

	if out, err := runExtIpset(ipsetCtx, "", "swap", fextCloudStagingIpsetName, FextCloudIpsetName); err != nil {
		cleanupStagingCloudIpset(ipsetCtx)
		if cErr := o.canCommitCloudSyncLocked(syncCtx, epoch, cfgGen, true, requireEnabled); cErr != nil {
			return true, cErr
		}
		return false, fmt.Errorf("swap %s -> %s: %w (%s)", fextCloudStagingIpsetName, FextCloudIpsetName, err, strings.TrimSpace(string(out)))
	}

	cleanupStagingCloudIpset(ipsetCtx)
	return false, nil
}

func cleanupStagingCloudIpset(ctx context.Context) {
	cCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, _ = runExtIpset(cCtx, "", "destroy", fextCloudStagingIpsetName)
}

func destroyOwnedCloudIpsets(ctx context.Context) error {
	pkgCloudIpsetMu.Lock()
	defer pkgCloudIpsetMu.Unlock()
	return destroyOwnedCloudIpsetsLocked(ctx)
}

func destroyOwnedCloudIpsetsLocked(ctx context.Context) error {
	if !isCloudIpsetDestroyEnabled() {
		return nil
	}
	var destroyErr error
	for _, setName := range []string{fextCloudStagingIpsetName, FextCloudIpsetName} {
		out, err := runExtIpset(ctx, "", "destroy", setName)
		if err != nil && !isMissingIpsetError(out, err) {
			destroyErr = errors.Join(destroyErr, fmt.Errorf("ipset destroy %s: %w (%s)", setName, err, strings.TrimSpace(string(out))))
		}
	}
	return destroyErr
}

// teardownKeenDNSCloudChainAndSet flushes/deletes FIRC's KeenDNS mirror chain (FEXT_c100d001)
// BEFORE destroying FIRC-owned ipsets (FEXT_CLOUD_TMP, FEXT_CLOUD). Foreign sets such as
// AWGM-CLOUD are never modified or destroyed.
func teardownKeenDNSCloudChainAndSet(ctx context.Context) error {
	return (*Operator)(nil).teardownKeenDNSCloudChainAndSet(ctx)
}

func (o *Operator) teardownKeenDNSCloudChainAndSet(ctx context.Context) error {
	if o != nil {
		o.abortKeenDNSCloudSync()
	}
	kdFextChain := "FEXT_" + KeenDNSGroupID
	var teardownErr error
	out, err := runExtIptablesOutput(ctx, "-w", "-t", "mangle", "-S", kdFextChain)
	if err == nil {
		if fErr := runIptables(ctx, "-t", "mangle", "-F", kdFextChain); fErr != nil {
			teardownErr = errors.Join(teardownErr, fErr)
		} else if xErr := runIptables(ctx, "-t", "mangle", "-X", kdFextChain); xErr != nil {
			// Best-effort chain delete: ignore only missing chain or still-referenced error surfaced below.
			if !strings.Contains(strings.ToLower(xErr.Error()), "no chain") &&
				!strings.Contains(strings.ToLower(xErr.Error()), "does not exist") {
				teardownErr = errors.Join(teardownErr, xErr)
			}
		}
	} else if !isMissingIptablesChain(ctx, "mangle", kdFextChain, out, err) {
		teardownErr = errors.Join(teardownErr, fmt.Errorf("iptables -t mangle -S %s: %w (%s)", kdFextChain, err, strings.TrimSpace(string(out))))
	}

	delete(lastGroupChainSigs, KeenDNSGroupID)

	// Never attempt ipset destroy if flushing FEXT_c100d001 failed while rules might still reference FEXT_CLOUD.
	if teardownErr != nil {
		return teardownErr
	}
	if o != nil {
		o.cloudCommitMu.Lock()
		o.cloudEpoch++
		o.clearKeenDNSCloudSyncState()
	}
	pkgCloudIpsetMu.Lock()
	setErr := destroyOwnedCloudIpsetsLocked(ctx)
	pkgCloudIpsetMu.Unlock()
	if o != nil {
		o.cloudCommitMu.Unlock()
	}
	if setErr != nil {
		teardownErr = errors.Join(teardownErr, setErr)
	}
	return teardownErr
}
