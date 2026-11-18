//go:build linux

package wireguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

type Kernel struct {
	mu   sync.Mutex
	deps KernelDeps
	m    *Managed
	lock func(context.Context, string, func() error) error
}

func NewKernel(d KernelDeps) (*Kernel, error) {
	if d.Directory == "" || d.NativeMarks == nil || d.WithLock == nil || d.StartProviders == nil || d.RestoreRoute == nil {
		return nil, errors.New("kernel mark catalogue and lifecycle lock required")
	}
	k := &Kernel{deps: d, lock: d.WithLock}
	if d.Run == nil {
		d.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "wg" {
				name = "/opt/sbin/awg"
			}
			return defaultRunner(ctx, name, args...)
		}
	}
	d.Directory = filepath.Join(d.Directory, "uplink-kernel-amneziawg")
	d.WithLock = func(_ context.Context, _ string, fn func() error) error { return fn() }
	d.PeerMark, d.PeerMarks = nil, nil
	m, err := NewManaged(d.ManagedDeps)
	if err != nil {
		return nil, err
	}
	for _, c := range m.profiles {
		if (c.KernelPhase != "preparing" && c.KernelPhase != "active" && c.KernelPhase != "releasing") || c.OriginalMark != 0 || c.LocalPublicKey == "" {
			return nil, errors.New("invalid kernel mark ownership journal")
		}
		if c.Mark < 0x41570001 || c.Mark > 0x4157ffff || c.Priority >= 100 || (c.KernelPreviousBinding != nil && !validBindingRecord(*c.KernelPreviousBinding)) {
			return nil, errors.New("invalid kernel routing reservation")
		}
	}
	k.m = m
	return k, nil
}
func (k *Kernel) OwnsTarget(id string) bool { return k.m.OwnsTarget(id) }
func kernelTarget(t uplink.Target) (uplink.Target, error) {
	if t.Engine != "kernel-amneziawg" || t.Kind != "" {
		return t, uplink.ErrUnsupported
	}
	t.Engine = "wireguard"
	return t, nil
}
func (k *Kernel) locked(ctx context.Context, t uplink.Target, fn func(uplink.Target, ManagedTarget) error) error {
	converted, err := kernelTarget(t)
	if err != nil {
		return err
	}
	return k.lock(ctx, t.Ref, func() error {
		k.mu.Lock()
		defer k.mu.Unlock()
		v, e := k.m.target(ctx, converted)
		if e != nil {
			return e
		}
		if v.LocalPublicKey == "" {
			return uplink.ErrUnsupported
		}
		return fn(converted, v)
	})
}
func (k *Kernel) actualMark(ctx context.Context, v ManagedTarget) (uint32, error) {
	out, err := k.m.deps.Run(ctx, "wg", "show", v.Interface, "fwmark")
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(out))
	if s == "off" {
		return 0, nil
	}
	n, err := strconv.ParseUint(s, 0, 32)
	return uint32(n), err
}
func (k *Kernel) identity(ctx context.Context, v ManagedTarget) error {
	out, err := k.m.deps.Run(ctx, "ip", "-d", "link", "show", "dev", v.Interface)
	if err != nil {
		return err
	}
	if !strings.Contains(string(out), "amneziawg") {
		return fmt.Errorf("%w: interface is not kernel AmneziaWG", uplink.ErrConflict)
	}
	for field, want := range map[string]string{"public-key": v.LocalPublicKey, "peers": v.Peer, "endpoints": v.Peer + "\t" + v.Endpoint, "allowed-ips": v.Peer + "\t" + payloadPrefixes(v.AllowedIPs)} {
		out, err = k.m.deps.Run(ctx, "wg", "show", v.Interface, field)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(out)) != want {
			return fmt.Errorf("%w: kernel AWG %s changed", uplink.ErrConflict, field)
		}
	}
	return nil
}

func (k *Kernel) exclusive(ctx context.Context, v ManagedTarget, mark uint32) error {
	native, err := k.deps.NativeMarks(ctx)
	if err != nil {
		return err
	}
	for _, marks := range native {
		for _, n := range marks {
			if n == mark {
				return fmt.Errorf("%w: mark reused by NDMS peer", uplink.ErrConflict)
			}
		}
	}
	for _, tool := range []string{"/opt/bin/wg", "wg"} {
		out, err := k.m.deps.Run(ctx, tool, "show", "all", "fwmark")
		if err != nil {
			return err
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line == "" {
				continue
			}
			p := strings.Fields(line)
			if len(p) != 2 {
				return uplink.ErrConflict
			}
			if p[1] == "off" {
				continue
			}
			n, e := strconv.ParseUint(p[1], 0, 32)
			if e != nil {
				return e
			}
			if uint32(n) == mark && p[0] != v.Interface {
				return fmt.Errorf("%w: kernel mark reused by another interface", uplink.ErrConflict)
			}
		}
	}
	return nil
}
func (k *Kernel) Validate(ctx context.Context, t uplink.Target) error {
	return k.locked(ctx, t, func(_ uplink.Target, v ManagedTarget) error {
		if c, ok := k.m.profiles[v.Ref]; ok {
			if err := k.m.compatible(v, c); err != nil {
				return err
			}
			if c.KernelPhase == "releasing" {
				return uplink.ErrConflict
			}
		}
		if !v.Enabled {
			return nil
		}
		if err := k.identity(ctx, v); err != nil {
			return err
		}
		mark, err := k.actualMark(ctx, v)
		if err != nil {
			return err
		}
		if c, ok := k.m.profiles[v.Ref]; ok {
			if mark != 0 && mark != c.Mark {
				return uplink.ErrConflict
			}
			return k.exclusive(ctx, v, c.Mark)
		} else if mark != 0 {
			return fmt.Errorf("%w: kernel device already has a foreign mark", uplink.ErrConflict)
		}
		return nil
	})
}
func (k *Kernel) Observe(ctx context.Context, t uplink.Target) (uplink.Observation, error) {
	var ob uplink.Observation
	err := k.locked(ctx, t, func(target uplink.Target, v ManagedTarget) error {
		ob.Enabled = v.Enabled
		if !v.Enabled {
			return nil
		}
		if err := k.identity(ctx, v); err != nil {
			return err
		}
		mark, err := k.actualMark(ctx, v)
		if err != nil {
			return err
		}
		if c, ok := k.m.profiles[v.Ref]; ok {
			if err = k.exclusive(ctx, v, c.Mark); err != nil {
				return err
			}
			if err = k.m.compatible(v, c); err != nil {
				return err
			}
			if c.KernelPhase == "releasing" {
				return uplink.ErrConflict
			}
			if mark == 0 {
				r, e := readBinding(c.BindingFile)
				if e != nil {
					return e
				}
				ob.Device, ob.NeedsRebind = r.Device, true
				return nil
			}
			if mark != c.Mark {
				return uplink.ErrConflict
			}
			ob, err = k.m.Observe(ctx, target)
			if err != nil && c.KernelPreviousBinding != nil {
				a, e := k.m.kernel()
				if e != nil {
					return e
				}
				if _, e = a.auditBinding(ctx, c, true, true, c.KernelPreviousBinding); e == nil {
					ob.Device = c.KernelPreviousBinding.Device
					ob.Enabled = v.Enabled
					ob.NeedsRebind = true
					return nil
				}
			}
			if err == nil && !ob.Enabled {
				ob.NeedsRebind = true
				ob.Enabled = v.Enabled
			}
			return err
		}
		if mark != 0 {
			return uplink.ErrConflict
		}
		out, err := k.m.deps.Run(ctx, "ip", "-4", "-j", "route", "get", netip.MustParseAddrPort(v.Endpoint).Addr().String(), "mark", "0x0")
		if err != nil {
			return err
		}
		var rows []route
		if err = json.Unmarshal(out, &rows); err != nil {
			return err
		}
		if len(rows) != 1 {
			return uplink.ErrConflict
		}
		for _, p := range k.m.deps.Providers() {
			if p.Device == rows[0].Dev {
				ob.Device = p.Device
				return nil
			}
		}
		return uplink.ErrPathUnavailable
	})
	return ob, err
}

// freeMark audits device marks, NDMS peer marks and every routing selector.
// It never assumes that a numeric constant is unused.
func (k *Kernel) freeMark(ctx context.Context) (uint32, error) {
	used := map[uint32]bool{}
	for _, c := range k.m.profiles {
		used[c.Mark] = true
		used[c.RetiringMark] = true
	}
	native, err := k.deps.NativeMarks(ctx)
	if err != nil {
		return 0, err
	}
	for _, marks := range native {
		for _, mark := range marks {
			used[mark] = true
		}
	}
	for _, tool := range []string{"/opt/bin/wg", "wg"} {
		out, err := k.m.deps.Run(ctx, tool, "show", "all", "fwmark")
		if err != nil {
			return 0, err
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line == "" {
				continue
			}
			p := strings.Fields(line)
			if len(p) != 2 {
				return 0, uplink.ErrConflict
			}
			if p[1] == "off" {
				continue
			}
			n, e := strconv.ParseUint(p[1], 0, 32)
			if e != nil {
				return 0, e
			}
			used[uint32(n)] = true
		}
	}
	type selector struct {
		value, mask uint32
		inverted    bool
	}
	var selectors []selector
	for _, family := range []string{"-4", "-6"} {
		out, err := k.m.deps.Run(ctx, "ip", family, "-j", "rule", "show")
		if err != nil {
			return 0, err
		}
		var rows []map[string]any
		if err = json.Unmarshal(out, &rows); err != nil {
			return 0, err
		}
		for _, r := range rows {
			raw, present := r["fwmark"]
			if !present {
				continue
			}
			v, e := strconv.ParseUint(fmt.Sprint(raw), 0, 32)
			if e != nil {
				return 0, e
			}
			mask := uint64(0xffffffff)
			if raw, present = r["fwmask"]; present {
				mask, e = strconv.ParseUint(fmt.Sprint(raw), 0, 32)
				if e != nil {
					return 0, e
				}
			}
			inverted, _ := r["not"].(bool)
			selectors = append(selectors, selector{uint32(v), uint32(mask), inverted})
		}
	}
	for mark := uint32(0x41570001); mark <= 0x4157ffff; mark++ {
		if used[mark] {
			continue
		}
		conflict := false
		for _, s := range selectors {
			matches := mark&s.mask == s.value&s.mask
			if s.inverted {
				matches = !matches
			}
			if matches {
				conflict = true
				break
			}
		}
		if !conflict {
			return mark, nil
		}
	}
	return 0, errors.New("no exclusive kernel AWG mark available")
}

func (k *Kernel) prepare(ctx context.Context, target uplink.Target, v ManagedTarget, old, next string) error {
	if err := k.identity(ctx, v); err != nil {
		return err
	}
	actual, err := k.actualMark(ctx, v)
	if err != nil {
		return err
	}
	c, exists := k.m.profiles[v.Ref]
	if exists {
		if err = k.m.compatible(v, c); err != nil {
			return err
		}
		if c.KernelPhase == "releasing" || (actual != 0 && actual != c.Mark) {
			return uplink.ErrConflict
		}
		if err = k.repairBinding(ctx, c); err != nil {
			return err
		}
	} else {
		if actual != 0 {
			return uplink.ErrConflict
		}
		out, e := k.m.deps.Run(ctx, "ip", "-4", "-j", "route", "get", netip.MustParseAddrPort(v.Endpoint).Addr().String(), "mark", "0x0")
		if e != nil {
			return e
		}
		var rows []route
		if e = json.Unmarshal(out, &rows); e != nil {
			return e
		}
		if len(rows) != 1 || rows[0].Dev != old {
			return uplink.ErrConflict
		}
		mark, e := k.freeMark(ctx)
		if e != nil {
			return e
		}
		c, e = k.m.allocate(ctx, v, mark)
		if e != nil {
			return e
		}
		c.KernelPhase = "preparing"
		c.OriginalMark = 0
	}
	provider, e := k.m.provider(next)
	if e != nil || !provider.Up {
		return uplink.ErrPathUnavailable
	}
	path, e := k.m.path(ctx, next)
	if e != nil {
		return fmt.Errorf("%w: %v", uplink.ErrPathUnavailable, e)
	}
	if !exists {
		if e = os.MkdirAll(filepath.Dir(c.BindingFile), 0700); e != nil {
			return e
		}
		if e = writeBinding(c.BindingFile, recordForPath(path)); e != nil {
			return e
		}
		a, e := New([]Config{c}, k.m.path, k.m.deps.Run)
		if e != nil {
			return e
		}
		if _, e = a.auditResources(ctx, c, true, false); e != nil {
			return e
		}
		k.m.profiles[v.Ref] = c
		if e = k.m.save(); e != nil {
			delete(k.m.profiles, v.Ref)
			return e
		}
		k.m.publishProtection()
	}
	if actual == c.Mark && c.KernelPhase == "active" {
		previous, e := readBinding(c.BindingFile)
		if e != nil {
			return e
		}
		c.KernelPreviousBinding = &previous
		k.m.profiles[v.Ref] = c
		if e = k.m.save(); e != nil {
			return e
		}
		if e = k.m.Activate(ctx, target, old, next); e != nil {
			return e
		}
		c.KernelPreviousBinding = nil
		k.m.profiles[v.Ref] = c
		return k.m.save()
	}
	// Recover an interrupted provision or a recreated DOWN interface. Audit
	// exact transport and existing resources before allowing the mark again.
	a, e := k.m.kernel()
	if e != nil {
		return e
	}
	if _, e = a.auditResources(ctx, c, true, false); e != nil {
		return e
	}
	if e = k.m.ensureInspected(ctx, c, false); e != nil {
		return e
	}
	if e = writeBinding(c.BindingFile, recordForPath(path)); e != nil {
		return e
	}
	args := []string{"-4", "route", "replace", "default"}
	if !path.Direct {
		args = append(args, "via", path.Gateway.String())
	}
	args = append(args, "dev", next, "src", path.Binding.IPv4, "table", strconv.Itoa(c.Table), "proto", strconv.Itoa(routeProtocol), "metric", "10")
	if _, e = k.m.deps.Run(ctx, "ip", args...); e != nil {
		return e
	}
	if e = a.verifyPath(ctx, c, next); e != nil {
		return e
	}
	if e = k.identity(ctx, v); e != nil {
		return e
	}
	current, e := k.actualMark(ctx, v)
	if e != nil {
		return e
	}
	if current != actual {
		return uplink.ErrConflict
	}
	if e = k.exclusive(ctx, v, c.Mark); e != nil {
		return e
	}
	if _, e = k.m.deps.Run(ctx, "wg", "set", v.Interface, "fwmark", fmt.Sprintf("0x%x", c.Mark)); e != nil {
		return e
	}
	if _, e = a.readResources(ctx, c, false); e != nil {
		return e
	}
	c.KernelPhase = "active"
	c.KernelPreviousBinding = nil
	k.m.profiles[v.Ref] = c
	if e = k.m.save(); e != nil {
		return e
	}
	return k.m.deps.ApplyBinding(ctx, v.Ref, provider.ID, next)
}

// If a route write failed after its binding intent was committed, only the
// exact previous route saved in our journal may reconcile that intent. Full
// table/rule audits still run; foreign rows are never guessed or flushed.
func (k *Kernel) repairBinding(ctx context.Context, c Config) error {
	a, err := k.m.kernel()
	if err != nil {
		return err
	}
	if _, err = a.auditResources(ctx, c, true, false); err == nil {
		return nil
	}
	if c.KernelPreviousBinding == nil {
		return err
	}
	if _, e := a.auditBinding(ctx, c, true, false, c.KernelPreviousBinding); e != nil {
		return err
	}
	return writeBinding(c.BindingFile, *c.KernelPreviousBinding)
}
func (k *Kernel) Activate(ctx context.Context, t uplink.Target, old, next string) error {
	return k.locked(ctx, t, func(target uplink.Target, v ManagedTarget) error {
		if !v.Enabled {
			return uplink.ErrConflict
		}
		if err := k.prepare(ctx, target, v, old, next); err != nil {
			return err
		}
		_, err := k.m.deps.Run(ctx, "ip", "link", "set", "up", "dev", v.Interface)
		return err
	})
}
func (k *Kernel) Probe(ctx context.Context, t uplink.Target, device string) error {
	return k.locked(ctx, t, func(target uplink.Target, v ManagedTarget) error {
		c, ok := k.m.profiles[v.Ref]
		if !ok || c.KernelPhase != "active" {
			return uplink.ErrConflict
		}
		if err := k.exclusive(ctx, v, c.Mark); err != nil {
			return err
		}
		if err := k.m.Probe(ctx, target, device); err != nil {
			return err
		}
		return k.exclusive(ctx, v, c.Mark)
	})
}

// Called under the orchestrator lock after setconf/syncconf, before link up.
func (k *Kernel) PrepareForStart(ctx context.Context, id string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	c, ok := k.m.profiles[id]
	if !ok {
		return nil
	}
	t := uplink.Target{Engine: "wireguard", Ref: id}
	v, err := k.m.target(ctx, t)
	if err != nil {
		return err
	}
	r, err := readBinding(c.BindingFile)
	if err != nil {
		return err
	}
	if !v.Enabled {
		return uplink.ErrConflict
	}
	next, err := k.startupDevice(ctx, id)
	if err != nil {
		return err
	}
	return k.prepare(ctx, t, v, r.Device, next)
}

func (k *Kernel) StartupDevice(ctx context.Context, id string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.startupDevice(ctx, id)
}
func (k *Kernel) startupDevice(ctx context.Context, id string) (string, error) {
	c, ok := k.m.profiles[id]
	if !ok || c.KernelPhase == "releasing" {
		return "", uplink.ErrConflict
	}
	r, err := readBinding(c.BindingFile)
	if err != nil {
		return "", err
	}
	allowed := k.deps.StartProviders(id)
	providers := k.m.deps.Providers()
	usable := func(p uplink.Provider) bool {
		if !p.Up {
			return false
		}
		_, e := k.m.path(ctx, p.Device)
		return e == nil
	}
	for _, id := range allowed {
		for _, p := range providers {
			if p.ID == id && p.Device == r.Device && usable(p) {
				return p.Device, nil
			}
		}
	}
	for _, id := range allowed {
		for _, p := range providers {
			if p.ID == id && usable(p) {
				return p.Device, nil
			}
		}
	}
	return "", uplink.ErrPathUnavailable
}
func (k *Kernel) ReleaseUplink(ctx context.Context, t uplink.Target) error {
	if !k.OwnsTarget(t.Ref) {
		return nil
	}
	return k.locked(ctx, t, func(target uplink.Target, v ManagedTarget) error {
		c := k.m.profiles[v.Ref]
		if err := k.m.compatible(v, c); err != nil {
			return err
		}
		if err := k.repairBinding(ctx, c); err != nil {
			return err
		}
		a, err := k.m.kernel()
		if err != nil {
			return err
		}
		if _, err = a.auditResources(ctx, c, true, false); err != nil {
			return err
		}
		c.KernelPhase = "releasing"
		k.m.profiles[v.Ref] = c
		if err = k.m.save(); err != nil {
			return err
		}
		// A stopped/deleted interface has no mark to restore. A live foreign
		// driver or changed peer must never be modified during cleanup.
		if _, err = os.Stat("/sys/class/net/" + v.Interface); err == nil {
			if err = k.identity(ctx, v); err != nil {
				return err
			}
			actual, e := k.actualMark(ctx, v)
			if e != nil {
				return e
			}
			if actual != 0 && actual != c.Mark {
				return uplink.ErrConflict
			}
			if !v.Enabled {
				if _, err = k.m.deps.Run(ctx, "ip", "link", "set", "down", "dev", v.Interface); err != nil {
					return err
				}
			}
			if err = k.deps.RestoreRoute(ctx, v.Ref); err != nil {
				return err
			}
			if actual == c.Mark {
				if _, err = k.m.deps.Run(ctx, "wg", "set", v.Interface, "fwmark", "off"); err != nil {
					return err
				}
			}
		} else if !os.IsNotExist(err) {
			return err
		} else if err = k.deps.RestoreRoute(ctx, v.Ref); err != nil {
			return err
		}
		apply := k.m.deps.ApplyBinding
		k.m.deps.ApplyBinding = func(context.Context, string, string, string) error { return nil }
		defer func() { k.m.deps.ApplyBinding = apply }()
		return k.m.ReleaseUplink(ctx, target)
	})
}
func (k *Kernel) UplinkTargets(ctx context.Context) ([]uplink.TargetInfo, error) {
	list, err := k.m.UplinkTargets(ctx)
	for i := range list {
		list[i].Target.Engine = "kernel-amneziawg"
		list[i].Label = strings.TrimSuffix(list[i].Label, " · native WireGuard/ASC") + " · kernel AmneziaWG"
	}
	return list, err
}
