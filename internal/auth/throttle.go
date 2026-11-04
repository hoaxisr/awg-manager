package auth

import (
	"sync"
	"time"
)

const (
	// throttleMaxFailures is how many consecutive failed login attempts a
	// client IP gets before being blocked.
	throttleMaxFailures = 5
	// throttleBlockDuration is how long an IP stays blocked after reaching
	// the failure limit.
	throttleBlockDuration = 30 * time.Second
	// throttleSweepInterval / throttleEntryTTL bound the map size: stale
	// entries are swept opportunistically on access (no extra goroutine —
	// the throttle must be safe to construct in every handler test).
	throttleSweepInterval = 5 * time.Minute
	throttleEntryTTL      = 10 * time.Minute

	// routerMaxFailures / routerWindow — лимит входа через роутер, он строже
	// лимита самого роутера (F459). Окно скользящее и с запасом в 5 с.
	// ponytail: значения зашиты; читать настройку роутера, если понадобится.
	routerMaxFailures = 4
	routerWindow      = 3*time.Minute + 5*time.Second
)

// LoginThrottle is a minimal in-memory anti-brute-force guard for the login
// endpoint, keyed by client IP. Local Entware credential verification adds
// an offline-guessing surface the NDMS challenge/response path never had
// (the router rate-limits and notifies by itself), so the whole Login
// handler is throttled: after throttleMaxFailures consecutive failures the
// IP is rejected for throttleBlockDuration; any success resets the counter.
type LoginThrottle struct {
	mu        sync.Mutex
	entries   map[string]*throttleEntry
	lastSweep time.Time
	now       func() time.Time // injectable for tests
}

type throttleEntry struct {
	failures     int
	inFlight     int
	blockedUntil time.Time
	lastSeen     time.Time
}

// NewLoginThrottle creates an empty throttle.
func NewLoginThrottle() *LoginThrottle {
	return &LoginThrottle{
		entries: make(map[string]*throttleEntry),
		now:     time.Now,
	}
}

// Check reports whether ip is currently blocked by its block window and, if
// so, how long until it expires (always > 0 when blocked is true). It is a
// pure query — it neither records a failure nor reserves an in-flight slot,
// and it deliberately ignores the in-flight burst guard. Handlers use
// Begin/Done to make the gate decision race-free; Check is kept for
// diagnostics and tests.
func (t *LoginThrottle) Check(ip string) (retryAfter time.Duration, blocked bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweepLocked()

	e, ok := t.entries[ip]
	if !ok {
		return 0, false
	}
	if remaining := e.blockedUntil.Sub(t.now()); remaining > 0 {
		return remaining, true
	}
	return 0, false
}

// Begin atomically decides whether ip may attempt a login and, when it may,
// reserves an in-flight slot — all under a single lock acquisition. This
// closes the check-then-increment race: without an in-flight count, N
// concurrent requests from one IP could each pass the failure check before
// any of them recorded a Fail, letting a burst bypass the limit.
//
// Returns blocked=true (retryAfter > 0, no slot reserved, caller must NOT
// call Done) when the IP is inside its block window OR already has
// failures+inFlight >= throttleMaxFailures. Otherwise returns blocked=false
// with a reserved slot; the caller MUST call Done(ip) exactly once when the
// attempt completes.
func (t *LoginThrottle) Begin(ip string) (retryAfter time.Duration, blocked bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweepLocked()

	now := t.now()
	e, ok := t.entries[ip]
	if !ok {
		e = &throttleEntry{}
		t.entries[ip] = e
	}
	if remaining := e.blockedUntil.Sub(now); remaining > 0 {
		return remaining, true
	}
	if !e.blockedUntil.IsZero() {
		// A previous block has elapsed. Consume that window: drop the failure
		// count to one below the limit so a single fresh failure re-arms the
		// block (matching the historical semantics — only Success fully
		// resets), while the guard below still admits this attempt.
		e.blockedUntil = time.Time{}
		e.failures = throttleMaxFailures - 1
	}
	if e.failures+e.inFlight >= throttleMaxFailures {
		// Burst guard: the limit is already committed across recorded failures
		// and concurrently in-flight attempts. Reject without reserving a slot
		// so at most throttleMaxFailures attempts ever reach verification
		// before the block arms. Not persisted as a window; it resolves once
		// the in-flight attempts drain or a real failure arms blockedUntil.
		return throttleBlockDuration, true
	}
	e.inFlight++
	e.lastSeen = now
	return 0, false
}

// Done releases an in-flight slot reserved by a successful Begin. Safe to
// call even if the entry was swept or cleared in the meantime. Drops the
// entry once it carries no failures, no block and no in-flight attempts, so a
// clean login leaves no residue (Success clears the failures/block; Done then
// removes the emptied entry).
func (t *LoginThrottle) Done(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[ip]
	if !ok {
		return
	}
	if e.inFlight > 0 {
		e.inFlight--
	}
	if e.inFlight == 0 && e.failures == 0 && e.blockedUntil.IsZero() {
		delete(t.entries, ip)
	}
}

// Fail records a failed login attempt for ip. Reaching the failure limit
// (re-)arms the block window.
// Fail регистрирует неудачную попытку и сообщает, активировала ли именно
// она блокировку (переход через порог) — вызывающий код логирует
// активацию один раз, а не на каждый заблокированный запрос.
func (t *LoginThrottle) Fail(ip string) (nowBlocked bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweepLocked()

	now := t.now()
	e, ok := t.entries[ip]
	if !ok {
		e = &throttleEntry{}
		t.entries[ip] = e
	}
	e.failures++
	e.lastSeen = now
	if e.failures >= throttleMaxFailures {
		nowBlocked = e.blockedUntil.Before(now) || e.blockedUntil.IsZero()
		e.blockedUntil = now.Add(throttleBlockDuration)
	}
	return nowBlocked
}

// Success clears the failure counter and any block for ip (sliding reset on
// success). Any in-flight slot is left for the matching Done to release, so a
// concurrent attempt's reservation is not lost; the now-idle entry is dropped
// here when nothing is in flight, otherwise it is swept once idle.
func (t *LoginThrottle) Success(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[ip]
	if !ok {
		return
	}
	e.failures = 0
	e.blockedUntil = time.Time{}
	if e.inFlight <= 0 {
		delete(t.entries, ip)
	}
}

// sweepLocked drops entries idle past throttleEntryTTL. Runs at most once
// per throttleSweepInterval. Caller holds t.mu.
func (t *LoginThrottle) sweepLocked() {
	now := t.now()
	if now.Sub(t.lastSweep) < throttleSweepInterval {
		return
	}
	t.lastSweep = now
	for ip, e := range t.entries {
		if now.Sub(e.lastSeen) > throttleEntryTTL && !e.blockedUntil.After(now) {
			delete(t.entries, ip)
		}
	}
}

// RouterLimiter — общий на всю панель лимит отказов роутера за скользящее окно.
// Редкие отказы никого не закрывают: блокировка держится, только пока в окне
// набран предел. Успех счётчик не сбрасывает: неизвестно, сбрасывает ли его
// роутер, а лишняя пауза дешевле (F459).
type RouterLimiter struct {
	mu       sync.Mutex
	failures []time.Time
	inFlight int
	now      func() time.Time // injectable for tests
}

// NewRouterLimiter creates an empty limiter.
func NewRouterLimiter() *RouterLimiter {
	return &RouterLimiter{now: time.Now}
}

func (l *RouterLimiter) pruneLocked(now time.Time) {
	i := 0
	for i < len(l.failures) && now.Sub(l.failures[i]) >= routerWindow {
		i++
	}
	l.failures = l.failures[i:]
}

// Begin резервирует попытку. blocked=true: предел набран, слот не занят, Done
// не вызывать; retryAfter — когда освободится ближайшее место.
func (l *RouterLimiter) Begin() (retryAfter time.Duration, blocked bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.pruneLocked(now)
	if len(l.failures)+l.inFlight < routerMaxFailures {
		l.inFlight++
		return 0, false
	}
	if len(l.failures) == 0 {
		return time.Second, true // предел заняли попытки в полёте
	}
	return routerWindow - now.Sub(l.failures[0]), true
}

// Done освобождает слот, занятый успешным Begin.
func (l *RouterLimiter) Done() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inFlight--
}

// Fail записывает отказ и сообщает, набран ли этим предел.
func (l *RouterLimiter) Fail() (limitReached bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.pruneLocked(now)
	l.failures = append(l.failures, now)
	return len(l.failures) >= routerMaxFailures
}
