package xhttpserver_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/testutil"
	"github.com/hoaxisr/awg-manager/internal/xhttpserver"
	"github.com/hoaxisr/awg-manager/internal/xhttpserver/xhttptest"
)

const (
	defaultMaxEachPostBytes = 2 * 1024 * 1024
	maxSessionsPerIP        = 16
)

func newCServer(t *testing.T, cfg xhttpserver.Config) *xhttpserver.Server {
	t.Helper()
	cfg.BinaryPath = xhttptest.RequireCXHTTPDBinary(t)
	return xhttpserver.New(cfg)
}

func startEchoTCPServer(t *testing.T) (net.IP, uint16, *atomic.Int64, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	var dials atomic.Int64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			dials.Add(1)
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP, uint16(addr.Port), &dials, func() { _ = ln.Close() }
}

func TestXHTTPServer_ConfigValidationAndACLUnit(t *testing.T) {
	srvEmpty := xhttpserver.New(xhttpserver.Config{
		ListenAddr: "127.0.0.1:0",
		PathPrefix: "/cdn-bridge",
		Clients:    nil,
	})
	if err := srvEmpty.Start(); !errors.Is(err, xhttpserver.ErrNoValidClients) {
		t.Fatalf("expected ErrNoValidClients on empty Clients, got: %v", err)
	}

	srvInvalid := xhttpserver.New(xhttpserver.Config{
		ListenAddr: "127.0.0.1:0",
		PathPrefix: "/cdn-bridge",
		Clients:    []string{"short-panel-id-not-a-uuid"},
	})
	if err := srvInvalid.Start(); !errors.Is(err, xhttpserver.ErrInvalidClientUUID) {
		t.Fatalf("expected ErrInvalidClientUUID on invalid UUID string, got: %v", err)
	}

	for _, badProxy := range []string{"not-an-ip", "10.0.0.1/99", "192.168.1.999", "2001:db8::/129"} {
		srv := xhttpserver.New(xhttpserver.Config{
			ListenAddr:     "127.0.0.1:0",
			PathPrefix:     "/cdn-bridge",
			Clients:        []string{"44444444-5555-6666-7777-888888888888"},
			TrustedProxies: []string{"127.0.0.1/32", badProxy},
		})
		if err := srv.Start(); !errors.Is(err, xhttpserver.ErrInvalidTrustedProxy) {
			_ = srv.Stop()
			t.Fatalf("expected ErrInvalidTrustedProxy on Start() for %q, got: %v", badProxy, err)
		}
	}

	for _, blockedHost := range []string{"127.0.0.1", "10.0.0.1", "192.168.10.1", "172.16.5.4", "169.254.1.1", "::1"} {
		if err := xhttpserver.CheckDestinationACL(blockedHost, false); !errors.Is(err, xhttpserver.ErrForbiddenDestination) {
			t.Fatalf("expected ErrForbiddenDestination for %s, got %v", blockedHost, err)
		}
	}
	if err := xhttpserver.CheckDestinationACL("1.1.1.1", false); err != nil {
		t.Fatalf("expected public IP 1.1.1.1 to pass ACL, got %v", err)
	}
}

func TestXHTTPServer_FailClosedValidationAndAuth(t *testing.T) {
	echoIP, echoPort, echoDials, closeEcho := startEchoTCPServer(t)
	defer closeEcho()

	allowedUUID := "11111111-2222-3333-4444-555555555555"
	unknownUUID := "99999999-8888-7777-6666-555555555555"
	srv := newCServer(t, xhttpserver.Config{
		ListenAddr: "127.0.0.1:0",
		PathPrefix: "/cdn-bridge",
		Clients:    []string{allowedUUID},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer srv.Stop()

	baseURL := "http://" + srv.Addr() + "/cdn-bridge"
	badFrame := xhttptest.BuildVLESSReq(t, unknownUUID, echoIP, echoPort, []byte("unauth-attempt"))

	for _, sub := range []string{"/?sid=unauthpkt1&page=0", "/?sid=unauthsup1", "/"} {
		r, err := http.Post(baseURL+sub, "application/octet-stream", bytes.NewReader(badFrame))
		if err != nil {
			t.Fatalf("POST %s unauth: %v", sub, err)
		}
		_ = r.Body.Close()
		if r.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for %s unknown UUID, got %d", sub, r.StatusCode)
		}
	}

	if gotDials := echoDials.Load(); gotDials != 0 {
		t.Fatalf("expected 0 upstream dials for unauthorized requests, got %d", gotDials)
	}
}

func TestXHTTPServer_DoSHardeningLimitsAnd413(t *testing.T) {
	echoIP, echoPort, _, closeEcho := startEchoTCPServer(t)
	defer closeEcho()

	clientUUID := "11111111-2222-3333-4444-555555555555"
	srv := newCServer(t, xhttpserver.Config{
		ListenAddr: "127.0.0.1:0",
		PathPrefix: "/cdn-bridge",
		Clients:    []string{clientUUID},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer srv.Stop()

	baseURL := "http://" + srv.Addr() + "/cdn-bridge"

	for _, badSID := range []string{"bad$sid!", "bad space", strings.Repeat("a", 65)} {
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/?page=0", bytes.NewReader([]byte("x")))
		req.Header.Set("X-Session-ID", badSID)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request with badSID %q failed: %v", badSID, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request for badSID %q, got %d", badSID, resp.StatusCode)
		}
	}

	oversized := make([]byte, defaultMaxEachPostBytes+1)
	vlessHdr := xhttptest.BuildVLESSReq(t, clientUUID, echoIP, echoPort, nil)
	copy(oversized, vlessHdr)
	resp413, err := http.Post(baseURL+"/?sid=oversizedsid&page=0", "application/octet-stream", bytes.NewReader(oversized))
	if err != nil {
		t.Fatalf("POST oversized: %v", err)
	}
	_ = resp413.Body.Close()
	if resp413.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 Request Entity Too Large, got %d", resp413.StatusCode)
	}

	respPreAuth, err := http.Post(baseURL+"/?sid=preauthflood&page=10", "application/octet-stream", bytes.NewReader([]byte("flood")))
	if err != nil {
		t.Fatalf("POST pre-auth flood: %v", err)
	}
	_ = respPreAuth.Body.Close()
	if respPreAuth.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests for pre-auth high seq, got %d", respPreAuth.StatusCode)
	}
	if gotSess := srv.SessionCount(); gotSess != 0 {
		t.Fatalf("expected 0 allocated sessions after out-of-range pre-auth seq=10, got %d", gotSess)
	}

	authPrefix := vlessHdr[:17]
	for i := 0; i < maxSessionsPerIP; i++ {
		sid := "ipsess_" + strconv.Itoa(i)
		r, err := http.Post(baseURL+"/?sid="+sid+"&page=0", "application/octet-stream", bytes.NewReader(authPrefix))
		if err != nil {
			t.Fatalf("POST session %d: %v", i, err)
		}
		_ = r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK for session %d, got %d", i, r.StatusCode)
		}
	}
	rOverflow, err := http.Post(baseURL+"/?sid=ipsess_overflow&page=0", "application/octet-stream", bytes.NewReader(authPrefix))
	if err != nil {
		t.Fatalf("POST overflow session: %v", err)
	}
	_ = rOverflow.Body.Close()
	if rOverflow.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests on 17th session from same IP, got %d", rOverflow.StatusCode)
	}
}

func TestXHTTPServer_BoundedPreAuthContractAndAutoExpiry(t *testing.T) {
	clientUUID := "11111111-2222-3333-4444-555555555555"
	srv := newCServer(t, xhttpserver.Config{
		ListenAddr:              "127.0.0.1:0",
		PathPrefix:              "/cdn-bridge",
		Clients:                 []string{clientUUID},
		PreAuthSessionTTL:       120 * time.Millisecond,
		MaxPreAuthSessions:      8,
		MaxPreAuthSessionsPerIP: 4,
		MaxPreAuthBytesPerIP:    16 * 1024,
		MaxPreAuthBytesGlobal:   64 * 1024,
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer srv.Stop()

	baseURL := "http://" + srv.Addr() + "/cdn-bridge"

	rHigh, err := http.Post(baseURL+"/?sid=preauth_high&page=5", "application/octet-stream", bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatalf("POST seq=5: %v", err)
	}
	_ = rHigh.Body.Close()
	if rHigh.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for seq=5 > preAuthMaxPackets, got %d", rHigh.StatusCode)
	}
	if srv.SessionCount() != 0 || srv.PreAuthSessionCount() != 0 {
		t.Fatalf("expected 0 sessions after rejected seq=5, got sessions=%d preAuth=%d", srv.SessionCount(), srv.PreAuthSessionCount())
	}

	chunk3K := bytes.Repeat([]byte("a"), 3*1024)
	for i := 0; i < 4; i++ {
		sid := "preauth_ok_" + strconv.Itoa(i)
		r, err := http.Post(baseURL+"/?sid="+sid+"&page=1", "application/octet-stream", bytes.NewReader(chunk3K))
		if err != nil {
			t.Fatalf("POST pre-auth %d: %v", i, err)
		}
		_ = r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK for bounded pre-auth session %d, got %d", i, r.StatusCode)
		}
	}
	if srv.PreAuthSessionCount() != 4 || srv.TotalPreAuthBufferedBytes() != 12*1024 {
		t.Fatalf("unexpected pre-auth counters: sessions=%d bytes=%d", srv.PreAuthSessionCount(), srv.TotalPreAuthBufferedBytes())
	}

	rFifth, err := http.Post(baseURL+"/?sid=preauth_fifth&page=1", "application/octet-stream", bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatalf("POST 5th pre-auth session: %v", err)
	}
	_ = rFifth.Body.Close()
	if rFifth.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 when exceeding MaxPreAuthSessionsPerIP=4, got %d", rFifth.StatusCode)
	}

	chunk8K := bytes.Repeat([]byte("b"), 8*1024)
	rBytesCap, err := http.Post(baseURL+"/?sid=preauth_ok_0&page=2", "application/octet-stream", bytes.NewReader(chunk8K))
	if err != nil {
		t.Fatalf("POST pre-auth byte cap: %v", err)
	}
	_ = rBytesCap.Body.Close()
	if rBytesCap.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 when exceeding MaxPreAuthBytesPerIP, got %d", rBytesCap.StatusCode)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.SessionCount() == 0 && srv.PreAuthSessionCount() == 0 && srv.TotalPreAuthBufferedBytes() == 0 {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if srv.SessionCount() != 0 || srv.PreAuthSessionCount() != 0 || srv.TotalPreAuthBufferedBytes() != 0 {
		t.Fatalf("expected automatic C reaper cleanup to zero pre-auth counters, got sessions=%d preAuth=%d preAuthBytes=%d",
			srv.SessionCount(), srv.PreAuthSessionCount(), srv.TotalPreAuthBufferedBytes())
	}

	rAfter, err := http.Post(baseURL+"/?sid=preauth_after_ttl&page=1", "application/octet-stream", bytes.NewReader([]byte("ok")))
	if err != nil {
		t.Fatalf("POST after TTL cleanup: %v", err)
	}
	_ = rAfter.Body.Close()
	if rAfter.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK after automatic pre-auth TTL cleanup, got %d", rAfter.StatusCode)
	}
}

func TestXHTTPServer_TrustedProxyAndSpoofedForwardedFor(t *testing.T) {
	echoIP, echoPort, _, closeEcho := startEchoTCPServer(t)
	defer closeEcho()

	clientUUID := "11111111-2222-3333-4444-555555555555"
	authPrefix := xhttptest.BuildVLESSReq(t, clientUUID, echoIP, echoPort, nil)[:17]

	srvUntrusted := newCServer(t, xhttpserver.Config{
		ListenAddr: "127.0.0.1:0",
		PathPrefix: "/cdn-bridge",
		Clients:    []string{clientUUID},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	})
	if err := srvUntrusted.Start(); err != nil {
		t.Fatalf("srvUntrusted.Start: %v", err)
	}
	defer srvUntrusted.Stop()

	untrustedURL := "http://" + srvUntrusted.Addr() + "/cdn-bridge"
	for i := 0; i < maxSessionsPerIP; i++ {
		req, _ := http.NewRequest(http.MethodPost, untrustedURL+"/?sid=spoof_"+strconv.Itoa(i)+"&page=0", bytes.NewReader(authPrefix))
		req.Header.Set("X-Forwarded-For", "203.0.113."+strconv.Itoa(i+1))
		req.Header.Set("CF-Connecting-IP", "203.0.113."+strconv.Itoa(i+1))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("untrusted POST %d: %v", i, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK on untrusted session %d, got %d", i, resp.StatusCode)
		}
	}
	reqSpoof17, _ := http.NewRequest(http.MethodPost, untrustedURL+"/?sid=spoof_overflow&page=0", bytes.NewReader(authPrefix))
	reqSpoof17.Header.Set("X-Forwarded-For", "198.51.100.250")
	reqSpoof17.Header.Set("CF-Connecting-IP", "198.51.100.250")
	respSpoof17, err := http.DefaultClient.Do(reqSpoof17)
	if err != nil {
		t.Fatalf("untrusted overflow POST: %v", err)
	}
	_ = respSpoof17.Body.Close()
	if respSpoof17.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests when untrusted peer spoofs X-Forwarded-For, got %d", respSpoof17.StatusCode)
	}

	srvTrusted := newCServer(t, xhttpserver.Config{
		ListenAddr:     "127.0.0.1:0",
		PathPrefix:     "/cdn-bridge",
		Clients:        []string{clientUUID},
		TrustedProxies: []string{"127.0.0.1/32"},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	})
	if err := srvTrusted.Start(); err != nil {
		t.Fatalf("srvTrusted.Start: %v", err)
	}
	defer srvTrusted.Stop()

	trustedURL := "http://" + srvTrusted.Addr() + "/cdn-bridge"
	for i := 0; i < 25; i++ {
		req, _ := http.NewRequest(http.MethodPost, trustedURL+"/?sid=cdn_client_"+strconv.Itoa(i)+"&page=0", bytes.NewReader(authPrefix))
		req.Header.Set("X-Forwarded-For", "198.51.100."+strconv.Itoa(i+1)+", 127.0.0.1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("trusted CDN POST %d: %v", i, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK for distinct CDN client %d behind trusted proxy, got %d", i, resp.StatusCode)
		}
	}

	for i := 0; i < maxSessionsPerIP; i++ {
		req, _ := http.NewRequest(http.MethodPost, trustedURL+"/?sid=cdn_single_"+strconv.Itoa(i)+"&page=0", bytes.NewReader(authPrefix))
		req.Header.Set("CF-Connecting-IP", "192.0.2.77")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("trusted single IP POST %d: %v", i, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK for single IP session %d, got %d", i, resp.StatusCode)
		}
	}
	reqSingleOverflow, _ := http.NewRequest(http.MethodPost, trustedURL+"/?sid=cdn_single_overflow&page=0", bytes.NewReader(authPrefix))
	reqSingleOverflow.Header.Set("CF-Connecting-IP", "192.0.2.77")
	respSingleOverflow, err := http.DefaultClient.Do(reqSingleOverflow)
	if err != nil {
		t.Fatalf("trusted single IP overflow POST: %v", err)
	}
	_ = respSingleOverflow.Body.Close()
	if respSingleOverflow.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for 17th session from same CF-Connecting-IP behind trusted proxy, got %d", respSingleOverflow.StatusCode)
	}
}

func TestXHTTPServer_PrivateDestinationACL(t *testing.T) {
	echoIP, echoPort, echoDials, closeEcho := startEchoTCPServer(t)
	defer closeEcho()

	clientUUID := "11111111-2222-3333-4444-555555555555"
	srv := newCServer(t, xhttpserver.Config{
		ListenAddr: "127.0.0.1:0",
		PathPrefix: "/cdn-bridge",
		Clients:    []string{clientUUID},
		Egress:     xhttpserver.EgressConfig{Mode: "direct"},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer srv.Stop()

	baseURL := "http://" + srv.Addr() + "/cdn-bridge"
	frame := xhttptest.BuildVLESSReq(t, clientUUID, echoIP, echoPort, []byte("blocked-loopback"))
	resp, err := http.Post(baseURL+"/", "application/octet-stream", bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("POST stream-one to loopback: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden when dialing 127.0.0.1 with AllowPrivateDestinations=false, got %d", resp.StatusCode)
	}
	if echoDials.Load() != 0 {
		t.Fatalf("expected 0 dials to blocked loopback target, got %d", echoDials.Load())
	}
}

func TestXHTTPServer_PacketUpStreamUpStreamOneAndLargePayload(t *testing.T) {
	echoIP, echoPort, _, closeEcho := startEchoTCPServer(t)
	defer closeEcho()

	clientUUID := "11111111-2222-3333-4444-555555555555"
	srv := newCServer(t, xhttpserver.Config{
		ListenAddr: "127.0.0.1:0",
		PathPrefix: "/cdn-bridge",
		Clients:    []string{clientUUID},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer srv.Stop()

	baseURL := "http://" + srv.Addr() + "/cdn-bridge"

	// 1. Out-of-order packet-up (seq=2, seq=1 before seq=0)
	vlessFrame := xhttptest.BuildVLESSReq(t, clientUUID, echoIP, echoPort, []byte("hello-from-firc-xhttp-reordered"))
	part0 := vlessFrame[:20]
	part1 := vlessFrame[20:32]
	part2 := vlessFrame[32:]

	getRespCh := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Get(baseURL + "/?sid=testsession123")
		if err == nil {
			getRespCh <- resp
		}
	}()
	time.Sleep(30 * time.Millisecond)

	for _, item := range []struct {
		seq  string
		data []byte
	}{
		{"2", part2},
		{"1", part1},
		{"0", part0},
	} {
		r, err := http.Post(baseURL+"/?sid=testsession123&page="+item.seq, "application/octet-stream", bytes.NewReader(item.data))
		if err != nil || r.StatusCode != http.StatusOK {
			t.Fatalf("POST seq=%s failed: err=%v status=%v", item.seq, err, r)
		}
		_ = r.Body.Close()
	}

	select {
	case getResp := <-getRespCh:
		want := append([]byte{0x00, 0x00}, []byte("hello-from-firc-xhttp-reordered")...)
		got := make([]byte, len(want))
		if _, err := io.ReadFull(getResp.Body, got); err != nil {
			_ = getResp.Body.Close()
			t.Fatalf("ReadFull packet-up downlink: %v", err)
		}
		_ = getResp.Body.Close()
		if !bytes.Equal(got, want) {
			t.Fatalf("packet-up echo mismatch: got %x, want %x", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for packet-up GET response")
	}

	// 2. Multi-packet payload > 2 MiB (2.5 MiB total across 3 POSTs)
	largePayload := make([]byte, 2560*1024)
	_, _ = rand.Read(largePayload)
	largeFrame := xhttptest.BuildVLESSReq(t, clientUUID, echoIP, echoPort, largePayload)
	chunkSize := 1024 * 1024

	largeRespCh := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Get(baseURL + "/?sid=largesession")
		if err == nil {
			largeRespCh <- resp
		}
	}()
	time.Sleep(30 * time.Millisecond)

	for seqIdx, offset := 0, 0; offset < len(largeFrame); seqIdx, offset = seqIdx+1, offset+chunkSize {
		end := offset + chunkSize
		if end > len(largeFrame) {
			end = len(largeFrame)
		}
		r, err := http.Post(baseURL+"/?sid=largesession&page="+strconv.Itoa(seqIdx), "application/octet-stream", bytes.NewReader(largeFrame[offset:end]))
		if err != nil || r.StatusCode != http.StatusOK {
			t.Fatalf("large packet-up seq=%d failed: err=%v status=%v", seqIdx, err, r)
		}
		_ = r.Body.Close()
	}

	select {
	case largeResp := <-largeRespCh:
		wantLarge := append([]byte{0x00, 0x00}, largePayload...)
		gotLarge := make([]byte, len(wantLarge))
		if _, err := io.ReadFull(largeResp.Body, gotLarge); err != nil {
			_ = largeResp.Body.Close()
			t.Fatalf("ReadFull 2.5MiB packet-up downlink: %v", err)
		}
		_ = largeResp.Body.Close()
		if !bytes.Equal(gotLarge, wantLarge) {
			t.Fatal("2.5MiB packet-up payload mismatch")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for 2.5MiB packet-up response")
	}

	// 3. stream-up mode
	streamUpRespCh := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Get(baseURL + "/?sid=streamupsess")
		if err == nil {
			streamUpRespCh <- resp
		}
	}()
	time.Sleep(30 * time.Millisecond)

	streamUpFrame := xhttptest.BuildVLESSReq(t, clientUUID, echoIP, echoPort, []byte("stream-up-bidirectional-payload"))
	rSup, err := http.Post(baseURL+"/?sid=streamupsess", "application/octet-stream", bytes.NewReader(streamUpFrame))
	if err != nil || rSup.StatusCode != http.StatusOK {
		t.Fatalf("stream-up POST failed: err=%v status=%v", err, rSup)
	}
	_ = rSup.Body.Close()

	select {
	case supResp := <-streamUpRespCh:
		wantSup := append([]byte{0x00, 0x00}, []byte("stream-up-bidirectional-payload")...)
		gotSup := make([]byte, len(wantSup))
		if _, err := io.ReadFull(supResp.Body, gotSup); err != nil {
			_ = supResp.Body.Close()
			t.Fatalf("ReadFull stream-up downlink: %v", err)
		}
		_ = supResp.Body.Close()
		if !bytes.Equal(gotSup, wantSup) {
			t.Fatalf("stream-up mismatch: got %x, want %x", gotSup, wantSup)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for stream-up response")
	}

	// 4. stream-one mode
	streamOnePayload := xhttptest.BuildVLESSReq(t, clientUUID, echoIP, echoPort, []byte("stream-one-payload"))
	resp, err := http.Post(baseURL+"/", "application/octet-stream", bytes.NewReader(streamOnePayload))
	if err != nil {
		t.Fatalf("stream-one POST: %v", err)
	}
	defer resp.Body.Close()
	wantOne := append([]byte{0x00, 0x00}, []byte("stream-one-payload")...)
	gotOne := make([]byte, len(wantOne))
	if _, err := io.ReadFull(resp.Body, gotOne); err != nil {
		t.Fatalf("ReadFull stream-one: %v", err)
	}
	if !bytes.Equal(gotOne, wantOne) {
		t.Fatalf("stream-one mismatch: got %x, want %x", gotOne, wantOne)
	}
}

func TestXHTTPServer_SocksAndInterfaceEgress(t *testing.T) {
	echoIP, echoPort, _, closeEcho := startEchoTCPServer(t)
	defer closeEcho()

	socksLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen socks5: %v", err)
	}
	defer socksLn.Close()
	var socksHits atomic.Int32
	go func() {
		for {
			c, err := socksLn.Accept()
			if err != nil {
				return
			}
			socksHits.Add(1)
			go func(conn net.Conn) {
				defer conn.Close()
				var g [3]byte
				if _, err := io.ReadFull(conn, g[:]); err != nil {
					return
				}
				_, _ = conn.Write([]byte{0x05, 0x00})
				var req [10]byte
				if _, err := io.ReadFull(conn, req[:]); err != nil {
					return
				}
				dstIP := net.IP(req[4:8]).String()
				dstPort := binary.BigEndian.Uint16(req[8:10])
				_, _ = conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 0})
				up, err := net.Dial("tcp", net.JoinHostPort(dstIP, strconv.Itoa(int(dstPort))))
				if err != nil {
					return
				}
				defer up.Close()
				go func() {
					_, _ = io.Copy(up, conn)
					if cw, ok := up.(interface{ CloseWrite() error }); ok {
						_ = cw.CloseWrite()
					}
				}()
				_, _ = io.Copy(conn, up)
			}(c)
		}
	}()

	clientUUID := "22222222-3333-4444-5555-666666666666"
	srv := newCServer(t, xhttpserver.Config{
		ListenAddr: "127.0.0.1:0",
		PathPrefix: "/cdn-bridge",
		Clients:    []string{clientUUID},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "socks",
			SocksAddr:                socksLn.Addr().String(),
			AllowPrivateDestinations: true,
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer srv.Stop()

	payload := xhttptest.BuildVLESSReq(t, clientUUID, echoIP, echoPort, []byte("via-socks-1099"))
	resp, err := http.Post("http://"+srv.Addr()+"/cdn-bridge/", "application/octet-stream", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("POST via socks egress: %v", err)
	}
	defer resp.Body.Close()
	want := append([]byte{0x00, 0x00}, []byte("via-socks-1099")...)
	got := make([]byte, len(want))
	if _, err := io.ReadFull(resp.Body, got); err != nil {
		t.Fatalf("ReadFull via socks egress: %v", err)
	}
	if !bytes.Equal(got, want) || socksHits.Load() == 0 {
		t.Fatalf("socks egress mismatch: got %x want %x socksHits=%d", got, want, socksHits.Load())
	}

	// Verify Sing-box interface egress ("interface" / "outbound" bound to lo) works via C awgm-xhttpd
	if err := srv.UpdateConfig(xhttpserver.Config{
		ListenAddr: srv.Addr(),
		PathPrefix: "/cdn-bridge",
		Clients:    []string{clientUUID},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "interface",
			InterfaceName:            "lo",
			AllowPrivateDestinations: true,
		},
	}); err != nil {
		t.Fatalf("UpdateConfig interface=lo: %v", err)
	}
	payloadIf := xhttptest.BuildVLESSReq(t, clientUUID, echoIP, echoPort, []byte("via-singbox-if-lo"))
	respIf, err := http.Post("http://"+srv.Addr()+"/cdn-bridge/", "application/octet-stream", bytes.NewReader(payloadIf))
	if err != nil {
		t.Fatalf("POST via interface egress: %v", err)
	}
	defer respIf.Body.Close()
	wantIf := append([]byte{0x00, 0x00}, []byte("via-singbox-if-lo")...)
	gotIf := make([]byte, len(wantIf))
	if _, err := io.ReadFull(respIf.Body, gotIf); err != nil || !bytes.Equal(gotIf, wantIf) {
		t.Fatalf("interface egress mismatch: err=%v got=%x want=%x", err, gotIf, wantIf)
	}
}

func TestXHTTPServer_PathStyleSplitStreamSimulation(t *testing.T) {
	echoIP, echoPort, _, closeEcho := startEchoTCPServer(t)
	defer closeEcho()

	clientUUID := "33333333-4444-5555-6666-777777777777"
	srv := newCServer(t, xhttpserver.Config{
		ListenAddr:     "127.0.0.1:0",
		PathPrefix:     "/cdn-bridge",
		Clients:        []string{clientUUID},
		TrustedProxies: []string{"127.0.0.1/32"},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer srv.Stop()

	hc := &http.Client{Timeout: 15 * time.Second}
	baseURL := "http://" + srv.Addr() + "/cdn-bridge"

	sid1 := "path_pktup_01"
	payload2MiB := make([]byte, 2200*1024)
	_, _ = rand.Read(payload2MiB)
	fullFrame := xhttptest.BuildVLESSReq(t, clientUUID, echoIP, echoPort, payload2MiB)

	downCh := make(chan *http.Response, 1)
	downErrCh := make(chan error, 1)
	go func() {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/"+sid1, nil)
		req.Header.Set("X-Forwarded-For", "198.51.100.10")
		resp, err := hc.Do(req)
		if err != nil {
			downErrCh <- err
			return
		}
		downCh <- resp
	}()
	time.Sleep(30 * time.Millisecond)

	c0 := fullFrame[:16384]
	c1 := fullFrame[16384 : 2*16384]
	c2 := fullFrame[2*16384 : 3*16384]
	for _, pkt := range []struct {
		seq  int
		data []byte
	}{
		{2, c2},
		{1, c1},
		{0, c0},
	} {
		path := "/" + sid1 + "/" + strconv.Itoa(pkt.seq)
		req, _ := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(pkt.data))
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-Forwarded-For", "198.51.100.10")
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST %s status=%d", path, resp.StatusCode)
		}
	}

	seqIdx := 3
	for off := 3 * 16384; off < len(fullFrame); off += 512 * 1024 {
		end := off + 512*1024
		if end > len(fullFrame) {
			end = len(fullFrame)
		}
		path := "/" + sid1 + "/" + strconv.Itoa(seqIdx)
		req, _ := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(fullFrame[off:end]))
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-Forwarded-For", "198.51.100.10")
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST %s status=%d", path, resp.StatusCode)
		}
		seqIdx++
	}

	select {
	case err := <-downErrCh:
		t.Fatalf("GET downlink error: %v", err)
	case downResp := <-downCh:
		want := append([]byte{0x00, 0x00}, payload2MiB...)
		got := make([]byte, len(want))
		if _, err := io.ReadFull(downResp.Body, got); err != nil {
			_ = downResp.Body.Close()
			t.Fatalf("ReadFull >2MiB downlink: %v", err)
		}
		_ = downResp.Body.Close()
		if !bytes.Equal(got, want) {
			t.Fatal(">2MiB packet-up echo payload mismatch")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("timeout waiting for >2MiB packet-up downlink")
	}

	badFrame := xhttptest.BuildVLESSReq(t, "99999999-9999-9999-9999-999999999999", echoIP, echoPort, []byte("nope"))
	respBad, err := hc.Post(baseURL+"/bad_sid_01/0", "application/octet-stream", bytes.NewReader(badFrame))
	if err != nil {
		t.Fatalf("bad UUID POST: %v", err)
	}
	_ = respBad.Body.Close()
	if respBad.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for bad UUID, got %d", respBad.StatusCode)
	}

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if srv.SessionCount() == 0 && srv.TotalBufferedBytes() == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if srv.SessionCount() != 0 || srv.TotalBufferedBytes() != 0 {
		t.Fatalf("expected 0 leaked sessions/bytes, got sessions=%d bufBytes=%d", srv.SessionCount(), srv.TotalBufferedBytes())
	}
}

func TestXHTTPServer_TrustedProxiesFailClosedLiveReload(t *testing.T) {
	echoIP, echoPort, _, closeEcho := startEchoTCPServer(t)
	defer closeEcho()

	clientUUID := "44444444-5555-6666-7777-888888888888"
	srv := newCServer(t, xhttpserver.Config{
		ListenAddr:     "127.0.0.1:0",
		PathPrefix:     "/cdn-bridge",
		Clients:        []string{clientUUID},
		TrustedProxies: []string{"127.0.0.1/32"},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer srv.Stop()

	err := srv.UpdateConfig(xhttpserver.Config{
		ListenAddr:     srv.Addr(),
		PathPrefix:     "/cdn-bridge",
		Clients:        []string{clientUUID},
		TrustedProxies: []string{"127.0.0.1/32", "malformed-cidr/24"},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	})
	if !errors.Is(err, xhttpserver.ErrInvalidTrustedProxy) {
		t.Fatalf("expected ErrInvalidTrustedProxy from UpdateConfig, got: %v", err)
	}

	frame := xhttptest.BuildVLESSReq(t, clientUUID, echoIP, echoPort, []byte("should-be-rejected"))
	resp, postErr := http.Post("http://"+srv.Addr()+"/cdn-bridge/", "application/octet-stream", bytes.NewReader(frame))
	if postErr != nil {
		t.Fatalf("POST after invalid TrustedProxies UpdateConfig: %v", postErr)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden fail-closed after invalid TrustedProxies UpdateConfig, got %d", resp.StatusCode)
	}
}

func dialViaSocks5(socksAddr string, dstIP net.IP, dstPort uint16, timeout time.Duration) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", socksAddr, timeout)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	var authResp [2]byte
	if _, err := io.ReadFull(conn, authResp[:]); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if authResp[0] != 0x05 || authResp[1] != 0x00 {
		_ = conn.Close()
		return nil, fmt.Errorf("unexpected socks5 auth response: %x", authResp)
	}
	ip4 := dstIP.To4()
	req := []byte{0x05, 0x01, 0x00, 0x01, ip4[0], ip4[1], ip4[2], ip4[3], byte(dstPort >> 8), byte(dstPort)}
	if _, err := conn.Write(req); err != nil {
		_ = conn.Close()
		return nil, err
	}
	var repHdr [4]byte
	if _, err := io.ReadFull(conn, repHdr[:]); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if repHdr[1] != 0x00 {
		_ = conn.Close()
		return nil, fmt.Errorf("socks5 connect failed with code %d", repHdr[1])
	}
	skip := 0
	switch repHdr[3] {
	case 0x01:
		skip = 6
	case 0x04:
		skip = 18
	case 0x03:
		var dlen [1]byte
		if _, err := io.ReadFull(conn, dlen[:]); err != nil {
			_ = conn.Close()
			return nil, err
		}
		skip = int(dlen[0]) + 2
	}
	if skip > 0 {
		buf := make([]byte, skip)
		if _, err := io.ReadFull(conn, buf); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	return conn, nil
}

func findExternalXrayClientBinary() string {
	if env := strings.TrimSpace(os.Getenv("XRAY_BIN")); env != "" {
		if info, err := os.Stat(env); err == nil && !info.IsDir() {
			return env
		}
	}
	for _, cand := range []string{"/usr/local/share/xray-test/xray", "/usr/local/bin/xray", "/usr/bin/xray"} {
		if info, err := os.Stat(cand); err == nil && !info.IsDir() {
			return cand
		}
	}
	return ""
}

func TestXHTTPServer_ExternalVLESSClientBinaryInterop(t *testing.T) {
	xrayBin := findExternalXrayClientBinary()
	if xrayBin == "" {
		t.Skip("external xray client test binary not installed; skipping external client interop test")
	}

	echoIP, echoPort, _, closeEcho := startEchoTCPServer(t)
	defer closeEcho()

	clientUUID := "55555555-6666-7777-8888-999999999999"
	srv := newCServer(t, xhttpserver.Config{
		ListenAddr:     "127.0.0.1:0",
		PathPrefix:     "/cdn-bridge",
		Clients:        []string{clientUUID},
		TrustedProxies: []string{"127.0.0.1/32"},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer srv.Stop()

	srvHost, srvPortStr, _ := net.SplitHostPort(srv.Addr())
	srvPort, _ := strconv.Atoi(srvPortStr)

	socksPort := testutil.FreePort(t)
	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)

	cfgJSON := fmt.Sprintf(`{
  "log": { "loglevel": "warning" },
  "inbounds": [{ "listen": "127.0.0.1", "port": %d, "protocol": "socks", "settings": { "auth": "noauth", "udp": false } }],
  "outbounds": [{
    "protocol": "vless",
    "settings": { "vnext": [{ "address": %q, "port": %d, "users": [{ "id": %q, "encryption": "none" }] }] },
    "streamSettings": { "network": "xhttp", "security": "none", "xhttpSettings": { "path": "/cdn-bridge", "mode": "packet-up" } }
  }]
}`, socksPort, srvHost, srvPort, clientUUID)

	cfgPath := filepath.Join(t.TempDir(), "client.json")
	_ = os.WriteFile(cfgPath, []byte(cfgJSON), 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, xrayBin, "run", "-c", cfgPath)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start external client: %v", err)
	}
	defer func() {
		cancel()
		_ = cmd.Wait()
	}()

	for i := 0; i < 50; i++ {
		if c, err := net.DialTimeout("tcp", socksAddr, 100*time.Millisecond); err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	conn, err := dialViaSocks5(socksAddr, echoIP, echoPort, 10*time.Second)
	if err != nil {
		t.Fatalf("dialViaSocks5: %v", err)
	}
	defer conn.Close()
	msg := bytes.Repeat([]byte("external-vless-xhttp-to-c-xhttpd-"), 1024)
	go func() { _, _ = conn.Write(msg) }()
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, msg) {
		t.Fatalf("external client interop mismatch: err=%v", err)
	}
}

func TestXHTTPServer_VLESSMuxXUDPDNSAndQUICReject(t *testing.T) {
	udpConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer udpConn.Close()
	dnsAddr := udpConn.LocalAddr().String()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, rerr := udpConn.ReadFrom(buf)
			if rerr != nil {
				return
			}
			_, _ = udpConn.WriteTo(append([]byte("DNS-OK:"), buf[:n]...), addr)
		}
	}()

	clientUUID := "11111111-2222-3333-4444-555555555555"
	srv := newCServer(t, xhttpserver.Config{
		ListenAddr: "127.0.0.1:0",
		PathPrefix: "/cdn-bridge",
		Clients:    []string{clientUUID},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "router",
			DNSProxyAddr:             dnsAddr,
			AllowPrivateDestinations: true,
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer srv.Stop()

	u, _ := xhttpserver.ParseUUID(clientUUID)
	var muxStream bytes.Buffer
	muxStream.WriteByte(0x00)
	muxStream.Write(u[:])
	muxStream.WriteByte(0x00)
	muxStream.WriteByte(xhttptest.VLESSCmdMux)
	muxStream.Write(xhttptest.EncodeMuxFrame(1, xhttptest.MuxStatusNew, xhttptest.MuxOptionData, true, "8.8.8.8", 443, []byte("quic-initial")))
	muxStream.Write(xhttptest.EncodeMuxFrame(2, xhttptest.MuxStatusNew, xhttptest.MuxOptionData, true, "8.8.8.8", 53, []byte("yt-query")))

	resp, err := http.Post("http://"+srv.Addr()+"/cdn-bridge/", "application/octet-stream", &muxStream)
	if err != nil {
		t.Fatalf("POST VLESSCmdMux: %v", err)
	}
	defer resp.Body.Close()

	var vlessRespHdr [2]byte
	if _, err := io.ReadFull(resp.Body, vlessRespHdr[:]); err != nil {
		t.Fatalf("read VLESS response header: %v", err)
	}
	gotQUICReject := false
	gotDNSReply := false
	for !(gotQUICReject && gotDNSReply) {
		var mLenBuf [2]byte
		if _, err := io.ReadFull(resp.Body, mLenBuf[:]); err != nil {
			t.Fatalf("read mux frame len: %v", err)
		}
		mLen := int(binary.BigEndian.Uint16(mLenBuf[:]))
		meta := make([]byte, mLen)
		if _, err := io.ReadFull(resp.Body, meta); err != nil {
			t.Fatalf("read mux meta: %v", err)
		}
		subID := binary.BigEndian.Uint16(meta[0:2])
		st := meta[2]
		opt := meta[3]
		var data []byte
		if (opt & xhttptest.MuxOptionData) != 0 {
			var dLenBuf [2]byte
			if _, err := io.ReadFull(resp.Body, dLenBuf[:]); err != nil {
				t.Fatalf("read mux data len: %v", err)
			}
			dLen := int(binary.BigEndian.Uint16(dLenBuf[:]))
			data = make([]byte, dLen)
			if _, err := io.ReadFull(resp.Body, data); err != nil {
				t.Fatalf("read mux data: %v", err)
			}
		}
		if subID == 1 && st == xhttptest.MuxStatusEnd && (opt&xhttptest.MuxOptionError) != 0 {
			gotQUICReject = true
		}
		if subID == 2 && st == xhttptest.MuxStatusKeep && string(data) == "DNS-OK:yt-query" {
			gotDNSReply = true
		}
	}
}

func TestXHTTPServer_SlowClientDownlinkDrainAndTrailingCloseNotify(t *testing.T) {
	videoChunk := make([]byte, 512*1024)
	_, _ = rand.Read(videoChunk)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				var one [1]byte
				_, _ = conn.Read(one[:])
				_, _ = conn.Write(videoChunk)
			}(c)
		}
	}()
	upAddr := ln.Addr().(*net.TCPAddr)

	clientUUID := "11111111-2222-3333-4444-555555555555"
	srv := newCServer(t, xhttpserver.Config{
		ListenAddr: "127.0.0.1:0",
		PathPrefix: "/cdn-bridge",
		Clients:    []string{clientUUID},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer srv.Stop()

	baseURL := "http://" + srv.Addr() + "/cdn-bridge"
	sid := "slow_video_session_01"

	getCh := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Get(baseURL + "/?sid=" + sid)
		if err == nil {
			getCh <- resp
		}
	}()
	time.Sleep(25 * time.Millisecond)

	frame := xhttptest.BuildVLESSReq(t, clientUUID, upAddr.IP, uint16(upAddr.Port), []byte("G"))
	r0, err := http.Post(baseURL+"/?sid="+sid+"&page=0", "application/octet-stream", bytes.NewReader(frame))
	if err != nil || r0.StatusCode != http.StatusOK {
		t.Fatalf("POST page=0: err=%v status=%v", err, r0)
	}
	_ = r0.Body.Close()

	var getResp *http.Response
	select {
	case getResp = <-getCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for GET response headers")
	}

	want := append([]byte{0x00, 0x00}, videoChunk...)
	var got bytes.Buffer
	buf := make([]byte, 64*1024)
	for {
		time.Sleep(75 * time.Millisecond)
		n, rerr := getResp.Body.Read(buf)
		if n > 0 {
			got.Write(buf[:n])
		}
		if rerr != nil {
			break
		}
	}
	_ = getResp.Body.Close()
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("slow downlink truncated: got %d bytes, want %d bytes", got.Len(), len(want))
	}

	for _, seq := range []string{"3", "6"} {
		rTrail, err := http.Post(baseURL+"/?sid="+sid+"&page="+seq, "application/octet-stream", bytes.NewReader(make([]byte, 24)))
		if err != nil {
			t.Fatalf("POST trailing page=%s: %v", seq, err)
		}
		_ = rTrail.Body.Close()
		if rTrail.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK for trailing close_notify page=%s, got %d", seq, rTrail.StatusCode)
		}
	}
	if srv.SessionCount() != 0 || srv.PreAuthSessionCount() != 0 {
		t.Fatalf("expected 0 zombie sessions after trailing packets, got sessions=%d preAuth=%d", srv.SessionCount(), srv.PreAuthSessionCount())
	}
}

func TestXHTTPServer_LiveClientUUIDRevocation(t *testing.T) {
	echoIP, echoPort, _, closeEcho := startEchoTCPServer(t)
	defer closeEcho()

	uuidA := "11111111-2222-3333-4444-555555555555"
	uuidB := "22222222-3333-4444-5555-666666666666"

	cfg := xhttpserver.Config{
		ListenAddr: "127.0.0.1:0",
		PathPrefix: "/cdn-bridge",
		Clients:    []string{uuidA, uuidB},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	}
	srv := newCServer(t, cfg)
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer srv.Stop()

	baseURL := "http://" + srv.Addr() + "/cdn-bridge"

	openPacketSession := func(sid, uuid string) *http.Response {
		t.Helper()
		getCh := make(chan *http.Response, 1)
		go func() {
			resp, err := http.Get(baseURL + "/?sid=" + sid)
			if err == nil {
				getCh <- resp
			}
		}()
		time.Sleep(20 * time.Millisecond)
		frame := xhttptest.BuildVLESSReq(t, uuid, echoIP, echoPort, []byte("hello-"+sid))
		r0, err := http.Post(baseURL+"/?sid="+sid+"&page=0", "application/octet-stream", bytes.NewReader(frame))
		if err != nil || r0.StatusCode != http.StatusOK {
			t.Fatalf("POST page=0 (%s): err=%v status=%v", sid, err, r0)
		}
		_ = r0.Body.Close()

		var getResp *http.Response
		select {
		case getResp = <-getCh:
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for GET response (%s)", sid)
		}
		want := append([]byte{0x00, 0x00}, []byte("hello-"+sid)...)
		buf := make([]byte, len(want))
		if _, err := io.ReadFull(getResp.Body, buf); err != nil {
			t.Fatalf("read initial echo (%s): %v", sid, err)
		}
		if !bytes.Equal(buf, want) {
			t.Fatalf("initial echo mismatch (%s): got %q, want %q", sid, buf, want)
		}
		return getResp
	}

	respA := openPacketSession("sid_revoke_a", uuidA)
	defer respA.Body.Close()
	respB := openPacketSession("sid_keep_b", uuidB)
	defer respB.Body.Close()

	prA, pwA := io.Pipe()
	defer pwA.Close()
	streamOneDoneA := make(chan error, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/", prA)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			streamOneDoneA <- err
			return
		}
		defer resp.Body.Close()
		_, err = io.ReadAll(resp.Body)
		streamOneDoneA <- err
	}()
	_, _ = pwA.Write(xhttptest.BuildVLESSReq(t, uuidA, echoIP, echoPort, []byte("stream-a")))
	time.Sleep(40 * time.Millisecond)

	cfg.ListenAddr = srv.Addr()
	cfg.Clients = []string{uuidB}
	if err := srv.UpdateConfig(cfg); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	readDoneA := make(chan error, 1)
	go func() {
		var tmp [16]byte
		_, err := respA.Body.Read(tmp[:])
		readDoneA <- err
	}()
	select {
	case err := <-readDoneA:
		if err == nil {
			t.Fatalf("expected revoked packet-up downlink to close, got nil error")
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatalf("revoked packet-up downlink did not close after UpdateConfig")
	}

	select {
	case <-streamOneDoneA:
	case <-time.After(1500 * time.Millisecond):
		t.Fatalf("revoked stream-one session did not terminate after UpdateConfig")
	}

	rRevokedPost, err := http.Post(baseURL+"/?sid=sid_revoke_a&page=1", "application/octet-stream", bytes.NewReader([]byte("after-revoke")))
	if err != nil {
		t.Fatalf("POST after revoke: %v", err)
	}
	_ = rRevokedPost.Body.Close()
	if rRevokedPost.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden on subsequent POST for revoked SID, got %d", rRevokedPost.StatusCode)
	}

	rKeepPost, err := http.Post(baseURL+"/?sid=sid_keep_b&page=1", "application/octet-stream", bytes.NewReader([]byte("still-alive")))
	if err != nil || rKeepPost.StatusCode != http.StatusOK {
		t.Fatalf("POST page=1 for uuidB failed: err=%v status=%v", err, rKeepPost)
	}
	_ = rKeepPost.Body.Close()
	aliveBuf := make([]byte, len("still-alive"))
	if _, err := io.ReadFull(respB.Body, aliveBuf); err != nil {
		t.Fatalf("read uuidB echo after uuidA revocation: %v", err)
	}
	if string(aliveBuf) != "still-alive" {
		t.Fatalf("unexpected uuidB payload: %q", string(aliveBuf))
	}
}

func TestXHTTPServer_LANPrivateTCPNoPrematureReadTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		var reqBuf [4]byte
		_, _ = io.ReadFull(c, reqBuf[:])
		time.Sleep(8600 * time.Millisecond)
		_, _ = c.Write([]byte("part1-"))
		time.Sleep(3800 * time.Millisecond)
		_, _ = c.Write([]byte("part2"))
	}()

	upAddr := ln.Addr().(*net.TCPAddr)
	clientUUID := "11111111-2222-3333-4444-555555555555"
	srv := newCServer(t, xhttpserver.Config{
		ListenAddr: "127.0.0.1:0",
		PathPrefix: "/cdn-bridge",
		Clients:    []string{clientUUID},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer srv.Stop()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		baseURL := "http://" + srv.Addr() + "/cdn-bridge/"
		frame := xhttptest.BuildVLESSReq(t, clientUUID, upAddr.IP, uint16(upAddr.Port), []byte("PING"))
		resp, err := http.Post(baseURL, "application/octet-stream", bytes.NewReader(frame))
		if err != nil {
			t.Errorf("POST stream-one: %v", err)
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Errorf("read response body: %v", err)
			return
		}
		want := append([]byte{0x00, 0x00}, []byte("part1-part2")...)
		if !bytes.Equal(body, want) {
			t.Errorf("got body %q, want %q", body, want)
		}
	}()
	wg.Wait()
}
