package l3

import "encoding/binary"

// splitTCP cuts a TCP packet that is larger than limit into packets of at most limit bytes, or
// returns nil when p is not a packet it can cut (not TCP, a fragment, a SYN/RST/URG, already small).
//
// The raw sockets the exit reads the Internet from hand over what the kernel's receive path made
// of the wire, and that is not always what was on it: generic receive offload, or a virtual NIC
// given a host's batch, merges consecutive segments of a flow into one packet of several MSS. Such
// an aggregate is a valid packet, but larger than the client takes, and "packet too big" does not
// help: the sender never sent a packet of that size, so it has nothing to shrink, and the data in
// it would be lost to a retransmission timeout. The segments are cut out of it again: the same
// headers, the sequence number moving on with the payload, PSH and FIN on the last one only.
func splitTCP(p []byte, limit int) [][]byte {
	p, ok := sliceIPv4(p)
	if !ok || p[9] != 6 || isFragmentedIPv4(p) || len(p) <= limit {
		return nil
	}
	ihl := int(p[0]&0x0f) * 4
	if len(p) < ihl+20 {
		return nil
	}
	thl := int(p[ihl+12]>>4) * 4
	if thl < 20 || len(p) < ihl+thl {
		return nil
	}
	const urg, syn, rst = 0x20, 0x02, 0x04
	flags := p[ihl+13]
	if flags&(urg|syn|rst) != 0 {
		return nil
	}
	hdr := ihl + thl
	room := limit - hdr
	if room <= 0 {
		return nil
	}
	payload := p[hdr:]
	seq := binary.BigEndian.Uint32(p[ihl+4 : ihl+8])
	id := binary.BigEndian.Uint16(p[4:6])

	const cwr, fin, psh = 0x80, 0x01, 0x08
	var out [][]byte
	for off := 0; off < len(payload); off += room {
		size := min(room, len(payload)-off)
		part := make([]byte, hdr+size)
		copy(part, p[:hdr])
		copy(part[hdr:], payload[off:off+size])
		binary.BigEndian.PutUint16(part[2:4], uint16(len(part)))
		binary.BigEndian.PutUint16(part[4:6], id+uint16(len(out)))
		binary.BigEndian.PutUint32(part[ihl+4:ihl+8], seq+uint32(off))
		f := flags
		if off > 0 {
			f &^= cwr
		}
		if off+size < len(payload) {
			f &^= fin | psh
		}
		part[ihl+13] = f
		fixChecksums(part)
		out = append(out, part)
	}
	return out
}
