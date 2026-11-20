//go:build linux

package wireguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func TestManagedNativeMarkHandoverIsDurableAndAudited(t *testing.T) {
	for _, scenario := range []string{"success", "add-failure", "retire-failure", "foreign-rule", "external-change"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			c := configFixture(t)
			dir := t.TempDir()
			c.BindingFile = filepath.Join(dir, "uplink-wireguard-bindings", fmt.Sprintf("%x.json", []byte(c.Ref)))
			if err := os.MkdirAll(filepath.Dir(c.BindingFile), 0700); err != nil {
				t.Fatal(err)
			}
			if err := writeBinding(c.BindingFile, bindingRecord{Version: 1, Device: "wan1", Source: "10.44.1.1", Gateway: "10.44.1.2"}); err != nil {
				t.Fatal(err)
			}
			newMark := c.Mark + 1
			rules := map[string]map[uint32]bool{"-4": {c.Mark: true}, "-6": {c.Mark: true}}
			writes := 0
			failed := false
			target := ManagedTarget{Ref: c.Ref, Interface: c.Interface, Peer: c.Peer, Endpoint: c.Endpoint.String(), TunnelDNS: c.TunnelDNS, Enabled: true}
			deps := ManagedDeps{Directory: dir, Lookup: func(context.Context, string) (ManagedTarget, error) { return target, nil }, List: func(context.Context) ([]ManagedTarget, error) { return []ManagedTarget{target}, nil }, Providers: func() []uplink.Provider { return []uplink.Provider{{ID: "isp1", Device: "wan1", Up: true}} }, WithLock: func(_ context.Context, _ string, fn func() error) error { return fn() }, ApplyBinding: func(context.Context, string, string, string) error { return nil },
				PeerMark: func(context.Context, ManagedTarget) (uint32, error) { return newMark, nil }, PeerMarks: func(context.Context) (map[string][]uint32, error) {
					return map[string][]uint32{c.Interface: {newMark}}, nil
				},
			}
			deps.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
				joined := strings.Join(args, " ")
				if name == "wg" {
					switch args[len(args)-1] {
					case "peers":
						return []byte(c.Peer), nil
					case "endpoints":
						return []byte(c.Peer + "\t" + c.Endpoint.String()), nil
					case "allowed-ips":
						return []byte(c.Peer + "\t0.0.0.0/0"), nil
					}
				}
				if strings.Contains(joined, "rule show") {
					var rows []map[string]any
					for mark := range rules[args[0]] {
						rows = append(rows, map[string]any{"priority": c.Priority, "src": "all", "fwmark": fmt.Sprintf("0x%x", mark), "table": c.Table})
					}
					if scenario == "foreign-rule" {
						rows = append(rows, map[string]any{"priority": c.Priority, "src": "all", "fwmark": fmt.Sprintf("0x%x", newMark), "table": c.Table + 1})
					}
					return json.Marshal(rows)
				}
				if strings.Contains(joined, "route show") {
					terminal := fmt.Sprintf(`{"table":%d,"type":"unreachable","dst":"default","protocol":196,"metric":32767}`, c.Table)
					if args[0] == "-6" {
						return []byte("[" + terminal + "]"), nil
					}
					return []byte(fmt.Sprintf(`[{"table":%d,"dst":"default","gateway":"10.44.1.2","dev":"wan1","prefsrc":"10.44.1.1","protocol":196,"metric":10},%s]`, c.Table, terminal)), nil
				}
				if strings.Contains(joined, "route get") {
					return []byte(fmt.Sprintf(`[{"dev":"wan1","prefsrc":"10.44.1.1","table":%d}]`, c.Table)), nil
				}
				if len(args) > 2 && args[1] == "rule" && (args[2] == "add" || args[2] == "del") {
					writes++
					if !failed && args[0] == "-6" && ((scenario == "add-failure" && args[2] == "add") || (scenario == "retire-failure" && args[2] == "del")) {
						failed = true
						return nil, errors.New("injected rule write failure")
					}
					var mark uint64
					for i, arg := range args {
						if arg == "fwmark" {
							mark, _ = strconv.ParseUint(args[i+1], 0, 32)
						}
					}
					if args[2] == "add" {
						rules[args[0]][uint32(mark)] = true
					} else {
						delete(rules[args[0]], uint32(mark))
					}
					return nil, nil
				}
				if strings.Contains(joined, "route del") {
					return nil, nil
				}
				t.Fatal("unexpected command", name, args)
				return nil, nil
			}
			m, err := NewManaged(deps)
			if err != nil {
				t.Fatal(err)
			}
			m.profiles[c.Ref] = c
			m.publishProtection()
			if err = m.save(); err != nil {
				t.Fatal(err)
			}
			if scenario == "external-change" {
				if err = m.Validate(ctx, uplink.Target{Engine: "wireguard", Ref: c.Ref}); !errors.Is(err, uplink.ErrConflict) || writes != 0 {
					t.Fatal("unsolicited change accepted", err, writes)
				}
				return
			}
			err = m.adoptAppliedMark(ctx, target, c, "wan1")
			if scenario == "foreign-rule" {
				if !errors.Is(err, uplink.ErrConflict) || writes != 0 {
					t.Fatal("foreign rule mutated", err, writes)
				}
				return
			}
			if scenario == "success" {
				if err != nil || m.profiles[c.Ref].Mark != newMark || m.profiles[c.Ref].RetiringMark != 0 {
					t.Fatal(err, m.profiles[c.Ref])
				}
				for _, family := range []string{"-4", "-6"} {
					if len(rules[family]) != 1 || !rules[family][newMark] {
						t.Fatal(rules)
					}
				}
				return
			}
			if err == nil {
				t.Fatal("write failure ignored")
			}
			// Simulate a new process after a partial write. Both old and new
			// ownership are durable, but ordinary use cannot report healthy.
			restarted, err := NewManaged(deps)
			if err != nil {
				t.Fatal(err)
			}
			if restarted.profiles[c.Ref].RetiringMark != c.Mark {
				t.Fatal("handover ownership lost")
			}
			if _, err = restarted.Observe(ctx, uplink.Target{Engine: "wireguard", Ref: c.Ref}); !errors.Is(err, uplink.ErrConflict) {
				t.Fatal("interrupted handover appeared settled", err)
			}
			if err = restarted.ReleaseUplink(ctx, uplink.Target{Engine: "wireguard", Ref: c.Ref}); err != nil {
				t.Fatal(err)
			}
			for _, family := range []string{"-4", "-6"} {
				if len(rules[family]) != 0 {
					t.Fatal("owned mark rule survived cleanup", rules)
				}
			}
		})
	}
}
