package packetcap

import "fmt"

// Instruction is one classic kernel BPF instruction. It mirrors
// syscall.SockFilter without making filter tests platform-specific.
type Instruction struct {
	Code uint16
	Jt   uint8
	Jf   uint8
	K    uint32
}

// Linux cBPF opcodes from linux/filter.h. Classic socket filters require no
// cgo, eBPF loader, or libpcap dependency.
const (
	bpfLDWImm  = 0x00 // BPF_LD|BPF_W|BPF_IMM
	bpfSTW     = 0x02 // BPF_ST|BPF_W
	bpfJMPJA   = 0x05 // BPF_JMP|BPF_JA
	bpfTAX     = 0x07 // BPF_MISC|BPF_TAX
	bpfALUAddX = 0x0c // BPF_ALU|BPF_ADD|BPF_X
	bpfJMPEQ   = 0x15 // BPF_JMP|BPF_JEQ|BPF_K
	bpfLDHAbs  = 0x28 // BPF_LD|BPF_H|BPF_ABS
	bpfLDBAbs  = 0x30 // BPF_LD|BPF_B|BPF_ABS
	bpfLDBInd  = 0x50 // BPF_LD|BPF_B|BPF_IND
	bpfALUAndK = 0x54 // BPF_ALU|BPF_AND|BPF_K
	bpfLDWMem  = 0x60 // BPF_LD|BPF_W|BPF_MEM
	bpfLDXWMem = 0x61 // BPF_LDX|BPF_W|BPF_MEM
	bpfALULshK = 0x64 // BPF_ALU|BPF_LSH|BPF_K
	bpfLDHInd  = 0x48 // BPF_LD|BPF_H|BPF_IND
	bpfRETK    = 0x06 // BPF_RET|BPF_K
)

type jumpFixup struct {
	at      int
	trueTag string
}

type filterBuilder struct {
	code   []Instruction
	labels map[string]int
	fixups []jumpFixup
}

func newFilterBuilder() *filterBuilder {
	return &filterBuilder{labels: make(map[string]int)}
}

func (b *filterBuilder) emit(code uint16, k uint32) {
	b.code = append(b.code, Instruction{Code: code, K: k})
}

func (b *filterBuilder) label(name string) {
	b.labels[name] = len(b.code)
}

func (b *filterBuilder) jump(k uint32, trueTag string) {
	b.fixups = append(b.fixups, jumpFixup{at: len(b.code), trueTag: trueTag})
	b.code = append(b.code, Instruction{Code: bpfJMPEQ, K: k})
}

func (b *filterBuilder) always(tag string) {
	b.fixups = append(b.fixups, jumpFixup{at: len(b.code), trueTag: tag})
	b.code = append(b.code, Instruction{Code: bpfJMPJA})
}

// KernelFilter accepts only TCP/443 and TCP/UDP/53, for either direction. It
// handles untagged, single-VLAN, and QinQ Ethernet frames, IPv4 IHL, and the
// extension-free IPv6 packets accepted by packet.ParseTCP/ParseUDP.
func KernelFilter() ([]Instruction, error) {
	b := newFilterBuilder()
	b.emit(bpfLDHAbs, 12)
	for _, typ := range []uint32{0x8100, 0x88a8, 0x9100} {
		b.jump(typ, "vlan1")
	}
	b.always("plain")
	b.label("vlan1")
	b.emit(bpfLDWImm, 18)
	b.emit(bpfSTW, 0)
	b.emit(bpfLDHAbs, 16)
	for _, typ := range []uint32{0x8100, 0x88a8, 0x9100} {
		b.jump(typ, "vlan2")
	}
	b.always("ethertype")
	b.label("vlan2")
	b.emit(bpfLDWImm, 22)
	b.emit(bpfSTW, 0)
	b.emit(bpfLDHAbs, 20)
	b.always("ethertype")
	b.label("plain")
	b.emit(bpfLDWImm, 14)
	b.emit(bpfSTW, 0)
	b.emit(bpfLDHAbs, 12)
	b.label("ethertype")
	b.jump(etherTypeIPv4, "ipv4")
	b.jump(etherTypeIPv6, "ipv6")
	b.emit(bpfRETK, 0)

	b.label("ipv4")
	b.emit(bpfLDXWMem, 0)
	b.emit(bpfLDBInd, 9)
	b.jump(6, "tcp4")
	b.jump(17, "udp4")
	b.emit(bpfRETK, 0)

	b.label("tcp4")
	b.ipv4TransportOffset()
	b.emit(bpfLDHInd, 0)
	b.jump(443, "accept")
	b.jump(53, "accept")
	b.emit(bpfLDHInd, 2)
	b.jump(443, "accept")
	b.jump(53, "accept")
	b.emit(bpfRETK, 0)

	b.label("udp4")
	b.ipv4TransportOffset()
	b.emit(bpfLDHInd, 0)
	b.jump(53, "accept")
	b.emit(bpfLDHInd, 2)
	b.jump(53, "accept")
	b.emit(bpfRETK, 0)

	b.label("ipv6")
	b.emit(bpfLDXWMem, 0)
	b.emit(bpfLDBInd, 6)
	b.jump(6, "tcp6")
	b.jump(17, "udp6")
	b.emit(bpfRETK, 0)

	b.label("tcp6")
	b.emit(bpfLDXWMem, 0)
	b.emit(bpfLDHInd, 40)
	b.jump(443, "accept")
	b.jump(53, "accept")
	b.emit(bpfLDHInd, 42)
	b.jump(443, "accept")
	b.jump(53, "accept")
	b.emit(bpfRETK, 0)

	b.label("udp6")
	b.emit(bpfLDXWMem, 0)
	b.emit(bpfLDHInd, 40)
	b.jump(53, "accept")
	b.emit(bpfLDHInd, 42)
	b.jump(53, "accept")
	b.emit(bpfRETK, 0)

	b.label("accept")
	b.emit(bpfRETK, 0xffffffff)
	return b.resolve()
}

// ipv4TransportOffset sets X to IP-header-start + IHL. M[0] retains the L3
// offset selected by Ethernet/VLAN parsing.
func (b *filterBuilder) ipv4TransportOffset() {
	b.emit(bpfLDXWMem, 0)
	b.emit(bpfLDBInd, 0)
	b.emit(bpfALUAndK, 0x0f)
	b.emit(bpfALULshK, 2)
	b.emit(bpfTAX, 0)
	b.emit(bpfLDWMem, 0)
	b.emit(bpfALUAddX, 0)
	b.emit(bpfTAX, 0)
}

func (b *filterBuilder) resolve() ([]Instruction, error) {
	for _, fixup := range b.fixups {
		target, ok := b.labels[fixup.trueTag]
		if !ok {
			return nil, fmt.Errorf("packetcap: unknown BPF label %q", fixup.trueTag)
		}
		delta := target - fixup.at - 1
		if b.code[fixup.at].Code == bpfJMPJA {
			if delta < 0 {
				return nil, fmt.Errorf("packetcap: backward BPF jump")
			}
			b.code[fixup.at].K = uint32(delta)
			continue
		}
		if delta < 0 || delta > 255 {
			return nil, fmt.Errorf("packetcap: conditional BPF jump %d out of range", delta)
		}
		b.code[fixup.at].Jt = uint8(delta)
	}
	return append([]Instruction(nil), b.code...), nil
}
