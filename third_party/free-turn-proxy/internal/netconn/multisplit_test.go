package netconn

import (
	"bytes"
	"testing"
)

// u8 - узкое преобразование длины в байт для тестового билдера ClientHello.
// Длины здесь малы и ограничены, переполнение невозможно.
//
//nolint:gosec // G115: тестовые длины малы и ограничены
func u8(v int) byte { return byte(v) }

// buildClientHello собирает минимальный валидный TLS ClientHello-record с
// заданным SNI host_name. Достаточно для проверки парсера и сегментации.
func buildClientHello(host string) []byte {
	hn := []byte(host)
	entry := append([]byte{0x00, u8(len(hn) >> 8), u8(len(hn))}, hn...)        // name_type=host_name + len + host
	sni := append([]byte{u8(len(entry) >> 8), u8(len(entry))}, entry...)       // server_name_list len + entry
	ext := append([]byte{0x00, 0x00, u8(len(sni) >> 8), u8(len(sni))}, sni...) // ext type 0 + len + body

	body := make([]byte, 0, 64)
	body = append(body, 0x03, 0x03)                    // client_version
	body = append(body, make([]byte, 32)...)           // random
	body = append(body, 0x00)                          // session_id len 0
	body = append(body, 0x00, 0x02, 0x13, 0x01)        // cipher_suites len 2 + TLS_AES_128_GCM_SHA256
	body = append(body, 0x01, 0x00)                    // compression methods len 1 + null
	body = append(body, u8(len(ext)>>8), u8(len(ext))) // extensions length
	body = append(body, ext...)

	hs := append([]byte{0x01, u8(len(body) >> 16), u8(len(body) >> 8), u8(len(body))}, body...)
	rec := append([]byte{0x16, 0x03, 0x01, u8(len(hs) >> 8), u8(len(hs))}, hs...)
	return rec
}

func TestClientHelloSNIRange(t *testing.T) {
	t.Parallel()
	for _, host := range []string{"login.vk.ru", "api.vk.ru", "ok.ru", "calls.okcdn.ru"} {
		ch := buildClientHello(host)
		start, end, ok := clientHelloSNIRange(ch)
		if !ok {
			t.Fatalf("%s: SNI not found", host)
		}
		if got := string(ch[start:end]); got != host {
			t.Fatalf("host range = %q, want %q", got, host)
		}
	}
}

func TestClientHelloSNIRange_NotTLS(t *testing.T) {
	t.Parallel()
	if _, _, ok := clientHelloSNIRange([]byte("not a tls record at all")); ok {
		t.Fatal("expected ok=false for non-TLS payload")
	}
	if _, _, ok := clientHelloSNIRange([]byte{0x16, 0x03}); ok {
		t.Fatal("expected ok=false for truncated record")
	}
}

func TestSNISplitOffsets_InsideHostname(t *testing.T) {
	t.Parallel()
	ch := buildClientHello("api.vk.ru")
	start, end, ok := clientHelloSNIRange(ch)
	if !ok {
		t.Fatal("SNI not found")
	}
	for range 64 {
		offs := sniSplitOffsets(start, end)
		if len(offs) < 1 || len(offs) > 2 {
			t.Fatalf("want 1-2 offsets, got %d (%v)", len(offs), offs)
		}
		for j, o := range offs {
			if o <= start || o >= end {
				t.Fatalf("offset %d = %d outside (%d,%d)", j, o, start, end)
			}
			if j > 0 && o <= offs[j-1] {
				t.Fatalf("offsets not strictly ascending: %v", offs)
			}
		}
	}
}

func TestMultiSplitWriteConn_SplitsSNI(t *testing.T) {
	t.Parallel()
	host := "login.vk.ru"
	ch := buildClientHello(host)

	for range 32 {
		rec := newRecorder()
		c := &MultiSplitWriteConn{Conn: rec, FallbackSplitAt: 6}
		n, err := c.Write(ch)
		if err != nil {
			t.Fatalf("Write err: %v", err)
		}
		if n != len(ch) {
			t.Fatalf("Write n=%d, want %d", n, len(ch))
		}
		if len(rec.writes) < 2 {
			t.Fatalf("expected multi-segment, got %d", len(rec.writes))
		}

		var got []byte
		for _, w := range rec.writes {
			got = append(got, w...)
		}
		if !bytes.Equal(got, ch) {
			t.Fatal("reassembled bytes differ from original")
		}
		// Анти-DPI инвариант: ни один сегмент не содержит hostname целиком.
		hb := []byte(host)
		for _, w := range rec.writes {
			if bytes.Contains(w, hb) {
				t.Fatalf("segment contains full SNI %q (writes=%d)", host, len(rec.writes))
			}
		}
	}
}

func TestMultiSplitWriteConn_FallbackNoSNI(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	c := &MultiSplitWriteConn{Conn: rec, FallbackSplitAt: 6}
	payload := []byte("plain bytes, not a tls clienthello here")
	if _, err := c.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(rec.writes) != 2 {
		t.Fatalf("expected 2 fallback segments, got %d", len(rec.writes))
	}
	if !bytes.Equal(rec.writes[0], payload[:6]) || !bytes.Equal(rec.writes[1], payload[6:]) {
		t.Fatal("fallback single-split offset wrong")
	}
}

func TestMultiSplitWriteConn_SubsequentPassThrough(t *testing.T) {
	t.Parallel()
	ch := buildClientHello("api.vk.ru")
	rec := newRecorder()
	c := &MultiSplitWriteConn{Conn: rec, FallbackSplitAt: 6}
	if _, err := c.Write(ch); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	before := len(rec.writes)
	if _, err := c.Write([]byte("XYZ")); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	if len(rec.writes) != before+1 {
		t.Fatalf("second write should be single segment: before=%d after=%d", before, len(rec.writes))
	}
	if !bytes.Equal(rec.writes[before], []byte("XYZ")) {
		t.Fatalf("subsequent write = %q, want XYZ", rec.writes[before])
	}
}
