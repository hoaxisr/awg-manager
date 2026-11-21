package script

import (
	"fmt"
	"time"

	"github.com/dop251/goja"
	"github.com/google/uuid"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"

	"github.com/p1neappleXpress/OpenFlux/transport/yandex"
)

// newMediaPeerConnectionJS builds a PeerConnection whose API is the Telemost
// publisher's or subscriber's (browser codecs without RTX, the publisher's
// header extensions / the subscriber's feedback). opts.media says which. It
// adds, on top of the plain newPeerConnectionJS surface:
//
//	addMediaTransceiver(kind, {direction, codec})
//	publishTrack({codec, fps, ...}) -> a data track (host_media.go)
//	onMediaTrack(fn)                 for an inbound remote track
func newMediaPeerConnectionJS(vm *goja.Runtime, t *ScriptTransport, call goja.FunctionCall, opts *goja.Object, media string) goja.Value {
	config := webrtc.Configuration{}
	if v := opts.Get("iceServers"); v != nil && !goja.IsUndefined(v) {
		if arr, ok := v.Export().([]interface{}); ok {
			for _, raw := range arr {
				if m, ok := raw.(map[string]interface{}); ok {
					config.ICEServers = append(config.ICEServers, parseICEServer(m))
				}
			}
		}
	}
	if v := opts.Get("iceTransportPolicy"); v != nil && !goja.IsUndefined(v) && v.String() == "relay" {
		config.ICETransportPolicy = webrtc.ICETransportPolicyRelay
	}
	config.SDPSemantics = webrtc.SDPSemanticsUnifiedPlan

	var api *webrtc.API
	var err error
	if media == "subscriber" {
		api, err = yandex.SubscriberAPI()
	} else {
		api, err = yandex.PublisherAPI()
	}
	if err != nil {
		panic(vm.NewGoError(err))
	}
	pc, err := api.NewPeerConnection(config)
	if err != nil {
		panic(vm.NewGoError(err))
	}
	t.addCloser(func() { _ = pc.Close() })

	obj := commonPCObject(vm, t, pc)

	obj.Set("addMediaTransceiver", func(call goja.FunctionCall) goja.Value {
		kind := webrtc.NewRTPCodecType(call.Argument(0).String())
		if kind == 0 {
			panic(vm.NewTypeError(`addMediaTransceiver(kind, init): kind must be "audio" or "video"`))
		}
		o, _ := call.Argument(1).(*goja.Object)
		dir := webrtc.RTPTransceiverDirectionRecvonly
		if d := strOpt(o, "direction", "recvonly"); d != "" {
			if nd := webrtc.NewRTPTransceiverDirection(d); nd != 0 {
				dir = nd
			}
		}
		tr, err := pc.AddTransceiverFromKind(kind, webrtc.RTPTransceiverInit{Direction: dir})
		if err != nil {
			panic(vm.NewGoError(err))
		}
		if cm := strOpt(o, "codec", ""); cm != "" {
			_ = tr.SetCodecPreferences([]webrtc.RTPCodecParameters{
				{RTPCodecCapability: codecCap(cm)},
			})
		}
		return goja.Undefined()
	})

	obj.Set("publishTrack", func(call goja.FunctionCall) goja.Value {
		o, _ := call.Argument(0).(*goja.Object)
		mime := codecMime(strOpt(o, "codec", "video/VP8"))
		kind := webrtc.RTPCodecTypeVideo
		if mime == webrtc.MimeTypeOpus {
			kind = webrtc.RTPCodecTypeAudio
		}
		local := yandex.NewTelemostTrack(webrtc.RTPCodecCapability{MimeType: mime, ClockRate: 90000}, kind, uuid.New().String(), uuid.New().String())
		if kind == webrtc.RTPCodecTypeAudio {
			local = yandex.NewTelemostTrack(webrtc.RTPCodecCapability{MimeType: mime, ClockRate: 48000, Channels: 1}, kind, uuid.New().String(), uuid.New().String())
		}
		tr, err := pc.AddTransceiverFromKind(kind, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
		if err != nil {
			panic(vm.NewGoError(err))
		}
		if mime != webrtc.MimeTypeVP8 {
			_ = tr.SetCodecPreferences([]webrtc.RTPCodecParameters{{RTPCodecCapability: codecCap(mime)}})
		}
		if err := tr.Sender().ReplaceTrack(local); err != nil {
			panic(vm.NewGoError(err))
		}
		pt := &publishTrack{
			t: t, pc: pc, local: local, codec: yandex.NewKeyFrameCodec(mime), mime: mime,
			fps: intOpt(o, "fps", 120), framesPerTick: intOpt(o, "framesPerTick", 12),
			pace: boolOpt(o, "pace", true), probe: boolOpt(o, "probe", false),
			maxQueue: intOpt(o, "queue", 4096), tsEpoch: time.Now(), stop: make(chan struct{}),
		}
		pt.lane.Store(-1)
		t.pubTrack.Store(&pt)

		// Read the sender's RTCP so NACKs are answered and loss recorded.
		go func() {
			buf := make([]byte, 1500)
			for {
				n, _, err := tr.Sender().Read(buf)
				if err != nil {
					return
				}
				pt.answerRTCP(buf[:n])
			}
		}()
		return newPublishTrackJS(vm, t, pt)
	})

	obj.Set("onMediaTrack", func(call goja.FunctionCall) goja.Value {
		fn, _ := goja.AssertFunction(call.Argument(0))
		pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
			rt := &recvTrack{t: t, pc: pc, remote: remote, lane: -1}
			mime := remote.Codec().MimeType
			if mime == webrtc.MimeTypeVP8 || mime == webrtc.MimeTypeVP9 || mime == webrtc.MimeTypeH264 {
				rt.codec = yandex.NewKeyFrameCodec(mime)
			}
			t.loop.RunOnLoop(func(vm *goja.Runtime) {
				if fn != nil {
					_, _ = fn(goja.Undefined(), newRecvTrackJS(vm, t, rt))
				}
			})
		})
		return goja.Undefined()
	})

	return obj
}

func newPublishTrackJS(vm *goja.Runtime, t *ScriptTransport, p *publishTrack) *goja.Object {
	obj := vm.NewObject()
	obj.Set("setLayers", func(call goja.FunctionCall) goja.Value {
		var layers []mediaLayer
		if arr, ok := call.Argument(0).Export().([]interface{}); ok {
			for _, raw := range arr {
				if m, ok := raw.(map[string]interface{}); ok {
					l := mediaLayer{}
					if s, ok := m["ssrc"].(int64); ok {
						l.ssrc = uint32(s)
					} else if f, ok := m["ssrc"].(float64); ok {
						l.ssrc = uint32(f)
					}
					l.rid, _ = m["rid"].(string)
					layers = append(layers, l)
				}
			}
		}
		p.layers.Store(&layers)
		p.ridExtID = int(call.Argument(1).ToInteger())
		return goja.Undefined()
	})
	obj.Set("useLane", func(call goja.FunctionCall) goja.Value {
		p.lane.Store(int32(call.Argument(0).ToInteger()))
		return goja.Undefined()
	})
	obj.Set("write", func(call goja.FunctionCall) goja.Value {
		b, err := bytesArg(vm, call.Argument(0))
		if err != nil {
			panic(vm.NewTypeError("publishTrack.write: " + err.Error()))
		}
		frags, err := yandex.FragmentFrame(append([]byte(nil), b...))
		if err != nil {
			return goja.Undefined()
		}
		p.queueMu.Lock()
		if len(p.queue) < p.maxQueue {
			p.queue = append(p.queue, frags...)
		}
		p.queueMu.Unlock()
		return goja.Undefined()
	})
	obj.Set("start", func(call goja.FunctionCall) goja.Value {
		promise, resolve, reject := vm.NewPromise()
		go func() {
			if !p.local.WaitBound(10 * time.Second) {
				t.loop.RunOnLoop(func(vm *goja.Runtime) { _ = reject(vm.NewGoError(fmt.Errorf("publish track never bound"))) })
				return
			}
			p.ready.Store(true)
			// SDES + Sender Reports keep the stream live to the SFU.
			go p.runRTCP()
			p.once.Do(func() { go p.clock() })
			ssrc := uint32(p.local.SSRC())
			ptype := uint8(p.local.PayloadType())
			t.loop.RunOnLoop(func(vm *goja.Runtime) {
				out := vm.NewObject()
				out.Set("ssrc", ssrc)
				out.Set("payloadType", ptype)
				_ = resolve(out)
			})
		}()
		return vm.ToValue(promise)
	})
	obj.Set("ready", func(call goja.FunctionCall) goja.Value {
		p.ready.Store(call.Argument(0).ToBoolean())
		return goja.Undefined()
	})
	obj.Set("stats", func(call goja.FunctionCall) goja.Value {
		out := vm.NewObject()
		out.Set("sent", p.seq.Load())
		out.Set("nackAsked", p.asked.Load())
		out.Set("resent", p.resent.Load())
		out.Set("missed", p.missed.Load())
		out.Set("uplinkLoss", float64(p.uplinkLoss.Load())/256)
		return out
	})
	return obj
}

func (p *publishTrack) runRTCP() {
	if !p.local.WaitBound(5 * time.Second) {
		return
	}
	ssrc := uint32(p.local.SSRC())
	if ssrc == 0 {
		return
	}
	_ = p.pc.WriteRTCP([]rtcp.Packet{rtcp.NewCNAMESourceDescription(ssrc, p.local.CNAME())})
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if !p.t.IsRunning() {
			return
		}
		sr := p.local.SenderReport()
		if sr == nil || sr.PacketCount == 0 {
			continue
		}
		if p.pc.WriteRTCP([]rtcp.Packet{sr}) != nil {
			return
		}
	}
}

func newRecvTrackJS(vm *goja.Runtime, t *ScriptTransport, r *recvTrack) *goja.Object {
	obj := vm.NewObject()
	obj.Set("mimeType", r.remote.Codec().MimeType)
	obj.Set("ssrc", uint32(r.remote.SSRC()))
	obj.Set("id", r.remote.ID())
	obj.Set("kind", r.remote.Kind().String())
	obj.Set("deliver", func(call goja.FunctionCall) goja.Value {
		r.lane = int32(call.Argument(0).ToInteger())
		go r.read()
		return goja.Undefined()
	})
	obj.Set("remb", func(call goja.FunctionCall) goja.Value {
		bps := uint64(call.Argument(0).ToInteger())
		go r.remb(r.pc, bps)
		return goja.Undefined()
	})
	obj.Set("lastRecvMs", func(call goja.FunctionCall) goja.Value {
		v := r.lastRecv.Load()
		if v == 0 {
			return vm.ToValue(-1)
		}
		return vm.ToValue(time.Since(time.Unix(0, v)).Milliseconds())
	})
	return obj
}

func codecMime(name string) string {
	switch name {
	case "video/VP9", "vp9", "VP9":
		return webrtc.MimeTypeVP9
	case "video/H264", "h264", "H264":
		return webrtc.MimeTypeH264
	case "audio/opus", "opus", "OPUS":
		return webrtc.MimeTypeOpus
	default:
		return webrtc.MimeTypeVP8
	}
}

func codecCap(name string) webrtc.RTPCodecCapability {
	switch codecMime(name) {
	case webrtc.MimeTypeVP9:
		return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP9, ClockRate: 90000, SDPFmtpLine: "profile-id=0"}
	case webrtc.MimeTypeH264:
		return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f"}
	case webrtc.MimeTypeOpus:
		return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}
	default:
		return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}
	}
}
