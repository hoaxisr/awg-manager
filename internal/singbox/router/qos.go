package router

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// QoS-by-DSCP (issue #371): traffic classes marked with a DSCP codepoint are
// dispatched by iptables `-m dscp` into per-class TPROXY/REDIRECT ports, each
// backed by a dedicated pair of sing-box inbounds; managed route rules
// (`{"inbound":[...],"outbound":X}`) send each class to its outbound. The
// rules live in their own orchestrator slot (18-qos-routes.json, see
// qos_routes.go) — never in the user-visible 20-router.json. sing-box ≤1.14
// has no native dscp route matcher, so the classification has to happen at
// the netfilter layer.

const (
	// MaxQoSClasses caps the number of QoS classes. Bounds both the settings
	// validator and the reserved listen-port ranges below.
	MaxQoSClasses = 8

	// QoSTPROXYPortBase / QoSRedirectPortBase are the first ports of the two
	// reserved contiguous ranges for per-class inbounds (base+index, index <
	// MaxQoSClasses): UDP TPROXY 51281-51288, TCP REDIRECT 51301-51308.
	// Chosen to not collide with TPROXYPort (51271), RedirectPort (51272) or
	// any other AWGM listener (device-proxy 1080+, clash API — по умолчанию
	// 9099, настраивается: singbox.DefaultClashPort); the two
	// ranges do not overlap each other (51281+8 = 51289 < 51301).
	QoSTPROXYPortBase   = 51281
	QoSRedirectPortBase = 51301
)

// QoSClassPorts returns the deterministic (tproxy, redirect) listen-port pair
// for the QoS class occupying persisted slot (storage.SingboxQoSClass.Slot,
// 0..MaxQoSClasses-1). Single source of truth shared by iptables rendering
// and inbound generation so the two can never drift. Keyed on the STABLE
// slot, not the class's position among active classes: positional ports
// would shift when another class is disabled/removed, RSTing untouched flows.
func QoSClassPorts(slot int) (tproxyPort, redirectPort int) {
	return QoSTPROXYPortBase + slot, QoSRedirectPortBase + slot
}

// qosTProxyTagPrefix / qosRedirectTagPrefix form the RESERVED inbound-tag
// namespace for QoS classes. validateRule rejects user rules that reference
// these prefixes so managed QoS routing (18-qos-routes.json) can never be
// shadowed or confused by hand-written lookalike rules. No persisted
// `awgm_managed` marker anywhere: both 18-qos-routes.json and 20-router.json
// are parsed by sing-box itself (config.d merge), and sing-box rejects
// unknown rule fields.
const (
	qosTProxyTagPrefix   = "tproxy-qos-"
	qosRedirectTagPrefix = "redirect-qos-"
)

func qosTProxyTag(dscp int) string   { return fmt.Sprintf("%s%d", qosTProxyTagPrefix, dscp) }
func qosRedirectTag(dscp int) string { return fmt.Sprintf("%s%d", qosRedirectTagPrefix, dscp) }

// isQoSInboundTag reports whether tag belongs to the reserved QoS inbound
// namespace (either protocol prefix).
func isQoSInboundTag(tag string) bool {
	return strings.HasPrefix(tag, qosTProxyTagPrefix) || strings.HasPrefix(tag, qosRedirectTagPrefix)
}

// qosClass is the resolved projection of one ACTIVE (enabled + valid) QoS
// class: settings data plus the deterministic port pair for its slot.
type qosClass struct {
	DSCP         int
	Outbound     string
	TProxyPort   int
	RedirectPort int
}

// activeQoSClasses filters settings classes down to the enabled, valid ones
// and derives their deterministic ports from the persisted Slot. Defensive by
// design: entries with an out-of-range DSCP, an empty outbound, a duplicate
// DSCP, an out-of-range slot or a duplicate slot are silently dropped (first
// occurrence wins) even though NormalizeSingboxRouterSettings repairs/rejects
// them at the API — a hand-edited settings.json must degrade to "class
// skipped", not to a broken iptables restore. Output order follows the
// persisted slice order (deterministic — syncQoSRoutesSlot byte-compares the
// marshalled slot); ports follow the STABLE per-class slot, so disabling or
// removing one class never shifts another class's ports. Slot range implies
// the MaxQoSClasses cap.
func activeQoSClasses(classes []storage.SingboxQoSClass) []qosClass {
	out := make([]qosClass, 0, len(classes))
	seenDSCP := make(map[int]bool, len(classes))
	seenSlot := make(map[int]bool, len(classes))
	for _, c := range classes {
		if !c.Enabled {
			continue
		}
		outbound := strings.TrimSpace(c.Outbound)
		if c.DSCP < 0 || c.DSCP > 63 || outbound == "" || seenDSCP[c.DSCP] {
			continue
		}
		if c.Slot < 0 || c.Slot >= MaxQoSClasses || seenSlot[c.Slot] {
			continue
		}
		seenDSCP[c.DSCP] = true
		seenSlot[c.Slot] = true
		tp, rp := QoSClassPorts(c.Slot)
		out = append(out, qosClass{
			DSCP:         c.DSCP,
			Outbound:     outbound,
			TProxyPort:   tp,
			RedirectPort: rp,
		})
	}
	return out
}

// qosIPTablesSpecs projects active classes into the RestoreInputSpec DTO
// (iptables cares about DSCP + ports only; the outbound lives in sing-box).
func qosIPTablesSpecs(classes []qosClass) []QoSClassSpec {
	if len(classes) == 0 {
		return nil
	}
	out := make([]QoSClassSpec, 0, len(classes))
	for _, c := range classes {
		out = append(out, QoSClassSpec{
			DSCP:         c.DSCP,
			TProxyPort:   c.TProxyPort,
			RedirectPort: c.RedirectPort,
		})
	}
	return out
}

// ensureQoSInbounds convergent-syncs the per-class inbound pairs: every
// active class gets a canonical tproxy (UDP) + redirect (TCP) inbound and
// every stale qos-* inbound (class removed/disabled, port drifted) is
// dropped. Mirrors ensureTProxyInbound's canonical shapes — tproxy on
// 127.0.0.1 (TPROXY --on-ip; wildcard bind self-loops UDP, issue #689),
// redirect on 0.0.0.0 (REDIRECT rewrites the packet destination to the
// LAN-bridge IP, a loopback listener would RST) — UDP-only tproxy with
// udp_fragment + the same effective udp_timeout and udp_nat_max as
// tproxy-in, TCP redirect with tcp_fast_open. Rebuild instead
// of patch-in-place: the whole spec is derived, so "drop all qos inbounds,
// append the canonical list" is the simplest convergent form. Returns the
// new slice and whether anything changed (callers on the reconcile path skip
// the persist+reload when false).
func ensureQoSInbounds(in []Inbound, classes []qosClass, udpTimeout string, udpNATMax int) ([]Inbound, bool) {
	if len(in) == 0 && len(classes) == 0 {
		return in, false // nil-vs-empty guard: no phantom "changed" on a bare config
	}
	effective := resolveUDPTimeout(udpTimeout)
	kept := make([]Inbound, 0, len(in)+2*len(classes))
	for _, i := range in {
		if isQoSInboundTag(i.Tag) {
			continue
		}
		kept = append(kept, i)
	}
	for _, c := range classes {
		kept = append(kept, Inbound{
			Type:        "tproxy",
			Tag:         qosTProxyTag(c.DSCP),
			Listen:      tproxyListen,
			ListenPort:  c.TProxyPort,
			Network:     "udp",
			UDPFragment: true,
			UDPTimeout:  effective,
			UDPNATMax:   udpNATMax,
		}, Inbound{
			Type:        "redirect",
			Tag:         qosRedirectTag(c.DSCP),
			Listen:      redirectListen,
			ListenPort:  c.RedirectPort,
			TCPFastOpen: true,
		})
	}
	return kept, !reflect.DeepEqual(in, kept)
}

// validateQoSClasses enforces the QoS settings contract at the API boundary
// (PUT /singbox/router/settings): at most MaxQoSClasses entries, DSCP within
// 0-63 and unique across ALL entries (enabled or not — a disabled duplicate
// would silently shadow on re-enable), Name at most 32 runes, Outbound
// non-empty. Errors wrap ErrQoSClassesInvalid so the API maps them to 400.
func validateQoSClasses(classes []storage.SingboxQoSClass) error {
	if len(classes) > MaxQoSClasses {
		return fmt.Errorf("%w: превышен лимит классов (%d)", ErrQoSClassesInvalid, MaxQoSClasses)
	}
	seen := make(map[int]bool, len(classes))
	for i, c := range classes {
		if c.DSCP < 0 || c.DSCP > 63 {
			return fmt.Errorf("%w: qosClasses[%d]: DSCP должен быть 0-63 (получено %d)", ErrQoSClassesInvalid, i, c.DSCP)
		}
		if seen[c.DSCP] {
			return fmt.Errorf("%w: qosClasses[%d]: дублирующийся DSCP %d", ErrQoSClassesInvalid, i, c.DSCP)
		}
		seen[c.DSCP] = true
		if len([]rune(c.Name)) > 32 {
			return fmt.Errorf("%w: qosClasses[%d]: имя не должно превышать 32 символа", ErrQoSClassesInvalid, i)
		}
		if strings.TrimSpace(c.Outbound) == "" {
			return fmt.Errorf("%w: qosClasses[%d]: outbound обязателен", ErrQoSClassesInvalid, i)
		}
	}
	return nil
}

// normalizeQoSClasses canonicalises the stored form: trimmed name/outbound
// plus a defensively repaired slot assignment — in-range unique slots are
// PRESERVED (they encode the stable per-class ports), while slotless,
// out-of-range or duplicate slots get the first free slot in slice order.
// Idempotent, so re-normalizing persisted settings is a fixed point and a
// class keeps its ports across reloads. Called after validateQoSClasses
// passed, so it never drops entries — the defensive dropping lives in
// activeQoSClasses on the apply path.
//
// NOTE: this only repairs a single slice in isolation. Re-associating a PUT
// payload (which carries no slots) with the previously persisted slots is
// reassociateQoSSlots' job — see UpdateSettings.
func normalizeQoSClasses(classes []storage.SingboxQoSClass) []storage.SingboxQoSClass {
	if len(classes) == 0 {
		return classes
	}
	out := make([]storage.SingboxQoSClass, len(classes))
	used := make(map[int]bool, len(classes))
	for i, c := range classes {
		c.Name = strings.TrimSpace(c.Name)
		c.Outbound = strings.TrimSpace(c.Outbound)
		if c.Slot < 0 || c.Slot >= MaxQoSClasses || used[c.Slot] {
			c.Slot = -1 // needs (re)assignment below
		} else {
			used[c.Slot] = true
		}
		out[i] = c
	}
	for i := range out {
		if out[i].Slot >= 0 {
			continue
		}
		out[i].Slot = firstFreeQoSSlot(used)
		if out[i].Slot >= 0 {
			used[out[i].Slot] = true
		}
	}
	return out
}

// firstFreeQoSSlot returns the lowest slot in [0, MaxQoSClasses) not marked
// used, or -1 when all are taken (only possible for a hand-edited settings
// file with more than MaxQoSClasses entries — validateQoSClasses rejects
// that at the API; activeQoSClasses skips slot -1 defensively).
func firstFreeQoSSlot(used map[int]bool) int {
	for s := 0; s < MaxQoSClasses; s++ {
		if !used[s] {
			return s
		}
	}
	return -1
}

// reassociateQoSSlots carries persisted port slots over to an incoming PUT
// payload. The UI contract has no slot field — clients send classes as
// {dscp,name,outbound,enabled} — so incoming slots are IGNORED entirely and
// each class is re-associated with its stored slot by DSCP (the unique key).
// Classes with a brand-new DSCP get the first slot not carried over (a freed
// slot is reused). This is what keeps class C's ports stable when class B is
// disabled, edited or removed: without it, positional assignment would shift
// C onto B's ports and RST/blackhole its flows.
func reassociateQoSSlots(incoming, stored []storage.SingboxQoSClass) []storage.SingboxQoSClass {
	if len(incoming) == 0 {
		return incoming
	}
	slotByDSCP := make(map[int]int, len(stored))
	storedUsed := make(map[int]bool, len(stored))
	for _, c := range stored {
		if c.Slot < 0 || c.Slot >= MaxQoSClasses || storedUsed[c.Slot] {
			continue // defensively skip corrupt persisted slots
		}
		if _, dup := slotByDSCP[c.DSCP]; dup {
			continue
		}
		slotByDSCP[c.DSCP] = c.Slot
		storedUsed[c.Slot] = true
	}
	out := make([]storage.SingboxQoSClass, len(incoming))
	used := make(map[int]bool, len(incoming))
	for i, c := range incoming {
		if slot, ok := slotByDSCP[c.DSCP]; ok && !used[slot] {
			c.Slot = slot
			used[slot] = true
		} else {
			c.Slot = -1 // new DSCP → assigned below from the free pool
		}
		out[i] = c
	}
	for i := range out {
		if out[i].Slot >= 0 {
			continue
		}
		out[i].Slot = firstFreeQoSSlot(used)
		if out[i].Slot >= 0 {
			used[out[i].Slot] = true
		}
	}
	return out
}
