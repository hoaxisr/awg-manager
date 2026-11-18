package session

import (
	"crypto/cipher"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/tgwebproxy/tproxy/config"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy/tproxy/frame"
)

var (
	ErrAuthentication = errors.New("authentication failed")
	ErrBackpressure   = errors.New("temporary backpressure")
	ErrLimit          = errors.New("resource limit reached")
	ErrProtocol       = errors.New("protocol error")
	ErrConcurrent     = errors.New("concurrent request")
	ErrClosed         = errors.New("session closed")
)

type CreateResult struct {
	Token   string
	Welcome []byte
	Session *Session
}

type bootstrap struct {
	seq          uint64
	expires      time.Time
	profile      *config.Profile
	issuanceIP   string
	bodyDigest   [sha256.Size]byte
	sessionToken string
	session      *Session
	used         bool
}

type rateState struct {
	tokens float64
	last   time.Time
}

type Metrics struct {
	SessionsCreated     uint64
	SessionsClosed      uint64
	StreamsOpened       uint64
	StreamsRejected     uint64
	BackendDialFailures uint64
	BytesUp             uint64
	BytesDown           uint64
	LimitHits           uint64
}

type Capacity struct {
	Sessions             int
	Streams              int
	BackendDialsInFlight int
	PendingBytes         int64
	PendingItems         int64
}

type profileSnapshot struct {
	profiles           []config.Profile
	capabilityPrefixes map[uint64]struct{}
}

func buildProfileSnapshot(profiles []config.Profile, globalLimits config.Limits) *profileSnapshot {
	cloned := make([]config.Profile, len(profiles))
	copy(cloned, profiles)
	prefixes := make(map[uint64]struct{}, len(cloned))
	for i := range cloned {
		cloned[i].CarrierMode = cloned[i].CarrierMode.WithDefault()
		cloned[i].Limits = cloned[i].Limits.WithDefaults(globalLimits)
		prefixes[capabilityPrefix(cloned[i].Capability[:])] = struct{}{}
	}
	return &profileSnapshot{
		profiles:           cloned,
		capabilityPrefixes: prefixes,
	}
}

func matchSnapshotCapability(snap *profileSnapshot, value []byte) *config.Profile {
	if snap == nil || len(value) != sha256.Size {
		return nil
	}
	if _, ok := snap.capabilityPrefixes[capabilityPrefix(value)]; !ok {
		return nil
	}
	matched := -1
	for i := range snap.profiles {
		equal := subtle.ConstantTimeCompare(value, snap.profiles[i].Capability[:])
		if equal == 1 {
			matched = i
		}
	}
	if matched < 0 {
		return nil
	}
	return &snap.profiles[matched]
}

func isProfileRetained(oldProf *config.Profile, newProfiles []config.Profile, globalLimits config.Limits) bool {
	if oldProf == nil {
		return false
	}
	oldMode := oldProf.CarrierMode.WithDefault()
	oldLimits := oldProf.Limits.WithDefaults(globalLimits)
	for i := range newProfiles {
		p := &newProfiles[i]
		if p.Name == oldProf.Name &&
			p.Backend == oldProf.Backend &&
			p.CarrierMode.WithDefault() == oldMode &&
			p.Limits.WithDefaults(globalLimits) == oldLimits &&
			subtle.ConstantTimeCompare(p.Capability[:], oldProf.Capability[:]) == 1 {
			return true
		}
	}
	return false
}

type Manager struct {
	config      config.Config
	tokenKey    [sha256.Size]byte
	tokenFilter cipher.Block
	// Capabilities are secret, so their leading bytes are an unpredictable
	// prefilter: request scanning compares every window against every profile
	// otherwise. The map hash is seeded per process and compares whole keys, so
	// a lookup reveals nothing short of a full 8-byte match.
	profilesSnap atomic.Pointer[profileSnapshot]

	mu                   sync.Mutex
	bootstrapSeq         uint64
	bootstraps           map[[sha256.Size]byte]*bootstrap
	bootstrapsPerIP      map[string]int
	sessions             map[[sha256.Size]byte]*Session
	closedTokens         map[[sha256.Size]byte]time.Time
	sessionsPerIP        map[string]int
	sessionsPerProfile   map[string]int
	streamsPerProfile    map[string]int
	dialsPerProfile      map[string]int
	bootstrapRate        rateState
	sessionRate          rateState
	streamRate           rateState
	profileSessionRates  map[string]rateState
	profileStreamRates   map[string]rateState
	streamsLive          int
	backendDialsInFlight int
	pendingGlobalCost    int64
	pendingGlobalItems   int64
	closed               bool
	stop                 chan struct{}
	done                 chan struct{}

	sessionsCreated     atomic.Uint64
	sessionsClosed      atomic.Uint64
	streamsOpened       atomic.Uint64
	streamsRejected     atomic.Uint64
	backendDialFailures atomic.Uint64
	bytesUp             atomic.Uint64
	bytesDown           atomic.Uint64
	limitHits           atomic.Uint64
}

func NewManager(value config.Config, tokenKey [sha256.Size]byte) *Manager {
	snap := buildProfileSnapshot(value.Profiles, value.Limits)
	value.Profiles = snap.profiles
	result := &Manager{
		config:              value,
		tokenKey:            tokenKey,
		tokenFilter:         newTokenFilter(tokenKey),
		bootstraps:          make(map[[sha256.Size]byte]*bootstrap),
		bootstrapsPerIP:     make(map[string]int),
		sessions:            make(map[[sha256.Size]byte]*Session),
		closedTokens:        make(map[[sha256.Size]byte]time.Time),
		sessionsPerIP:       make(map[string]int),
		sessionsPerProfile:  make(map[string]int),
		streamsPerProfile:   make(map[string]int),
		dialsPerProfile:     make(map[string]int),
		profileSessionRates: make(map[string]rateState),
		profileStreamRates:  make(map[string]rateState),
		stop:                make(chan struct{}),
		done:                make(chan struct{}),
	}
	result.profilesSnap.Store(snap)
	go result.cleanupLoop()
	return result
}

func capabilityPrefix(value []byte) uint64 {
	return binary.LittleEndian.Uint64(value)
}

func (m *Manager) Profiles() []config.Profile {
	snap := m.profilesSnap.Load()
	if snap == nil {
		return nil
	}
	return snap.profiles
}

func (m *Manager) UpdateProfiles(profiles []config.Profile) error {
	snap := buildProfileSnapshot(profiles, m.config.Limits)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	candidateCfg := m.config
	candidateCfg.Profiles = snap.profiles
	if err := ValidateBudget(candidateCfg); err != nil {
		m.mu.Unlock()
		return err
	}
	m.config.Profiles = snap.profiles
	m.profilesSnap.Store(snap)

	for hash, b := range m.bootstraps {
		if !isProfileRetained(b.profile, snap.profiles, m.config.Limits) {
			m.removeBootstrapLocked(hash, b)
		}
	}

	var revoked []*Session
	for _, s := range m.sessions {
		if !isProfileRetained(s.profile, snap.profiles, m.config.Limits) {
			revoked = append(revoked, s)
		}
	}
	m.mu.Unlock()

	for _, s := range revoked {
		s.Close()
	}
	for _, s := range revoked {
		s.wait()
	}
	return nil
}

func (m *Manager) MatchCapability(value []byte) *config.Profile {
	return matchSnapshotCapability(m.profilesSnap.Load(), value)
}

func (m *Manager) IssueBootstrap(profile *config.Profile, clientIP string) (string, error) {
	if profile == nil {
		return "", ErrAuthentication
	}
	if m.MatchCapability(profile.Capability[:]) == nil {
		return "", ErrAuthentication
	}
	token, hash, err := m.newToken(TokenBootstrap)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", ErrClosed
	}
	snap := m.profilesSnap.Load()
	activeProfile := matchSnapshotCapability(snap, profile.Capability[:])
	if activeProfile == nil || !isProfileRetained(profile, snap.profiles, m.config.Limits) {
		return "", ErrAuthentication
	}
	if m.config.IPGuard != nil && m.config.IPGuard.IsCoolingDown(activeProfile.Name, clientIP) {
		m.limitHits.Add(1)
		return "", ErrAuthentication
	}
	now := time.Now()
	m.removeExpiredBootstrapsLocked(now)
	if (m.config.Limits.MaxBootstrapsPerIP != 0 &&
		m.bootstrapsPerIP[clientIP] >= m.config.Limits.MaxBootstrapsPerIP) || !allowRateLocked(
		&m.bootstrapRate,
		now,
		m.config.Limits.NewBootstrapsPerMinute,
		m.config.Limits.NewBootstrapsBurst) {
		m.limitHits.Add(1)
		return "", ErrLimit
	}
	if len(m.bootstraps) >= m.config.Limits.MaxBootstrapsGlobal &&
		!m.evictOldestUnusedBootstrapLocked() {
		m.limitHits.Add(1)
		return "", ErrLimit
	}
	m.bootstrapSeq++
	m.bootstraps[hash] = &bootstrap{
		seq:        m.bootstrapSeq,
		expires:    now.Add(m.config.Timeouts.BootstrapLifetime.Value()),
		profile:    activeProfile,
		issuanceIP: clientIP,
	}
	m.bootstrapsPerIP[clientIP]++
	return token, nil
}

func (m *Manager) Create(token, clientIP string, body []byte) (CreateResult, error) {
	res, evictFns, err := m.createLocked(token, clientIP, body)
	for _, fn := range evictFns {
		fn()
	}
	return res, err
}

func (m *Manager) createLocked(token, clientIP string, body []byte) (CreateResult, []func(), error) {
	if err := frame.ParseHello(body); err != nil {
		return CreateResult{}, nil, ErrProtocol
	}
	tokenHash, err := tokenHash(token)
	if err != nil {
		return CreateResult{}, nil, ErrAuthentication
	}
	bodyDigest := sha256.Sum256(body)
	now := time.Now()

	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.bootstraps[tokenHash]
	if entry == nil || now.After(entry.expires) {
		return CreateResult{}, nil, ErrAuthentication
	}
	snap := m.profilesSnap.Load()
	if snap == nil || !isProfileRetained(entry.profile, snap.profiles, m.config.Limits) {
		m.removeBootstrapLocked(tokenHash, entry)
		return CreateResult{}, nil, ErrAuthentication
	}
	if entry.used {
		if subtle.ConstantTimeCompare(entry.bodyDigest[:], bodyDigest[:]) != 1 || entry.session == nil {
			return CreateResult{}, nil, ErrAuthentication
		}
		return CreateResult{
			Token:   entry.sessionToken,
			Welcome: frame.Encode(frame.Welcome, 0, nil),
			Session: entry.session,
		}, nil, nil
	}
	if m.config.IPGuard != nil && m.config.IPGuard.IsCoolingDown(entry.profile.Name, clientIP) {
		m.limitHits.Add(1)
		return CreateResult{}, nil, ErrAuthentication
	}
	profileLimits := entry.profile.Limits
	if m.closed ||
		len(m.sessions) >= m.config.Limits.MaxSessionsGlobal ||
		m.sessionsPerProfile[entry.profile.Name] >= profileLimits.MaxSessions ||
		(m.config.Limits.MaxSessionsPerIP != 0 &&
			m.sessionsPerIP[clientIP] >= m.config.Limits.MaxSessionsPerIP) {
		m.limitHits.Add(1)
		return CreateResult{}, nil, ErrLimit
	}
	if !allowProfileRateLocked(
		&m.sessionRate,
		m.profileSessionRates,
		entry.profile.Name,
		now,
		m.config.Limits.NewSessionsPerMinute,
		m.config.Limits.NewSessionsBurst,
		profileLimits.NewSessionsPerMinute,
		profileLimits.NewSessionsBurst) {
		m.limitHits.Add(1)
		return CreateResult{}, nil, ErrLimit
	}
	sessionToken, sessionHash, err := m.newToken(TokenSession)
	if err != nil {
		return CreateResult{}, nil, err
	}
	limits := m.config.Limits
	limits.MaxStreamsPerSession = profileLimits.MaxStreamsPerSession
	limits.MaxPendingPerSession = profileLimits.MaxPendingPerSession
	created := newSession(sessionOptions{
		profile:       entry.profile,
		clientIP:      clientIP,
		guardID:       sessionToken,
		limits:        limits,
		timeouts:      m.config.Timeouts,
		budget:        m.changePendingBudget,
		onFinished:    m.sessionFinished,
		acquireStream: func() bool { return m.acquireStream(entry.profile) },
		onBackendDialFinished: func(failed bool) {
			m.backendDialFinished(entry.profile, failed)
		},
		onStreamFinished: func() { m.streamFinished(entry.profile) },
		onStreamRejected: func() {
			m.streamsRejected.Add(1)
			m.limitHits.Add(1)
		},
		onUp:   func(count int) { m.bytesUp.Add(uint64(count)) },
		onDown: func(count int) { m.bytesDown.Add(uint64(count)) },
	})
	var evictFns []func()
	if m.config.IPGuard != nil {
		allowed, fns := m.config.IPGuard.AcquireSessionDetached(entry.profile.Name, clientIP, sessionToken, created.Close)
		if !allowed {
			m.limitHits.Add(1)
			return CreateResult{}, nil, ErrAuthentication
		}
		evictFns = fns
	}
	m.sessions[sessionHash] = created
	m.sessionsPerIP[clientIP]++
	m.sessionsPerProfile[entry.profile.Name]++
	m.releaseUnusedBootstrapLocked(entry)
	entry.used = true
	entry.bodyDigest = bodyDigest
	entry.sessionToken = sessionToken
	entry.session = created
	m.sessionsCreated.Add(1)
	return CreateResult{
		Token:   sessionToken,
		Welcome: frame.Encode(frame.Welcome, 0, nil),
		Session: created,
	}, evictFns, nil
}

func (m *Manager) HasBootstrap(token string) bool {
	hash, err := tokenHash(token)
	if err != nil {
		return false
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.bootstraps[hash]
	return entry != nil && !now.After(entry.expires)
}

func (m *Manager) Get(token string) (*Session, error) {
	hash, err := tokenHash(token)
	if err != nil {
		return nil, ErrAuthentication
	}
	m.mu.Lock()
	result := m.sessions[hash]
	m.mu.Unlock()
	if result == nil {
		return nil, ErrAuthentication
	}
	return result, nil
}

func (m *Manager) CloseToken(token string) error {
	hash, err := tokenHash(token)
	if err != nil {
		return ErrAuthentication
	}
	m.mu.Lock()
	value := m.sessions[hash]
	_, closed := m.closedTokens[hash]
	m.mu.Unlock()
	if value == nil {
		if closed {
			return nil
		}
		return ErrAuthentication
	}
	value.Close()
	return nil
}

func (m *Manager) Metrics() Metrics {
	return Metrics{
		SessionsCreated:     m.sessionsCreated.Load(),
		SessionsClosed:      m.sessionsClosed.Load(),
		StreamsOpened:       m.streamsOpened.Load(),
		StreamsRejected:     m.streamsRejected.Load(),
		BackendDialFailures: m.backendDialFailures.Load(),
		BytesUp:             m.bytesUp.Load(),
		BytesDown:           m.bytesDown.Load(),
		LimitHits:           m.limitHits.Load(),
	}
}

func (m *Manager) Capacity() Capacity {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Capacity{
		Sessions:             len(m.sessions),
		Streams:              m.streamsLive,
		BackendDialsInFlight: m.backendDialsInFlight,
		PendingBytes:         m.pendingGlobalCost,
		PendingItems:         m.pendingGlobalItems,
	}
}

func (m *Manager) Shutdown() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		<-m.done
		return
	}
	m.closed = true
	close(m.stop)
	sessions := make([]*Session, 0, len(m.sessions))
	for _, value := range m.sessions {
		sessions = append(sessions, value)
	}
	m.mu.Unlock()
	for _, value := range sessions {
		value.Close()
	}
	for _, value := range sessions {
		value.wait()
	}
	<-m.done
}

func (m *Manager) ObserveClientIP(value *Session, clientIP string) bool {
	if m.config.IPGuard == nil || value == nil || value.guardID == "" {
		return true
	}
	if !m.config.IPGuard.ObserveSession(value.profile.Name, value.guardID, clientIP) {
		value.Close()
		return false
	}
	return true
}

func (m *Manager) sessionFinished(value *Session) {
	m.mu.Lock()
	for hash, current := range m.sessions {
		if current == value {
			delete(m.sessions, hash)
			if len(m.closedTokens) >= m.config.Limits.MaxSessionsGlobal*16 {
				var oldestHash [sha256.Size]byte
				var oldest time.Time
				for candidate, expires := range m.closedTokens {
					if oldest.IsZero() || expires.Before(oldest) {
						oldestHash = candidate
						oldest = expires
					}
				}
				delete(m.closedTokens, oldestHash)
			}
			m.closedTokens[hash] = time.Now().Add(m.config.Timeouts.BootstrapLifetime.Value())
			m.sessionsPerIP[value.clientIP]--
			if m.sessionsPerIP[value.clientIP] == 0 {
				delete(m.sessionsPerIP, value.clientIP)
			}
			m.sessionsPerProfile[value.profile.Name]--
			if m.sessionsPerProfile[value.profile.Name] == 0 {
				delete(m.sessionsPerProfile, value.profile.Name)
			}
			break
		}
	}
	for hash, current := range m.bootstraps {
		if current.session == value {
			m.removeBootstrapLocked(hash, current)
		}
	}
	m.mu.Unlock()
	if m.config.IPGuard != nil && value.guardID != "" && value.profile != nil {
		m.config.IPGuard.ReleaseSession(value.profile.Name, value.guardID)
	}
	m.sessionsClosed.Add(1)
}

func (m *Manager) acquireStream(profile *config.Profile) bool {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	limits := profile.Limits
	if m.closed ||
		m.streamsLive >= m.config.Limits.MaxStreamsGlobal ||
		m.backendDialsInFlight >= m.config.Limits.MaxBackendDialsInFlight ||
		m.streamsPerProfile[profile.Name] >= limits.MaxStreams ||
		m.dialsPerProfile[profile.Name] >= limits.MaxBackendDialsInFlight {
		return false
	}
	if !allowProfileRateLocked(
		&m.streamRate,
		m.profileStreamRates,
		profile.Name,
		now,
		m.config.Limits.NewStreamsPerMinute,
		m.config.Limits.NewStreamsBurst,
		limits.NewStreamsPerMinute,
		limits.NewStreamsBurst) {
		return false
	}
	m.streamsLive++
	m.backendDialsInFlight++
	m.streamsPerProfile[profile.Name]++
	m.dialsPerProfile[profile.Name]++
	m.streamsOpened.Add(1)
	return true
}

func (m *Manager) backendDialFinished(
	profile *config.Profile,
	failed bool) {
	m.mu.Lock()
	if m.backendDialsInFlight <= 0 || m.dialsPerProfile[profile.Name] <= 0 {
		m.mu.Unlock()
		panic("invalid backend dial accounting")
	}
	m.backendDialsInFlight--
	m.dialsPerProfile[profile.Name]--
	if m.dialsPerProfile[profile.Name] == 0 {
		delete(m.dialsPerProfile, profile.Name)
	}
	m.mu.Unlock()
	if failed {
		m.backendDialFailures.Add(1)
	}
}

func (m *Manager) streamFinished(profile *config.Profile) {
	m.mu.Lock()
	if m.streamsLive <= 0 || m.streamsPerProfile[profile.Name] <= 0 {
		m.mu.Unlock()
		panic("invalid stream accounting")
	}
	m.streamsLive--
	m.streamsPerProfile[profile.Name]--
	if m.streamsPerProfile[profile.Name] == 0 {
		delete(m.streamsPerProfile, profile.Name)
	}
	m.mu.Unlock()
}

func (m *Manager) changePendingBudget(
	costDelta, itemDelta int,
	class pendingClass) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if costDelta > 0 || itemDelta > 0 {
		costLimit := m.config.Limits.MaxPendingGlobal
		itemLimit := m.config.Limits.MaxPendingItemsGlobal
		if class != pendingControl {
			reserveCost, reserveItems := pendingControlReserve(m.config.Limits)
			sessions := m.config.Limits.MaxSessionsGlobal
			if reserveCost > costLimit/sessions {
				costLimit = 0
			} else {
				costLimit -= reserveCost * sessions
			}
			if reserveItems > itemLimit/sessions {
				itemLimit = 0
			} else {
				itemLimit -= reserveItems * sessions
			}
		}
		if costDelta > costLimit || m.pendingGlobalCost > int64(costLimit-costDelta) {
			m.limitHits.Add(1)
			return false
		}
		if itemDelta > itemLimit || m.pendingGlobalItems > int64(itemLimit-itemDelta) {
			m.limitHits.Add(1)
			return false
		}
	}
	m.pendingGlobalCost += int64(costDelta)
	m.pendingGlobalItems += int64(itemDelta)
	if m.pendingGlobalCost < 0 || m.pendingGlobalItems < 0 {
		panic("negative global pending budget")
	}
	return true
}

func (m *Manager) releaseUnusedBootstrapLocked(value *bootstrap) {
	if value.used {
		return
	}
	m.bootstrapsPerIP[value.issuanceIP]--
	if m.bootstrapsPerIP[value.issuanceIP] <= 0 {
		delete(m.bootstrapsPerIP, value.issuanceIP)
	}
}

func (m *Manager) removeBootstrapLocked(
	hash [sha256.Size]byte,
	value *bootstrap) {
	m.releaseUnusedBootstrapLocked(value)
	delete(m.bootstraps, hash)
}

func (m *Manager) evictOldestUnusedBootstrapLocked() bool {
	var oldestHash [sha256.Size]byte
	var oldest *bootstrap
	for hash, value := range m.bootstraps {
		if value.used {
			continue
		}
		if oldest != nil {
			if value.expires.After(oldest.expires) {
				continue
			}
			if value.expires.Equal(oldest.expires) && value.seq > oldest.seq {
				continue
			}
		}
		oldestHash = hash
		oldest = value
	}
	if oldest == nil {
		return false
	}
	m.removeBootstrapLocked(oldestHash, oldest)
	return true
}

func allowRateLocked(
	state *rateState,
	now time.Time,
	perMinute int,
	burstLimit int) bool {
	updated, allowed := takeRate(*state, now, perMinute, burstLimit)
	*state = updated
	return allowed
}

func allowProfileRateLocked(
	global *rateState,
	profiles map[string]rateState,
	profile string,
	now time.Time,
	globalPerMinute int,
	globalBurst int,
	profilePerMinute int,
	profileBurst int) bool {
	updatedGlobal, globalAllowed := takeRate(
		*global,
		now,
		globalPerMinute,
		globalBurst)
	updatedProfile, profileAllowed := takeRate(
		profiles[profile],
		now,
		profilePerMinute,
		profileBurst)
	if !globalAllowed || !profileAllowed {
		return false
	}
	*global = updatedGlobal
	profiles[profile] = updatedProfile
	return true
}

func takeRate(
	state rateState,
	now time.Time,
	perMinute int,
	burstLimit int) (rateState, bool) {
	burst := float64(burstLimit)
	if state.last.IsZero() {
		state.tokens = burst
		state.last = now
	} else {
		perSecond := float64(perMinute) / 60
		state.tokens += now.Sub(state.last).Seconds() * perSecond
		if state.tokens > burst {
			state.tokens = burst
		}
		state.last = now
	}
	if state.tokens < 1 {
		return state, false
	}
	state.tokens--
	return state, true
}

func (m *Manager) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		close(m.done)
	}()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			m.mu.Lock()
			m.removeExpiredBootstrapsLocked(now)
			sessions := make([]*Session, 0, len(m.sessions))
			for _, value := range m.sessions {
				sessions = append(sessions, value)
			}
			m.mu.Unlock()
			for _, value := range sessions {
				if now.Sub(value.LastActivity()) > m.config.Timeouts.ReconnectGrace.Value() {
					value.Close()
				}
			}
		case <-m.stop:
			return
		}
	}
}

func (m *Manager) removeExpiredBootstrapsLocked(now time.Time) {
	for hash, value := range m.bootstraps {
		if now.After(value.expires) {
			m.removeBootstrapLocked(hash, value)
		}
	}
	for hash, expires := range m.closedTokens {
		if now.After(expires) {
			delete(m.closedTokens, hash)
		}
	}
}

func tokenHash(token string) ([sha256.Size]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != token {
		return [sha256.Size]byte{}, ErrAuthentication
	}
	return sha256.Sum256(decoded), nil
}
