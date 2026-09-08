// Adapted from payloadGen (MIT) — https://github.com/Sketchystan1/payloadGen
package signature

import (
	"encoding/hex"
	"hash/crc32"
	mrand "math/rand"
	"strconv"
)

func init() { builders["stun"] = buildSTUN }

// Провайдер-профили STUN/TURN (порт-спека §2.1), в порядке r.Intn(5).
var stunProviders = []stunProvider{
	{
		name: "google", servers: googleStunServers, realm: "google.com",
		software:    func(r *mrand.Rand) string { return "Google STUN client" },
		lifetimeMin: 300, lifetimeMax: 600,
	},
	{
		name: "twilio_stun", servers: twilioStunServers, realm: "twilio.com",
		software:    func(r *mrand.Rand) string { return "Twilio WebRTC ICE agent" },
		lifetimeMin: 300, lifetimeMax: 600,
	},
	{
		name: "twilio", servers: twilioTurnServers, realm: "twilio.com",
		software:         func(r *mrand.Rand) string { return "Twilio WebRTC ICE agent" },
		supportsAllocate: true, autoAllocateProb: 0.67,
		lifetimeMin: 300, lifetimeMax: 600,
	},
	{
		name: "cloudflare", servers: cloudflareWebrtcServers, realm: "cloudflare.com",
		software:         func(r *mrand.Rand) string { return "Cloudflare WebRTC client" },
		supportsAllocate: true, autoAllocateProb: 0.67,
		lifetimeMin: 600, lifetimeMax: 1200,
	},
	{
		name: "meta", servers: metaWebrtcServers, realm: "facebook.com",
		software:         func(r *mrand.Rand) string { return metaSoftwareNames[r.Intn(len(metaSoftwareNames))] },
		supportsAllocate: true, autoAllocateProb: 0.75,
		lifetimeMin: 180, lifetimeMax: 600,
	},
}

var googleStunServers = []string{
	"stun.l.google.com", "stun1.l.google.com", "stun2.l.google.com",
	"stun3.l.google.com", "stun4.l.google.com", "stun.services.googleapis.com",
	"stun.phonebox.google.com", "stun.stunprotocol.org",
}
var twilioStunServers = []string{"global.stun.twilio.com"}
var twilioTurnServers = []string{
	"global.turn.twilio.com", "de01-1.turn.twilio.com", "de01-2.turn.twilio.com",
	"sg01-1.turn.twilio.com", "sg01-2.turn.twilio.com", "us1-1.turn.twilio.com",
	"us1-2.turn.twilio.com", "us2-1.turn.twilio.com", "us2-2.turn.twilio.com",
	"ie01-1.turn.twilio.com", "ie01-2.turn.twilio.com", "jp01-1.turn.twilio.com",
	"jp01-2.turn.twilio.com", "au01-1.turn.twilio.com", "br01-1.turn.twilio.com",
	"in01-1.turn.twilio.com",
}
var cloudflareWebrtcServers = []string{
	"turn.cloudflare.com", "webrtc.cloudflare.net", "spectrum.cloudflare.com", "calls.cloudflare.com",
}
var metaWebrtcServers = []string{
	"turn.instagram.com", "stun.whatsapp.com", "edge-turn.whatsapp.com",
	"turn-messenger.whatsapp.com", "star.c10r.facebook.com", "turn.dnsalias.com", "edge-chat.facebook.com",
}
var metaSoftwareNames = []string{"WhatsApp/2", "Instagram/2", "Messenger WebRTC"}

var twilioUsernamePrefixes = []string{
	"a1b2c3d4e5f6g7h8i9j0", "1a2b3c4d5e6f7g8h9i0j", "abcdef1234567890abcd", "1234567890abcdef1234",
}

type stunProvider struct {
	name             string
	servers          []string
	realm            string
	software         func(r *mrand.Rand) string
	supportsAllocate bool
	autoAllocateProb float64
	lifetimeMin      int
	lifetimeMax      int
}

// buildSTUN — статическое STUN/TURN-сообщение (§2): FINGERPRINT CRC32 покрывает
// всё сообщение, поэтому рантайм-токенов нет, только один <b>. Провайдер и
// Allocate/Binding выбираются заново на каждую генерацию через r.
//
// Два намеренных отклонения от payloadGen (см. брифинг задачи): Allocate —
// тип 0x0003 (RFC 5766, а не 0x000A источника), REQUESTED-TRANSPORT — 4 байта
// `11 00 00 00` (RFC 5766 §14.7, а не 8 байт источника).
func buildSTUN(r *mrand.Rand) (GeneratedPackets, error) {
	p := stunProviders[r.Intn(len(stunProviders))]
	host := p.servers[r.Intn(len(p.servers))]

	allocate := p.supportsAllocate && r.Float64() < p.autoAllocateProb
	msgType := uint16(0x0001)
	if allocate {
		msgType = 0x0003
	}

	var attrs []byte
	if allocate {
		attrs = append(attrs, stunAttr(0x0014, []byte(p.realm))...) // REALM
		lifetime := p.lifetimeMin + r.Intn(p.lifetimeMax-p.lifetimeMin)
		attrs = append(attrs, stunAttr(0x000D, u32Bytes(uint32(lifetime)))...) // LIFETIME
		attrs = append(attrs, stunAttr(0x0019, []byte{0x11, 0, 0, 0})...)      // REQUESTED-TRANSPORT
		family := byte(0x01)
		if r.Intn(2) == 1 {
			family = 0x02
		}
		attrs = append(attrs, stunAttr(0x8027, []byte{0x00, family, 0x00, 0x00})...) // REQUESTED-ADDRESS-FAMILY
	}

	attrs = append(attrs, stunAttr(0x8022, []byte(p.software(r)))...) // SOFTWARE

	priority := r.Uint32() | 0x40000000
	attrs = append(attrs, stunAttr(0x0024, u32Bytes(priority))...) // PRIORITY

	controlType := uint16(0x8029) // ICE-CONTROLLED
	if r.Intn(2) == 1 {
		controlType = 0x802A // ICE-CONTROLLING
	}
	control := append(u32Bytes(r.Uint32()), u32Bytes(r.Uint32())...)
	attrs = append(attrs, stunAttr(controlType, control)...)

	attrs = append(attrs, stunAttr(0x0006, []byte(stunUsername(r, p, host, allocate)))...) // USERNAME

	return GeneratedPackets{I1: tokB(buildSTUNMessage(msgType, attrs))}, nil
}

// stunUsername — форматы USERNAME (§2.6).
func stunUsername(r *mrand.Rand, p stunProvider, host string, allocate bool) string {
	if allocate {
		suffix := strconv.Itoa(1000 + r.Intn(9000))
		switch p.name {
		case "meta":
			return "WA-" + strconv.FormatInt(1000000000+r.Int63n(9000000000), 10) + "@" + host
		case "twilio":
			return twilioUsernamePrefixes[r.Intn(len(twilioUsernamePrefixes))] + suffix + "@" + host
		default:
			b := make([]byte, 8)
			r.Read(b)
			return hex.EncodeToString(b) + suffix + "@" + host
		}
	}
	switch p.name {
	case "meta":
		return "WA-" + strconv.FormatInt(1000000000+r.Int63n(9000000000), 10) + ":" + host
	case "twilio":
		return twilioUsernamePrefixes[r.Intn(len(twilioUsernamePrefixes))] + ":" + host
	default:
		b := make([]byte, 4)
		r.Read(b)
		return hex.EncodeToString(b) + ":" + host
	}
}

// stunAttr — TLV-атрибут (§2.5): длина в заголовке без набивки, набивка нулями до 4 байт.
func stunAttr(typ uint16, val []byte) []byte {
	w := &wire{}
	w.u16(int(typ))
	w.u16(len(val))
	w.raw(val)
	if pad := (4 - len(val)%4) % 4; pad > 0 {
		w.raw(make([]byte, pad))
	}
	return w.bytes()
}

func u32Bytes(v uint32) []byte {
	w := &wire{}
	w.u32(v)
	return w.bytes()
}

// buildSTUNMessage — заголовок + атрибуты + FINGERPRINT (§2.7). CRC32 считается
// по сообщению БЕЗ атрибута FINGERPRINT целиком (RFC 5389 §15.5), а поле длины
// уже учитывает его 8 байт.
func buildSTUNMessage(msgType uint16, attrs []byte) []byte {
	txID := randBytes(12)
	h := &wire{}
	h.u16(int(msgType))
	h.u16(len(attrs) + 8)
	h.u32(0x2112A442)
	h.raw(txID)
	h.raw(attrs)
	base := h.bytes()

	crc := crc32.ChecksumIEEE(base) ^ 0x5354554E
	return append(base, stunAttr(0x8028, u32Bytes(crc))...)
}
