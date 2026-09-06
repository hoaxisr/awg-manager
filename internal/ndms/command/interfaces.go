package command

import (
	"context"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// InterfaceCommands performs write operations on NDMS Interface objects.
type InterfaceCommands struct {
	poster       Poster
	save         *SaveCoordinator
	queries      *query.Queries
	hookNotifier HookNotifier
}

func NewInterfaceCommands(p Poster, s *SaveCoordinator, q *query.Queries, hn HookNotifier) *InterfaceCommands {
	return &InterfaceCommands{poster: p, save: s, queries: q, hookNotifier: hn}
}

// SetHookNotifier replaces the HookNotifier after construction. Used to
// break the construction cycle between Commands and the Orchestrator
// (Commands are needed to build the Operator, which feeds the Orchestrator,
// which is then the HookNotifier for Commands).
func (c *InterfaceCommands) SetHookNotifier(hn HookNotifier) { c.hookNotifier = hn }

// CreateOpkgTunWithSecurityLevel creates an OpkgTun with an explicit security
// level ("public" or "private"). fakeip-tun mode requires "private" so
// segment→tun forwarding is permitted by default; non-global keeps traffic
// un-masqueraded only when the segment is in a no-masquerade NAT mode (see
// fakeip-tun spec §2 fact 3 — source-preservation depends on segment NAT mode).
func (c *InterfaceCommands) CreateOpkgTunWithSecurityLevel(ctx context.Context, name, description, securityLevel string) error {
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{
				"description": description,
				"security-level": map[string]any{
					securityLevel: true,
				},
			},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, payload, "create opkgtun "+name,
		c.queries.Interfaces.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}

// CreateOpkgTun creates an OpkgTun interface in NDMS (public by default,
// preserving existing callers).
func (c *InterfaceCommands) CreateOpkgTun(ctx context.Context, name, description string) error {
	return c.CreateOpkgTunWithSecurityLevel(ctx, name, description, "public")
}

// DeleteOpkgTun removes an interface (any type — NDMS accepts "no": true for any).
func (c *InterfaceCommands) DeleteOpkgTun(ctx context.Context, name string) error {
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{"no": true},
		},
	}
	// InvalidateAll already drops the deleted interface from the
	// rebuilt map; a per-name Invalidate would issue a now-pointless
	// GET that 404s for the just-deleted resource.
	return postMutationCheckedTolerant(ctx, c.poster, c.save, payload, "delete interface "+name,
		isMissingInterface,
		c.queries.Interfaces.InvalidateAll,
		func() { c.queries.Peers.Invalidate(name) },
		c.queries.RunningConfig.InvalidateAll)
}

// SetSecurityLevel switches interface between public (egress) and private (LAN).
func (c *InterfaceCommands) SetSecurityLevel(ctx context.Context, name, level string) error {
	switch level {
	case "public", "private":
	default:
		level = "public"
	}
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{
				"security-level": map[string]any{level: true},
			},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, payload, "set security-level "+name,
		func() { c.queries.Interfaces.Invalidate(name) },
		c.queries.RunningConfig.InvalidateAll)
}

// SetIPGlobal enables auto-global IP assignment on the interface.
func (c *InterfaceCommands) SetIPGlobal(ctx context.Context, name string) error {
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{
				"ip": map[string]any{
					"global": map[string]any{"auto": true},
				},
			},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, payload, "set ip global "+name,
		func() { c.queries.Interfaces.Invalidate(name) },
		c.queries.RunningConfig.InvalidateAll)
}

// ClearIPGlobal снимает `ip global` — половина больше не выходит в политики.
// Форма и ответ («global priority cleared») сняты со стенда 5.01 2026-09-06;
// security-level от неё не зависит (private принимается и при стоящем
// global — и наоборот).
func (c *InterfaceCommands) ClearIPGlobal(ctx context.Context, name string) error {
	return postMutationChecked(ctx, c.poster, c.save,
		map[string]any{"parse": fmt.Sprintf("interface %s no ip global", name)},
		"clear ip global "+name,
		func() { c.queries.Interfaces.Invalidate(name) },
		c.queries.RunningConfig.InvalidateAll)
}

// clearAddressPayload is the RCI form that removes the configured IPv4
// address; shared by SetAddress (inline best-effort clear) and ClearAddress.
func clearAddressPayload(name string) map[string]any {
	return map[string]any{
		"interface": map[string]any{
			name: map[string]any{
				"ip": map[string]any{
					"address": map[string]any{"no": true},
				},
			},
		},
	}
}

// SetAddress sets the IPv4 address on the interface. Composite: clears
// any existing address first (best-effort), then sets the new one.
func (c *InterfaceCommands) SetAddress(ctx context.Context, name, address, mask string) error {
	_, _ = c.poster.Post(ctx, clearAddressPayload(name))

	setPayload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{
				"ip": map[string]any{
					"address": map[string]any{"address": address, "mask": mask},
				},
			},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, setPayload, "set address "+name,
		func() { c.queries.Interfaces.Invalidate(name) },
		c.queries.Routes.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}

// ClearAddress removes the configured IPv4 address from the interface.
// Idempotent: NDMS accepts no:true when no address is set.
func (c *InterfaceCommands) ClearAddress(ctx context.Context, name string) error {
	return postMutationChecked(ctx, c.poster, c.save, clearAddressPayload(name), "clear address "+name,
		func() { c.queries.Interfaces.Invalidate(name) },
		c.queries.Routes.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}

// SetIPv6Address sets a single IPv6 address on the interface, clearing
// any existing IPv6 assignments in the same call. The clearing element is
// `no:true` — an empty element clears nothing and NDMS answers it with
// `no input [http/rci 127.0.0.1]` (Command::Root refusing an argument-less
// command), which since the response check landed fails the whole call even
// though the address itself was applied. Both forms verified on the stand
// (KeeneticOS 5.01): `no:true` answers "cleared addresses" and is idempotent
// on an interface with no addresses.
func (c *InterfaceCommands) SetIPv6Address(ctx context.Context, name, address string) error {
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{
				"ipv6": map[string]any{
					"address": []any{
						map[string]any{"no": true},
						map[string]any{"block": address + "/128"},
					},
				},
			},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, payload, "set ipv6 address "+name,
		func() { c.queries.Interfaces.Invalidate(name) },
		c.queries.RunningConfig.InvalidateAll)
}

// ClearIPv6Address removes the IPv6 address from the interface.
func (c *InterfaceCommands) ClearIPv6Address(ctx context.Context, name string) error {
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{
				"ipv6": map[string]any{
					"address": map[string]any{"no": true},
				},
			},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, payload, "clear ipv6 address "+name,
		func() { c.queries.Interfaces.Invalidate(name) },
		c.queries.RunningConfig.InvalidateAll)
}

// SetMTU sets the interface MTU and auto-adjusts TCP MSS.
func (c *InterfaceCommands) SetMTU(ctx context.Context, name string, mtu int) error {
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{
				"ip": map[string]any{
					"mtu": mtu,
					"tcp": map[string]any{
						"adjust-mss": map[string]any{"pmtu": true},
					},
				},
			},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, payload, "set mtu "+name,
		func() { c.queries.Interfaces.Invalidate(name) },
		c.queries.RunningConfig.InvalidateAll)
}

// SetDescription updates the NDMS description of the interface.
func (c *InterfaceCommands) SetDescription(ctx context.Context, name, description string) error {
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{"description": description},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, payload, "set description "+name,
		func() { c.queries.Interfaces.Invalidate(name) },
		c.queries.RunningConfig.InvalidateAll)
}

// SetDNS sets DNS name-servers for the interface. One POST per server.
func (c *InterfaceCommands) SetDNS(ctx context.Context, name string, servers []string) error {
	for _, dns := range servers {
		payload := map[string]any{
			"ip": map[string]any{
				"name-server": map[string]any{
					"address":   dns,
					"interface": name,
				},
			},
		}
		if _, err := c.poster.Post(ctx, payload); err != nil {
			return fmt.Errorf("set dns %s=%s: %w", name, dns, err)
		}
	}
	c.save.Request()
	c.queries.RunningConfig.InvalidateAll()
	return nil
}

// ClearDNS removes DNS name-servers for the interface. One best-effort POST per server.
func (c *InterfaceCommands) ClearDNS(ctx context.Context, name string, servers []string) error {
	for _, dns := range servers {
		payload := map[string]any{
			"ip": map[string]any{
				"name-server": map[string]any{
					"no":        true,
					"address":   dns,
					"interface": name,
				},
			},
		}
		_, _ = c.poster.Post(ctx, payload)
	}
	c.save.Request()
	c.queries.RunningConfig.InvalidateAll()
	return nil
}

// InterfaceUp brings the interface administratively up.
// Registers expected hook if notifier is set.
// RunningConfig invalidation is deliberately skipped — Plan 4's
// events.Dispatcher invalidates RunningConfig on iflayerchanged hooks,
// which fire on every interface up/down.
func (c *InterfaceCommands) InterfaceUp(ctx context.Context, name string) error {
	if c.hookNotifier != nil {
		c.hookNotifier.ExpectHook(name, "running")
	}
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{"up": true},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, payload, "interface up "+name,
		func() { c.queries.Interfaces.Invalidate(name) },
		func() { c.queries.Peers.Invalidate(name) })
}

// InterfaceDown brings the interface administratively down.
// Registers expected hook if notifier is set.
// RunningConfig invalidation is deliberately skipped — Plan 4's
// events.Dispatcher invalidates RunningConfig on iflayerchanged hooks,
// which fire on every interface up/down.
func (c *InterfaceCommands) InterfaceDown(ctx context.Context, name string) error {
	if c.hookNotifier != nil {
		c.hookNotifier.ExpectHook(name, "disabled")
	}
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{"up": false},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, payload, "interface down "+name,
		func() { c.queries.Interfaces.Invalidate(name) },
		func() { c.queries.Peers.Invalidate(name) })
}
