package script

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/p1neappleXpress/OpenFlux/transport/yandex"
	"github.com/p1neappleXpress/OpenFlux/utils"
)

// ---- media: tunnel bytes as a video-call participant's stream ----
//
// A carrier that rides a conference SFU (Telemost) publishes a video track
// whose frames hold tunnel data and subscribes to the far side's. What is the
// same for any such service lives here, in Go, because it runs at a frame
// clock of ~120 ticks a second per participant: a track with hand-written RTP
// headers, data inside codec keyframes, fragments that fit one RTP packet,
// answering the SFU's NACKs, SDES and Sender Reports, and on the way back
// putting frames together and handing them to a lane of the stripe. What is
// the service's own - joining, signaling, SDP, slots - is the script's. The
// pieces reuse transport/yandex (the native Telemost carrier).
//
//	webrtc.newPeerConnection({..., media: "publisher"|"subscriber"})
//	pc.addMediaTransceiver(kind, {direction, codec})  codec: "video/VP8"|...
//	pc.publishTrack({codec, fps, framesPerTick, pace, probe, queue})
//	    -> a track: setLayers([{ssrc, rid}], ridExtId), useLane(i), write(bytes),
//	       start() (Promise of {ssrc, payloadType}), stats()
//	pc.onMediaTrack = function (t) {...}  t: mimeType, ssrc, deliver(lane), remb(bps), lastRecvMs()

const mediaRTXRing = 4096

type rtxSlot struct {
	hdr     rtp.Header
	payload []byte
	ok      bool
}

// publishTrack is the publisher side: tunnel bytes out as a video stream.
type publishTrack struct {
	t     *ScriptTransport
	pc    *webrtc.PeerConnection
	local *yandex.TelemostTrack
	codec *yandex.KeyFrameCodec
	mime  string

	fps           int
	framesPerTick int
	pace          bool
	probe         bool
	maxQueue      int

	layers   atomic.Pointer[[]mediaLayer]
	ridExtID int

	seq     atomic.Uint32
	tsEpoch time.Time
	ts      atomic.Uint32

	queueMu sync.Mutex
	queue   [][]byte     // fragments waiting for the clock
	lane    atomic.Int32 // the stripe lane this track pulls from, -1 for none

	rtxMu                 sync.Mutex
	rtxRing               [mediaRTXRing]rtxSlot
	asked, resent, missed atomic.Int64
	uplinkLoss            atomic.Uint32

	ready atomic.Bool
	stop  chan struct{}
	once  sync.Once
}

type mediaLayer struct {
	ssrc uint32
	rid  string
}

func (p *publishTrack) layerList() []mediaLayer {
	if l := p.layers.Load(); l != nil {
		return *l
	}
	return []mediaLayer{{ssrc: uint32(p.local.SSRC())}}
}

// nextTS advances the RTP timestamp with wall-clock time at 90 kHz.
func (p *publishTrack) nextTS() uint32 {
	want := uint32(time.Since(p.tsEpoch) * 90000 / time.Second)
	for {
		cur := p.ts.Load()
		next := want
		if int32(next-cur) <= 0 {
			next = cur + 1
		}
		if p.ts.CompareAndSwap(cur, next) {
			return next
		}
	}
}

func (p *publishTrack) rememberRTX(hdr rtp.Header, payload []byte) {
	p.rtxMu.Lock()
	p.rtxRing[hdr.SequenceNumber%mediaRTXRing] = rtxSlot{hdr: hdr, payload: payload, ok: true}
	p.rtxMu.Unlock()
}

// sendFrame writes one wire frame on every declared layer.
func (p *publishTrack) sendFrame(wire []byte) {
	if len(wire) == 0 || !p.ready.Load() {
		return
	}
	frame := p.codec.Encode(wire)
	ts := p.nextTS()
	pt := uint8(p.local.PayloadType())
	if pt == 0 {
		pt = 96
	}
	for _, layer := range p.layerList() {
		hdr := rtp.Header{Version: 2, PayloadType: pt, SequenceNumber: uint16(p.seq.Add(1)),
			Timestamp: ts, Marker: true, SSRC: layer.ssrc}
		if layer.rid != "" && p.ridExtID > 0 {
			if hdr.SetExtension(uint8(p.ridExtID), []byte(layer.rid)) != nil {
				continue
			}
		}
		if n, err := p.local.WriteHeader(hdr, frame); err == nil && n > 0 {
			p.rememberRTX(hdr, frame)
		}
	}
}

// clock is the publish loop: one frame a tick, data or filler.
func (p *publishTrack) clock() {
	if !p.local.WaitBound(10 * time.Second) {
		return
	}
	if p.mime == webrtc.MimeTypeH264 {
		for _, nal := range yandex.H264ParameterSets() {
			for _, layer := range p.layerList() {
				hdr := rtp.Header{Version: 2, PayloadType: uint8(p.local.PayloadType()), SequenceNumber: uint16(p.seq.Add(1)), SSRC: layer.ssrc}
				_, _ = p.local.WriteHeader(hdr, nal)
			}
		}
	}
	interval := time.Second / time.Duration(p.fps)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	sent := 0
	for {
		select {
		case <-p.stop:
			return
		case <-p.t.Done():
			return
		case <-ticker.C:
		}
		if !p.ready.Load() {
			continue
		}
		frames := p.popQueue()
		if lane := int(p.lane.Load()); lane >= 0 && len(frames) < p.framesPerTick {
			if st, _ := p.t.lanes.get(); st != nil {
				_, ls := p.t.lanes.get()
				if lane < len(ls) {
					src := ls[lane].src.Load()
					if src != nil {
						for _, w := range (*src)(p.framesPerTick - len(frames)) {
							if frags, err := yandex.FragmentFrame(w); err == nil {
								frames = append(frames, frags...)
							}
						}
					}
				}
			}
		}
		if len(frames) == 0 {
			frames = [][]byte{yandex.FillerFrame(p.probe)}
		}
		gap := interval / time.Duration(len(frames))
		start := time.Now()
		for i, f := range frames {
			if i > 0 && p.pace {
				if d := time.Until(start.Add(time.Duration(i) * gap)); d > 0 {
					time.Sleep(d)
				}
			}
			p.sendFrame(f)
		}
		sent++
	}
}

func (p *publishTrack) popQueue() [][]byte {
	p.queueMu.Lock()
	defer p.queueMu.Unlock()
	n := len(p.queue)
	if n > p.framesPerTick {
		n = p.framesPerTick
	}
	if n == 0 {
		return nil
	}
	out := make([][]byte, n)
	copy(out, p.queue[:n])
	p.queue = p.queue[n:]
	return out
}

// answerRTCP resends packets the SFU NACKs and records the RR loss fraction.
func (p *publishTrack) answerRTCP(b []byte) {
	pkts, err := rtcp.Unmarshal(b)
	if err != nil {
		return
	}
	for _, pk := range pkts {
		if rr, ok := pk.(*rtcp.ReceiverReport); ok {
			for _, rep := range rr.Reports {
				p.uplinkLoss.Store(uint32(rep.FractionLost))
			}
			continue
		}
		nack, ok := pk.(*rtcp.TransportLayerNack)
		if !ok {
			continue
		}
		for _, pair := range nack.Nacks {
			for _, seq := range pair.PacketList() {
				p.asked.Add(1)
				p.rtxMu.Lock()
				e := p.rtxRing[seq%mediaRTXRing]
				p.rtxMu.Unlock()
				if !e.ok || e.hdr.SequenceNumber != seq {
					p.missed.Add(1)
					continue
				}
				if _, err := p.local.WriteHeader(e.hdr, e.payload); err == nil {
					p.resent.Add(1)
				}
			}
		}
	}
}

// recvTrack is the subscriber side: one remote stream back to the tunnel.
type recvTrack struct {
	t        *ScriptTransport
	pc       *webrtc.PeerConnection
	remote   *webrtc.TrackRemote
	codec    *yandex.KeyFrameCodec
	reasm    yandex.FrameReassembler
	lane     int32
	lastRecv atomic.Int64
}

func (r *recvTrack) read() {
	buf := make([]byte, 1500)
	for r.t.IsRunning() {
		n, _, err := r.remote.Read(buf)
		if err != nil {
			return
		}
		var pkt rtp.Packet
		if pkt.Unmarshal(buf[:n]) != nil || len(pkt.Payload) == 0 {
			continue
		}
		r.lastRecv.Store(time.Now().UnixNano())
		if r.codec == nil {
			continue
		}
		body, err := r.codec.Decode(pkt.Payload)
		if err != nil || len(body) == 0 {
			continue
		}
		frame, ok := r.reasm.Push(body, time.Now().UnixNano())
		if !ok {
			continue
		}
		if lane := int(r.lane); lane >= 0 {
			if _, ls := r.t.lanes.get(); lane < len(ls) {
				ls[lane].recv.Add(uint64(len(frame)))
				ls[lane].pktRecv.Add(1)
				if ls[lane].cb != nil {
					ls[lane].cb(append([]byte(nil), frame...))
				}
				continue
			}
		}
		r.t.deliverOrdered(append([]byte(nil), frame...))
	}
}

func (r *recvTrack) remb(pc *webrtc.PeerConnection, bps uint64) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for r.t.IsRunning() {
		if err := pc.WriteRTCP([]rtcp.Packet{&rtcp.ReceiverEstimatedMaximumBitrate{
			Bitrate: float32(bps), SSRCs: []uint32{uint32(r.remote.SSRC())},
		}}); err != nil {
			return
		}
		<-ticker.C
	}
}

// UplinkLoss implements transport.LossReporter for the stripe, from the one
// publishing track of this transport.
func (t *ScriptTransport) UplinkLoss() float64 {
	if p := t.pubTrack.Load(); p != nil {
		return float64((*p).uplinkLoss.Load()) / 256
	}
	return 0
}

func intOpt(o *goja.Object, key string, def int) int {
	if o == nil {
		return def
	}
	v := o.Get(key)
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return def
	}
	return int(v.ToInteger())
}

func boolOpt(o *goja.Object, key string, def bool) bool {
	if o == nil {
		return def
	}
	v := o.Get(key)
	if v == nil || goja.IsUndefined(v) {
		return def
	}
	return v.ToBoolean()
}

func strOpt(o *goja.Object, key, def string) string {
	if o == nil {
		return def
	}
	v := o.Get(key)
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return def
	}
	return v.String()
}

var _ = utils.Infof
