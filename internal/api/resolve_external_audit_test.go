package api

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func externalResolveQuestion(query []byte) (int, uint16, error) {
	if len(query) < 17 || binary.BigEndian.Uint16(query[4:6]) != 1 {
		return 0, 0, fmt.Errorf("bad query")
	}
	pos := 12
	for {
		if pos >= len(query) {
			return 0, 0, fmt.Errorf("bad name")
		}
		n := int(query[pos])
		pos++
		if n == 0 {
			break
		}
		if n > 63 || pos+n > len(query) {
			return 0, 0, fmt.Errorf("bad label")
		}
		pos += n
	}
	if pos+4 > len(query) {
		return 0, 0, fmt.Errorf("short question")
	}
	return pos + 4, binary.BigEndian.Uint16(query[pos:]), nil
}

func externalResolveResponse(query []byte) ([]byte, error) {
	end, qtype, err := externalResolveQuestion(query)
	if err != nil {
		return nil, err
	}
	response := append([]byte(nil), query[:end]...)
	binary.BigEndian.PutUint16(response[2:4], 0x8180)
	binary.BigEndian.PutUint16(response[4:6], 1)
	binary.BigEndian.PutUint16(response[8:12], 0)
	if qtype == 28 {
		binary.BigEndian.PutUint16(response[2:4], 0x8182)
		binary.BigEndian.PutUint16(response[6:8], 0)
		return response, nil
	}
	binary.BigEndian.PutUint16(response[6:8], 1)
	response = append(response, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 30, 0, 4, 192, 0, 2, 77)
	return response, nil
}

func TestExternalAuditAWGMResolveFamilyAllFailsOnPartialLookup(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, peer, readErr := conn.ReadFrom(buf)
			if readErr != nil {
				return
			}
			response, responseErr := externalResolveResponse(append([]byte(nil), buf[:n]...))
			if responseErr == nil {
				_, _ = conn.WriteTo(response, peer)
			}
		}
	}()
	defer func() { _ = conn.Close(); <-done }()
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", conn.LocalAddr().String())
	}}
	handler := NewResolveHandler()
	handler.lookupHost = resolver.LookupHost
	handler.lookupIP = resolver.LookupIP

	for _, test := range []struct {
		name      string
		target    string
		wantError bool
		wantIPv4  bool
	}{
		{name: "family all rejects partial A plus AAAA SERVFAIL", target: "/api/routing/resolve?domain=awgm-partial.example&family=all", wantError: true},
		{name: "default remains IPv4 only", target: "/api/routing/resolve?domain=awgm-partial.example", wantIPv4: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.Resolve(recorder, httptest.NewRequest(http.MethodGet, test.target, nil))
			var envelope struct {
				Success bool            `json:"success"`
				Data    ResolveResponse `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if !envelope.Success {
				t.Fatalf("unexpected envelope: %s", recorder.Body.String())
			}
			if test.wantError && envelope.Data.Error == "" {
				t.Fatalf("family=all masked AAAA SERVFAIL and returned partial addresses: %+v", envelope.Data)
			}
			if test.wantIPv4 && (len(envelope.Data.IPs) != 1 || envelope.Data.IPs[0] != "192.0.2.77" || envelope.Data.Error != "") {
				t.Fatalf("default IPv4-only contract changed: %+v", envelope.Data)
			}
		})
	}
}
