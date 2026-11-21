package yandex

import (
	"github.com/pion/webrtc/v4"

	"github.com/p1neappleXpress/OpenFlux/transport"
)

// What the script host (transport/script, host_media.go) reuses to give a
// JS transport the media plumbing the Telemost carrier is built on: the
// browser-like WebRTC APIs, a track whose RTP headers are written by hand,
// tunnel bytes inside codec keyframes, fragments that fit one RTP packet and
// the filler frames that keep a published stream alive. The protocol around
// them (signaling, SDP, slots) is the script's.

// PublisherAPI is the WebRTC API a publishing participant uses: browser codecs
// without RTX, and the header extensions a browser offers.
func PublisherAPI() (*webrtc.API, error) {
	m, err := newTelemostMediaEngine()
	if err != nil {
		return nil, err
	}
	return webrtc.NewAPI(webrtc.WithMediaEngine(m)), nil
}

// SubscriberAPI is the WebRTC API a subscribing participant uses (see
// newTelemostSubscriberAPI for its feedback options).
func SubscriberAPI() (*webrtc.API, error) { return newTelemostSubscriberAPI() }

// KeyFrameCodec is a keyframe generator and the matching decoder for one
// video codec (video/VP8, video/VP9, video/H264).
type KeyFrameCodec struct {
	gen keyFrameGenerator
	dec keyFrameDecoder
}

// NewKeyFrameCodec returns the codec for mime; VP8 for anything unknown.
func NewKeyFrameCodec(mime string) *KeyFrameCodec {
	switch mime {
	case webrtc.MimeTypeVP9:
		return &KeyFrameCodec{gen: &VP9FrameGenerator{}, dec: NewVP9FrameDecoder()}
	case webrtc.MimeTypeH264:
		return &KeyFrameCodec{gen: &H264FrameGenerator{}, dec: NewH264FrameDecoder()}
	}
	return &KeyFrameCodec{gen: &CanvasVideoGenerator{width: 1920, height: 1080}, dec: NewCanvasVideoDecoder(1920, 1080)}
}

// Encode wraps data in a keyframe.
func (c *KeyFrameCodec) Encode(data []byte) []byte { return c.gen.GenerateKeyFrame(data) }

// Decode takes the data back out of a received frame's RTP payload.
func (c *KeyFrameCodec) Decode(payload []byte) ([]byte, error) { return c.dec.DecodeFrame(payload) }

// H264ParameterSets are the SPS and PPS an H.264 stream starts with.
func H264ParameterSets() [][]byte { return [][]byte{H264SPS, H264PPS} }

// FrameReassembler puts fragments (FragmentFrame) back together.
type FrameReassembler struct{ r frameReassembler }

// Push adds one decoded fragment body; ok is true with a whole frame.
func (f *FrameReassembler) Push(body []byte, nowNano int64) ([]byte, bool) {
	return f.r.push(body, nowNano)
}

// FillerFrame is the frame a publish tick sends when it has no data: an empty
// batch, or (probe, the exit's) a batch with one empty record, which tells a
// subscriber the other side apart from its own self-view.
func FillerFrame(probe bool) []byte {
	if probe {
		return transport.EncodeBatchFrame([][]byte{{}})
	}
	return transport.EncodeBatchFrame(nil)
}

// RIDExtensionURI is the RTP header extension that carries a simulcast RID.
const RIDExtensionURI = extURIRID
