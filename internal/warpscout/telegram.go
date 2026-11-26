package warpscout

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// Public DC bootstrap addresses, not the Telegram Web HTTP frontend.
var telegramTargets = [...]string{"149.154.167.51:443", "149.154.167.91:443"}

// checkTelegram performs a bounded req_pq_multi/resPQ exchange against DC2 and DC4
// through dial. It proves MTProto reaches Telegram servers rather than a raw open TCP
// port or HTTP frontend.
func checkTelegram(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error)) error {
	results := make([]chan error, len(telegramTargets))
	for i, addr := range telegramTargets {
		results[i] = make(chan error, 1)
		go func(out chan error, addr string) {
			cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			out <- probeTelegram(cctx, dial, addr)
		}(results[i], addr)
	}
	var first error
	for i, out := range results {
		if err := <-out; err != nil && first == nil {
			first = fmt.Errorf("Telegram MTProto %s: %w", telegramTargets[i], err)
		}
	}
	return first
}

func probeTelegram(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error), addr string) error {
	c, err := dial(ctx, "tcp4", addr)
	if err != nil {
		return err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()

	deadline := time.Now().Add(4 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.SetDeadline(deadline); err != nil {
		return err
	}

	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	packet := make([]byte, 42) // abridged marker + length + unencrypted envelope + req_pq_multi
	packet[0], packet[1] = 0xef, 10
	now := time.Now()
	msgID := uint64(now.Unix())<<32 | (uint64(now.Nanosecond())<<32)/1_000_000_000
	msgID = msgID&^3 | 4
	binary.LittleEndian.PutUint64(packet[10:18], msgID)
	binary.LittleEndian.PutUint32(packet[18:22], 20)
	binary.LittleEndian.PutUint32(packet[22:26], 0xbe7e8ef1)
	copy(packet[26:], nonce[:])
	if _, err := io.Copy(c, bytes.NewReader(packet)); err != nil {
		return err
	}

	var header [4]byte
	if _, err := io.ReadFull(c, header[:1]); err != nil {
		return err
	}
	n := int(header[0])
	if n == 127 {
		if _, err := io.ReadFull(c, header[:3]); err != nil {
			return err
		}
		n = int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	} else if n >= 128 {
		return errors.New("неверный заголовок MTProto")
	}
	n *= 4
	if n < 76 || n > 4096 {
		return errors.New("неверная длина ответа MTProto")
	}
	response := make([]byte, n)
	if _, err := io.ReadFull(c, response); err != nil {
		return err
	}
	return validateResPQ(response, nonce[:])
}

func validateResPQ(b, nonce []byte) error {
	bad := errors.New("ожидался resPQ с nonce текущей проверки")
	if len(b) < 76 ||
		binary.LittleEndian.Uint64(b[:8]) != 0 ||
		binary.LittleEndian.Uint64(b[8:16])%4 != 1 ||
		int(binary.LittleEndian.Uint32(b[16:20])) != len(b)-20 ||
		binary.LittleEndian.Uint32(b[20:24]) != 0x05162463 ||
		!bytes.Equal(b[24:40], nonce) {
		return bad
	}
	pqLen := int(b[56])
	if pqLen < 1 || pqLen > 8 {
		return bad
	}
	offset := 56 + ((1 + pqLen + 3) &^ 3)
	if offset+8 > len(b) || binary.LittleEndian.Uint32(b[offset:offset+4]) != 0x1cb5c415 {
		return bad
	}
	count := int(binary.LittleEndian.Uint32(b[offset+4 : offset+8]))
	if count < 1 || count > 32 || offset+8+count*8 != len(b) {
		return bad
	}
	return nil
}
