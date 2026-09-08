// Adapted from payloadGen (MIT) — https://github.com/Sketchystan1/payloadGen
package signature

import (
	mrand "math/rand"
	"strings"
)

func init() { builders["dns"] = buildDNS }

var dnsQueryTypes = []int{1, 28, 65} // A, AAAA, HTTPS — §3.4

// buildDNS — DNS-запрос (§3): TXID случаен на рантайме (<r 2>), остальное тело
// (флаги, счётчики, QNAME/QTYPE/QCLASS, EDNS0 OPT) статично внутри генерации.
func buildDNS(r *mrand.Rand) (GeneratedPackets, error) {
	w := &wire{}
	w.u16(0x0100) // flags: standard query, RD=1
	w.u16(1)      // QDCOUNT
	w.u16(0)      // ANCOUNT
	w.u16(0)      // NSCOUNT
	w.u16(1)      // ARCOUNT

	for _, label := range strings.Split(pickHost(r), ".") {
		w.u8(len(label))
		w.str(label)
	}
	w.u8(0) // QNAME terminator

	w.u16(dnsQueryTypes[r.Intn(len(dnsQueryTypes))]) // QTYPE
	w.u16(1)                                         // QCLASS IN

	w.u8(0)     // OPT: root name
	w.u16(0x29) // TYPE = OPT (41)
	w.u16(1232) // UDP payload size
	w.u32(0)    // extended-RCODE + version + flags (DO=0)
	w.u16(0)    // RDLEN = 0

	return GeneratedPackets{I1: "<r 2>" + tokB(w.bytes())}, nil
}
