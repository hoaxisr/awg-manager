package uplink

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// holdAdapter — адаптер с привязкой на каждый ref и пробой, которую тест
// может задержать: моделирует зависшую пробу под замком ref.
type holdAdapter struct {
	mu       sync.Mutex
	activate chan struct{} // не nil — Activate ждёт его закрытия
	devices  map[string]string
	failing  map[string]bool
	hold     map[string]chan struct{}
	entered  chan string
	onProbe  func(ref string)
}

func newHoldAdapter() *holdAdapter {
	return &holdAdapter{devices: map[string]string{}, failing: map[string]bool{}, hold: map[string]chan struct{}{}, entered: make(chan string, 8)}
}
func (a *holdAdapter) Validate(context.Context, Target) error { return nil }
func (a *holdAdapter) Observe(_ context.Context, t Target) (Observation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	d := a.devices[t.Ref]
	if d == "" {
		d = "wan1"
	}
	return Observation{Device: d, Enabled: true}, nil
}
func (a *holdAdapter) Activate(_ context.Context, t Target, _, next string) error {
	a.mu.Lock()
	hold := a.activate
	a.mu.Unlock()
	if hold != nil {
		a.entered <- t.Ref
		<-hold
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.devices[t.Ref] = next
	return nil
}
func (a *holdAdapter) Probe(ctx context.Context, t Target, device string) error {
	a.mu.Lock()
	ch, hook := a.hold[t.Ref], a.onProbe
	a.mu.Unlock()
	if ch != nil {
		a.entered <- t.Ref
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if hook != nil {
		hook(t.Ref)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failing[device] {
		return errors.New("path failed")
	}
	return nil
}
func (a *holdAdapter) block(ref string) chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	ch := make(chan struct{})
	a.hold[ref] = ch
	return ch
}

func holdController(t *testing.T, refs ...string) (*Controller, *holdAdapter, *time.Time) {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "policies.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range refs {
		if _, err := s.Put(Policy{ID: "p-" + ref, Target: Target{Engine: "test", Ref: ref}, Providers: []string{"ISP1", "ISP2"}, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	a := newHoldAdapter()
	r := &testResolver{[]Provider{{ID: "ISP1", Device: "wan1", Up: true}, {ID: "ISP2", Device: "wan2", Up: true}}}
	c := New(s, r, map[string]Adapter{"test": a})
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	return c, a, &now
}

func returnsWithin(t *testing.T, d time.Duration, f func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		t.Fatal("call blocked")
		return nil
	}
}

func guardOnce(c *Controller, ref string) func() error {
	return func() error {
		release, err := c.GuardNativeMutation(context.Background(), ref, "lifecycle")
		if err == nil {
			release()
		}
		return err
	}
}

// (1) Туннель вне политик не ждёт ничего: ни зависшую пробу чужой политики,
// ни вставшую за ней паузу бэкапа, ни занятый c.mu.
func TestGuardFastPathForTunnelOutsidePolicies(t *testing.T) {
	c, a, _ := holdController(t, "awg10")
	unblock := a.block("awg10")
	ticked := make(chan struct{})
	go func() { c.Tick(context.Background()); close(ticked) }()
	<-a.entered
	paused := make(chan error, 1)
	go func() { paused <- c.Pause(context.Background()) }()
	// Пауза встала в очередь за пробой.
	deadline := time.Now().Add(time.Second)
	for {
		c.gate.mu.Lock()
		queued := c.gate.queued
		c.gate.mu.Unlock()
		if queued > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pause did not queue")
		}
		time.Sleep(time.Millisecond)
	}
	c.mu.Lock()
	err := returnsWithin(t, 200*time.Millisecond, guardOnce(c, "awg99"))
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	close(unblock)
	<-ticked
	if err := <-paused; err != nil {
		t.Fatal(err)
	}
	// Под паузой бэкапа туннель вне политик по-прежнему проходит.
	if err := returnsWithin(t, 200*time.Millisecond, guardOnce(c, "awg99")); err != nil {
		t.Fatal(err)
	}
	if err := guardOnce(c, "awg10")(); !errors.Is(err, ErrConflict) {
		t.Fatal("policy tunnel passed during backup", err)
	}
}

// (2) Туннель политики ждёт сверку своего ref; туннель другой политики — нет.
func TestGuardWaitsOnlyForOwnRef(t *testing.T) {
	c, a, _ := holdController(t, "awg10", "awg11")
	unblock := a.block("awg10")
	ticked := make(chan struct{})
	go func() { c.Tick(context.Background()); close(ticked) }()
	<-a.entered
	if err := returnsWithin(t, 200*time.Millisecond, guardOnce(c, "awg11")); err != nil {
		t.Fatal(err)
	}
	own := make(chan error, 1)
	go func() { own <- guardOnce(c, "awg10")() }()
	select {
	case err := <-own:
		t.Fatal("guard entered during own reconcile", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(unblock)
	select {
	case err := <-own:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("guard not released after reconcile")
	}
	<-ticked
}

// (3) Put другой политики не ждёт зависшую пробу первой.
func TestPutDoesNotWaitForOtherPolicyProbe(t *testing.T) {
	c, a, _ := holdController(t, "awg10")
	unblock := a.block("awg10")
	ticked := make(chan struct{})
	go func() { c.Tick(context.Background()); close(ticked) }()
	<-a.entered
	err := returnsWithin(t, 200*time.Millisecond, func() error {
		_, err := c.Put(context.Background(), Policy{ID: "p2", Target: Target{Engine: "test", Ref: "awg11"}, Providers: []string{"ISP1", "ISP2"}, Enabled: true})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	close(unblock)
	<-ticked
}

// (4) Итог сверки, за время которой ревизия политики сменилась, не
// публикуется.
func TestStaleReconcileResultDropped(t *testing.T) {
	c, a, now := holdController(t, "awg10")
	c.Tick(context.Background())
	before := c.List()[0].State
	if before.Phase != "healthy" {
		t.Fatal(before)
	}
	a.mu.Lock()
	a.failing["wan1"] = true
	a.onProbe = func(string) {
		p := c.store.List()[0]
		p.IntervalSeconds++
		if _, err := c.store.Put(p); err != nil {
			t.Error(err)
		}
	}
	a.mu.Unlock()
	*now = now.Add(time.Minute)
	c.Tick(context.Background())
	if after := c.List()[0].State; after != before {
		t.Fatal("stale result published", after)
	}
}

// (5) WAN-события перепроверяют путь, но не копят отказы.
func TestWANEventsDoNotAccumulateFailures(t *testing.T) {
	c, a, _, now := fixture(t)
	c.Tick(context.Background())
	if state(c).Phase != "healthy" {
		t.Fatal(state(c))
	}
	a.activated = nil
	a.failing["wan1"] = true
	for range 3 {
		*now = now.Add(5 * time.Second)
		c.NotifyWANChange("ISP1")
		<-c.wake
		c.tick(context.Background(), true)
	}
	if s := state(c); s.Provider != "ISP1" || s.Failures != 0 || len(a.activated) != 0 {
		t.Fatal("failover from forced ticks", s, a.activated)
	}
	// Событие по чужому провайдеру здоровую политику не трогает; по своему —
	// перепроверяет не чаще раза в 5 с.
	a.failing["wan1"] = false
	*now = now.Add(10 * time.Second)
	c.Tick(context.Background())
	if state(c).Phase != "healthy" {
		t.Fatal(state(c))
	}
	event := func(provider string) uint64 {
		c.NotifyWANChange(provider)
		<-c.wake
		c.tick(context.Background(), true)
		return state(c).Generation
	}
	g := state(c).Generation
	if event("ISP2") != g {
		t.Fatal("unrelated event reprobed healthy policy")
	}
	if event("ISP1") != g+1 {
		t.Fatal("own provider event ignored")
	}
	if event("ISP1") != g+1 {
		t.Fatal("event throttle bypassed")
	}
}

// Мёртвый путь при живом WAN: частые события другого WAN политики не
// откладывают обычный счёт отказов — переход случается по порогу.
func TestFrequentWANEventsDoNotPostponeFailover(t *testing.T) {
	c, a, _, now := fixture(t)
	c.Tick(context.Background())
	a.activated = nil
	a.failing["wan1"] = true
	for range 20 {
		*now = now.Add(3 * time.Second)
		c.NotifyWANChange("ISP2")
		<-c.wake
		c.tick(context.Background(), true)
		c.Tick(context.Background())
	}
	if s := state(c); s.Provider != "ISP2" || s.Phase != "healthy" {
		t.Fatal("dead path kept by WAN events", s, a.activated)
	}
	// Нездоровую политику событие WAN не из её списка не трогает.
	a.failing["wan2"] = true
	*now = now.Add(time.Minute)
	c.Tick(context.Background())
	if state(c).Phase == "healthy" {
		t.Fatal(state(c))
	}
	g := state(c).Generation
	*now = now.Add(time.Second)
	c.NotifyWANChange("ISP9")
	<-c.wake
	c.tick(context.Background(), true)
	if state(c).Generation != g {
		t.Fatal("foreign WAN event reprobed policy", state(c))
	}
}

// Мутация туннеля вне политик на быстром пути и Put политики на тот же ref
// не перекрываются: страж ждёт Put и затем видит новую политику.
func TestGuardFastPathSerializesWithPutOfSameRef(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "policies.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := newHoldAdapter()
	r := &testResolver{[]Provider{{ID: "ISP1", Device: "wan1", Up: true}, {ID: "ISP2", Device: "wan2", Up: true}}}
	c := New(s, r, map[string]Adapter{"wireguard": a})
	a.activate = make(chan struct{})
	put := make(chan error, 1)
	go func() {
		_, err := c.Put(context.Background(), Policy{ID: "p", Target: Target{Engine: "wireguard", Ref: "awg20"}, Providers: []string{"ISP1", "ISP2"}, Enabled: true})
		put <- err
	}()
	<-a.entered
	guard := make(chan error, 1)
	go func() {
		release, err := c.GuardNativeMutation(context.Background(), "awg20", "delete")
		if err == nil {
			release()
		}
		guard <- err
	}()
	select {
	case err := <-guard:
		t.Fatal("mutation entered during Put of the same ref", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(a.activate)
	if err := <-put; err != nil {
		t.Fatal(err)
	}
	if err := <-guard; !errors.Is(err, ErrConflict) {
		t.Fatal("deletion of a now-protected tunnel allowed", err)
	}
}

type cancelOnRelease struct {
	nativeTestAdapter
	cancel context.CancelFunc
}

func (a *cancelOnRelease) ReleaseUplink(ctx context.Context, _ Target) error {
	a.cancel()
	return ctx.Err()
}

// Закрытый запрос посреди Delete не обрывает снятие привязки.
func TestDeleteReleaseDetachedFromRequest(t *testing.T) {
	c, _, p := nativeController(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.adapters["wireguard"] = &cancelOnRelease{cancel: cancel}
	if err := c.Delete(ctx, p.ID, p.Revision); err != nil {
		t.Fatal(err)
	}
	if len(c.List()) != 0 {
		t.Fatal(c.List())
	}
}
