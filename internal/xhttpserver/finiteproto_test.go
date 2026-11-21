package xhttpserver_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/xhttpserver"
	"github.com/hoaxisr/awg-manager/internal/xhttpserver/xhttptest"
)

func TestFiniteProto_UnitCredentialGuards(t *testing.T) {
	uuidStr := "41b7ef34-6b19-48f3-8d95-82cf4474e109"
	u, err := xhttpserver.ParseUUID(uuidStr)
	if err != nil {
		t.Fatalf("ParseUUID: %v", err)
	}
	canonical := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
	hexNoDash := hex.EncodeToString(u[:])
	v1Sum := sha256.Sum256([]byte("awgm-fp-v1:" + canonical))
	rawSum := sha256.Sum256(u[:])

	fpClientID, _, _, err := xhttpserver.GenerateFPCredentials()
	if err != nil {
		t.Fatalf("GenerateFPCredentials: %v", err)
	}

	if _, err := xhttptest.SignFPRequest(canonical, v1Sum[:], http.MethodPost, "/cdn-bridge", []byte("x"), time.Now()); !errors.Is(err, xhttpserver.ErrFPUUIDDerivedKey) {
		t.Fatalf("expected ErrFPUUIDDerivedKey for v1Sum, got %v", err)
	}
	if _, err := xhttptest.SignFPRequest(canonical, rawSum[:], http.MethodPost, "/cdn-bridge", []byte("x"), time.Now()); !errors.Is(err, xhttpserver.ErrFPUUIDDerivedKey) {
		t.Fatalf("expected ErrFPUUIDDerivedKey for rawSum, got %v", err)
	}
	if _, err := xhttptest.SignFPRequest(canonical, []byte(hexNoDash), http.MethodPost, "/cdn-bridge", []byte("x"), time.Now()); !errors.Is(err, xhttpserver.ErrFPUUIDDerivedKey) {
		t.Fatalf("expected ErrFPUUIDDerivedKey for hexNoDash, got %v", err)
	}
	if _, err := xhttptest.SignFPRequest(fpClientID, u[:], http.MethodPost, "/cdn-bridge", []byte("x"), time.Now()); !errors.Is(err, xhttpserver.ErrFPInvalidCredentials) {
		t.Fatalf("expected ErrFPInvalidCredentials for 16-byte secret, got %v", err)
	}
}

func TestFiniteProto_RoundTripAndAuth_LiveCDaemon(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	defer echoLn.Close()
	echoAddr := echoLn.Addr().(*net.TCPAddr)
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()

	uuidStr := "41b7ef34-6b19-48f3-8d95-82cf4474e109"
	fpClientID, _, fpSecret, err := xhttpserver.GenerateFPCredentials()
	if err != nil {
		t.Fatalf("GenerateFPCredentials: %v", err)
	}

	srv := newCServer(t, xhttpserver.Config{
		ListenAddr: "127.0.0.1:0",
		PathPrefix: "/cdn-bridge",
		Clients:    []string{uuidStr},
		FiniteCredentials: map[string][]byte{
			fpClientID: fpSecret,
		},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer srv.Stop()

	baseURL := "http://" + srv.Addr()
	hc := &http.Client{Timeout: 5 * time.Second}

	doFP := func(path, clientID string, secret []byte, f *xhttptest.FPRequestEnvelope) *xhttptest.FPResponseEnvelope {
		t.Helper()
		raw, err := xhttptest.EncodeFPRequest(f)
		if err != nil {
			t.Fatalf("EncodeFPRequest: %v", err)
		}
		req, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		authVal, err := xhttptest.SignFPRequest(clientID, secret, http.MethodPost, path, raw, time.Now())
		if err != nil {
			t.Fatalf("SignFPRequest: %v", err)
		}
		req.Header.Set(xhttptest.FPHeaderProtoVersion, xhttptest.FPProtoVersion)
		req.Header.Set(xhttptest.FPHeaderAuth, authVal)

		resp, err := hc.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("unexpected HTTP status: %d", resp.StatusCode)
		}
		respRaw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		rf, err := xhttptest.DecodeFPResponse(respRaw, xhttptest.FPMaxRespPayload)
		if err != nil {
			t.Fatalf("DecodeFPResponse: %v", err)
		}
		return rf
	}

	openID := xhttptest.FPSessionID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	targetAddr := fmt.Sprintf("127.0.0.1:%d", echoAddr.Port)

	openResp := doFP("/cdn-bridge/fp-stage2-probe", fpClientID, fpSecret, &xhttptest.FPRequestEnvelope{
		Op:        xhttptest.FPOpOpen,
		SessionID: openID,
		Payload:   []byte(targetAddr),
	})
	if openResp.Status != xhttptest.FPStatusOK || openResp.SessionID.IsZero() {
		t.Fatalf("open status=%d sid=%s", openResp.Status, openResp.SessionID)
	}
	sid := openResp.SessionID

	upResp := doFP("/cdn-bridge/fp-stage2-probe", fpClientID, fpSecret, &xhttptest.FPRequestEnvelope{
		Op:           xhttptest.FPOpUp,
		SessionID:    sid,
		UpOffset:     0,
		MaxDownBytes: 4096,
		Payload:      []byte("hello-fp-c-only"),
	})
	if upResp.Status != xhttptest.FPStatusOK {
		t.Fatalf("up status=%d", upResp.Status)
	}

	var collected []byte
	var downOffset uint64
	for i := 0; i < 15 && len(collected) < len("hello-fp-c-only"); i++ {
		downResp := doFP("/cdn-bridge/fp-stage2-probe", fpClientID, fpSecret, &xhttptest.FPRequestEnvelope{
			Op:             xhttptest.FPOpDown,
			SessionID:      sid,
			AckDownOffset:  downOffset,
			WantDownOffset: downOffset,
			MaxDownBytes:   4096,
		})
		if downResp.Status != xhttptest.FPStatusOK {
			t.Fatalf("down status=%d", downResp.Status)
		}
		if len(downResp.Payload) > 0 {
			collected = append(collected, downResp.Payload...)
			downOffset = downResp.DownOffsetEnd
		} else {
			time.Sleep(15 * time.Millisecond)
		}
	}
	if string(collected) != "hello-fp-c-only" {
		t.Fatalf("echo mismatch: got %q, want %q", string(collected), "hello-fp-c-only")
	}

	closeResp := doFP("/cdn-bridge/fp-stage2-probe", fpClientID, fpSecret, &xhttptest.FPRequestEnvelope{
		Op:        xhttptest.FPOpClose,
		SessionID: sid,
	})
	if closeResp.Status != xhttptest.FPStatusOK {
		t.Fatalf("close status=%d", closeResp.Status)
	}

	// Default stage2probe must be rejected
	legacyStage2Secret, _ := hex.DecodeString("e4c9a17b82f04d63951ea8072bc4f31968d20a5c7e139b48206df51a3c9e74b2")
	openID2 := xhttptest.FPSessionID{9, 9, 9, 9, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	rawOpen2, _ := xhttptest.EncodeFPRequest(&xhttptest.FPRequestEnvelope{
		Op:        xhttptest.FPOpOpen,
		SessionID: openID2,
		Payload:   []byte(targetAddr),
	})
	req2, _ := http.NewRequest(http.MethodPost, baseURL+"/cdn-bridge/fp-stage2-probe", bytes.NewReader(rawOpen2))
	authVal2, _ := xhttptest.SignFPRequest(xhttptest.Stage2ProbeClientID, legacyStage2Secret, http.MethodPost, "/cdn-bridge/fp-stage2-probe", rawOpen2, time.Now())
	req2.Header.Set(xhttptest.FPHeaderProtoVersion, xhttptest.FPProtoVersion)
	req2.Header.Set(xhttptest.FPHeaderAuth, authVal2)
	resp2, err := hc.Do(req2)
	if err != nil {
		t.Fatalf("Do stage2probe default: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for default stage2probe, got %d", resp2.StatusCode)
	}

	// Explicit opt-in via FP_ENABLE_DIAG_CRED=1 + 64-hex FP_SECRET_HEX
	customDiagHex := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	t.Setenv("FP_ENABLE_DIAG_CRED", "1")
	t.Setenv("FP_SECRET_HEX", customDiagHex)
	if err := srv.UpdateConfig(xhttpserver.Config{
		ListenAddr: srv.Addr(),
		PathPrefix: "/cdn-bridge",
		Clients:    []string{uuidStr},
		FiniteCredentials: map[string][]byte{
			fpClientID: fpSecret,
		},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	customDiagSecret, _ := hex.DecodeString(customDiagHex)
	openResp2 := doFP("/cdn-bridge/fp-stage2-probe", xhttptest.Stage2ProbeClientID, customDiagSecret, &xhttptest.FPRequestEnvelope{
		Op:        xhttptest.FPOpOpen,
		SessionID: openID2,
		Payload:   []byte(targetAddr),
	})
	if openResp2.Status != xhttptest.FPStatusOK || openResp2.SessionID.IsZero() {
		t.Fatalf("stage2probe opt-in open status=%d", openResp2.Status)
	}
	_ = doFP("/cdn-bridge/fp-stage2-probe", xhttptest.Stage2ProbeClientID, customDiagSecret, &xhttptest.FPRequestEnvelope{
		Op:        xhttptest.FPOpClose,
		SessionID: openResp2.SessionID,
	})
}

func TestFiniteProto_RejectsAllUUIDDerivedVariantsAndWrongKeys_LiveCDaemon(t *testing.T) {
	uuidStr := "41b7ef34-6b19-48f3-8d95-82cf4474e109"
	u, _ := xhttpserver.ParseUUID(uuidStr)
	canonical := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
	hexNoDash := hex.EncodeToString(u[:])
	v1Sum := sha256.Sum256([]byte("awgm-fp-v1:" + canonical))
	rawSum := sha256.Sum256(u[:])

	fpClientID, _, fpSecret, err := xhttpserver.GenerateFPCredentials()
	if err != nil {
		t.Fatalf("GenerateFPCredentials: %v", err)
	}

	srv := newCServer(t, xhttpserver.Config{
		ListenAddr: "127.0.0.1:0",
		PathPrefix: "/cdn-bridge",
		Clients:    []string{uuidStr},
		FiniteCredentials: map[string][]byte{
			fpClientID: fpSecret,
			canonical:  v1Sum[:],
			hexNoDash:  rawSum[:],
		},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer srv.Stop()

	hc := &http.Client{Timeout: 5 * time.Second}
	baseURL := "http://" + srv.Addr()

	sendCraftedOpen := func(clientID string, key []byte, tamperSig bool) int {
		t.Helper()
		rawOpen, _ := xhttptest.EncodeFPRequest(&xhttptest.FPRequestEnvelope{
			Op:        xhttptest.FPOpOpen,
			SessionID: xhttptest.FPSessionID{1, 1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3, 4, 4, 4, 4},
			Payload:   []byte("127.0.0.1:443"),
		})
		tsStr := strconv.FormatInt(time.Now().UnixMilli(), 10)
		nonceHex := "0102030405060708090a0b0c"
		sig := xhttptest.ComputeFPHMAC(key, http.MethodPost, "/cdn-bridge", clientID, tsStr, nonceHex, rawOpen)
		if tamperSig {
			sig[0] ^= 0xff
		}
		authHdr := fmt.Sprintf("%s:%s:%s:%s", clientID, tsStr, nonceHex, hex.EncodeToString(sig))
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/cdn-bridge", bytes.NewReader(rawOpen))
		req.Header.Set(xhttptest.FPHeaderProtoVersion, xhttptest.FPProtoVersion)
		req.Header.Set(xhttptest.FPHeaderAuth, authHdr)
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	uuidDerivedKeys := []struct {
		name string
		key  []byte
	}{
		{name: "sha256_prefixed", key: v1Sum[:]},
		{name: "sha256_raw_uuid", key: rawSum[:]},
		{name: "ascii_hex_no_dash", key: []byte(hexNoDash)},
		{name: "raw_uuid_16b", key: u[:]},
	}
	for _, id := range []string{canonical, hexNoDash, fpClientID} {
		for _, k := range uuidDerivedKeys {
			if code := sendCraftedOpen(id, k.key, false); code != http.StatusUnauthorized {
				t.Fatalf("clientID=%q variant=%s: expected 401 Unauthorized, got %d", id, k.name, code)
			}
		}
	}

	_, _, wrongSecret, _ := xhttpserver.GenerateFPCredentials()
	if code := sendCraftedOpen(fpClientID, wrongSecret, false); code != http.StatusUnauthorized {
		t.Fatalf("known clientID with wrong key: expected 401 Unauthorized, got %d", code)
	}
	if code := sendCraftedOpen(fpClientID, fpSecret, true); code != http.StatusUnauthorized {
		t.Fatalf("tampered signature: expected 401 Unauthorized, got %d", code)
	}
	if got := srv.FPSessionCount(); got != 0 {
		t.Fatalf("expected 0 open FP sessions after rejected auth attempts, got %d", got)
	}
}

func TestFiniteProto_CredentialRotationAndMultiClient_LiveCDaemon(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	defer echoLn.Close()
	echoAddr := echoLn.Addr().(*net.TCPAddr)
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()

	uuidStr := "41b7ef34-6b19-48f3-8d95-82cf4474e109"
	fpClientID, _, secretA, _ := xhttpserver.GenerateFPCredentials()
	_, _, secretB, _ := xhttpserver.GenerateFPCredentials()
	fpClient2, _, secretClient2, _ := xhttpserver.GenerateFPCredentials()

	srv := newCServer(t, xhttpserver.Config{
		ListenAddr: "127.0.0.1:0",
		PathPrefix: "/cdn-bridge",
		Clients:    []string{uuidStr},
		FiniteCredentials: map[string][]byte{
			fpClientID: secretA,
			fpClient2:  secretClient2,
		},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer srv.Stop()

	hc := &http.Client{Timeout: 5 * time.Second}
	baseURL := "http://" + srv.Addr()

	sendFP := func(clientID string, secret []byte, env *xhttptest.FPRequestEnvelope) (int, *xhttptest.FPResponseEnvelope) {
		t.Helper()
		raw, _ := xhttptest.EncodeFPRequest(env)
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/cdn-bridge", bytes.NewReader(raw))
		authVal, err := xhttptest.SignFPRequest(clientID, secret, http.MethodPost, "/cdn-bridge", raw, time.Now())
		if err != nil {
			t.Fatalf("SignFPRequest: %v", err)
		}
		req.Header.Set(xhttptest.FPHeaderProtoVersion, xhttptest.FPProtoVersion)
		req.Header.Set(xhttptest.FPHeaderAuth, authVal)
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()
		respRaw, _ := io.ReadAll(resp.Body)
		rf, _ := xhttptest.DecodeFPResponse(respRaw, xhttptest.FPMaxRespPayload)
		return resp.StatusCode, rf
	}

	targetAddr := fmt.Sprintf("127.0.0.1:%d", echoAddr.Port)

	// Concurrent multi-client verification before rotation
	var wg sync.WaitGroup
	for idx, cl := range []struct {
		id  string
		sec []byte
	}{
		{fpClientID, secretA},
		{fpClient2, secretClient2},
	} {
		wg.Add(1)
		go func(i int, cid string, sec []byte) {
			defer wg.Done()
			code, oResp := sendFP(cid, sec, &xhttptest.FPRequestEnvelope{
				Op:        xhttptest.FPOpOpen,
				SessionID: xhttptest.FPSessionID{byte(10 + i), 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
				Payload:   []byte(targetAddr),
			})
			if code != http.StatusOK || oResp == nil || oResp.Status != xhttptest.FPStatusOK {
				t.Errorf("multi-client %d open failed: code=%d", i, code)
				return
			}
			_, _ = sendFP(cid, sec, &xhttptest.FPRequestEnvelope{Op: xhttptest.FPOpClose, SessionID: oResp.SessionID})
		}(idx, cl.id, cl.sec)
	}
	wg.Wait()

	code, openResp := sendFP(fpClientID, secretA, &xhttptest.FPRequestEnvelope{
		Op:        xhttptest.FPOpOpen,
		SessionID: xhttptest.FPSessionID{7, 7, 7, 7, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12},
		Payload:   []byte(targetAddr),
	})
	if code != http.StatusOK || openResp == nil || openResp.Status != xhttptest.FPStatusOK {
		t.Fatalf("open with secretA failed: http=%d", code)
	}
	sidA := openResp.SessionID

	if err := srv.UpdateConfig(xhttpserver.Config{
		ListenAddr: srv.Addr(),
		PathPrefix: "/cdn-bridge",
		Clients:    []string{uuidStr},
		FiniteCredentials: map[string][]byte{
			fpClientID: secretB,
		},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	}); err != nil {
		t.Fatalf("UpdateConfig rotate: %v", err)
	}

	codeOld, _ := sendFP(fpClientID, secretA, &xhttptest.FPRequestEnvelope{
		Op:        xhttptest.FPOpUp,
		SessionID: sidA,
		UpOffset:  0,
		Payload:   []byte("after-rotation"),
	})
	if codeOld != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for rotated-out secretA, got %d", codeOld)
	}

	codeGone, goneResp := sendFP(fpClientID, secretB, &xhttptest.FPRequestEnvelope{
		Op:        xhttptest.FPOpUp,
		SessionID: sidA,
		UpOffset:  0,
		Payload:   []byte("after-rotation"),
	})
	if codeGone != http.StatusGone || goneResp == nil || goneResp.Status != xhttptest.FPStatusUnknownSession {
		t.Fatalf("expected 410 Gone / FPStatusUnknownSession for old sidA after key rotation, got http=%d", codeGone)
	}

	codeB, openRespB := sendFP(fpClientID, secretB, &xhttptest.FPRequestEnvelope{
		Op:        xhttptest.FPOpOpen,
		SessionID: xhttptest.FPSessionID{8, 8, 8, 8, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12},
		Payload:   []byte(targetAddr),
	})
	if codeB != http.StatusOK || openRespB == nil || openRespB.Status != xhttptest.FPStatusOK {
		t.Fatalf("open with secretB failed: http=%d", codeB)
	}
	sidB := openRespB.SessionID

	if err := srv.UpdateConfig(xhttpserver.Config{
		ListenAddr:        srv.Addr(),
		PathPrefix:        "/cdn-bridge",
		Clients:           []string{uuidStr},
		FiniteCredentials: nil,
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	}); err != nil {
		t.Fatalf("UpdateConfig revoke: %v", err)
	}

	codeRevoked, _ := sendFP(fpClientID, secretB, &xhttptest.FPRequestEnvelope{
		Op:        xhttptest.FPOpUp,
		SessionID: sidB,
		UpOffset:  0,
		Payload:   []byte("after-revoke"),
	})
	if codeRevoked != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized after credential revocation, got %d", codeRevoked)
	}
}

func TestFiniteProto_TrailingSlashProxyNormalizedPathAuth_LiveCDaemon(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	defer echoLn.Close()
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	uuidStr := "41b7ef34-6b19-48f3-8d95-82cf4474e109"
	fpClientID, _, fpSecret, _ := xhttpserver.GenerateFPCredentials()

	srv := newCServer(t, xhttpserver.Config{
		ListenAddr: "127.0.0.1:0",
		PathPrefix: "/cdn-bridge",
		Clients:    []string{uuidStr},
		FiniteCredentials: map[string][]byte{
			fpClientID: fpSecret,
		},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer srv.Stop()

	openID := xhttptest.FPSessionID{7, 7, 7, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	raw, _ := xhttptest.EncodeFPRequest(&xhttptest.FPRequestEnvelope{
		Op:        xhttptest.FPOpOpen,
		SessionID: openID,
		Payload:   []byte(echoLn.Addr().String()),
	})

	authVal, err := xhttptest.SignFPRequest(fpClientID, fpSecret, http.MethodPost, "/cdn-bridge", raw, time.Now())
	if err != nil {
		t.Fatalf("SignFPRequest: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, "http://"+srv.Addr()+"/cdn-bridge/", bytes.NewReader(raw))
	req.Header.Set(xhttptest.FPHeaderProtoVersion, xhttptest.FPProtoVersion)
	req.Header.Set(xhttptest.FPHeaderAuth, authVal)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200 OK when reverse proxy appends trailing slash, got %d", resp.StatusCode)
	}
}

func TestFiniteProto_UDPOverTCP_LiveCDaemon(t *testing.T) {
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp echo: %v", err)
	}
	defer pc.Close()
	echoAddr := pc.LocalAddr().(*net.UDPAddr)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, addr, rerr := pc.ReadFrom(buf)
			if rerr != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], addr)
		}
	}()

	uuidStr := "41b7ef34-6b19-48f3-8d95-82cf4474e109"
	fpClientID, _, fpSecret, err := xhttpserver.GenerateFPCredentials()
	if err != nil {
		t.Fatalf("GenerateFPCredentials: %v", err)
	}

	srv := newCServer(t, xhttpserver.Config{
		ListenAddr: "127.0.0.1:0",
		PathPrefix: "/cdn-bridge",
		Clients:    []string{uuidStr},
		FiniteCredentials: map[string][]byte{
			fpClientID: fpSecret,
		},
		Egress: xhttpserver.EgressConfig{
			Mode:                     "direct",
			AllowPrivateDestinations: true,
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer srv.Stop()

	baseURL := "http://" + srv.Addr()
	hc := &http.Client{Timeout: 5 * time.Second}

	doFPRaw := func(f *xhttptest.FPRequestEnvelope) (int, *xhttptest.FPResponseEnvelope) {
		t.Helper()
		raw, err := xhttptest.EncodeFPRequest(f)
		if err != nil {
			t.Fatalf("EncodeFPRequest: %v", err)
		}
		path := "/cdn-bridge/udp-test"
		req, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		authVal, err := xhttptest.SignFPRequest(fpClientID, fpSecret, http.MethodPost, path, raw, time.Now())
		if err != nil {
			t.Fatalf("SignFPRequest: %v", err)
		}
		req.Header.Set(xhttptest.FPHeaderProtoVersion, xhttptest.FPProtoVersion)
		req.Header.Set(xhttptest.FPHeaderAuth, authVal)

		resp, err := hc.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()
		respRaw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		rf, err := xhttptest.DecodeFPResponse(respRaw, xhttptest.FPMaxRespPayload)
		if err != nil {
			t.Fatalf("DecodeFPResponse (status=%d): %v", resp.StatusCode, err)
		}
		return resp.StatusCode, rf
	}

	// QUIC UDP 443 must be refused with HTTP 403.
	codeQuic, _ := doFPRaw(&xhttptest.FPRequestEnvelope{
		Op:        xhttptest.FPOpOpen,
		SessionID: xhttptest.FPSessionID{0x55, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
		Payload:   []byte("udp://8.8.8.8:443"),
	})
	if codeQuic != http.StatusForbidden {
		t.Fatalf("expected 403 for udp://8.8.8.8:443 QUIC, got %d", codeQuic)
	}

	openID := xhttptest.FPSessionID{0x44, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	targetAddr := fmt.Sprintf("udp://127.0.0.1:%d", echoAddr.Port)
	codeOpen, openResp := doFPRaw(&xhttptest.FPRequestEnvelope{
		Op:        xhttptest.FPOpOpen,
		SessionID: openID,
		Payload:   []byte(targetAddr),
	})
	if codeOpen != http.StatusOK || openResp.Status != xhttptest.FPStatusOK || openResp.SessionID.IsZero() {
		t.Fatalf("open udp failed: code=%d status=%d sid=%s", codeOpen, openResp.Status, openResp.SessionID)
	}
	sid := openResp.SessionID

	// Send 1 split frame across two UP calls + 1 coalesced pair of frames.
	part1 := []byte{0x00, 0x05, 'A', 'L'}
	part2 := []byte{'P', 'H', 'A', 0x00, 0x04, 'B', 'E', 'T', 'A', 0x00, 0x05, 'G', 'A', 'M', 'M', 'A'}
	codeUp1, up1 := doFPRaw(&xhttptest.FPRequestEnvelope{
		Op:           xhttptest.FPOpUp,
		SessionID:    sid,
		UpOffset:     0,
		MaxDownBytes: 4096,
		Payload:      part1,
	})
	if codeUp1 != http.StatusOK || up1.Status != xhttptest.FPStatusOK {
		t.Fatalf("up1 failed: code=%d status=%d", codeUp1, up1.Status)
	}
	codeUp2, up2 := doFPRaw(&xhttptest.FPRequestEnvelope{
		Op:           xhttptest.FPOpUp,
		SessionID:    sid,
		UpOffset:     uint64(len(part1)),
		MaxDownBytes: 4096,
		Payload:      part2,
	})
	if codeUp2 != http.StatusOK || up2.Status != xhttptest.FPStatusOK {
		t.Fatalf("up2 failed: code=%d status=%d", codeUp2, up2.Status)
	}

	wantWire := append(append([]byte{}, part1...), part2...)
	var collected []byte
	var downOffset uint64
	for i := 0; i < 30 && len(collected) < len(wantWire); i++ {
		codeDown, downResp := doFPRaw(&xhttptest.FPRequestEnvelope{
			Op:             xhttptest.FPOpDown,
			SessionID:      sid,
			AckDownOffset:  downOffset,
			WantDownOffset: downOffset,
			MaxDownBytes:   4096,
		})
		if codeDown != http.StatusOK || downResp.Status != xhttptest.FPStatusOK {
			t.Fatalf("down failed: code=%d status=%d", codeDown, downResp.Status)
		}
		if len(downResp.Payload) > 0 {
			collected = append(collected, downResp.Payload...)
			downOffset = downResp.DownOffsetEnd
		} else {
			time.Sleep(15 * time.Millisecond)
		}
	}
	if !bytes.Equal(collected, wantWire) {
		t.Fatalf("UDP echo mismatch: got %x, want %x", collected, wantWire)
	}

	codeClose, closeResp := doFPRaw(&xhttptest.FPRequestEnvelope{
		Op:        xhttptest.FPOpClose,
		SessionID: sid,
	})
	if codeClose != http.StatusOK || closeResp.Status != xhttptest.FPStatusOK {
		t.Fatalf("close failed: code=%d status=%d", codeClose, closeResp.Status)
	}
}
