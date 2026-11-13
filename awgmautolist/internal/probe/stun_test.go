package probe

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

// udpTarget builds a UDP target pointing at a local STUN test server.
func udpTarget(t *testing.T, port int) model.Target {
	t.Helper()
	target, err := model.NewTarget("", "127.0.0.1", model.NetworkUDP, uint16(port))
	if err != nil {
		t.Fatal(err)
	}
	return target
}

// stunServer answers every Binding Request according to handler.
type stunServer struct {
	conn *net.UDPConn
}

func startSTUN(t *testing.T, handler func(request []byte, from *net.UDPAddr) []byte) *stunServer {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buffer := make([]byte, 2048)
		for {
			n, from, err := conn.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			if reply := handler(append([]byte(nil), buffer[:n]...), from); reply != nil {
				_, _ = conn.WriteToUDP(reply, from)
			}
		}
	}()
	return &stunServer{conn: conn}
}

func (s *stunServer) port() int {
	return s.conn.LocalAddr().(*net.UDPAddr).Port
}

// validRequest builds a STUN Binding Request with a known transaction ID.
func validRequest() []byte {
	request := make([]byte, stunHeaderSize)
	binary.BigEndian.PutUint16(request[0:2], stunBindingRequest)
	binary.BigEndian.PutUint32(request[4:8], stunMagicCookie)
	binary.BigEndian.PutUint16(request[2:4], 0)
	for i := 8; i < 20; i++ {
		request[i] = byte(i)
	}
	return request
}

func TestSTUNSuccessIsReachable(t *testing.T) {
	server := startSTUN(t, func(request []byte, from *net.UDPAddr) []byte {
		reply := append([]byte(nil), request...)
		binary.BigEndian.PutUint16(reply[0:2], stunBindingSuccess)
		return reply
	})
	got := Check(context.Background(), udpTarget(t, server.port()), Path{})
	if got.Verdict != model.VerdictReachable {
		t.Fatalf("result=%+v, want reachable", got)
	}
	if got.Bytes < stunHeaderSize {
		t.Fatalf("bytes=%d, want at least 20", got.Bytes)
	}
	if got.TimeConnect > 0 && got.Duration > 5*time.Second {
		t.Fatalf("duration=%v exceeds the budget", got.Duration)
	}
}

func TestSTUNErrorResponseIsReachable(t *testing.T) {
	// A Binding Error response still proves the STUN endpoint answered.
	server := startSTUN(t, func(request []byte, from *net.UDPAddr) []byte {
		reply := append([]byte(nil), request...)
		binary.BigEndian.PutUint16(reply[0:2], stunBindingError)
		return reply
	})
	got := Check(context.Background(), udpTarget(t, server.port()), Path{})
	if got.Verdict != model.VerdictReachable {
		t.Fatalf("result=%+v, want reachable on a Binding Error response", got)
	}
}

func TestSTUNTimeoutIsBlocked(t *testing.T) {
	old := probeTimeout
	probeTimeout = 500 * time.Millisecond
	t.Cleanup(func() { probeTimeout = old })

	// A UDP socket that receives but never answers.
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	go func() {
		buffer := make([]byte, 2048)
		for {
			if _, _, err := server.ReadFromUDP(buffer); err != nil {
				return
			}
		}
	}()
	port := server.LocalAddr().(*net.UDPAddr).Port

	got := Check(context.Background(), udpTarget(t, port), Path{})
	if got.Verdict != model.VerdictBlocked {
		t.Fatalf("result=%+v, want blocked on timeout", got)
	}
}

func TestSTUNWrongTransactionIgnored(t *testing.T) {
	old := probeTimeout
	probeTimeout = 500 * time.Millisecond
	t.Cleanup(func() { probeTimeout = old })

	server := startSTUN(t, func(request []byte, from *net.UDPAddr) []byte {
		reply := append([]byte(nil), request...)
		binary.BigEndian.PutUint16(reply[0:2], stunBindingSuccess)
		for i := 8; i < 20; i++ {
			reply[i] ^= 0xff // corrupt every transaction byte
		}
		return reply
	})
	got := Check(context.Background(), udpTarget(t, server.port()), Path{})
	if got.Verdict != model.VerdictBlocked {
		t.Fatalf("result=%+v, want blocked when the transaction never matches", got)
	}
}

func TestSTUNWrongSourceIgnored(t *testing.T) {
	old := probeTimeout
	probeTimeout = 500 * time.Millisecond
	t.Cleanup(func() { probeTimeout = old })

	// An off-path impostor answers with a valid-looking Success response;
	// the probe must ignore it because the source endpoint differs.
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	impostor, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer impostor.Close()
	go func() {
		buffer := make([]byte, 2048)
		for {
			n, from, err := server.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			_ = n
			reply := append([]byte(nil), buffer[:20]...)
			binary.BigEndian.PutUint16(reply[0:2], stunBindingSuccess)
			_, _ = impostor.WriteToUDP(reply, from) // wrong source: not the server
		}
	}()
	port := server.LocalAddr().(*net.UDPAddr).Port

	got := Check(context.Background(), udpTarget(t, port), Path{})
	if got.Verdict != model.VerdictBlocked {
		t.Fatalf("result=%+v, want blocked when only an impostor answers", got)
	}
}

func TestSTUNRequiresIPOrPort(t *testing.T) {
	target, err := model.NewTarget("stun.example.test", "", model.NetworkUDP, 3478)
	if err != nil {
		t.Fatal(err)
	}
	got := Check(context.Background(), target, Path{})
	if got.Verdict != model.VerdictError {
		t.Fatalf("result=%+v, want error without an observed IP", got)
	}
}

func TestSTUNContextCancellation(t *testing.T) {
	old := probeTimeout
	probeTimeout = 10 * time.Second
	t.Cleanup(func() { probeTimeout = old })

	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	port := server.LocalAddr().(*net.UDPAddr).Port

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	started := time.Now()
	got := Check(ctx, udpTarget(t, port), Path{})
	if got.Verdict != model.VerdictError {
		t.Fatalf("result=%+v, want error on cancellation", got)
	}
	if elapsed := time.Since(started); elapsed > probeTimeout {
		t.Fatalf("cancellation took %v, want prompt", elapsed)
	}
}

func TestIsSTUNMessage(t *testing.T) {
	valid := validRequest()
	if !isSTUNMessage(valid) {
		t.Fatal("valid request not recognised")
	}
	bad := append([]byte(nil), valid...)
	bad[0] = 0xc0 // top bits set
	if isSTUNMessage(bad) {
		t.Fatal("accepted a non-STUN first byte")
	}
	bad = append([]byte(nil), valid...)
	binary.BigEndian.PutUint32(bad[4:8], 0) // cookie missing
	if isSTUNMessage(bad) {
		t.Fatal("accepted a missing cookie")
	}
	short := validRequest()[:10]
	if isSTUNMessage(short) {
		t.Fatal("accepted a truncated message")
	}
}

func TestBodyCounterStopsPastLimit(t *testing.T) {
	sink := &bodySink{}
	if n, err := sink.Write(make([]byte, bodyLimit)); err != nil || n != bodyLimit {
		t.Fatalf("at limit: n=%d err=%v", n, err)
	}
	if _, err := sink.Write([]byte{1}); err == nil {
		t.Fatal("accepted a write past the limit")
	}
}
