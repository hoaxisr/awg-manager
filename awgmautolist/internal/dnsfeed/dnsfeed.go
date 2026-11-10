// Package dnsfeed extracts autolist address hints from DNS responses.
//
// It deliberately handles DNS payloads only: callers provide a UDP DNS message
// (or one DNS-over-TCP frame), together with transport metadata such as the
// client address. No IP packet decoding or network I/O belongs here.
package dnsfeed

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

const (
	typeA     = 1
	typeAAAA  = 28
	typeCNAME = 5

	maxRealIPs      = 16
	maxChainNames   = 12 // queried name plus at most eleven CNAME targets
	maxPointerHops  = 128
	maxDNSNameWire  = 255
	dnsHeaderLength = 12
)

var (
	// ErrMalformed reports structurally invalid DNS wire data.
	ErrMalformed = errors.New("dnsfeed: malformed DNS response")
	// ErrTruncated reports a short payload, a truncated field, or the DNS TC bit.
	ErrTruncated = errors.New("dnsfeed: truncated DNS response")
	// ErrNotResponse reports a message without the DNS response bit set.
	ErrNotResponse = errors.New("dnsfeed: not a DNS response")
	// ErrNameLoop reports a cycle in DNS compression pointers.
	ErrNameLoop = errors.New("dnsfeed: DNS compression pointer loop")
	// ErrCNAMELoop reports a cycle reachable from the queried name.
	ErrCNAMELoop = errors.New("dnsfeed: CNAME loop")
	// ErrNoUsableAnswers reports a response that cannot produce an autolist hint.
	ErrNoUsableAnswers = errors.New("dnsfeed: no usable DNS answers")
)

// Answer is the part of a DNS response needed for an autolist hint.
// QueriedName is the normalized first question name and is the name to pass to
// the judge, matching autolist's hint semantics. FinalName is the last name in its
// CNAME chain, or QueriedName when no CNAME is present. Names are ASCII-folded
// to lower case and have no trailing dot.
//
// RealIPs contains at most 16 A/AAAA addresses in answer-section order. Every
// address owner is the queried name or a name on its CNAME chain; addresses for
// unrelated owners are never returned.
type Answer struct {
	ID          uint16
	QueriedName string
	FinalName   string
	RealIPs     []netip.Addr
}

type parser struct {
	msg []byte
}

type record struct {
	name   string
	typ    uint16
	rdata  []byte
	target string // decoded CNAME target
}

// ParseResponse parses one DNS response carried as a UDP payload. Only
// successful (RCODE=0), non-truncated A/AAAA responses can yield an Answer.
func ParseResponse(payload []byte) (Answer, error) {
	p := parser{msg: payload}
	if len(payload) < dnsHeaderLength {
		return Answer{}, fmt.Errorf("%w: header", ErrTruncated)
	}

	id := binary.BigEndian.Uint16(payload[0:2])
	flags := binary.BigEndian.Uint16(payload[2:4])
	if flags&0x8000 == 0 {
		return Answer{}, ErrNotResponse
	}
	if flags&0x0200 != 0 {
		return Answer{}, fmt.Errorf("%w: TC bit is set", ErrTruncated)
	}

	qdcount := int(binary.BigEndian.Uint16(payload[4:6]))
	ancount := int(binary.BigEndian.Uint16(payload[6:8]))
	nscount := int(binary.BigEndian.Uint16(payload[8:10]))
	arcount := int(binary.BigEndian.Uint16(payload[10:12]))
	if qdcount == 0 {
		return Answer{}, fmt.Errorf("%w: missing question", ErrNoUsableAnswers)
	}

	off := dnsHeaderLength
	var queriedName string
	var queryType uint16
	for i := 0; i < qdcount; i++ {
		name, next, err := p.decodeName(off)
		if err != nil {
			return Answer{}, fmt.Errorf("question %d: %w", i, err)
		}
		off = next
		if off+4 > len(payload) {
			return Answer{}, fmt.Errorf("%w: question %d type/class", ErrTruncated, i)
		}
		if i == 0 {
			queriedName = name
			queryType = binary.BigEndian.Uint16(payload[off : off+2])
		}
		off += 4
	}

	answers := make([]record, 0, min(ancount, maxRealIPs))
	var err error
	off, answers, err = p.parseRecords(off, ancount, true)
	if err != nil {
		return Answer{}, fmt.Errorf("answer section: %w", err)
	}
	off, _, err = p.parseRecords(off, nscount, false)
	if err != nil {
		return Answer{}, fmt.Errorf("authority section: %w", err)
	}
	off, _, err = p.parseRecords(off, arcount, false)
	if err != nil {
		return Answer{}, fmt.Errorf("additional section: %w", err)
	}
	if off != len(payload) {
		return Answer{}, fmt.Errorf("%w: %d trailing bytes", ErrMalformed, len(payload)-off)
	}

	if flags&0x000f != 0 {
		return Answer{}, fmt.Errorf("%w: RCODE=%d", ErrNoUsableAnswers, flags&0x000f)
	}
	if queriedName == "" || (queryType != typeA && queryType != typeAAAA) {
		return Answer{}, fmt.Errorf("%w: question is not a named A/AAAA query", ErrNoUsableAnswers)
	}

	chain, err := cnameChain(queriedName, answers)
	if err != nil {
		return Answer{}, err
	}
	inChain := make(map[string]struct{}, len(chain))
	for _, name := range chain {
		inChain[name] = struct{}{}
	}

	answer := Answer{
		ID:          id,
		QueriedName: queriedName,
		FinalName:   chain[len(chain)-1],
		RealIPs:     make([]netip.Addr, 0, min(len(answers), maxRealIPs)),
	}
	for _, rr := range answers {
		if _, ok := inChain[rr.name]; !ok {
			continue
		}
		switch rr.typ {
		case typeA:
			var raw [4]byte
			copy(raw[:], rr.rdata)
			answer.RealIPs = append(answer.RealIPs, netip.AddrFrom4(raw))
		case typeAAAA:
			var raw [16]byte
			copy(raw[:], rr.rdata)
			answer.RealIPs = append(answer.RealIPs, netip.AddrFrom16(raw))
		default:
			continue
		}
		if len(answer.RealIPs) == maxRealIPs {
			break
		}
	}
	if len(answer.RealIPs) == 0 {
		return Answer{}, ErrNoUsableAnswers
	}
	return answer, nil
}

// ParseTCPResponse parses exactly one complete DNS-over-TCP frame, including
// its two-byte length prefix. It does not consume a stream or a partial frame.
func ParseTCPResponse(frame []byte) (Answer, error) {
	if len(frame) < 2 {
		return Answer{}, fmt.Errorf("%w: TCP length prefix", ErrTruncated)
	}
	length := int(binary.BigEndian.Uint16(frame[:2]))
	if len(frame)-2 < length {
		return Answer{}, fmt.Errorf("%w: TCP frame declares %d bytes, has %d", ErrTruncated, length, len(frame)-2)
	}
	if len(frame)-2 > length {
		return Answer{}, fmt.Errorf("%w: trailing bytes after TCP frame", ErrMalformed)
	}
	return ParseResponse(frame[2:])
}

func (p *parser) parseRecords(off, count int, keep bool) (int, []record, error) {
	var records []record
	if keep {
		records = make([]record, 0, min(count, maxRealIPs))
	}
	for i := 0; i < count; i++ {
		name, next, err := p.decodeName(off)
		if err != nil {
			return off, nil, fmt.Errorf("record %d name: %w", i, err)
		}
		off = next
		if off+10 > len(p.msg) {
			return off, nil, fmt.Errorf("%w: record %d header", ErrTruncated, i)
		}
		typ := binary.BigEndian.Uint16(p.msg[off : off+2])
		rdlength := int(binary.BigEndian.Uint16(p.msg[off+8 : off+10]))
		rdataOff := off + 10
		rdataEnd := rdataOff + rdlength
		if rdataEnd > len(p.msg) {
			return off, nil, fmt.Errorf("%w: record %d RDATA", ErrTruncated, i)
		}

		rr := record{name: name, typ: typ}
		if keep {
			switch typ {
			case typeA:
				if rdlength != 4 {
					return off, nil, fmt.Errorf("%w: A record %d has RDLENGTH %d", ErrMalformed, i, rdlength)
				}
				rr.rdata = p.msg[rdataOff:rdataEnd]
			case typeAAAA:
				if rdlength != 16 {
					return off, nil, fmt.Errorf("%w: AAAA record %d has RDLENGTH %d", ErrMalformed, i, rdlength)
				}
				rr.rdata = p.msg[rdataOff:rdataEnd]
			case typeCNAME:
				target, targetEnd, err := p.decodeName(rdataOff)
				if err != nil {
					return off, nil, fmt.Errorf("CNAME record %d target: %w", i, err)
				}
				if targetEnd != rdataEnd {
					return off, nil, fmt.Errorf("%w: CNAME record %d has trailing RDATA", ErrMalformed, i)
				}
				rr.target = target
			}
			records = append(records, rr)
		}
		off = rdataEnd
	}
	return off, records, nil
}

// decodeName decodes a possibly compressed name and returns the first byte
// after its uncompressed portion. Pointer targets are visited in a separate
// path, so compression never changes where the enclosing field continues.
func (p *parser) decodeName(off int) (string, int, error) {
	if off < 0 || off >= len(p.msg) {
		return "", off, fmt.Errorf("%w: name starts outside message", ErrTruncated)
	}

	pos := off
	next := -1
	hops := 0
	wireLength := 0
	visited := make(map[int]struct{})
	var name strings.Builder

	for {
		if pos < 0 || pos >= len(p.msg) {
			return "", off, fmt.Errorf("%w: name extends outside message", ErrTruncated)
		}
		if _, ok := visited[pos]; ok {
			return "", off, ErrNameLoop
		}
		visited[pos] = struct{}{}

		length := p.msg[pos]
		switch length & 0xc0 {
		case 0xc0:
			if pos+1 >= len(p.msg) {
				return "", off, fmt.Errorf("%w: compression pointer", ErrTruncated)
			}
			target := int(length&0x3f)<<8 | int(p.msg[pos+1])
			if target >= len(p.msg) {
				return "", off, fmt.Errorf("%w: compression pointer target %d", ErrMalformed, target)
			}
			if next < 0 {
				next = pos + 2
			}
			hops++
			if hops > maxPointerHops {
				return "", off, fmt.Errorf("%w: too many compression pointers", ErrMalformed)
			}
			pos = target
			continue
		case 0x00:
			if length == 0 {
				wireLength++
				if wireLength > maxDNSNameWire {
					return "", off, fmt.Errorf("%w: expanded name exceeds 255 bytes", ErrMalformed)
				}
				if next < 0 {
					next = pos + 1
				}
				return name.String(), next, nil
			}
			labelLength := int(length)
			if pos+1+labelLength > len(p.msg) {
				return "", off, fmt.Errorf("%w: label", ErrTruncated)
			}
			wireLength += 1 + labelLength
			if wireLength > maxDNSNameWire {
				return "", off, fmt.Errorf("%w: expanded name exceeds 255 bytes", ErrMalformed)
			}
			if name.Len() != 0 {
				name.WriteByte('.')
			}
			writeNormalizedLabel(&name, p.msg[pos+1:pos+1+labelLength])
			pos += 1 + labelLength
		default:
			return "", off, fmt.Errorf("%w: unsupported label tag 0x%02x", ErrMalformed, length&0xc0)
		}
	}
}

func writeNormalizedLabel(dst *strings.Builder, label []byte) {
	for _, b := range label {
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		switch b {
		case '.', '(', ')', ';', ' ', '@', '"', '\\':
			dst.WriteByte('\\')
			dst.WriteByte(b)
		default:
			if b < 32 || b >= 127 {
				fmt.Fprintf(dst, "\\%03d", b)
			} else {
				dst.WriteByte(b)
			}
		}
	}
}

func cnameChain(queried string, answers []record) ([]string, error) {
	chain := []string{queried}
	seen := map[string]struct{}{queried: {}}
	current := queried

	for {
		target := ""
		found := false
		for _, rr := range answers {
			if rr.typ != typeCNAME || rr.name != current {
				continue
			}
			if found && rr.target != target {
				return nil, fmt.Errorf("%w: conflicting CNAME targets for %q", ErrMalformed, current)
			}
			target = rr.target
			found = true
		}
		if !found {
			return chain, nil
		}
		if _, ok := seen[target]; ok {
			return nil, fmt.Errorf("%w at %q", ErrCNAMELoop, target)
		}
		if len(chain) >= maxChainNames {
			return nil, fmt.Errorf("%w: CNAME chain exceeds %d names", ErrMalformed, maxChainNames)
		}
		chain = append(chain, target)
		seen[target] = struct{}{}
		current = target
	}
}
