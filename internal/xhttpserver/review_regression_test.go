package xhttpserver_test

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/xhttpserver"
	"github.com/hoaxisr/awg-manager/internal/xhttpserver/xhttptest"
)

func TestReviewSingleRequestDelayedResponseSurvives(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, e := ln.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		var b [1]byte
		_, _ = io.ReadFull(c, b[:])
		_, _ = c.Write([]byte("A"))
		time.Sleep(7500 * time.Millisecond)
		_, _ = c.Write([]byte("B"))
	}()

	uuid := "11111111-2222-3333-4444-555555555555"
	srv := newCServer(t, xhttpserver.Config{
		ListenAddr:              "127.0.0.1:0",
		PathPrefix:              "/cdn-bridge",
		Clients:                 []string{uuid},
		MaxTotalSessions:        256,
		MaxSessionsPerIP:        64,
		MaxPreAuthSessions:      64,
		MaxPreAuthSessionsPerIP: 32,
		Egress:                  xhttpserver.EgressConfig{Mode: "direct", AllowPrivateDestinations: true},
	})
	if e := srv.Start(); e != nil {
		t.Fatal(e)
	}
	defer srv.Stop()

	base := "http://" + srv.Addr() + "/cdn-bridge/?sid=review-delayed"
	client := &http.Client{Timeout: 12 * time.Second}
	down, e := client.Get(base)
	if e != nil {
		t.Fatal(e)
	}
	defer down.Body.Close()

	a := ln.Addr().(*net.TCPAddr)
	up, e := client.Post(base+"&page=0", "application/octet-stream", bytes.NewReader(xhttptest.BuildVLESSReq(t, uuid, a.IP, uint16(a.Port), []byte("G"))))
	if e != nil {
		t.Fatal(e)
	}
	_ = up.Body.Close()
	got, e := io.ReadAll(down.Body)
	if e != nil || !bytes.Equal(got, []byte{0, 0, 'A', 'B'}) {
		t.Fatalf("valid delayed response truncated: got=%q err=%v", got, e)
	}
}

func TestReviewSustainedRealClientGetWithIdleGapsAndThumbnailBurst(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
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
				buf := make([]byte, 4096)
				for {
					n, rerr := conn.Read(buf)
					if n > 0 {
						if _, werr := conn.Write(buf[:n]); werr != nil {
							return
						}
					}
					if rerr != nil {
						return
					}
				}
			}(c)
		}
	}()

	uuid := "11111111-2222-3333-4444-555555555555"
	srv := newCServer(t, xhttpserver.Config{
		ListenAddr:              "127.0.0.1:0",
		PathPrefix:              "/cdn-bridge",
		Clients:                 []string{uuid},
		MaxTotalSessions:        256,
		MaxSessionsPerIP:        64,
		MaxPreAuthSessions:      64,
		MaxPreAuthSessionsPerIP: 32,
		Egress:                  xhttpserver.EgressConfig{Mode: "direct", AllowPrivateDestinations: true},
	})
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	client := &http.Client{Timeout: 20 * time.Second}
	targetAddr := ln.Addr().(*net.TCPAddr)

	longBase := "http://" + srv.Addr() + "/cdn-bridge/?sid=sustained-gap"
	downResp, err := client.Get(longBase)
	if err != nil {
		t.Fatal(err)
	}
	defer downResp.Body.Close()

	up0, err := client.Post(longBase+"&page=0", "application/octet-stream", bytes.NewReader(xhttptest.BuildVLESSReq(t, uuid, targetAddr.IP, uint16(targetAddr.Port), []byte("PING1"))))
	if err != nil {
		t.Fatal(err)
	}
	_ = up0.Body.Close()

	firstBuf := make([]byte, 7)
	if _, err := io.ReadFull(downResp.Body, firstBuf); err != nil {
		t.Fatalf("read first response failed: %v", err)
	}
	if !bytes.Equal(firstBuf, []byte{0, 0, 'P', 'I', 'N', 'G', '1'}) {
		t.Fatalf("unexpected first response: %q", firstBuf)
	}

	var burstWG sync.WaitGroup
	errCh := make(chan error, 16)
	for i := 0; i < 16; i++ {
		burstWG.Add(1)
		go func(idx int) {
			defer burstWG.Done()
			sid := fmt.Sprintf("thumb-burst-%d", idx)
			u := "http://" + srv.Addr() + "/cdn-bridge/?sid=" + sid
			d, err := client.Get(u)
			if err != nil {
				errCh <- err
				return
			}
			defer d.Body.Close()
			payload := []byte(fmt.Sprintf("THUMB-%02d", idx))
			p, err := client.Post(u+"&page=0", "application/octet-stream", bytes.NewReader(xhttptest.BuildVLESSReq(t, uuid, targetAddr.IP, uint16(targetAddr.Port), payload)))
			if err != nil {
				errCh <- err
				return
			}
			_ = p.Body.Close()
			got := make([]byte, 2+len(payload))
			if _, err := io.ReadFull(d.Body, got); err != nil {
				errCh <- err
				return
			}
			if !bytes.Equal(got[2:], payload) {
				errCh <- fmt.Errorf("burst %d mismatch: got %q want %q", idx, got[2:], payload)
			}
		}(i)
	}

	time.Sleep(6500 * time.Millisecond)
	burstWG.Wait()
	close(errCh)
	for berr := range errCh {
		t.Fatalf("thumbnail burst error: %v", berr)
	}

	reqUp1, err := http.NewRequest(http.MethodGet, longBase+"&page=1", bytes.NewReader([]byte("PING2")))
	if err != nil {
		t.Fatal(err)
	}
	up1, err := client.Do(reqUp1)
	if err != nil {
		t.Fatalf("GET-uplink seq=1 after 6.5s idle failed: %v", err)
	}
	if up1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for GET-uplink seq=1 after 6.5s idle, got %d", up1.StatusCode)
	}
	_ = up1.Body.Close()

	secondBuf := make([]byte, 5)
	if _, err := io.ReadFull(downResp.Body, secondBuf); err != nil {
		t.Fatalf("read second response after 6.5s idle failed: %v", err)
	}
	if !bytes.Equal(secondBuf, []byte("PING2")) {
		t.Fatalf("unexpected second response: %q", secondBuf)
	}
	_ = downResp.Body.Close()

	reconnBase := "http://" + srv.Addr() + "/cdn-bridge/?sid=sustained-reconnect"
	downReconn, err := client.Get(reconnBase)
	if err != nil {
		t.Fatalf("reconnect GET downlink failed: %v", err)
	}
	defer downReconn.Body.Close()
	reqReconn0, err := http.NewRequest(http.MethodGet, reconnBase+"&page=0", bytes.NewReader(xhttptest.BuildVLESSReq(t, uuid, targetAddr.IP, uint16(targetAddr.Port), []byte("RECONN-OK"))))
	if err != nil {
		t.Fatal(err)
	}
	upReconn0, err := client.Do(reqReconn0)
	if err != nil {
		t.Fatalf("reconnect GET-uplink seq=0 failed: %v", err)
	}
	_ = upReconn0.Body.Close()
	reconnBuf := make([]byte, 2+len("RECONN-OK"))
	if _, err := io.ReadFull(downReconn.Body, reconnBuf); err != nil {
		t.Fatalf("reconnect read response failed: %v", err)
	}
	if !bytes.Equal(reconnBuf[2:], []byte("RECONN-OK")) {
		t.Fatalf("reconnect response mismatch: got %q", reconnBuf[2:])
	}
}
