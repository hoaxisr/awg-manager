package dns

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"
)

// DNS record types and RCODEs used by the coverage check.
const (
	typeA    = 1
	typeAAAA = 28
	classIN  = 1
)

// Header field offsets and wire constants.
const (
	hdrLen     = 12
	qrResponse = 1 << 15
	flagTC     = 1 << 9
	rcodeMask  = 0xF
	rcodeNX    = 3
	rcodeServ  = 2
	rcodeRef   = 5

	typeCNAME = 5
)

// rcodeName names the RCODEs the resolver can report.
func rcodeName(rcode int) string {
	switch rcode {
	case 0:
		return "NOERROR"
	case rcodeNX:
		return "NXDOMAIN"
	case rcodeServ:
		return "SERVFAIL"
	case rcodeRef:
		return "REFUSED"
	case 1:
		return "FORMERR"
	case 4:
		return "NOTIMP"
	default:
		return fmt.Sprintf("RCODE %d", rcode)
	}
}

// buildQuery encodes one standard recursive A/AAAA question. The question
// section is the only variable part: each label is written as a length byte
// plus its bytes, so a malformed name cannot produce a malformed encoding.
func buildQuery(id uint16, name string, qtype int) ([]byte, error) {
	if len(name) > 253 {
		return nil, fmt.Errorf("question name %q is too long", name)
	}
	// 12 header + name (at most 255 on the wire) + 4 question bytes.
	msg := make([]byte, 0, hdrLen+len(name)+2+4)
	msg = binary.BigEndian.AppendUint16(msg, id)
	msg = binary.BigEndian.AppendUint16(msg, 0x0100) // RD=1
	msg = binary.BigEndian.AppendUint16(msg, 1)      // QDCOUNT=1
	msg = binary.BigEndian.AppendUint16(msg, 0)      // ANCOUNT=0
	msg = binary.BigEndian.AppendUint16(msg, 0)      // NSCOUNT=0
	msg = binary.BigEndian.AppendUint16(msg, 0)      // ARCOUNT=0
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 {
			return nil, fmt.Errorf("invalid question label in %q", name)
		}
		msg = append(msg, byte(len(label)))
		msg = append(msg, label...)
	}
	msg = append(msg, 0) // root
	msg = binary.BigEndian.AppendUint16(msg, uint16(qtype))
	msg = binary.BigEndian.AppendUint16(msg, classIN)
	return msg, nil
}

// parsedName is a decoded resource record name plus the offset just after it,
// so record parsing can continue deterministically.
type parsedName struct {
	name string
	next int
}

// parseName decodes a possibly compressed name. Loops, forward pointers and
// overflows are rejected instead of trusted.
func parseName(msg []byte, off int, depth int) (parsedName, error) {
	if depth > 8 {
		return parsedName{}, fmt.Errorf("name compression too deep")
	}
	var labels []string
	jumps := 0
	end := -1
	for {
		if off >= len(msg) {
			return parsedName{}, fmt.Errorf("name runs past the end of the message")
		}
		length := int(msg[off])
		switch {
		case length == 0:
			off++
			if end < 0 {
				end = off
			}
			return parsedName{name: strings.Join(labels, "."), next: end}, nil
		case length&0xC0 == 0xC0:
			if off+1 >= len(msg) {
				return parsedName{}, fmt.Errorf("truncated compression pointer")
			}
			ptr := int(binary.BigEndian.Uint16(msg[off:])) & 0x3FFF
			if ptr >= off {
				return parsedName{}, fmt.Errorf("forward compression pointer")
			}
			off += 2
			if end < 0 {
				end = off
			}
			jumps++
			if jumps > 8 {
				return parsedName{}, fmt.Errorf("too many compression jumps")
			}
			sub, err := parseName(msg, ptr, depth+1)
			if err != nil {
				return parsedName{}, err
			}
			if sub.name != "" {
				labels = append(labels, sub.name)
			}
			return parsedName{name: strings.Join(labels, "."), next: end}, nil
		case length&0xC0 != 0:
			return parsedName{}, fmt.Errorf("reserved name label type 0b%02b", (length&0xC0)>>6)
		case length > 63:
			return parsedName{}, fmt.Errorf("name label longer than 63 bytes")
		default:
			if off+1+length > len(msg) {
				return parsedName{}, fmt.Errorf("name label runs past the end of the message")
			}
			labels = append(labels, string(msg[off+1:off+1+length]))
			off += 1 + length
		}
	}
}

// parseResponse validates the reply and returns the addresses of the asked
// family from the answer section. Every structural deviation is an error:
// wrong transaction ID, mismatched question, non-zero RCODE, TC bit, class or
// type other than IN and A/AAAA/CNAME, malformed bytes or trailing bytes.
func parseResponse(msg []byte, id uint16, name string, qtype int) ([]netip.Addr, error) {
	if len(msg) < hdrLen {
		return nil, fmt.Errorf("response shorter than the DNS header")
	}
	gotID := binary.BigEndian.Uint16(msg[0:2])
	if gotID != id {
		return nil, fmt.Errorf("transaction ID mismatch: got %#04x, want %#04x", gotID, id)
	}
	flags := binary.BigEndian.Uint16(msg[2:4])
	if flags&qrResponse == 0 {
		return nil, fmt.Errorf("message is not a response")
	}
	if op := flags >> 11 & 0xF; op != 0 {
		return nil, fmt.Errorf("unsupported opcode %d", op)
	}
	if flags&flagTC != 0 {
		return nil, fmt.Errorf("truncated response")
	}
	if rcode := int(flags & rcodeMask); rcode != 0 {
		return nil, fmt.Errorf("%s", rcodeName(rcode))
	}
	qdcount := int(binary.BigEndian.Uint16(msg[4:6]))
	ancount := int(binary.BigEndian.Uint16(msg[6:8]))
	nscount := int(binary.BigEndian.Uint16(msg[8:10]))
	arcount := int(binary.BigEndian.Uint16(msg[10:12]))
	qname, gotType, gotClass, off, err := parseQuestion(msg, hdrLen, qdcount)
	if err != nil {
		return nil, err
	}
	if gotType != qtype || gotClass != classIN {
		return nil, fmt.Errorf("question type/class mismatch: got type %d class %d, want type %d class IN(%d)",
			gotType, gotClass, qtype, classIN)
	}
	if !strings.EqualFold(qname, name) {
		return nil, fmt.Errorf("question name mismatch: got %q, want %q", qname, name)
	}

	answers := make([]resourceRecord, 0, ancount)
	aliases := map[string]string{}
	for i := 0; i < ancount; i++ {
		record, next, err := parseResourceRecord(msg, off, qtype, true)
		if err != nil {
			return nil, fmt.Errorf("answer #%d: %w", i, err)
		}
		off = next
		answers = append(answers, record)
		if record.typ == typeCNAME {
			aliases[strings.ToLower(record.name)] = strings.ToLower(record.cname)
		}
	}
	for i := 0; i < nscount+arcount; i++ {
		next, err := skipResourceRecord(msg, off)
		if err != nil {
			return nil, fmt.Errorf("authority/additional #%d: %w", i, err)
		}
		off = next
	}
	if off != len(msg) {
		return nil, fmt.Errorf("%d trailing bytes after the message", len(msg)-off)
	}
	var addrs []netip.Addr
	for _, record := range answers {
		if !dnsNameInChain(name, record.name, aliases) {
			return nil, fmt.Errorf("answer owner %q is outside the CNAME chain for %q", record.name, name)
		}
		if record.typ == qtype {
			addr, ok := netip.AddrFromSlice(record.data)
			if !ok {
				return nil, fmt.Errorf("%s RDATA is not a valid address", typeName(qtype))
			}
			addrs = append(addrs, addr.Unmap())
		}
	}
	return addrs, nil
}

type resourceRecord struct {
	name  string
	typ   int
	data  []byte
	cname string
}

func parseResourceRecord(msg []byte, off, qtype int, answer bool) (resourceRecord, int, error) {
	owner, err := parseName(msg, off, 0)
	if err != nil {
		return resourceRecord{}, 0, err
	}
	off = owner.next
	if off+10 > len(msg) {
		return resourceRecord{}, 0, fmt.Errorf("fixed part runs past the end of the message")
	}
	record := resourceRecord{name: owner.name, typ: int(binary.BigEndian.Uint16(msg[off:]))}
	class := int(binary.BigEndian.Uint16(msg[off+2:]))
	rdlen := int(binary.BigEndian.Uint16(msg[off+8:]))
	off += 10
	if answer && class != classIN {
		return resourceRecord{}, 0, fmt.Errorf("class %d is not IN", class)
	}
	if off+rdlen > len(msg) {
		return resourceRecord{}, 0, fmt.Errorf("RDATA runs past the end of the message")
	}
	dataEnd := off + rdlen
	switch record.typ {
	case qtype:
		if rdlen != expectedRDLength(qtype) {
			return resourceRecord{}, 0, fmt.Errorf("%s RDLENGTH %d, want %d", typeName(qtype), rdlen, expectedRDLength(qtype))
		}
		record.data = msg[off:dataEnd]
	case typeCNAME:
		cname, err := parseName(msg, off, 0)
		if err != nil {
			return resourceRecord{}, 0, fmt.Errorf("CNAME: %w", err)
		}
		if cname.next != dataEnd {
			return resourceRecord{}, 0, fmt.Errorf("CNAME length does not match RDLENGTH %d", rdlen)
		}
		record.cname = cname.name
	default:
		if answer {
			return resourceRecord{}, 0, fmt.Errorf("unsupported record type %d", record.typ)
		}
	}
	return record, dataEnd, nil
}

func skipResourceRecord(msg []byte, off int) (int, error) {
	owner, err := parseName(msg, off, 0)
	if err != nil {
		return 0, err
	}
	off = owner.next
	if off+10 > len(msg) {
		return 0, fmt.Errorf("fixed part runs past the end of the message")
	}
	rdlen := int(binary.BigEndian.Uint16(msg[off+8:]))
	off += 10 + rdlen
	if off > len(msg) {
		return 0, fmt.Errorf("RDATA runs past the end of the message")
	}
	return off, nil
}

func dnsNameInChain(query, owner string, aliases map[string]string) bool {
	query = strings.ToLower(query)
	owner = strings.ToLower(owner)
	for steps := 0; steps < 16; steps++ {
		if query == owner {
			return true
		}
		next, ok := aliases[query]
		if !ok {
			return false
		}
		query = next
	}
	return false
}

// parseQuestion validates the single question of the reply.
func parseQuestion(msg []byte, off int, qdcount int) (string, int, int, int, error) {
	if qdcount != 1 {
		return "", 0, 0, 0, fmt.Errorf("QDCOUNT %d, want 1", qdcount)
	}
	qn, err := parseName(msg, off, 0)
	if err != nil {
		return "", 0, 0, 0, fmt.Errorf("question: %w", err)
	}
	off = qn.next
	if off+4 > len(msg) {
		return "", 0, 0, 0, fmt.Errorf("question fixed part runs past the end of the message")
	}
	qtype := int(binary.BigEndian.Uint16(msg[off:]))
	qclass := int(binary.BigEndian.Uint16(msg[off+2:]))
	return qn.name, qtype, qclass, off + 4, nil
}

// expectedRDLength is the fixed RDATA size of A and AAAA records.
func expectedRDLength(qtype int) int {
	if qtype == typeA {
		return 4
	}
	return 16
}

// typeName names the two record types the resolver asks for.
func typeName(qtype int) string {
	if qtype == typeA {
		return "A"
	}
	return "AAAA"
}
