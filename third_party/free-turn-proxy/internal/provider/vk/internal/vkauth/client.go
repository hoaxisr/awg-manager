package vkauth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/browserprofile"
	"github.com/samosvalishe/free-turn-proxy/internal/randx"

	tlsclient "github.com/bogdanfinn/tls-client"
)

// Config конфигурирует Client. Нулевые значения - безопасные дефолты,
// кроме Dialer (должен быть задан явно для кастомного DNS).
type Config struct {
	// Credentials для перебора по порядку. nil/empty -> DefaultCredentials.
	Credentials []VKCredentials

	Dialer net.Dialer

	// ManualOnly форсирует ручной путь captcha с первой попытки.
	ManualOnly bool

	// StreamsPerCache - делитель streamID -> cacheID. <=0 -> дефолт.
	StreamsPerCache int

	// StreamsAlive возвращает число подключённых потоков; используется для
	// решения, является ли исчерпанная captcha фатальной или только throttle.
	// nil -> 1.
	StreamsAlive func() int32

	// AutoSolver / ManualSolver - подключаемые решалки captcha. nil отключает
	// соответствующий путь (поток переходит к следующей попытке).
	AutoSolver   AutoSolveFunc
	ManualSolver ManualSolveFunc

	// Browser - браузерный профиль (UA + JA3 + client hints) для control-plane.
	// Нулевое значение -> Chrome (исторический дефолт для прямых вызовов в тестах).
	Browser browserprofile.Kind

	// Platform - класс устройства персоны (desktop|mobile). Нулевое -> desktop.
	Platform browserprofile.Platform

	// Log - уровневый логгер. nil -> no-op.
	Log logx.Logger
}

type Client struct {
	credentials []VKCredentials
	dialer      net.Dialer
	manualOnly  bool
	browser     browserprofile.Kind
	platform    browserprofile.Platform
	streamsFn   func() int32
	autoSolver  AutoSolveFunc
	manualSolve ManualSolveFunc
	log         logx.Logger

	store *Store

	lockout atomic.Int64

	fetchMu       sync.Mutex
	lastFetchTime time.Time

	// tokenChain - 4-шаговый получатель токена для пары credentials.
	// В prod подключён (*Client).getTokenChain; тесты подменяют fake.
	tokenChain tokenChainFn

	// minFetchIntervalFn ограничивает частоту запросов к VK. Тесты снижают.
	minFetchIntervalFn func() time.Duration
}

type tokenChainFn func(ctx context.Context, link string, streamID int, creds VKCredentials, jar tlsclient.CookieJar) (string, string, []string, error)

func New(cfg Config) *Client {
	c := &Client{
		credentials: cfg.Credentials,
		dialer:      cfg.Dialer,
		manualOnly:  cfg.ManualOnly,
		browser:     cfg.Browser,
		platform:    cfg.Platform,
		streamsFn:   cfg.StreamsAlive,
		autoSolver:  cfg.AutoSolver,
		manualSolve: cfg.ManualSolver,
		log:         cfg.Log,
		store:       NewStore(cfg.StreamsPerCache),
	}
	if len(c.credentials) == 0 {
		c.credentials = DefaultCredentials
	}
	if c.log == nil {
		c.log = logx.Nop()
	}
	if c.streamsFn == nil {
		c.streamsFn = func() int32 { return 1 }
	}
	c.tokenChain = c.getTokenChain
	c.minFetchIntervalFn = func() time.Duration {
		return 3*time.Second + time.Duration(randx.Intn(3000))*time.Millisecond
	}
	return c
}

// GetCredentials -> (username, password, server-addrs); addrs ротированы под
// streamID (предпочтительный первым, см. orderAddrs), fetch к VK только при промахе кэша.
func (c *Client) GetCredentials(ctx context.Context, link string, streamID int) (string, string, []string, error) {
	cache := c.store.Get(streamID)
	cacheID := c.store.CacheID(streamID)

	cache.mutex.RLock()
	if cache.creds.Link == link && time.Now().Before(cache.creds.ExpiresAt) && len(cache.creds.ServerAddrs) > 0 {
		expires := time.Until(cache.creds.ExpiresAt)
		u, p := cache.creds.Username, cache.creds.Password
		addrs := orderAddrs(cache.creds.ServerAddrs, streamID)
		cache.mutex.RUnlock()
		c.log.Debugf("[STREAM %d] [VK Auth] Using cached credentials (cache=%d, expires in %v, server=%s)", streamID, cacheID, expires, addrs[0])
		return u, p, addrs, nil
	}
	cache.mutex.RUnlock()

	cache.mutex.Lock()
	defer cache.mutex.Unlock()

	if cache.creds.Link == link && time.Now().Before(cache.creds.ExpiresAt) && len(cache.creds.ServerAddrs) > 0 {
		return cache.creds.Username, cache.creds.Password, orderAddrs(cache.creds.ServerAddrs, streamID), nil
	}

	user, pass, addrs, err := c.fetchSerialized(ctx, link, streamID)
	if err != nil {
		return "", "", nil, err
	}

	cache.creds = TurnCredentials{
		Username:    user,
		Password:    pass,
		ServerAddrs: addrs,
		ExpiresAt:   time.Now().Add(CredentialLifetime - CacheSafetyMargin),
		Link:        link,
	}
	return user, pass, orderAddrs(addrs, streamID), nil
}

// orderAddrs возвращает копию addrs, ротированную так, что предпочтительный
// для streamID адрес стоит первым, остальные - следом (сохраняя порядок).
// Раскидывает primary по стримам (балансировка relay-IP), оставляя остальные
// как фоллбэк при DPI-дропе.
func orderAddrs(addrs []string, streamID int) []string {
	n := len(addrs)
	if n <= 1 {
		return append([]string(nil), addrs...)
	}
	k := streamID % n
	out := make([]string, 0, n)
	out = append(out, addrs[k:]...)
	out = append(out, addrs[:k]...)
	return out
}

// HandleAuthError инвалидирует кэш при MaxCacheErrors в окне ErrorWindow; true = инвалидирован.
func (c *Client) HandleAuthError(streamID int) bool {
	cache := c.store.Get(streamID)
	cacheID := c.store.CacheID(streamID)
	now := time.Now().Unix()

	if now-cache.lastErrorTime.Load() > int64(ErrorWindow.Seconds()) {
		cache.errorCount.Store(0)
	}
	count := cache.errorCount.Add(1)
	cache.lastErrorTime.Store(now)

	c.log.Warnf("[STREAM %d] [VK Auth] Auth error (cache=%d, count=%d/%d)", streamID, cacheID, count, MaxCacheErrors)

	if count >= MaxCacheErrors {
		c.log.Warnf("[VK Auth] Multiple auth errors (%d), invalidating cache %d for stream %d", count, cacheID, streamID)
		cache.Invalidate()
		c.log.Warnf("[STREAM %d] [VK Auth] Credentials cache invalidated", streamID)
		return true
	}
	return false
}

// ResetErrors вызывать при успешном allocate.
func (c *Client) ResetErrors(streamID int) {
	c.store.Get(streamID).errorCount.Store(0)
}

func (c *Client) LockoutUntilUnix() int64 {
	return c.lockout.Load()
}

// BackoffUntilUnix - алиас LockoutUntilUnix: lockout глобальный, а provider.Provider
// требует no-arg сигнатуру (без streamID).
func (c *Client) BackoffUntilUnix() int64 { return c.LockoutUntilUnix() }

func (*Client) Name() string { return "vk" }

func (*Client) IsAuthError(err error) bool { return IsAuthError(err) }

func (c *Client) engageLockout(d time.Duration) {
	c.lockout.Store(time.Now().Add(d).Unix())
}

// globalCaptchaMu — один слот captcha на весь freeturn-client процесс.
// При multi(4) иначе до 4 vk.Provider параллельно бьют VK → ERROR_LIMIT.
var globalCaptchaMu sync.Mutex

// acquireCaptchaLock ждёт host-level (между процессами) и in-process слот captcha.
func (c *Client) acquireCaptchaLock(ctx context.Context, streamID int) (unlock func(), err error) {
	hostRelease, err := acquireHostCaptchaLock(ctx, streamID, c.log)
	if err != nil {
		return nil, err
	}
	waiting := false
	for {
		if globalCaptchaMu.TryLock() {
			if waiting {
				c.log.Infof("[STREAM %d] [Captcha] Acquired captcha slot", streamID)
			}
			return func() {
				globalCaptchaMu.Unlock()
				hostRelease()
			}, nil
		}
		if !waiting {
			c.log.Infof("[STREAM %d] [Captcha] Waiting for captcha slot (serialized across streams)", streamID)
			waiting = true
		}
		select {
		case <-ctx.Done():
			hostRelease()
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// fetchSerialized сериализует fetch и держит min-интервал между запросами (анти
// rate-limit VK).
func (c *Client) fetchSerialized(ctx context.Context, link string, streamID int) (string, string, []string, error) {
	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()

	minInterval := c.minFetchIntervalFn()
	elapsed := time.Since(c.lastFetchTime)
	if !c.lastFetchTime.IsZero() && elapsed < minInterval {
		wait := minInterval - elapsed
		c.log.Debugf("[STREAM %d] [VK Auth] Throttling: waiting %v to prevent rate limit", streamID, wait.Truncate(time.Millisecond))
		select {
		case <-ctx.Done():
			return "", "", nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	defer func() { c.lastFetchTime = time.Now() }()
	return c.fetch(ctx, link, streamID)
}

func (c *Client) fetch(ctx context.Context, link string, streamID int) (string, string, []string, error) {
	if time.Now().Unix() < c.lockout.Load() {
		return "", "", nil, fmt.Errorf("%w: %w", ErrCaptchaWaitRequired, ErrLockoutActive)
	}

	var lastErr error
	jar := tlsclient.NewCookieJar()
	for _, creds := range c.credentials {
		c.log.Infof("[STREAM %d] [VK Auth] Trying credentials: client_id=%s", streamID, creds.ClientID)

		user, pass, addrs, err := c.tokenChain(ctx, link, streamID, creds, jar)
		if err == nil {
			c.log.Infof("[STREAM %d] [VK Auth] Success with client_id=%s", streamID, creds.ClientID)
			return user, pass, addrs, nil
		}
		lastErr = err
		c.log.Warnf("[STREAM %d] [VK Auth] Failed with client_id=%s: %v", streamID, creds.ClientID, err)

		if errors.Is(err, ErrCaptchaWaitRequired) || errors.Is(err, ErrFatalCaptchaNoStreams) ||
			errors.Is(err, ErrInvalidJoinLink) || errors.Is(err, ErrAnonymousBlocked) ||
			errors.Is(err, ErrCallFull) {
			return "", "", nil, err
		}
		es := err.Error()
		if strings.Contains(es, "error_code:29") || strings.Contains(es, "error_code: 29") || strings.Contains(es, "Rate limit") {
			c.log.Warnf("[STREAM %d] [VK Auth] Rate limit detected, trying next credentials", streamID)
		}
	}
	return "", "", nil, fmt.Errorf("all VK credentials failed: %w", lastErr)
}

func vkDelayRandom(ctx context.Context, minMs, maxMs int) error {
	ms := minMs + randx.Intn(maxMs-minMs+1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Duration(ms) * time.Millisecond):
		return nil
	}
}
