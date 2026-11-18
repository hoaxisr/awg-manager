package uplink

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type dialCall struct{ network, address, source, device string }

// net.Pipe is a stream, so the Go resolver uses DNS-over-TCP framing even
// though the injected socket factory records the requested UDP binding.
func dnsReply(c net.Conn, answer [4]byte) {
	defer c.Close()
	var size [2]byte
	if _, err := io.ReadFull(c, size[:]); err != nil {
		return
	}
	payload := make([]byte, int(binary.BigEndian.Uint16(size[:])))
	if _, err := io.ReadFull(c, payload); err != nil {
		return
	}
	if len(payload) < 17 {
		return
	}
	end := 12
	for end < len(payload) && payload[end] != 0 {
		end += int(payload[end]) + 1
	}
	if end+5 > len(payload) {
		return
	}
	qtype := binary.BigEndian.Uint16(payload[end+1 : end+3])
	data := append([]byte(nil), payload[:end+5]...)
	data[2] = 0x81
	data[3] = 0x80
	for i := 6; i < 12; i++ {
		data[i] = 0
	}
	if qtype == 1 {
		data[7] = 1
		data = append(data, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 30, 0, 4)
		data = append(data, answer[:]...)
	}
	binary.BigEndian.PutUint16(size[:], uint16(len(data)))
	c.Write(append(size[:], data...))
}

func TestBoundDNSAndHTTPUseSameSourceAndProvider(t *testing.T) {
	var mu sync.Mutex
	var calls []dialCall
	d := bindingDialer{binding: Binding{Device: "wan1", IPv4: "192.0.2.2", DNSServers: []string{"192.0.2.53:5353"}}}
	d.raw = func(ctx context.Context, network, address, source, device string) (net.Conn, error) {
		mu.Lock()
		calls = append(calls, dialCall{network, address, source, device})
		mu.Unlock()
		client, server := net.Pipe()
		if strings.HasPrefix(network, "udp") {
			go dnsReply(server, [4]byte{203, 0, 113, 40})
		} else {
			go server.Close()
		}
		return client, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := d.dial(ctx, "tcp", "unique-endpoint.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	mu.Lock()
	defer mu.Unlock()
	dnsCount, httpCount := 0, 0
	for _, call := range calls {
		if call.device != "wan1" || call.source != "192.0.2.2" {
			t.Fatal("binding lost", call)
		}
		if strings.HasPrefix(call.network, "udp") {
			dnsCount++
			if call.address != "192.0.2.53:5353" {
				t.Fatal("system DNS used", call)
			}
		} else {
			httpCount++
			if call.address != "203.0.113.40:443" {
				t.Fatal(call)
			}
		}
	}
	if dnsCount == 0 || httpCount != 1 {
		t.Fatal(calls)
	}
}

func TestFailedBoundDNSDoesNotTrySystemOrHTTP(t *testing.T) {
	d := bindingDialer{binding: Binding{Device: "wan1", IPv4: "192.0.2.2", DNSServers: []string{"192.0.2.53"}}}
	var mu sync.Mutex
	unexpected := false
	d.raw = func(_ context.Context, network, address, _, _ string) (net.Conn, error) {
		mu.Lock()
		defer mu.Unlock()
		if !strings.HasPrefix(network, "udp") || address != "192.0.2.53:53" {
			unexpected = true
		}
		return nil, errors.New("selected DNS unreachable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := d.dial(ctx, "tcp", "no-system-fallback.invalid:443"); err == nil {
		t.Fatal("unreachable DNS reported success")
	}
	mu.Lock()
	defer mu.Unlock()
	if unexpected {
		t.Fatal("unbound resolver or HTTP fallback attempted")
	}
}

func TestBoundDNSRebindingCannotReachRouterLoopback(t *testing.T) {
	d := bindingDialer{binding: Binding{Device: "wan1", IPv4: "192.0.2.2", DNSServers: []string{"192.0.2.53"}}}
	var mu sync.Mutex
	httpCalls := 0
	d.raw = func(_ context.Context, network, address, _, _ string) (net.Conn, error) {
		if !strings.HasPrefix(network, "udp") {
			mu.Lock()
			httpCalls++
			mu.Unlock()
			return nil, errors.New("unexpected HTTP connect")
		}
		client, server := net.Pipe()
		go dnsReply(server, [4]byte{127, 0, 0, 1})
		return client, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := d.dial(ctx, "tcp", "rebound-router.invalid:79"); err == nil {
		t.Fatal("loopback answer accepted")
	}
	mu.Lock()
	defer mu.Unlock()
	if httpCalls != 0 {
		t.Fatal("router connect attempted")
	}
}
