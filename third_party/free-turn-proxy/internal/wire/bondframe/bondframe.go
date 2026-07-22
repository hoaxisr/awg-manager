// Package bondframe содержит wire-формат VLESS bond multi-lane transport
// (hello + framed data/FIN). Клиент (инициатор) и сервер (акцептор) используют
// одинаковую кодировку.
package bondframe

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net"
	"time"
)

const (
	Version uint8 = 1
	Magic         = "VLB1"

	FrameData byte = 1
	FrameFIN  byte = 2

	MaxChunk = 16 * 1024

	LaneAttachTimeout = 300 * time.Millisecond
)

// Hello - per-lane handshake header, отправляемый сразу после открытия smux-потока.
type Hello struct {
	ConnID    uint64
	LaneIndex uint16
	LaneCount uint16
}

// Frame - одна bonded data или FIN единица, идентифицированная Seq внутри ConnID.
type Frame struct {
	Type byte
	Seq  uint64
	Data []byte
}

func WriteHello(w io.Writer, connID uint64, laneIndex, laneCount uint16) error {
	var hdr [17]byte
	copy(hdr[0:4], Magic)
	hdr[4] = Version
	binary.BigEndian.PutUint64(hdr[5:13], connID)
	binary.BigEndian.PutUint16(hdr[13:15], laneIndex)
	binary.BigEndian.PutUint16(hdr[15:17], laneCount)
	_, err := w.Write(hdr[:])
	return err
}

// ReadHelloAfterMagic завершает чтение Hello, у которого первые 4 magic-байта
// уже прочитаны (сервер pre-peek для мультиплексирования протоколов).
func ReadHelloAfterMagic(r io.Reader, magic [4]byte) (Hello, error) {
	var hdr [17]byte
	copy(hdr[0:4], magic[:])
	if _, err := io.ReadFull(r, hdr[4:]); err != nil {
		return Hello{}, err
	}
	return ParseHelloHeader(hdr[:])
}

func ParseHelloHeader(hdr []byte) (Hello, error) {
	if len(hdr) != 17 {
		return Hello{}, fmt.Errorf("bad bond hello size: %d", len(hdr))
	}
	if string(hdr[0:4]) != Magic {
		return Hello{}, fmt.Errorf("bad bond magic")
	}
	if hdr[4] != Version {
		return Hello{}, fmt.Errorf("unsupported bond version: %d", hdr[4])
	}
	return Hello{
		ConnID:    binary.BigEndian.Uint64(hdr[5:13]),
		LaneIndex: binary.BigEndian.Uint16(hdr[13:15]),
		LaneCount: binary.BigEndian.Uint16(hdr[15:17]),
	}, nil
}

func WriteFrame(w io.Writer, typ byte, seq uint64, data []byte) error {
	if uint64(len(data)) > math.MaxUint32 {
		return fmt.Errorf("bondframe: data too large: %d", len(data))
	}
	var hdr [13]byte
	hdr[0] = typ
	binary.BigEndian.PutUint64(hdr[1:9], seq)
	binary.BigEndian.PutUint32(hdr[9:13], uint32(len(data))) //nolint:gosec // bounded above
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	_, err := w.Write(data)
	return err
}

func ReadFrame(r io.Reader) (Frame, error) {
	var hdr [13]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err
	}
	size := binary.BigEndian.Uint32(hdr[9:13])
	if size > MaxChunk {
		return Frame{}, fmt.Errorf("bond frame too large: %d", size)
	}
	f := Frame{
		Type: hdr[0],
		Seq:  binary.BigEndian.Uint64(hdr[1:9]),
	}
	if size > 0 {
		f.Data = make([]byte, size)
		if _, err := io.ReadFull(r, f.Data); err != nil {
			return Frame{}, err
		}
	}
	return f, nil
}

// PendingCap ограничивает per-bond буфер переупорядочивания. Пир, генерирующий
// seq с постоянными пропусками, не вырастит его больше этого числа фреймов.
const PendingCap = 1024

// ReorderHooks подключает caller-специфичное логирование к Reorder. Все поля могут быть nil.
type ReorderHooks struct {
	OnOverflow    func(have int)
	OnUnknownType func(typ byte)
	OnWriteError  func(err error)
	OnCloseWrite  func(format string, v ...any)
}

// Reorder потребляет Frame из recv, пишет payload в dst в порядке Seq и
// возвращает число полностью доставленных чанков. Возвращается когда:
//   - достигнут FIN seq (на dst вызывается CloseWrite);
//   - recv закрыт;
//   - ctx отменён;
//   - неизвестный тип фрейма, переполнение pending или ошибка записи.
func Reorder(ctx context.Context, dst net.Conn, recv <-chan Frame, h ReorderHooks) uint64 {
	pending := make(map[uint64][]byte)
	var expect uint64
	var finSeq *uint64

	for {
		if finSeq != nil && expect == *finSeq {
			CloseWrite(dst, h.OnCloseWrite)
			return expect
		}
		select {
		case <-ctx.Done():
			return expect
		case f, ok := <-recv:
			if !ok {
				return expect
			}
			switch f.Type {
			case FrameData:
				if len(pending) >= PendingCap {
					if h.OnOverflow != nil {
						h.OnOverflow(len(pending))
					}
					return expect
				}
				pending[f.Seq] = f.Data
			case FrameFIN:
				v := f.Seq
				if finSeq == nil || v < *finSeq {
					finSeq = &v
				}
			default:
				if h.OnUnknownType != nil {
					h.OnUnknownType(f.Type)
				}
				return expect
			}
			for {
				data, ok := pending[expect]
				if !ok {
					break
				}
				delete(pending, expect)
				if len(data) > 0 {
					if _, err := dst.Write(data); err != nil {
						if h.OnWriteError != nil {
							h.OnWriteError(err)
						}
						return expect
					}
				}
				expect++
			}
		}
	}
}

// CloseWrite полузакрывает write-сторону conn, если базовый тип поддерживает
// это (TCPConn, smux.Stream, …); иначе no-op. errf вызывается с ошибкой при
// сбое CloseWrite; вызывающие обычно передают debug-gated log func.
func CloseWrite(conn net.Conn, errf func(format string, v ...any)) {
	type closeWriter interface {
		CloseWrite() error
	}
	if cw, ok := conn.(closeWriter); ok {
		if err := cw.CloseWrite(); err != nil && errf != nil {
			errf("CloseWrite failed: %v", err)
		}
	}
}
