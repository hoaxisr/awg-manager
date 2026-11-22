package tgwebproxy

import (
	"context"
	"errors"
	"fmt"
	mrand "math/rand"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/9seconds/mtg/v2/essentials"
	"golang.org/x/net/proxy"
)

type upstreamPoolEntry struct {
	spec          UpstreamSpec
	dialer        *net.Dialer
	socksDialer   proxy.ContextDialer
	cooldownUntil atomic.Int64
}

type dialContextAdapter struct {
	d proxy.Dialer
}

func (a dialContextAdapter) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	type result struct {
		c   net.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := a.d.Dial(network, addr)
		ch <- result{c: c, err: err}
	}()
	select {
	case <-ctx.Done():
		go func() {
			r := <-ch
			if r.c != nil {
				_ = r.c.Close()
			}
		}()
		return nil, ctx.Err()
	case r := <-ch:
		return r.c, r.err
	}
}

type mtgUpstreamPool struct {
	entries           []*upstreamPoolEntry
	cooldown          time.Duration
	perAttemptTimeout time.Duration
	nowFn             func() time.Time
	rngMu             sync.Mutex
	rng               *mrand.Rand
}

func buildUpstreamDialers(u UpstreamSpec) (*net.Dialer, proxy.ContextDialer, error) {
	base := &net.Dialer{
		Timeout:       10 * time.Second,
		KeepAlive:     30 * time.Second,
		FallbackDelay: -1,
		Control:       bindDeviceControl(u.Device),
	}
	if u.Type != UpstreamTypeSocks5 {
		return base, nil, nil
	}
	var auth *proxy.Auth
	if u.Username != "" || u.Password != "" {
		auth = &proxy.Auth{
			User:     u.Username,
			Password: u.Password,
		}
	}
	d, err := proxy.SOCKS5("tcp", u.Address, auth, base)
	if err != nil {
		return base, nil, err
	}
	if cd, ok := d.(proxy.ContextDialer); ok {
		return base, cd, nil
	}
	return base, dialContextAdapter{d: d}, nil
}

func newMtgUpstreamPool(upstreams []UpstreamSpec) *mtgUpstreamPool {
	norm := NormalizeUpstreams(upstreams)
	var active []UpstreamSpec
	for _, u := range norm {
		if u.Enabled {
			active = append(active, u)
		}
	}
	if len(active) == 0 {
		active = []UpstreamSpec{{
			ID:      "up-1",
			Type:    UpstreamTypeDirect,
			Weight:  1,
			Enabled: true,
		}}
	}

	entries := make([]*upstreamPoolEntry, 0, len(active))
	for _, u := range active {
		d, sd, _ := buildUpstreamDialers(u)
		entries = append(entries, &upstreamPoolEntry{
			spec:        u,
			dialer:      d,
			socksDialer: sd,
		})
	}

	perAttempt := 10 * time.Second
	if len(entries) > 1 {
		perAttempt = 5 * time.Second
	}

	return &mtgUpstreamPool{
		entries:           entries,
		cooldown:          20 * time.Second,
		perAttemptTimeout: perAttempt,
		nowFn:             time.Now,
		rng:               mrand.New(mrand.NewSource(time.Now().UnixNano())),
	}
}

func (p *mtgUpstreamPool) randIntn(n int) int {
	if n <= 1 {
		return 0
	}
	p.rngMu.Lock()
	v := p.rng.Intn(n)
	p.rngMu.Unlock()
	return v
}

func (p *mtgUpstreamPool) selectOrder() []*upstreamPoolEntry {
	if len(p.entries) <= 1 {
		return p.entries
	}
	nowUnix := p.nowFn().UnixNano()
	healthy := make([]*upstreamPoolEntry, 0, len(p.entries))
	cooling := make([]*upstreamPoolEntry, 0, len(p.entries))
	for _, e := range p.entries {
		if e.cooldownUntil.Load() <= nowUnix {
			healthy = append(healthy, e)
		} else {
			cooling = append(cooling, e)
		}
	}

	// Weighted shuffle without replacement across healthy upstreams.
	ordered := make([]*upstreamPoolEntry, 0, len(p.entries))
	for len(healthy) > 0 {
		totalWeight := 0
		for _, e := range healthy {
			w := e.spec.Weight
			if w <= 0 {
				w = 1
			}
			totalWeight += w
		}
		pick := p.randIntn(totalWeight)
		acc := 0
		chosenIdx := len(healthy) - 1
		for i, e := range healthy {
			w := e.spec.Weight
			if w <= 0 {
				w = 1
			}
			acc += w
			if pick < acc {
				chosenIdx = i
				break
			}
		}
		ordered = append(ordered, healthy[chosenIdx])
		healthy = append(healthy[:chosenIdx], healthy[chosenIdx+1:]...)
	}

	if len(cooling) > 1 {
		sort.Slice(cooling, func(i, j int) bool {
			return cooling[i].cooldownUntil.Load() < cooling[j].cooldownUntil.Load()
		})
	}
	ordered = append(ordered, cooling...)
	return ordered
}

func (p *mtgUpstreamPool) Dial(network, address string) (essentials.Conn, error) {
	return p.DialContext(context.Background(), network, address)
}

func (p *mtgUpstreamPool) dialEntry(ctx context.Context, entry *upstreamPoolEntry, network, address string) (net.Conn, error) {
	if entry.spec.Type == UpstreamTypeSocks5 {
		if entry.socksDialer == nil {
			return nil, fmt.Errorf("socks5 dialer unavailable for %s", entry.spec.Address)
		}
		return entry.socksDialer.DialContext(ctx, network, address)
	}
	return entry.dialer.DialContext(ctx, network, address)
}

func (p *mtgUpstreamPool) DialContext(ctx context.Context, network, address string) (essentials.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, fmt.Errorf("unsupported network %s", network)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	order := p.selectOrder()
	if len(order) == 0 {
		return nil, errors.New("no configured upstreams")
	}

	var lastErr error
	for _, entry := range order {
		if ctx.Err() != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, ctx.Err()
		}

		attemptCtx := ctx
		var cancel context.CancelFunc
		if len(order) > 1 && p.perAttemptTimeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, p.perAttemptTimeout)
		}
		conn, err := p.dialEntry(attemptCtx, entry, network, address)
		if cancel != nil {
			cancel()
		}
		if err == nil {
			entry.cooldownUntil.Store(0)
			if tc, ok := conn.(*net.TCPConn); ok {
				_ = tc.SetNoDelay(true)
				_ = tc.SetKeepAlive(true)
				return tc, nil
			}
			return essentials.WrapNetConn(conn), nil
		}

		lastErr = err
		if ctx.Err() == nil && p.cooldown > 0 {
			entry.cooldownUntil.Store(p.nowFn().Add(p.cooldown).UnixNano())
		}
	}

	if lastErr == nil {
		lastErr = errors.New("all upstreams failed")
	}
	return nil, lastErr
}

func (p *mtgUpstreamPool) MakeHTTPClient(dialFunc func(ctx context.Context, network, address string) (essentials.Conn, error)) *http.Client {
	if dialFunc == nil {
		dialFunc = p.DialContext
	}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			IdleConnTimeout: time.Minute,
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				return dialFunc(ctx, network, address)
			},
		},
	}
}

func (p *mtgUpstreamPool) NativeDialer() *net.Dialer {
	if len(p.entries) > 0 && p.entries[0].dialer != nil {
		return p.entries[0].dialer
	}
	return &net.Dialer{
		Timeout:       10 * time.Second,
		KeepAlive:     30 * time.Second,
		FallbackDelay: -1,
	}
}

func dialViaUpstream(ctx context.Context, u UpstreamSpec, addr string) (net.Conn, error) {
	if u.Type == UpstreamTypeSocks5 {
		d, sd, err := buildUpstreamDialers(u)
		if err != nil {
			return nil, err
		}
		if sd != nil {
			return sd.DialContext(ctx, "tcp", addr)
		}
		return d.DialContext(ctx, "tcp", addr)
	}
	d := &net.Dialer{
		Timeout: 2 * time.Second,
		Control: bindDeviceControl(u.Device),
	}
	return d.DialContext(ctx, "tcp", addr)
}

func upstreamPoolSignature(upstreams []UpstreamSpec) string {
	norm := NormalizeUpstreams(upstreams)
	var b strings.Builder
	for _, u := range norm {
		if !u.Enabled {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(';')
		}
		fmt.Fprintf(&b, "%s|%s|%s|%s|%s|%d", u.Type, u.Device, u.Address, u.Username, u.Password, u.Weight)
	}
	return b.String()
}
