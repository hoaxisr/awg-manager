package uplink

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Controller serializes decisions and policy mutations. It deliberately limits
// probes to one at a time on small routers. Status readers do not wait on I/O.
type Controller struct {
	mu           sync.Mutex
	stateMu      sync.RWMutex
	store        *Store
	resolver     Resolver
	adapters     map[string]Adapter
	states       map[string]State
	now          func() time.Time
	wake         chan struct{}
	paused       bool
	backupPaused atomic.Bool
	WANDNS       *WANDNS // configured before serving requests
	dnsDirty     map[string]bool
	// OnTransition is set before Run. It must not call back into Controller.
	OnTransition func(string, State)
	// TargetCatalog is set before serving requests. It adds inventory-only
	// targets alongside executable adapters; unavailable targets cannot be enabled.
	TargetCatalog func(context.Context) ([]TargetInfo, error)
}

func New(store *Store, resolver Resolver, adapters map[string]Adapter) *Controller {
	return &Controller{store: store, resolver: resolver, adapters: adapters, states: map[string]State{}, now: time.Now, wake: make(chan struct{}, 1)}
}

func (c *Controller) Providers() []Provider { return c.resolver.Providers() }

func (c *Controller) List() []Entry {
	policies := c.store.List()
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	list := make([]Entry, 0, len(policies))
	for _, p := range policies {
		s := c.states[p.ID]
		if s.Phase == "" {
			s.Phase = "pending"
		}
		list = append(list, Entry{Policy: p, State: s, Capabilities: c.Capabilities(p.Target)})
	}
	return list
}

func (c *Controller) Capabilities(t Target) Capabilities {
	a := c.adapters[t.Engine]
	if provider, ok := a.(CapabilityProvider); ok {
		return provider.UplinkCapabilities(t)
	}
	_, independent := a.(CandidateProber)
	limitation := ""
	if !independent {
		limitation = "independent candidate probe unavailable"
	}
	return Capabilities{IndependentProbe: independent, AutomaticFailback: independent, RestartScope: "target", Limitation: limitation}
}

func (c *Controller) EngineCapabilities() map[string]Capabilities {
	result := map[string]Capabilities{}
	for engine := range c.adapters {
		result[engine] = c.Capabilities(Target{Engine: engine})
		result[engine+":subscription"] = c.Capabilities(Target{Engine: engine, Kind: "subscription"})
	}
	return result
}

func (c *Controller) publish(id string, s State) {
	c.stateMu.Lock()
	previous := c.states[id]
	c.states[id] = s
	c.stateMu.Unlock()
	if c.OnTransition != nil && (previous.Phase != s.Phase || previous.Provider != s.Provider || previous.LastError != s.LastError) {
		c.OnTransition(id, s)
	}
}

func (c *Controller) Put(ctx context.Context, p Policy) (Policy, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.paused {
		return Policy{}, fmt.Errorf("%w: uplink controller paused for backup", ErrConflict)
	}
	if err := ctx.Err(); err != nil {
		return Policy{}, err
	}
	p.Defaults()
	if err := p.Validate(); err != nil {
		return Policy{}, err
	}
	a := c.adapters[p.Target.Engine]
	if a == nil && p.Enabled {
		return Policy{}, fmt.Errorf("%w: no uplink adapter registered for engine %q", ErrUnsupported, p.Target.Engine)
	}
	if p.Enabled && p.Failback == "automatic" && !c.Capabilities(p.Target).AutomaticFailback {
		return Policy{}, fmt.Errorf("%w: automatic failback requires an independent candidate probe", ErrUnsupported)
	}
	if p.Enabled && p.Failback == "controlled" && !c.Capabilities(p.Target).ControlledFailback {
		return Policy{}, fmt.Errorf("%w: controlled failback unavailable", ErrUnsupported)
	}
	if err := c.store.CheckPut(p); err != nil {
		return Policy{}, err
	}
	var old Policy
	for _, existing := range c.store.List() {
		if existing.ID == p.ID {
			old = existing
		}
	}
	if old.ID != "" && old.Target != p.Target && old.Enabled {
		return Policy{}, fmt.Errorf("%w: disable policy before remapping its target", ErrConflict)
	}
	if p.Enabled {
		seenDevices := map[string]bool{}
		for _, id := range p.Providers {
			provider, err := providerFor(c.Providers(), id)
			if err != nil {
				return Policy{}, err
			}
			if seenDevices[provider.Device] {
				return Policy{}, errors.New("fallback providers must have distinct kernel interfaces")
			}
			seenDevices[provider.Device] = true
		}
		if prepare, ok := a.(TargetPreparer); ok {
			if err := prepare.PrepareUplink(ctx, p.Target); err != nil {
				return Policy{}, err
			}
		}
		if err := a.Validate(ctx, p.Target); err != nil {
			return Policy{}, err
		}
	}
	if err := c.store.CheckPut(p); err != nil {
		return Policy{}, err
	}
	if old.ID != "" && old.Target != p.Target {
		if old.Enabled {
			return Policy{}, fmt.Errorf("%w: disable policy before remapping its target", ErrConflict)
		}
		if err := c.release(ctx, old.Target); err != nil {
			return Policy{}, err
		}
	}
	prepared := State{Phase: "pending"}
	if p.Enabled && (!old.Enabled || old.Target != p.Target) {
		ob, err := a.Observe(ctx, p.Target)
		if err != nil {
			return Policy{}, err
		}
		if !ob.Enabled {
			return Policy{}, fmt.Errorf("enable the tunnel before enabling its uplink policy")
		}
		candidates := append([]string(nil), p.Providers...)
		for i, id := range candidates {
			provider, e := providerFor(c.Providers(), id)
			if e == nil && provider.Device == ob.Device {
				candidates = append([]string{id}, append(candidates[:i], candidates[i+1:]...)...)
				break
			}
		}
		var last error
		for _, id := range candidates {
			provider, e := providerFor(c.Providers(), id)
			if e != nil || !provider.Up {
				continue
			}
			applyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			e = a.Activate(applyCtx, p.Target, ob.Device, provider.Device)
			cancel()
			if errors.Is(e, ErrPathUnavailable) {
				last = e
				continue
			}
			if e != nil {
				return Policy{}, fmt.Errorf("initial uplink activation failed before policy publication: %w", e)
			}
			prepared.Provider = id
			prepared.Device = provider.Device
			prepared.LastSwitch = c.now()
			break
		}
		if prepared.Device == "" {
			if last == nil {
				last = ErrPathUnavailable
			}
			return Policy{}, fmt.Errorf("no permitted WAN could be prepared before enabling policy: %w", last)
		}
	}
	saved, err := c.store.Put(p)
	if err == nil {
		c.publish(p.ID, prepared)
		if !p.Enabled {
			if releaseErr := c.release(ctx, saved.Target); releaseErr != nil {
				c.publish(p.ID, State{Phase: "recovery-required", LastError: releaseErr.Error()})
				return saved, releaseErr
			}
		}
	}
	if err != nil && prepared.Device != "" {
		return Policy{}, fmt.Errorf("WAN binding applied but policy was not saved; explicit binding retained: %w", err)
	}
	return saved, err
}

// Delete relinquishes management, preserving the last explicit binding. It
// never silently restores automatic/direct routing.
func (c *Controller) Delete(id string, revision uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.paused {
		return fmt.Errorf("%w: uplink controller paused for backup", ErrConflict)
	}
	for _, p := range c.store.List() {
		if p.ID == id {
			if p.Revision != revision {
				return ErrConflict
			}
			if _, ok := c.adapters[p.Target.Engine].(Releaser); ok {
				if p.Enabled {
					p.Enabled = false
					var err error
					p, err = c.store.Put(p)
					if err != nil {
						return err
					}
					revision = p.Revision
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				err := c.release(ctx, p.Target)
				cancel()
				if err != nil {
					c.publish(id, State{Phase: "recovery-required", LastError: err.Error()})
					return err
				}
			}
			break
		}
	}
	if err := c.store.Delete(id, revision); err != nil {
		return err
	}
	c.stateMu.Lock()
	delete(c.states, id)
	c.stateMu.Unlock()
	return nil
}

func (c *Controller) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.Tick(ctx)
		case <-c.wake:
			c.tick(ctx, true)
		}
	}
}

func (c *Controller) Tick(ctx context.Context) {
	c.tick(ctx, false)
}

// NotifyWANChange is safe inside an NDMS hook: it never waits for a probe or
// mutation lock. Repeated notifications coalesce into one fresh reconciliation.
func (c *Controller) NotifyWANChange() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Controller) tick(ctx context.Context, force bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.paused {
		return
	}
	if err := c.recoverRename(ctx); err != nil {
		for _, p := range c.store.List() {
			c.publish(p.ID, State{Phase: "recovery-required", LastError: err.Error()})
		}
		return
	}
	for _, p := range c.store.List() {
		if ctx.Err() != nil {
			return
		}
		c.stateMu.RLock()
		s := c.states[p.ID]
		c.stateMu.RUnlock()
		if !p.Enabled {
			if !force && c.now().Before(s.NextCheck) {
				continue
			}
			s.Phase = "disabled"
			s.NextCheck = c.now().Add(time.Duration(p.IntervalSeconds) * time.Second)
			if err := c.release(ctx, p.Target); err != nil {
				s.Phase = "recovery-required"
				s.LastError = err.Error()
			} else {
				s.LastError = ""
			}
			c.publish(p.ID, s)
			continue
		}
		if !force && c.now().Before(s.NextCheck) {
			// The WAN model is updated by NDMS hooks. A physical outage
			// bypasses the ordinary probe interval, including long intervals.
			v, err := providerFor(c.Providers(), s.Provider)
			if s.Phase != "healthy" || (err == nil && v.Up && v.Device == s.Device) {
				continue
			}
		}
		c.reconcile(ctx, p, s, "")
	}
}

func (c *Controller) release(ctx context.Context, t Target) error {
	if a, ok := c.adapters[t.Engine].(Releaser); ok {
		bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return a.ReleaseUplink(bounded, t)
	}
	return nil
}

// Switch performs an explicit controlled attempt, also used for manual return
// to the preferred provider. The caller must supply the current policy revision.
func (c *Controller) Switch(ctx context.Context, id, provider string, revision uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.paused {
		return fmt.Errorf("%w: uplink controller paused for backup", ErrConflict)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, p := range c.store.List() {
		if p.ID != id {
			continue
		}
		if p.Revision != revision {
			return ErrConflict
		}
		if !p.Enabled {
			return errors.New("uplink policy is disabled")
		}
		found := false
		for _, candidate := range p.Providers {
			if candidate == provider {
				found = true
			}
		}
		if !found {
			return errors.New("provider is outside policy")
		}
		wan, err := providerFor(c.Providers(), provider)
		if err != nil {
			return err
		}
		if !wan.Up {
			return errors.New("provider is down")
		}
		c.stateMu.RLock()
		s := c.states[id]
		c.stateMu.RUnlock()
		c.reconcile(ctx, p, s, provider)
		c.stateMu.RLock()
		result := c.states[id]
		c.stateMu.RUnlock()
		if result.Phase != "healthy" || result.Provider != provider {
			if result.LastError != "" {
				return fmt.Errorf("requested path did not pass verification: %s", result.LastError)
			}
			return errors.New("requested path did not pass verification")
		}
		return nil
	}
	return ErrNotFound
}

func (c *Controller) reconcile(ctx context.Context, p Policy, s State, forced string) {
	automaticReturn := false
	s.PolicyRevision = p.Revision
	s.Generation++
	s.OperationID = fmt.Sprintf("%s:%d:%d", p.ID, p.Revision, s.Generation)
	a := c.adapters[p.Target.Engine]
	s.NextCheck = c.now().Add(time.Duration(p.IntervalSeconds) * time.Second)
	defer func() { c.publish(p.ID, s) }()
	if a == nil {
		s.Phase = "unsupported"
		s.LastError = ErrUnsupported.Error()
		return
	}
	if p.Failback == "automatic" && !c.Capabilities(p.Target).AutomaticFailback {
		s.Phase = "unsupported"
		s.LastError = "automatic failback requires an independent candidate probe"
		return
	}
	if p.Failback == "controlled" && !c.Capabilities(p.Target).ControlledFailback {
		s.Phase = "unsupported"
		s.LastError = "controlled failback unavailable"
		return
	}
	// Validation is repeated: user edits, engine switches and disabled tunnels
	// must stop management rather than resurrect an intentionally stopped path.
	opCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := a.Validate(opCtx, p.Target); err != nil {
		s.Phase = "degraded"
		s.LastError = err.Error()
		return
	}
	obs, err := a.Observe(opCtx, p.Target)
	if err != nil {
		s.Phase = "degraded"
		s.LastError = err.Error()
		return
	}
	if !obs.Enabled {
		s.Phase = "stopped"
		return
	}
	providers := c.Providers()
	// Adopt only a path named by the policy. A changed/removed binding is a
	// conflict, not permission to overwrite a concurrent editor's configuration.
	current := Provider{}
	for _, id := range p.Providers {
		v, e := providerFor(providers, id)
		if e == nil && v.Device == obs.Device {
			current = v
			break
		}
	}
	if s.Device != "" && obs.Device != s.Device {
		s.Phase = "conflict"
		s.LastError = "tunnel binding changed outside uplink controller"
		return
	}
	// After daemon restart no in-memory observation proves that an engine has
	// consumed its on-disk binding. Reapply the adopted path before claiming
	// health, rather than testing a potentially stale runtime configuration.
	initializing := s.Device == "" || obs.NeedsRebind || c.dnsDirty[p.ID]
	if forced == "" && !initializing && current.ID != "" && current.Up {
		s.Provider = current.ID
		s.Device = current.Device
		if err := c.probe(ctx, a, p, current.Device); err == nil {
			s.Phase = "healthy"
			s.Failures = 0
			s.LastError = ""
			s.LastSuccess = c.now()
			if (p.Failback != "automatic" && p.Failback != "controlled") || current.ID == p.Providers[0] || c.now().Before(s.LastSwitch.Add(time.Duration(p.HoldDownSeconds)*time.Second)) || c.now().Before(s.NextRecoveryCheck) {
				return
			}
			prober, ok := a.(CandidateProber)
			if p.Failback == "automatic" && (!ok || !c.Capabilities(p.Target).IndependentProbe) {
				s.LastError = "independent candidate probe unavailable"
				return
			}
			s.NextRecoveryCheck = c.now().Add(time.Duration(p.RecoveryIntervalSeconds) * time.Second)
			candidate := Provider{}
			for _, id := range p.Providers {
				if id == current.ID {
					break
				}
				v, e := providerFor(providers, id)
				if e == nil && v.Up {
					candidate = v
					break
				}
			}
			if candidate.ID == "" {
				s.RecoverySuccesses = 0
				s.Candidate = ""
				return
			}
			if s.Candidate != candidate.ID {
				s.Candidate = candidate.ID
				s.RecoverySuccesses = 0
			}
			if p.Failback == "automatic" {
				candidateCtx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutSeconds)*time.Second)
				err := prober.ProbeCandidate(candidateCtx, p.Target, candidate.Device)
				cancel()
				if err != nil {
					s.RecoverySuccesses = 0
					s.LastError = fmt.Sprintf("preferred WAN verification: %v", err)
					return
				}
			}
			s.RecoverySuccesses++
			if s.RecoverySuccesses < p.RecoveryThreshold {
				return
			}
			s.RecoverySuccesses = 0
			forced = candidate.ID
			automaticReturn = true
		} else {
			s.LastError = err.Error()
		}
		if forced == "" {
			if ctx.Err() != nil {
				return
			}
			s.Failures++
			if s.Failures < p.FailureThreshold && !s.LastSuccess.IsZero() {
				s.Phase = "suspect"
				return
			}
		}
	}
	// Independent return follows candidate payload probes; controlled return
	// follows stable WAN observations and verifies payload after reconnection.
	var lastErr error
	candidates := p.Providers
	if initializing && current.ID != "" {
		candidates = []string{current.ID}
		for _, id := range p.Providers {
			if id != current.ID {
				candidates = append(candidates, id)
			}
		}
	}
	for _, id := range candidates {
		if ctx.Err() != nil {
			return
		}
		if forced != "" && id != forced {
			continue
		}
		v, e := providerFor(providers, id)
		if e != nil || !v.Up {
			continue
		}
		if forced == "" && !initializing && v.ID == current.ID {
			continue
		}
		s.Phase = "switching"
		c.publish(p.ID, s)
		applyCtx, done := context.WithTimeout(ctx, 30*time.Second)
		e = a.Activate(applyCtx, p.Target, obs.Device, v.Device)
		done()
		if errors.Is(e, ErrPathUnavailable) {
			lastErr = e
			continue
		}
		if e != nil {
			s.Phase = "recovery-required"
			s.LastError = e.Error()
			return
		}
		s.Provider = v.ID
		s.Device = v.Device
		s.LastSwitch = c.now()
		obs.Device = v.Device
		s.Phase = "verifying"
		c.publish(p.ID, s)
		verifyCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		observed, e := a.Observe(verifyCtx, p.Target)
		stop()
		if e != nil || observed.Device != v.Device || !observed.Enabled || observed.NeedsRebind {
			s.Phase = "recovery-required"
			s.LastError = fmt.Sprintf("applied binding could not be observed (device=%q enabled=%t rebind=%t): %v", observed.Device, observed.Enabled, observed.NeedsRebind, e)
			return
		}
		if e = c.probe(ctx, a, p, v.Device); e == nil {
			delete(c.dnsDirty, p.ID)
			s.Phase = "healthy"
			s.Failures = 0
			s.Candidate = ""
			s.RecoverySuccesses = 0
			s.LastError = ""
			s.LastSuccess = c.now()
			s.NextCheck = c.now().Add(time.Duration(p.IntervalSeconds) * time.Second)
			return
		}
		lastErr = e
	}
	s.Phase = "unavailable"
	// A failed manual attempt must not strand an otherwise working tunnel.
	// Restore only a previously healthy, still permitted provider, and prove
	// the restored path again. The switch caller still receives a failure.
	if forced != "" && current.ID != "" && current.ID != forced && current.Up && !s.LastSuccess.IsZero() && ctx.Err() == nil {
		rollbackCtx, stop := context.WithTimeout(ctx, 30*time.Second)
		var e error
		if s.Device != current.Device {
			e = a.Activate(rollbackCtx, p.Target, s.Device, current.Device)
		}
		stop()
		if e == nil {
			s.Provider = current.ID
			s.Device = current.Device
			verifyCtx, done := context.WithTimeout(ctx, 10*time.Second)
			v, observeErr := a.Observe(verifyCtx, p.Target)
			done()
			if observeErr == nil && v.Enabled && v.Device == current.Device {
				e = c.probe(ctx, a, p, current.Device)
				if e == nil {
					s.Phase = "healthy"
					s.Failures = 0
					s.LastSuccess = c.now()
					s.LastError = "manual switch failed; previous path restored and verified"
					if automaticReturn {
						s.LastError = "automatic return failed; previous path restored and verified"
						s.RecoverySuccesses = 0
						s.Candidate = ""
						s.NextRecoveryCheck = c.now().Add(time.Duration(p.HoldDownSeconds) * time.Second)
					}
					if lastErr != nil {
						s.LastError += ": " + lastErr.Error()
					}
					return
				}
			} else {
				e = errors.New("rollback binding could not be observed")
			}
		}
		if e != nil {
			s.Phase = "recovery-required"
			s.LastError = e.Error()
			return
		}
	}
	if lastErr != nil {
		s.LastError = lastErr.Error()
	} else if s.LastError == "" {
		s.LastError = "no available provider"
	}
	s.NextCheck = c.now().Add(time.Duration(p.HoldDownSeconds) * time.Second)
}

func (c *Controller) probe(ctx context.Context, a Adapter, p Policy, device string) error {
	probeCtx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutSeconds)*time.Second)
	defer cancel()
	if err := a.Probe(probeCtx, p.Target, device); err != nil {
		return fmt.Errorf("tunnel probe: %w", err)
	}
	return nil
}
