package capture

import (
	"encoding/binary"
	"net/netip"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

type dnsMapping struct {
	ip      netip.Addr
	name    string
	expires time.Time
}

type dnsAnswer struct {
	name  string
	typ   uint16
	class uint16
	ttl   uint32
	data  []byte
	alias string
}

func readDNSName(message []byte, start int) (string, int, bool) {
	labels := []string{}
	next := start
	offset := start
	jumped := false
	for steps := 0; steps < 32; steps++ {
		if offset >= len(message) {
			return "", 0, false
		}
		length := int(message[offset])
		switch {
		case length == 0:
			if !jumped {
				next = offset + 1
			}
			return strings.ToLower(strings.Join(labels, ".")), next, true
		case length&0xc0 == 0xc0:
			if offset+1 >= len(message) {
				return "", 0, false
			}
			if !jumped {
				next = offset + 2
			}
			offset = (length&0x3f)<<8 | int(message[offset+1])
			jumped = true
		case length&0xc0 == 0 && length <= 63:
			offset++
			if offset+length > len(message) {
				return "", 0, false
			}
			labels = append(labels, string(message[offset:offset+length]))
			offset += length
			if !jumped {
				next = offset
			}
		default:
			return "", 0, false
		}
	}
	return "", 0, false
}

func parseDNSAnswers(message []byte, now time.Time) []dnsMapping {
	if len(message) < 12 || message[2]&0x80 == 0 || message[3]&0x0f != 0 ||
		binary.BigEndian.Uint16(message[4:6]) != 1 || binary.BigEndian.Uint16(message[6:8]) == 0 {
		return nil
	}
	offset := 12
	query, next, ok := readDNSName(message, offset)
	if !ok || next+4 > len(message) {
		return nil
	}
	query, err := model.NormalizeDomain(query)
	qtype := binary.BigEndian.Uint16(message[next : next+2])
	if err != nil || (qtype != 1 && qtype != 28) || binary.BigEndian.Uint16(message[next+2:next+4]) != 1 {
		return nil
	}
	offset = next + 4
	answerCount := int(binary.BigEndian.Uint16(message[6:8]))
	answers := make([]dnsAnswer, 0, min(answerCount, 32))
	for i := 0; i < answerCount; i++ {
		name, next, ok := readDNSName(message, offset)
		if !ok || next+10 > len(message) {
			return nil
		}
		answer := dnsAnswer{
			name:  name,
			typ:   binary.BigEndian.Uint16(message[next : next+2]),
			class: binary.BigEndian.Uint16(message[next+2 : next+4]),
			ttl:   binary.BigEndian.Uint32(message[next+4 : next+8]),
		}
		dataLen := int(binary.BigEndian.Uint16(message[next+8 : next+10]))
		offset = next + 10
		if offset+dataLen > len(message) {
			return nil
		}
		answer.data = message[offset : offset+dataLen]
		offset += dataLen
		if answer.typ == 5 {
			answer.alias, _, ok = readDNSName(message, offset-dataLen)
			if !ok {
				return nil
			}
		}
		answers = append(answers, answer)
		if len(answers) > 32 {
			return nil
		}
	}

	aliases := map[string]string{}
	for _, answer := range answers {
		if answer.typ == 5 && answer.class == 1 && answer.alias != "" {
			aliases[answer.name] = answer.alias
		}
	}
	var mappings []dnsMapping
	for _, answer := range answers {
		if answer.class != 1 || (answer.typ != 1 && answer.typ != 28) || !dnsChainLeadsTo(query, answer.name, aliases) {
			continue
		}
		var ip netip.Addr
		if answer.typ == 1 && len(answer.data) == 4 {
			ip, _ = netip.AddrFromSlice(answer.data)
		} else if answer.typ == 28 && len(answer.data) == 16 {
			ip, _ = netip.AddrFromSlice(answer.data)
		}
		if ip.IsValid() {
			mappings = append(mappings, dnsMapping{ip: ip, name: query, expires: now.Add(time.Duration(answer.ttl) * time.Second)})
		}
	}
	return mappings
}

func dnsChainLeadsTo(query, owner string, aliases map[string]string) bool {
	if owner == query {
		return true
	}
	for steps := 0; steps < 16; steps++ {
		next, ok := aliases[query]
		if !ok {
			return false
		}
		if next == owner {
			return true
		}
		query = next
	}
	return false
}
