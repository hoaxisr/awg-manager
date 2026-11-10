package packetcap

import (
	"encoding/binary"
	"testing"
)

func TestParseFrameEthernetVLANAndErrors(t *testing.T) {
	ip := []byte{0x45, 0, 0, 20}
	frame := ethernetFrame(etherTypeIPv4, ip)
	for _, tags := range []int{0, 1, 2} {
		wrapped := frame
		for range tags {
			wrapped = vlanFrame(wrapped)
		}
		got, err := ParseFrame(wrapped)
		if err != nil || len(got) != len(ip) || got[0] != 0x45 {
			t.Fatalf("tags=%d ParseFrame = %x, %v", tags, got, err)
		}
	}
	if _, err := ParseFrame(ethernetFrame(0x0806, ip)); err != ErrNotIP {
		t.Fatalf("ARP error = %v", err)
	}
	if _, err := ParseFrame(frame[:10]); err != ErrTruncated {
		t.Fatalf("short Ethernet error = %v", err)
	}
	triple := vlanFrame(vlanFrame(vlanFrame(frame)))
	if _, err := ParseFrame(triple); err == nil {
		t.Fatal("third VLAN tag was accepted")
	}
}

func TestKernelFilterUsesCanonicalIndirectHalfwordOpcode(t *testing.T) {
	// 0x68 is LDX/H/IND and is rejected by SO_ATTACH_FILTER. The packet load
	// used for TCP/UDP ports is LD/H/IND = 0x00|0x08|0x40 = 0x48.
	if bpfLDHInd != 0x48 {
		t.Fatalf("BPF_LD|BPF_H|BPF_IND = %#x, want 0x48", bpfLDHInd)
	}
}

func TestKernelFilterOnlySelectedTCPAndDNS(t *testing.T) {
	program, err := KernelFilter()
	if err != nil {
		t.Fatal(err)
	}
	if len(program) == 0 || len(program) > 4096 {
		t.Fatalf("invalid BPF length %d", len(program))
	}
	for pc, instruction := range program {
		switch instruction.Code {
		case bpfJMPEQ:
			for _, target := range []int{pc + 1 + int(instruction.Jt), pc + 1 + int(instruction.Jf)} {
				if target < 0 || target >= len(program) {
					t.Fatalf("pc %d conditional target %d outside program", pc, target)
				}
			}
		case bpfJMPJA:
			target := pc + 1 + int(instruction.K)
			if target < 0 || target >= len(program) {
				t.Fatalf("pc %d jump target %d outside program", pc, target)
			}
		}
	}

	tests := []struct {
		name  string
		frame []byte
		want  bool
	}{
		{name: "TCP destination 443", frame: ethernetFrame(etherTypeIPv4, ipv4Packet(6, 50000, 443, 5)), want: true},
		{name: "TCP source 443", frame: ethernetFrame(etherTypeIPv4, ipv4Packet(6, 443, 50000, 5)), want: true},
		{name: "TCP DNS", frame: ethernetFrame(etherTypeIPv4, ipv4Packet(6, 53, 5353, 5)), want: true},
		{name: "UDP DNS", frame: ethernetFrame(etherTypeIPv4, ipv4Packet(17, 5353, 53, 5)), want: true},
		{name: "IPv4 options", frame: ethernetFrame(etherTypeIPv4, ipv4Packet(6, 50000, 443, 6)), want: true},
		{name: "UDP 443 is not DNS", frame: ethernetFrame(etherTypeIPv4, ipv4Packet(17, 50000, 443, 5)), want: false},
		{name: "ordinary TCP 80", frame: ethernetFrame(etherTypeIPv4, ipv4Packet(6, 50000, 80, 5)), want: false},
		{name: "ordinary UDP 123", frame: ethernetFrame(etherTypeIPv4, ipv4Packet(17, 50000, 123, 5)), want: false},
		{name: "ICMP", frame: ethernetFrame(etherTypeIPv4, ipv4Packet(1, 0, 0, 5)), want: false},
		{name: "IPv6 TCP 443", frame: ethernetFrame(etherTypeIPv6, ipv6Packet(6, 50000, 443)), want: true},
		{name: "IPv6 UDP DNS", frame: ethernetFrame(etherTypeIPv6, ipv6Packet(17, 53, 5353)), want: true},
		{name: "IPv6 UDP 443", frame: ethernetFrame(etherTypeIPv6, ipv6Packet(17, 50000, 443)), want: false},
		{name: "VLAN TCP 443", frame: vlanFrame(ethernetFrame(etherTypeIPv4, ipv4Packet(6, 50000, 443, 5))), want: true},
		{name: "QinQ UDP DNS", frame: vlanFrame(vlanFrame(ethernetFrame(etherTypeIPv4, ipv4Packet(17, 50000, 53, 5)))), want: true},
		{name: "ARP", frame: ethernetFrame(0x0806, []byte{1, 2, 3}), want: false},
		{name: "short frame", frame: []byte{1, 2, 3}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := runClassicBPF(program, tc.frame); got != tc.want {
				t.Fatalf("filter accept = %v, want %v", got, tc.want)
			}
		})
	}
}

func ethernetFrame(typ uint16, payload []byte) []byte {
	raw := make([]byte, 14+len(payload))
	binary.BigEndian.PutUint16(raw[12:14], typ)
	copy(raw[14:], payload)
	return raw
}

func vlanFrame(frame []byte) []byte {
	raw := make([]byte, len(frame)+4)
	copy(raw, frame[:12])
	binary.BigEndian.PutUint16(raw[12:14], 0x8100)
	copy(raw[16:], frame[12:])
	return raw
}

func ipv4Packet(protocol byte, srcPort, dstPort uint16, ihl int) []byte {
	raw := make([]byte, ihl*4+4)
	raw[0] = byte(4<<4 | ihl)
	raw[9] = protocol
	binary.BigEndian.PutUint16(raw[ihl*4:ihl*4+2], srcPort)
	binary.BigEndian.PutUint16(raw[ihl*4+2:ihl*4+4], dstPort)
	return raw
}

func ipv6Packet(protocol byte, srcPort, dstPort uint16) []byte {
	raw := make([]byte, 44)
	raw[6] = protocol
	binary.BigEndian.PutUint16(raw[40:42], srcPort)
	binary.BigEndian.PutUint16(raw[42:44], dstPort)
	return raw
}

// runClassicBPF is a deliberately small interpreter for the instructions used
// by KernelFilter. Out-of-range packet reads reject, matching classic BPF.
func runClassicBPF(program []Instruction, raw []byte) (accepted bool) {
	var a, x uint32
	var memory [16]uint32
	read := func(offset, width int) (uint32, bool) {
		if offset < 0 || offset+width > len(raw) {
			return 0, false
		}
		switch width {
		case 1:
			return uint32(raw[offset]), true
		case 2:
			return uint32(binary.BigEndian.Uint16(raw[offset : offset+2])), true
		default:
			return 0, false
		}
	}
	for pc := 0; pc >= 0 && pc < len(program); {
		instruction := program[pc]
		next := pc + 1
		switch instruction.Code {
		case bpfLDWImm:
			a = instruction.K
		case bpfSTW:
			if instruction.K >= uint32(len(memory)) {
				return false
			}
			memory[instruction.K] = a
		case bpfLDWMem:
			if instruction.K >= uint32(len(memory)) {
				return false
			}
			a = memory[instruction.K]
		case bpfLDXWMem:
			if instruction.K >= uint32(len(memory)) {
				return false
			}
			x = memory[instruction.K]
		case bpfLDHAbs, bpfLDBAbs, bpfLDHInd, bpfLDBInd:
			offset := int(instruction.K)
			width := 1
			if instruction.Code == bpfLDHAbs || instruction.Code == bpfLDHInd {
				width = 2
			}
			if instruction.Code == bpfLDHInd || instruction.Code == bpfLDBInd {
				offset += int(x)
			}
			value, ok := read(offset, width)
			if !ok {
				return false
			}
			a = value
		case bpfALUAndK:
			a &= instruction.K
		case bpfALULshK:
			a <<= instruction.K
		case bpfALUAddX:
			a += x
		case bpfTAX:
			x = a
		case bpfJMPEQ:
			if a == instruction.K {
				next += int(instruction.Jt)
			} else {
				next += int(instruction.Jf)
			}
		case bpfJMPJA:
			next += int(instruction.K)
		case bpfRETK:
			return instruction.K != 0
		default:
			return false
		}
		pc = next
	}
	return false
}
