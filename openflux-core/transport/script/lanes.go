package script

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"

	"github.com/p1neappleXpress/OpenFlux/transport"
	"github.com/p1neappleXpress/OpenFlux/utils"
)

// ---- lanes: a script's rooms as lanes of the core's stripe ----
//
// A carrier made of several rooms, documents or call participants (cups.online,
// Avito, Telemost) carries N times what one does only if the packets are
// spread over them, numbered, put back in order and resent when a room drops
// - which is what transport.StripedTransport does for the native carriers.
// A script gets the very same stripe (and so the same wire as the native
// carrier) instead of writing its own:
//
//	lanes.open(n, opts)      the stripe over n lanes; opts in ms: pieceBytes,
//	                         deadAfterMs, noDups, initRtoMs, minRtoMs,
//	                         maxRtoMs, holeDeadlineMs, ackIntervalMs,
//	                         laneWindow, noPacing, codelTargetMs,
//	                         codelIntervalMs, healthDeadlineMs
//	lanes.pull(i, max)       up to max frames (ArrayBuffers) lane i is to send
//	                         now - call it on the lane's own send clock and
//	                         pack them into one message
//	lanes.deliver(i, data)   a frame lane i received
//	lanes.state(i, up)       lane i's socket is up / down
//	lanes.count()            how many lanes are open
//
// and calls back into the script, on its loop:
//
//	Transport.laneSend(i, data)  a frame for lane i to send at once (the
//	                             stripe's own signalling); required
//	Transport.laneWake(i)        the stripe has frames for lane i (optional:
//	                             a lane that ticks on a clock just pulls)
//
// Once lanes are open, Send no longer calls write(): the core's packets go to
// the stripe and come out of lanes.pull; what the stripe puts back together is
// delivered as emit() would.

type scriptLane struct {
	t   *ScriptTransport
	idx int

	cb      func([]byte)
	src     atomic.Pointer[func(max int) [][]byte]
	up      atomic.Bool
	waking  atomic.Bool
	sent    atomic.Uint64
	recv    atomic.Uint64
	pktSent atomic.Uint64
	pktRecv atomic.Uint64
}

func (l *scriptLane) Start() error            { return nil }
func (l *scriptLane) Stop() error             { return nil }
func (l *scriptLane) Receive(cb func([]byte)) { l.cb = cb }
func (l *scriptLane) IsConnected() bool       { return l.up.Load() }

func (l *scriptLane) Stats() transport.TransportStats {
	return transport.TransportStats{
		BytesSent: l.sent.Load(), BytesReceived: l.recv.Load(),
		PacketsSent: l.pktSent.Load(), PacketsRecv: l.pktRecv.Load(),
		Connected: l.up.Load(),
	}
}

// SetFrameSource implements transport.FramePuller.
func (l *scriptLane) SetFrameSource(src func(max int) [][]byte) { l.src.Store(&src) }

// Wake implements transport.LaneWaker: one laneWake call in flight at a time.
func (l *scriptLane) Wake() {
	if !l.waking.CompareAndSwap(false, true) {
		return
	}
	posted := l.t.loop.RunOnLoop(func(vm *goja.Runtime) {
		l.waking.Store(false)
		if fn, ok := goja.AssertFunction(l.t.jsTransport.Get("laneWake")); ok {
			if _, err := fn(l.t.jsTransport, vm.ToValue(l.idx)); err != nil {
				utils.Debugf("[SCRIPT:%s] laneWake(%d): %v", l.t.name, l.idx, err)
			}
		}
	})
	if !posted {
		l.waking.Store(false)
	}
}

// Send carries the stripe's own frames (acks, probes) out of turn.
func (l *scriptLane) Send(b []byte) error {
	data := append([]byte(nil), b...)
	posted := l.t.loop.RunOnLoop(func(vm *goja.Runtime) {
		fn, ok := goja.AssertFunction(l.t.jsTransport.Get("laneSend"))
		if !ok {
			return
		}
		if _, err := fn(l.t.jsTransport, vm.ToValue(l.idx), vm.ToValue(vm.NewArrayBuffer(data))); err != nil {
			utils.Debugf("[SCRIPT:%s] laneSend(%d): %v", l.t.name, l.idx, err)
			return
		}
		l.sent.Add(uint64(len(data)))
		l.pktSent.Add(1)
	})
	if !posted {
		return fmt.Errorf("script stopped")
	}
	return nil
}

// laneSet is the stripe a script opened.
type laneSet struct {
	mu     sync.Mutex
	stripe *transport.StripedTransport
	lanes  []*scriptLane
}

func (s *laneSet) get() (*transport.StripedTransport, []*scriptLane) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stripe, s.lanes
}

func stripeOptionsFrom(o *goja.Object) transport.StripeOptions {
	var opt transport.StripeOptions
	if o == nil {
		return opt
	}
	ms := func(k string) time.Duration {
		v := o.Get(k)
		if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
			return 0
		}
		return time.Duration(v.ToFloat() * float64(time.Millisecond))
	}
	flag := func(k string) bool {
		v := o.Get(k)
		return v != nil && !goja.IsUndefined(v) && v.ToBoolean()
	}
	if v := o.Get("pieceBytes"); v != nil && !goja.IsUndefined(v) {
		opt.PieceBytes = int(v.ToInteger())
	}
	opt.DeadAfter = ms("deadAfterMs")
	opt.NoDups = flag("noDups")
	opt.InitRTO, opt.MinRTO, opt.MaxRTO = ms("initRtoMs"), ms("minRtoMs"), ms("maxRtoMs")
	opt.HoleDeadline = ms("holeDeadlineMs")
	opt.AckInterval = ms("ackIntervalMs")
	opt.LaneWindow = flag("laneWindow")
	opt.NoPacing = flag("noPacing")
	opt.CodelTarget, opt.CodelInterval = ms("codelTargetMs"), ms("codelIntervalMs")
	opt.HealthDeadline = ms("healthDeadlineMs")
	return opt
}

func registerLanes(vm *goja.Runtime, t *ScriptTransport) {
	obj := vm.NewObject()
	lane := func(call goja.FunctionCall) *scriptLane {
		_, ls := t.lanes.get()
		i := int(call.Argument(0).ToInteger())
		if i < 0 || i >= len(ls) {
			panic(vm.NewTypeError(fmt.Sprintf("lanes: no lane %d (open: %d)", i, len(ls))))
		}
		return ls[i]
	}
	obj.Set("open", func(call goja.FunctionCall) goja.Value {
		n := int(call.Argument(0).ToInteger())
		if n < 1 || n > transport.MaxLanes() {
			panic(vm.NewTypeError(fmt.Sprintf("lanes.open: 1..%d lanes, not %d", transport.MaxLanes(), n)))
		}
		if _, ok := goja.AssertFunction(t.jsTransport.Get("laneSend")); !ok {
			panic(vm.NewTypeError("lanes.open: Transport.laneSend(i, data) is required"))
		}
		t.lanes.mu.Lock()
		if t.lanes.stripe != nil {
			t.lanes.mu.Unlock()
			panic(vm.NewTypeError("lanes.open: already open"))
		}
		ls := make([]*scriptLane, n)
		tl := make([]transport.Transport, n)
		for i := range ls {
			ls[i] = &scriptLane{t: t, idx: i}
			tl[i] = ls[i]
		}
		st := transport.NewStripedTransport(tl)
		var o *goja.Object
		if v := call.Argument(1); v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) {
			o = v.ToObject(vm)
		}
		st.SetOptions(stripeOptionsFrom(o))
		st.Receive(t.deliverOrdered)
		t.lanes.stripe, t.lanes.lanes = st, ls
		t.lanes.mu.Unlock()
		if err := st.Start(); err != nil {
			panic(vm.NewGoError(err))
		}
		utils.Infof("[SCRIPT:%s] %d lanes striped", t.name, n)
		return goja.Undefined()
	})
	obj.Set("pull", func(call goja.FunctionCall) goja.Value {
		l := lane(call)
		max := int(call.Argument(1).ToInteger())
		if max <= 0 {
			max = 64
		}
		src := l.src.Load()
		if src == nil {
			return vm.NewArray()
		}
		frames := (*src)(max)
		out := make([]interface{}, len(frames))
		for i, f := range frames {
			l.sent.Add(uint64(len(f)))
			out[i] = vm.NewArrayBuffer(append([]byte(nil), f...))
		}
		l.pktSent.Add(uint64(len(frames)))
		return vm.NewArray(out...)
	})
	obj.Set("deliver", func(call goja.FunctionCall) goja.Value {
		l := lane(call)
		b, err := bytesArg(vm, call.Argument(1))
		if err != nil {
			panic(vm.NewTypeError("lanes.deliver: " + err.Error()))
		}
		l.recv.Add(uint64(len(b)))
		l.pktRecv.Add(1)
		if l.cb != nil {
			l.cb(append([]byte(nil), b...))
		}
		return goja.Undefined()
	})
	obj.Set("state", func(call goja.FunctionCall) goja.Value {
		l := lane(call)
		l.up.Store(call.Argument(1).ToBoolean())
		return goja.Undefined()
	})
	obj.Set("count", func(call goja.FunctionCall) goja.Value {
		_, ls := t.lanes.get()
		return vm.ToValue(len(ls))
	})
	vm.Set("lanes", obj)
}
