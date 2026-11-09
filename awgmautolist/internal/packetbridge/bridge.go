package packetbridge

import (
	"net/netip"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/dnsfeed"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/judge"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/packet"
)

// Bypass is the common part of a bypass Failure or Nomination. The original
// judge value remains available in Result.Failure or Result.Nomination.
type Bypass struct {
	Host   string
	Client netip.Addr
	Server netip.Addr
	Why    judge.Why
}

// Result describes the observable output of one accepted Event. Flow is always
// client-to-server, including for RESET and ANSWERED captures. First is true
// only when a NEW event is the first sighting according to Judge.FlowFirst.
// At most one of Failure and Nomination is non-nil.
type Result struct {
	Kind       packet.Kind
	Flow       packet.Flow
	First      bool
	Failure    *judge.Failure
	Nomination *judge.Nomination
	Bypass     *Bypass
}

func (r Result) observable() bool {
	return r.First || r.Failure != nil || r.Nomination != nil
}

// Hooks are synchronous observations of bridge output. Failure and Nomination
// are also called for bypass results; Bypass is an additional, common view of
// those results. Flow is called only for a first NEW sighting.
//
// Direct and Coverage are correlation hooks, not predicates: they receive the
// same Result after its specific callbacks and cannot change judge state or the
// returned Result. Callers can use them to correlate a flow with a direct route
// or a host/address with existing coverage. The bridge itself performs no AWGM
// requests or other I/O.
type Hooks struct {
	Failure    func(judge.Failure)
	Nomination func(judge.Nomination)
	Bypass     func(Bypass)
	Flow       func(packet.Flow)
	Direct     func(Result)
	Coverage   func(Result)
}

// Config controls a Bridge. LocalPools extend judge.IsLocal's built-in local
// destination ranges. The slice is copied by New.
type Config struct {
	LocalPools []netip.Prefix
	Hooks      Hooks
}

// Bridge adapts packet and DNS inputs to one Judge.
type Bridge struct {
	judge      *judge.Judge
	localPools []netip.Prefix
	hooks      Hooks
}

// New constructs a Bridge. A nil Judge is accepted as a no-op bridge, which is
// useful to disable packet observation without changing caller control flow.
func New(j *judge.Judge, cfg Config) *Bridge {
	return &Bridge{
		judge:      j,
		localPools: append([]netip.Prefix(nil), cfg.LocalPools...),
		hooks:      cfg.Hooks,
	}
}

// Answer records a parsed DNS response as judge hints. QueriedName and RealIPs
// are passed unchanged, preserving both the client key and every real server
// address supplied by dnsfeed.
func (b *Bridge) Answer(client netip.Addr, answer dnsfeed.Answer, now time.Time) {
	if b == nil || b.judge == nil {
		return
	}
	b.judge.Hint(client, answer.QueriedName, answer.RealIPs, now)
}

// Process applies one normalized Event to the Judge. The boolean is false for
// an invalid synthetic event or a local destination; no judge method or
// callback is invoked in that case. Local filtering concerns the server, not
// the ordinarily local LAN client.
func (b *Bridge) Process(event packet.Event, now time.Time) (Result, bool) {
	if b == nil || b.judge == nil {
		return Result{}, false
	}
	flow, ok := normalizedFlow(event)
	if !ok || judge.IsLocal(flow.Dst, b.localPools...) {
		return Result{}, false
	}

	result := Result{Kind: event.Kind, Flow: flow}
	key := judgeFlow(flow)
	switch event.Kind {
	case packet.HELLO:
		failure, emitted := b.judge.Hello(key, event.Seq, event.Ack, event.SNI, now)
		if emitted {
			result.Failure = &failure
		}
	case packet.RESET:
		failure, emitted := b.judge.Reset(key, now)
		if emitted {
			result.Failure = &failure
		}
	case packet.ANSWERED:
		b.judge.Answered(key, now)
	case packet.FIN, packet.CLOSE_RST:
		nomination, emitted := b.judge.Close(key, event.Ack, now)
		if emitted {
			result.Nomination = &nomination
		}
	case packet.SYN:
		nomination, emitted := b.judge.SYN(key, event.Seq, now)
		if emitted {
			result.Nomination = &nomination
		}
	case packet.NEW:
		result.First = b.judge.FlowFirst(
			flow.Src, flow.SrcPort, flow.Dst, flow.Protocol, flow.DstPort, now,
		)
	}

	if result.Failure != nil && result.Failure.Bypass {
		failure := result.Failure
		result.Bypass = &Bypass{
			Host: failure.Host, Client: failure.Client, Server: failure.Server, Why: failure.Why,
		}
	}
	if result.Nomination != nil && result.Nomination.Bypass {
		nomination := result.Nomination
		result.Bypass = &Bypass{
			Host: nomination.Host, Client: nomination.Client, Server: nomination.Server, Why: judge.WhyNoConnect,
		}
	}
	b.emit(result)
	return result, true
}

func normalizedFlow(event packet.Event) (packet.Flow, bool) {
	if !event.Kind.Valid() {
		return packet.Flow{}, false
	}
	if event.Kind == packet.NEW {
		if event.Protocol != packet.ProtocolTCP && event.Protocol != packet.ProtocolUDP {
			return packet.Flow{}, false
		}
	} else if event.Protocol != packet.ProtocolTCP {
		return packet.Flow{}, false
	}
	if event.Kind == packet.RESET || event.Kind == packet.ANSWERED {
		event.Packet = event.Packet.Reverse()
	}
	flow := event.Flow()
	if !flow.Src.IsValid() || !flow.Dst.IsValid() {
		return packet.Flow{}, false
	}
	return flow, true
}

func judgeFlow(flow packet.Flow) judge.Flow {
	return judge.Flow{
		Client:     flow.Src,
		Server:     flow.Dst,
		ClientPort: flow.SrcPort,
		ServerPort: flow.DstPort,
	}
}

func (b *Bridge) emit(result Result) {
	if result.Failure != nil && b.hooks.Failure != nil {
		b.hooks.Failure(*result.Failure)
	}
	if result.Nomination != nil && b.hooks.Nomination != nil {
		b.hooks.Nomination(*result.Nomination)
	}
	if result.Bypass != nil && b.hooks.Bypass != nil {
		b.hooks.Bypass(*result.Bypass)
	}
	if result.First && b.hooks.Flow != nil {
		b.hooks.Flow(result.Flow)
	}
	if !result.observable() {
		return
	}
	if b.hooks.Direct != nil {
		b.hooks.Direct(result)
	}
	if b.hooks.Coverage != nil {
		b.hooks.Coverage(result)
	}
}
