//go:build linux

package wireguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

type kernelFixture struct {
	t                                    *testing.T
	target                               ManagedTarget
	mark, otherMark                      uint32
	rules                                map[string]map[uint32]int
	term                                 map[string]bool
	active                               map[string]any
	paths                                map[string]Path
	providers                            []uplink.Provider
	fail                                 string
	markWrites, applyCalls, restoreCalls int
	k                                    *Kernel
	deps                                 KernelDeps
}

func TestKernelStartupRespectsProviderChainAndConcurrentOwnershipReads(t *testing.T) {
	f := newKernelFixture(t)
	ctx := context.Background()
	target := uplink.Target{Engine: "kernel-amneziawg", Ref: f.target.Ref}
	if err := f.k.Activate(ctx, target, "wan1", "wan2"); err != nil {
		t.Fatal(err)
	}
	f.providers[1].Up = false
	if dev, err := f.k.StartupDevice(ctx, target.Ref); err != nil || dev != "wan1" {
		t.Fatal("startup did not avoid failed selected WAN", dev, err)
	}
	f.providers[0].Up = false
	if _, err := f.k.StartupDevice(ctx, target.Ref); !errors.Is(err, uplink.ErrPathUnavailable) {
		t.Fatal("both down escaped policy", err)
	}
	f.providers[0].Up = true
	f.providers[1].Up = true
	if err := f.k.ReleaseUplink(ctx, target); err != nil {
		t.Fatal(err)
	}
	var readers sync.WaitGroup
	done := make(chan struct{})
	defer func() { close(done); readers.Wait() }()
	for n := 0; n < 3; n++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-done:
					return
				default:
					f.k.OwnsTarget(target.Ref)
				}
			}
		}()
	}
	for n := 0; n < 3; n++ {
		if err := f.k.Activate(ctx, target, "wan1", "wan1"); err != nil {
			t.Fatal(err)
		}
		if err := f.k.Validate(ctx, target); err != nil {
			t.Fatal(err)
		}
		if err := f.k.ReleaseUplink(ctx, target); err != nil {
			t.Fatal(err)
		}
	}
}

func newKernelFixture(t *testing.T) *kernelFixture {
	c := configFixture(t)
	f := &kernelFixture{t: t, target: ManagedTarget{Ref: c.Ref, Label: "AWG", Interface: c.Interface, Peer: c.Peer, Endpoint: c.Endpoint.String(), TunnelDNS: c.TunnelDNS, LocalPublicKey: c.Peer, Enabled: true}, rules: map[string]map[uint32]int{"-4": {}, "-6": {}}, term: map[string]bool{}, paths: map[string]Path{}, providers: []uplink.Provider{{ID: "isp1", Device: "wan1", Up: true}, {ID: "isp2", Device: "wan2", Up: true}}}
	for n := 1; n <= 2; n++ {
		dev := fmt.Sprintf("wan%d", n)
		f.paths[dev] = Path{Binding: uplink.Binding{Device: dev, IPv4: fmt.Sprintf("10.44.%d.1", n)}, Gateway: netip.MustParseAddr(fmt.Sprintf("10.44.%d.2", n))}
	}
	f.deps = KernelDeps{ManagedDeps: ManagedDeps{Directory: t.TempDir(), Lookup: func(context.Context, string) (ManagedTarget, error) { return f.target, nil }, List: func(context.Context) ([]ManagedTarget, error) { return []ManagedTarget{f.target}, nil }, Providers: func() []uplink.Provider { return f.providers }, WithLock: func(_ context.Context, _ string, fn func() error) error { return fn() }, Run: f.run, ResolvePath: func(_ context.Context, dev string) (Path, error) {
		p, ok := f.paths[dev]
		if !ok {
			return p, uplink.ErrPathUnavailable
		}
		return p, nil
	}, ApplyBinding: func(context.Context, string, string, string) error { f.applyCalls++; return nil }}, NativeMarks: func(context.Context) (map[string][]uint32, error) { return nil, nil }, StartProviders: func(string) []string { return []string{"isp1", "isp2"} }, RestoreRoute: func(context.Context, string) error {
		f.restoreCalls++
		if f.fail == "restore" {
			f.fail = ""
			return errors.New("baseline restoration refused")
		}
		return nil
	}}
	f.restart()
	return f
}
func (f *kernelFixture) restart() {
	f.t.Helper()
	k, err := NewKernel(f.deps)
	if err != nil {
		f.t.Fatal(err)
	}
	f.k = k
}
func (f *kernelFixture) run(_ context.Context, name string, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	encode := func(v any) ([]byte, error) { return json.Marshal(v) }
	after := func(key string) string {
		for i, arg := range args {
			if arg == key && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	if name == "wg" || name == "/opt/bin/wg" {
		if len(args) > 1 && args[0] == "set" {
			f.markWrites++
			if f.fail == "mark" {
				f.fail = ""
				return nil, errors.New("mark write refused")
			}
			s := after("fwmark")
			f.mark = 0
			if s != "off" {
				n, e := strconv.ParseUint(s, 0, 32)
				if e != nil {
					return nil, e
				}
				f.mark = uint32(n)
			}
			if f.mark != 0 && (!f.term["-4"] || !f.term["-6"] || f.active == nil || len(f.rules["-4"]) == 0 || len(f.rules["-6"]) == 0) {
				f.t.Fatal("mark installed before guarded routing")
			}
			if f.fail == "ambiguous-mark" {
				f.fail = ""
				return nil, errors.New("mark committed but reply lost")
			}
			return nil, nil
		}
		if len(args) > 1 && args[1] == "all" {
			if name == "/opt/bin/wg" {
				return []byte("nwg0\toff\n"), nil
			}
			s := "off"
			if f.mark != 0 {
				s = fmt.Sprintf("0x%x", f.mark)
			}
			out := f.target.Interface + "\t" + s + "\n"
			if f.otherMark != 0 {
				out += fmt.Sprintf("other\t0x%x\n", f.otherMark)
			}
			return []byte(out), nil
		}
		switch args[len(args)-1] {
		case "public-key":
			return []byte(f.target.LocalPublicKey), nil
		case "peers":
			return []byte(f.target.Peer), nil
		case "endpoints":
			return []byte(f.target.Peer + "\t" + f.target.Endpoint), nil
		case "allowed-ips":
			return []byte(f.target.Peer + "\t" + payloadPrefixes(f.target.AllowedIPs)), nil
		case "fwmark":
			if f.mark == 0 {
				return []byte("off"), nil
			}
			return []byte(fmt.Sprintf("0x%x", f.mark)), nil
		}
	}
	if name == "ip" {
		if strings.Contains(joined, "-d link show") {
			return []byte("lo: amneziawg"), nil
		}
		if strings.HasPrefix(joined, "link set up") {
			if f.mark == 0 {
				f.t.Fatal("link up before owned mark")
			}
			return nil, nil
		}
		if strings.HasPrefix(joined, "link set down") {
			return nil, nil
		}
		family := args[0]
		if strings.Contains(joined, "rule show") {
			rows := []map[string]any{}
			for mark, table := range f.rules[family] {
				rows = append(rows, map[string]any{"priority": 1, "src": "all", "fwmark": fmt.Sprintf("0x%x", mark), "table": table})
			}
			return encode(rows)
		}
		if strings.Contains(joined, "route show") {
			rows := []map[string]any{}
			if f.term[family] {
				rows = append(rows, map[string]any{"table": 100, "type": "unreachable", "dst": "default", "protocol": 196, "metric": 32767})
			}
			if family == "-4" && f.active != nil {
				rows = append(rows, f.active)
			}
			return encode(rows)
		}
		if strings.Contains(joined, "route get") {
			if after("mark") == "0x0" {
				return encode([]map[string]any{{"dev": "wan1", "prefsrc": "10.44.1.1", "table": 254}})
			}
			if f.active == nil {
				return nil, errors.New("unreachable")
			}
			return encode([]map[string]any{{"dev": f.active["dev"], "prefsrc": f.active["prefsrc"], "table": 100}})
		}
		if strings.Contains(joined, "rule add") || strings.Contains(joined, "rule del") {
			if f.fail == "rule-"+family && strings.Contains(joined, "rule add") {
				f.fail = ""
				return nil, errors.New("rule add refused")
			}
			if f.fail == "delete-"+family && strings.Contains(joined, "rule del") {
				f.fail = ""
				return nil, errors.New("rule delete refused")
			}
			n, _ := strconv.ParseUint(after("fwmark"), 0, 32)
			if strings.Contains(joined, "rule add") {
				table, _ := strconv.Atoi(after("lookup"))
				f.rules[family][uint32(n)] = table
			} else {
				delete(f.rules[family], uint32(n))
			}
			return nil, nil
		}
		if strings.Contains(joined, "route add unreachable") {
			f.term[family] = true
			return nil, nil
		}
		if strings.Contains(joined, "route replace") {
			if f.fail == "route" {
				f.fail = ""
				return nil, errors.New("route replace refused")
			}
			f.active = map[string]any{"table": 100, "dst": "default", "protocol": 196, "metric": 10, "dev": after("dev"), "prefsrc": after("src"), "gateway": after("via")}
			return nil, nil
		}
		if strings.Contains(joined, "route del") {
			if after("metric") == "32767" {
				f.term[family] = false
			} else {
				f.active = nil
			}
			return nil, nil
		}
	}
	f.t.Fatal("unexpected kernel fixture command", name, args)
	return nil, nil
}

func TestKernelProvisionRecoveryAndCleanup(t *testing.T) {
	for _, failure := range []string{"", "rule--6", "route", "mark", "ambiguous-mark"} {
		t.Run(failure, func(t *testing.T) {
			f := newKernelFixture(t)
			target := uplink.Target{Engine: "kernel-amneziawg", Ref: f.target.Ref}
			ctx := context.Background()
			if err := f.k.Validate(ctx, target); err != nil || f.markWrites != 0 || f.k.OwnsTarget(target.Ref) {
				t.Fatal("validation mutated ownership", err)
			}
			f.fail = failure
			err := f.k.Activate(ctx, target, "wan1", "wan1")
			if failure != "" {
				if err == nil {
					t.Fatal("write failure ignored")
				}
				f.restart()
				if !f.k.OwnsTarget(target.Ref) {
					t.Fatal("ownership lost on restart")
				}
				if err = f.k.Activate(ctx, target, "wan1", "wan1"); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if f.mark == 0 || f.k.m.profiles[target.Ref].KernelPhase != "active" {
				t.Fatal("not active")
			}
			if err = f.k.Activate(ctx, target, "wan1", "wan2"); err != nil {
				t.Fatal(err)
			}
			f.restart()
			if dev, e := f.k.StartupDevice(ctx, target.Ref); e != nil || dev != "wan2" {
				t.Fatal("restart returned to primary", dev, e)
			}
			// Simulate setconf/recreation: the interface's mark and owned routing
			// disappeared, while its exact public transport identity is unchanged.
			f.mark = 0
			f.rules = map[string]map[uint32]int{"-4": {}, "-6": {}}
			f.term = map[string]bool{}
			f.active = nil
			if err = f.k.PrepareForStart(ctx, target.Ref); err != nil {
				t.Fatal(err)
			}
			if f.active["dev"] != "wan2" || f.mark == 0 {
				t.Fatal("pre-up lost binding")
			}
			applyBefore := f.applyCalls
			if err = f.k.ReleaseUplink(ctx, target); err != nil {
				t.Fatal(err)
			}
			if f.mark != 0 || f.k.OwnsTarget(target.Ref) || len(f.rules["-4"]) != 0 || len(f.rules["-6"]) != 0 || f.active != nil || f.term["-4"] || f.term["-6"] || f.restoreCalls != 1 || f.applyCalls != applyBefore {
				t.Fatal("cleanup lost original mode or reapplied selected metadata", f)
			}
		})
	}
}

func TestKernelForeignOwnershipAndReleaseFailures(t *testing.T) {
	f := newKernelFixture(t)
	target := uplink.Target{Engine: "kernel-amneziawg", Ref: f.target.Ref}
	ctx := context.Background()
	f.mark = 9
	if err := f.k.Validate(ctx, target); !errors.Is(err, uplink.ErrConflict) || f.markWrites != 0 {
		t.Fatal("foreign mark adopted", err)
	}
	f.mark = 0
	f.otherMark = 0x41570001
	if err := f.k.Activate(ctx, target, "wan1", "wan1"); err != nil {
		t.Fatal(err)
	}
	if f.mark == f.otherMark {
		t.Fatal("colliding mark allocated")
	}
	f.otherMark = f.mark
	if _, err := f.k.Observe(ctx, target); !errors.Is(err, uplink.ErrConflict) {
		t.Fatal("shared mark accepted", err)
	}
	f.otherMark = 0
	f.fail = "restore"
	if err := f.k.ReleaseUplink(ctx, target); err == nil || f.mark == 0 {
		t.Fatal("mark removed before restoring baseline route", err)
	}
	f.restart()
	f.fail = "delete--6"
	if err := f.k.ReleaseUplink(ctx, target); err == nil || f.mark != 0 || !f.k.OwnsTarget(target.Ref) {
		t.Fatal("partial cleanup not durable", err)
	}
	f.restart()
	if err := f.k.ReleaseUplink(ctx, target); err != nil {
		t.Fatal(err)
	}
	if f.k.OwnsTarget(target.Ref) {
		t.Fatal("cleanup retry failed")
	}
}

func TestKernelInterruptedWANWriteCanResumeOrReleaseAfterRestart(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprint("retry=", retry), func(t *testing.T) {
			f := newKernelFixture(t)
			ctx := context.Background()
			target := uplink.Target{Engine: "kernel-amneziawg", Ref: f.target.Ref}
			if err := f.k.Activate(ctx, target, "wan1", "wan1"); err != nil {
				t.Fatal(err)
			}
			f.fail = "route"
			if err := f.k.Activate(ctx, target, "wan1", "wan2"); err == nil {
				t.Fatal("route failure ignored")
			}
			f.restart()
			ob, err := f.k.Observe(ctx, target)
			if err != nil || ob.Device != "wan1" || !ob.NeedsRebind {
				t.Fatal("interrupted binding has no audited recovery path", ob, err)
			}
			if retry {
				if err = f.k.Activate(ctx, target, "wan1", "wan2"); err != nil {
					t.Fatal(err)
				}
				if f.active["dev"] != "wan2" || f.k.m.profiles[target.Ref].KernelPreviousBinding != nil {
					t.Fatal("recovery did not commit")
				}
			}
			if err = f.k.ReleaseUplink(ctx, target); err != nil {
				t.Fatal(err)
			}
			if f.mark != 0 || f.k.OwnsTarget(target.Ref) {
				t.Fatal("interrupted binding could not be relinquished")
			}
		})
	}
}
