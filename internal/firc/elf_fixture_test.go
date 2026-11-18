package firc

import (
	"debug/elf"
	"encoding/binary"
)

// makeMinimalELF constructs a valid minimal ELF executable header for testing VerifyELFArch and IPK packaging.
func makeMinimalELF(class elf.Class, data elf.Data, machine elf.Machine, payloadTag string) []byte {
	var bo binary.ByteOrder = binary.LittleEndian
	if data == elf.ELFDATA2MSB {
		bo = binary.BigEndian
	}
	if class == elf.ELFCLASS64 {
		buf := make([]byte, 64+len(payloadTag))
		copy(buf[0:4], []byte{0x7f, 'E', 'L', 'F'})
		buf[4] = byte(class)
		buf[5] = byte(data)
		buf[6] = byte(elf.EV_CURRENT)
		bo.PutUint16(buf[16:18], uint16(elf.ET_EXEC))
		bo.PutUint16(buf[18:20], uint16(machine))
		bo.PutUint32(buf[20:24], uint32(elf.EV_CURRENT))
		bo.PutUint16(buf[52:54], 64) // e_ehsize
		copy(buf[64:], []byte(payloadTag))
		return buf
	}
	buf := make([]byte, 52+len(payloadTag))
	copy(buf[0:4], []byte{0x7f, 'E', 'L', 'F'})
	buf[4] = byte(class)
	buf[5] = byte(data)
	buf[6] = byte(elf.EV_CURRENT)
	bo.PutUint16(buf[16:18], uint16(elf.ET_EXEC))
	bo.PutUint16(buf[18:20], uint16(machine))
	bo.PutUint32(buf[20:24], uint32(elf.EV_CURRENT))
	bo.PutUint16(buf[40:42], 52) // e_ehsize
	copy(buf[52:], []byte(payloadTag))
	return buf
}
