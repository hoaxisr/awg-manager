package netconn

import (
	"net"
	"sync/atomic"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/randx"
)

// MultiSplitWriteConn оборачивает TCP net.Conn и разбивает первый Write на
// несколько сегментов так, чтобы SNI из TLS ClientHello попадал на границу.
// DPI без TCP-реассемблинга не собирает hostname и не инжектит SNI-based RST.
//
// В отличие от SplitFirstWriteConn (один рез на фиксированном offset - рвёт
// только TLS record header, SNI остаётся целым) парсит ClientHello и режет
// внутри host_name. Если первый Write не ClientHello или SNI не найден -
// фоллбэк на одиночный рез FallbackSplitAt (поведение SplitFirstWriteConn).
type MultiSplitWriteConn struct {
	net.Conn
	// Delay - базовая пауза между сегментами.
	Delay time.Duration
	// Jitter - верхняя граница случайной добавки к Delay (антифингерпринт тайминга).
	Jitter time.Duration
	// FallbackSplitAt - offset одиночного реза, когда SNI не найден. 0 - без реза.
	FallbackSplitAt int
	done            atomic.Bool
}

// Write на первом вызове дробит ClientHello по границам внутри host_name (SNI),
// далее проксирует напрямую.
func (s *MultiSplitWriteConn) Write(b []byte) (int, error) {
	if !s.done.CompareAndSwap(false, true) {
		return s.Conn.Write(b)
	}
	offsets := s.splitOffsets(b)
	if len(offsets) == 0 {
		return s.Conn.Write(b)
	}

	bounds := make([]int, len(offsets)+1)
	copy(bounds, offsets)
	bounds[len(offsets)] = len(b)

	total, prev := 0, 0
	for i, bound := range bounds {
		if i > 0 && s.Delay > 0 {
			d := s.Delay
			if s.Jitter > 0 {
				d += time.Duration(randx.Intn(int(s.Jitter)))
			}
			time.Sleep(d)
		}
		n, err := s.Conn.Write(b[prev:bound])
		total += n
		if err != nil {
			return total, err
		}
		prev = bound
	}
	return total, nil
}

// splitOffsets возвращает отсортированные точки реза первого сегмента. Сначала
// пробует SNI (рез внутри host_name), иначе одиночный FallbackSplitAt.
func (s *MultiSplitWriteConn) splitOffsets(b []byte) []int {
	if start, end, ok := clientHelloSNIRange(b); ok {
		if offs := sniSplitOffsets(start, end); len(offs) > 0 {
			return offs
		}
	}
	if s.FallbackSplitAt > 0 && len(b) > s.FallbackSplitAt {
		return []int{s.FallbackSplitAt}
	}
	return nil
}

// sniSplitOffsets делит host_name [start,end) на ~трети с джиттером. Гарантирует:
// registrable-домен (vk.ru и т.п.) пересекает хотя бы одну границу независимо от
// раскладки лейблов. Короткий hostname (len 2) - один рез посередине; len<2 -
// без реза (nil, уходит на фоллбэк).
func sniSplitOffsets(start, end int) []int {
	l := end - start
	switch {
	case l < 2:
		return nil
	case l < 3:
		return []int{start + 1}
	default:
		third := l / 3
		c1 := start + 1 + randx.Intn(third)   // [start+1, start+third]
		c2 := end - third + randx.Intn(third) // [end-third, end-1]
		if c2 <= c1 {
			c2 = c1 + 1
		}
		return []int{c1, c2}
	}
}

// clientHelloSNIRange парсит TLS ClientHello (b начинается с TLS record header)
// и возвращает байтовый диапазон [start,end) первого host_name в SNI-расширении.
// ok=false если b не ClientHello-record либо SNI/host_name отсутствует.
func clientHelloSNIRange(b []byte) (int, int, bool) {
	u16 := func(i int) int { return int(b[i])<<8 | int(b[i+1]) }

	// record layer: type(1)=0x16 handshake, version(2), length(2)
	if len(b) < 5 || b[0] != 0x16 {
		return 0, 0, false
	}
	// handshake: msg_type(1)=0x01 ClientHello, length(3)
	p := 5
	if p+4 > len(b) || b[p] != 0x01 {
		return 0, 0, false
	}
	p += 4
	p += 2 + 32 // client_version + random
	if p+1 > len(b) {
		return 0, 0, false
	}
	p += 1 + int(b[p]) // session_id
	if p+2 > len(b) {
		return 0, 0, false
	}
	p += 2 + u16(p) // cipher_suites
	if p+1 > len(b) {
		return 0, 0, false
	}
	p += 1 + int(b[p]) // compression_methods
	if p+2 > len(b) {
		return 0, 0, false
	}
	extEnd := p + 2 + u16(p)
	p += 2
	if extEnd > len(b) {
		extEnd = len(b)
	}
	for p+4 <= extEnd {
		etype := u16(p)
		elen := u16(p + 2)
		p += 4
		if p+elen > extEnd {
			break
		}
		if etype == 0x0000 { // server_name
			return parseSNIExtension(b, p, p+elen)
		}
		p += elen
	}
	return 0, 0, false
}

// parseSNIExtension ищет первый host_name (name_type 0) в server_name_list
// внутри [p,end). Возвращает абсолютный диапазон host_name в b.
func parseSNIExtension(b []byte, p, end int) (int, int, bool) {
	u16 := func(i int) int { return int(b[i])<<8 | int(b[i+1]) }
	if p+2 > end {
		return 0, 0, false
	}
	listEnd := p + 2 + u16(p) // server_name_list length
	p += 2
	if listEnd > end {
		listEnd = end
	}
	for p+3 <= listEnd {
		nameType := b[p]
		nameLen := u16(p + 1)
		p += 3
		if p+nameLen > listEnd {
			break
		}
		if nameType == 0x00 { // host_name
			return p, p + nameLen, true
		}
		p += nameLen
	}
	return 0, 0, false
}
