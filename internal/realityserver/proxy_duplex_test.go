package realityserver

// F854: прод-прокси newXHTTPProxy реверс-проксирует XHTTP
// в awgm-xhttpd (HTTP/1.1) без потери дуплекса. REALITY здесь не нужен —
// обычный TLS с самоподписанным сертификатом, h2 по ALPN.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/xhttpserver"
	"github.com/hoaxisr/awg-manager/internal/xhttpserver/xhttptest"
	"golang.org/x/net/http2"
)

const (
	duplexUUID    = "11111111-2222-3333-4444-555555555555"
	duplexPrefix  = "/cdn-bridge"
	duplexPayload = 1 << 20
)

// recordingRT видит исходящий к C запрос после Rewrite прод-прокси.
type recordingRT struct {
	http.RoundTripper
	onOut func(*http.Request)
}

func (r recordingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.onOut(req)
	return r.RoundTripper.RoundTrip(req)
}

type duplexEnv struct {
	c        *xhttpserver.Server
	front    *httptest.Server
	client   *http.Client
	baseURL  string
	echoIP   net.IP
	echoPort uint16
	vlessHdr []byte
	mu       sync.Mutex
	outHdrs  []http.Header
}

func startDuplexEnv(t *testing.T) *duplexEnv {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("awgm-xhttpd integration needs linux")
	}
	if os.Getenv("AWGM_C_XHTTPD_BIN") == "" {
		_, errCC := exec.LookPath("cc")
		_, errGCC := exec.LookPath("gcc")
		if errCC != nil && errGCC != nil {
			t.Skip("no C compiler and AWGM_C_XHTTPD_BIN unset: awgm-xhttpd unavailable")
		}
	}

	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = echoLn.Close() })
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()

	c := xhttpserver.New(xhttpserver.Config{
		ListenAddr:     "127.0.0.1:0",
		BinaryPath:     xhttptest.RequireCXHTTPDBinary(t),
		ConfigPath:     t.TempDir() + "/awgm-xhttpd.conf",
		PathPrefix:     duplexPrefix,
		Clients:        []string{duplexUUID},
		TrustedProxies: []string{"127.0.0.1/32"},
		Egress:         xhttpserver.EgressConfig{Mode: "direct", AllowPrivateDestinations: true},
	})
	if err := c.Start(); err != nil {
		t.Fatalf("awgm-xhttpd start: %v", err)
	}
	t.Cleanup(func() { _ = c.Stop() })

	env := &duplexEnv{c: c}
	ea := echoLn.Addr().(*net.TCPAddr)
	env.echoIP, env.echoPort = ea.IP, uint16(ea.Port)
	env.vlessHdr = xhttptest.BuildVLESSReq(t, duplexUUID, env.echoIP, env.echoPort, nil)

	front := httptest.NewUnstartedServer(newXHTTPProxy(c.Addr(), duplexPrefix, recordingRT{newUpstreamTransport(), func(r *http.Request) {
		env.mu.Lock()
		env.outHdrs = append(env.outHdrs, r.Header.Clone())
		env.mu.Unlock()
	}}))
	front.EnableHTTP2 = true
	front.StartTLS()
	t.Cleanup(front.Close)
	env.front = front
	env.baseURL = front.URL + duplexPrefix

	tr := &http2.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2"}}}
	t.Cleanup(tr.CloseIdleConnections)
	env.client = &http.Client{Transport: tr}
	return env
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// readEcho читает VLESS-ответ (2 байта) + want и сверяет побайтно.
func readEcho(r io.Reader, want []byte) error {
	got := make([]byte, 2+len(want))
	if _, err := io.ReadFull(r, got); err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if got[0] != 0 || got[1] != 0 {
		return fmt.Errorf("bad vless resp header %x", got[:2])
	}
	if !bytes.Equal(got[2:], want) {
		return errors.New("echo mismatch")
	}
	return nil
}

// pumpChunks пишет data в w кусками по 16 КиБ (имитация живого потока).
func pumpChunks(w io.Writer, data []byte) error {
	for off := 0; off < len(data); off += 16 << 10 {
		end := min(off+16<<10, len(data))
		if _, err := w.Write(data[off:end]); err != nil {
			return err
		}
	}
	return nil
}

func withTimeout(t *testing.T, d time.Duration, name string, fn func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	case <-time.After(d):
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("%s: timeout (deadlock?)\n%s", name, buf[:n])
	}
}

// streamOne открывает stream-one сессию: тело — живой pipe, ответ читается
// параллельно с записью. Возвращает writer тела и ответ.
func (e *duplexEnv) streamOne(ctx context.Context) (*io.PipeWriter, *http.Response, error) {
	pr, pw := io.Pipe()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/", pr)
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("X-Forwarded-For", "198.51.100.7")
	respCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := e.client.Do(req)
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()
	// C ждёт ≥24 байт тела до ответа (main.c stream_first_only), поэтому
	// заголовок VLESS пишется до ожидания ответа.
	if _, err := pw.Write(e.vlessHdr); err != nil {
		return nil, nil, err
	}
	select {
	case resp := <-respCh:
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, nil, fmt.Errorf("status %d", resp.StatusCode)
		}
		if resp.ProtoMajor != 2 {
			return nil, nil, fmt.Errorf("client proto %s, want h2", resp.Proto)
		}
		return pw, resp, nil
	case err := <-errCh:
		return nil, nil, err
	case <-time.After(5 * time.Second):
		return nil, nil, errors.New("no response headers")
	}
}

func TestDuplexProxy_StreamOne(t *testing.T) {
	e := startDuplexEnv(t)
	payload := randBytes(duplexPayload)
	withTimeout(t, 20*time.Second, "stream-one 1MiB", func() error {
		pw, resp, err := e.streamOne(context.Background())
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		werr := make(chan error, 1)
		go func() { werr <- pumpChunks(pw, payload) }()
		if err := readEcho(resp.Body, payload); err != nil {
			return err
		}
		if err := <-werr; err != nil {
			return err
		}
		return pw.Close()
	})
	e.assertForwardedReplaced(t)
}

func TestDuplexProxy_StreamUp(t *testing.T) {
	e := startDuplexEnv(t)
	payload := randBytes(duplexPayload)
	sid := "dupstreamup01"
	withTimeout(t, 20*time.Second, "stream-up 1MiB", func() error {
		down, err := e.client.Get(e.baseURL + "/?sid=" + sid)
		if err != nil {
			return err
		}
		defer down.Body.Close()
		if down.StatusCode != http.StatusOK {
			return fmt.Errorf("GET status %d", down.StatusCode)
		}
		pr, pw := io.Pipe()
		req, _ := http.NewRequest(http.MethodPost, e.baseURL+"/?sid="+sid, pr)
		upErr := make(chan error, 1)
		go func() {
			resp, err := e.client.Do(req)
			if err != nil {
				upErr <- err
				return
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				upErr <- fmt.Errorf("POST status %d", resp.StatusCode)
				return
			}
			upErr <- nil
		}()
		werr := make(chan error, 1)
		go func() {
			if _, err := pw.Write(e.vlessHdr); err != nil {
				werr <- err
				return
			}
			werr <- pumpChunks(pw, payload)
		}()
		if err := readEcho(down.Body, payload); err != nil {
			return err
		}
		if err := <-werr; err != nil {
			return err
		}
		_ = pw.Close()
		return <-upErr
	})
}

func TestDuplexProxy_PacketUp(t *testing.T) {
	e := startDuplexEnv(t)
	payload := randBytes(duplexPayload)
	sid := "duppacketup01"
	withTimeout(t, 20*time.Second, "packet-up 1MiB", func() error {
		down, err := e.client.Get(e.baseURL + "/?sid=" + sid)
		if err != nil {
			return err
		}
		defer down.Body.Close()
		if down.StatusCode != http.StatusOK {
			return fmt.Errorf("GET status %d", down.StatusCode)
		}
		frame := append(bytes.Clone(e.vlessHdr), payload...)
		werr := make(chan error, 1)
		go func() {
			const pkt = 64 << 10
			for seq, off := 0, 0; off < len(frame); seq, off = seq+1, off+pkt {
				end := min(off+pkt, len(frame))
				resp, err := e.client.Post(e.baseURL+"/?sid="+sid+"&page="+strconv.Itoa(seq), "application/octet-stream", bytes.NewReader(frame[off:end]))
				if err != nil {
					werr <- err
					return
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					werr <- fmt.Errorf("POST page=%d status %d", seq, resp.StatusCode)
					return
				}
			}
			werr <- nil
		}()
		if err := readEcho(down.Body, payload); err != nil {
			return err
		}
		return <-werr
	})
}

// Подделанный клиентом X-Forwarded-For/CF-Connecting-IP не доходит до C:
// 16 сессий packet-up с разными поддельными адресами упираются в лимит
// «на IP» (16) — значит, C считает их от одного реального адреса.
func TestDuplexProxy_SpoofedForwardedForDoesNotReachC(t *testing.T) {
	e := startDuplexEnv(t)
	authPrefix := e.vlessHdr[:17]
	post := func(i int) int {
		req, _ := http.NewRequest(http.MethodPost, e.baseURL+"/?sid=spoof"+strconv.Itoa(i)+"&page=0", bytes.NewReader(authPrefix))
		req.Header.Set("X-Forwarded-For", "203.0.113."+strconv.Itoa(i+1))
		req.Header.Set("CF-Connecting-IP", "203.0.113."+strconv.Itoa(i+1))
		req.Header.Set("X-Real-IP", "203.0.113."+strconv.Itoa(i+1))
		resp, err := e.client.Do(req)
		if err != nil {
			t.Fatalf("POST %d: %v", i, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for i := range 16 {
		if code := post(i); code != http.StatusOK {
			t.Fatalf("session %d: status %d, want 200", i, code)
		}
	}
	if code := post(16); code != http.StatusTooManyRequests {
		t.Fatalf("17th session with spoofed XFF: status %d, want 429 (spoof reached C)", code)
	}
	e.assertForwardedReplaced(t)
}

func (e *duplexEnv) assertForwardedReplaced(t *testing.T) {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.outHdrs) == 0 {
		t.Fatal("no proxied requests observed")
	}
	for _, h := range e.outHdrs {
		if got := h.Values("X-Forwarded-For"); len(got) != 1 || got[0] != "127.0.0.1" {
			t.Fatalf("outgoing X-Forwarded-For = %q, want [127.0.0.1]", got)
		}
		if h.Get("CF-Connecting-IP") != "" || h.Get("X-Real-IP") != "" {
			t.Fatalf("client CF-Connecting-IP/X-Real-IP leaked: %v", h)
		}
	}
}

// Замер: 50 одновременных stream-one сессий через прокси.
func TestDuplexProxy_Measure50StreamOne(t *testing.T) {
	e := startDuplexEnv(t)
	const n = 50
	// прогрев одного соединения, чтобы базовая линия включала h2-conn
	withTimeout(t, 10*time.Second, "warmup", func() error {
		pw, resp, err := e.streamOne(context.Background())
		if err != nil {
			return err
		}
		msg := []byte("warm")
		go func() { _, _ = pw.Write(msg) }()
		err = readEcho(resp.Body, msg)
		pw.Close()
		resp.Body.Close()
		return err
	})
	time.Sleep(300 * time.Millisecond)
	runtime.GC()
	baseG, baseProxyG := countGoroutines()
	baseRSS := rssKB(t, os.Getpid())
	baseCRSS, baseCThreads := procStat(t, e.c.CDaemonPID())

	type sess struct {
		pw   *io.PipeWriter
		resp *http.Response
	}
	sessions := make([]sess, n)
	chunk := randBytes(64 << 10)
	withTimeout(t, 30*time.Second, "open 50 stream-one", func() error {
		for i := range n {
			pw, resp, err := e.streamOne(context.Background())
			if err != nil {
				return fmt.Errorf("session %d: %w", i, err)
			}
			sessions[i] = sess{pw, resp}
		}
		var wg sync.WaitGroup
		errs := make(chan error, n)
		for _, s := range sessions {
			wg.Add(1)
			go func() {
				defer wg.Done()
				go func() { _ = pumpChunks(s.pw, chunk) }()
				errs <- readEcho(s.resp.Body, chunk)
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				return err
			}
		}
		return nil
	})
	time.Sleep(300 * time.Millisecond)
	g, proxyG := countGoroutines()
	rss := rssKB(t, os.Getpid())
	cRSS, cThreads := procStat(t, e.c.CDaemonPID())
	t.Logf("goroutines total: %d -> %d (+%.2f/stream); proxy-side (net/http server+transport+httputil): %d -> %d (+%.2f/stream)",
		baseG, g, float64(g-baseG)/n, baseProxyG, proxyG, float64(proxyG-baseProxyG)/n)
	t.Logf("test-process RSS (proxy+client+test): %d KiB -> %d KiB (+%d KiB, %.1f KiB/stream)",
		baseRSS, rss, rss-baseRSS, float64(rss-baseRSS)/n)
	t.Logf("awgm-xhttpd RSS: %d KiB -> %d KiB; threads %d -> %d", baseCRSS, cRSS, baseCThreads, cThreads)
	for _, s := range sessions {
		s.pw.Close()
		s.resp.Body.Close()
	}
}

// countGoroutines: всего и «прокси-сторона» — стеки с кадрами std net/http
// (h2-сервер в бандле net/http, транспорт апстрима, httputil), без x/net/http2
// клиента теста.
func countGoroutines() (total, proxy int) {
	buf := make([]byte, 8<<20)
	n := runtime.Stack(buf, true)
	for _, g := range strings.Split(string(buf[:n]), "\n\n") {
		total++
		if strings.Contains(g, "golang.org/x/net/http2") || strings.Contains(g, "realityserver_test.") {
			continue
		}
		if strings.Contains(g, "net/http.") || strings.Contains(g, "net/http/httputil.") {
			proxy++
		}
	}
	return
}

func rssKB(t *testing.T, pid int) int {
	kb, _ := procStat(t, pid)
	return kb
}

func procStat(t *testing.T, pid int) (rssKiB, threads int) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		t.Logf("read /proc/%d/status: %v", pid, err)
		return 0, 0
	}
	for _, l := range strings.Split(string(raw), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "VmRSS:":
			rssKiB, _ = strconv.Atoi(f[1])
		case "Threads:":
			threads, _ = strconv.Atoi(f[1])
		}
	}
	return
}

// Пинг-понг: следующий кусок тела уходит только после того, как эхо
// предыдущего вернулось в ответе. Без настоящего дуплекса (прокси ждёт конца
// тела перед ответом или копит тело) тест зависает, а не проходит за счёт
// буферов сокетов.
func TestDuplexProxy_StreamOneLockstep(t *testing.T) {
	e := startDuplexEnv(t)
	withTimeout(t, 20*time.Second, "stream-one lockstep", func() error {
		pw, resp, err := e.streamOne(context.Background())
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		defer pw.Close()
		// Заголовок ответа VLESS (2 байта) C шлёт вместе с первыми данными цели
		// (vless_xhttp.c run_vless_stream), поэтому он читается в раунде 0.
		for i := range 64 {
			chunk := randBytes(16 << 10)
			if _, err := pw.Write(chunk); err != nil {
				return fmt.Errorf("round %d write: %w", i, err)
			}
			if i == 0 {
				hdr := make([]byte, 2)
				if _, err := io.ReadFull(resp.Body, hdr); err != nil {
					return fmt.Errorf("vless resp header: %w", err)
				}
			}
			got := make([]byte, len(chunk))
			if _, err := io.ReadFull(resp.Body, got); err != nil {
				return fmt.Errorf("round %d read: %w", i, err)
			}
			if !bytes.Equal(got, chunk) {
				return fmt.Errorf("round %d mismatch", i)
			}
		}
		return nil
	})
}

func TestDuplexProxy_StreamUpLockstep(t *testing.T) {
	e := startDuplexEnv(t)
	sid := "dupstreamup02"
	withTimeout(t, 20*time.Second, "stream-up lockstep", func() error {
		down, err := e.client.Get(e.baseURL + "/?sid=" + sid)
		if err != nil {
			return err
		}
		defer down.Body.Close()
		pr, pw := io.Pipe()
		defer pw.Close()
		req, _ := http.NewRequest(http.MethodPost, e.baseURL+"/?sid="+sid, pr)
		go func() {
			if resp, err := e.client.Do(req); err == nil {
				resp.Body.Close()
			}
		}()
		if _, err := pw.Write(e.vlessHdr); err != nil {
			return err
		}
		got := make([]byte, 16<<10)
		for i := range 64 {
			chunk := randBytes(16 << 10)
			if _, err := pw.Write(chunk); err != nil {
				return fmt.Errorf("round %d write: %w", i, err)
			}
			want := chunk
			if i == 0 {
				want = append([]byte{0, 0}, chunk...)
				got = make([]byte, len(want))
			} else if len(got) != len(chunk) {
				got = make([]byte, len(chunk))
			}
			if _, err := io.ReadFull(down.Body, got); err != nil {
				return fmt.Errorf("round %d read: %w", i, err)
			}
			if !bytes.Equal(got, want) {
				return fmt.Errorf("round %d mismatch", i)
			}
		}
		return nil
	})
}

// Вход REALITY пускает к C только свой путь: служебные и чужие пути демона
// с WAN недостижимы, в том числе через "..".
func TestXHTTPProxyOnlyServesPrefix(t *testing.T) {
	var hits []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
	}))
	defer up.Close()
	h := newXHTTPProxy(strings.TrimPrefix(up.URL, "http://"), "/rx", newUpstreamTransport())
	for _, p := range []string{"/.xhttpd/health", "/cdn-bridge/", "/rxx/", "/rx/../.xhttpd/health", "/rx/%2e%2e/.xhttpd/health"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/rx/abc", nil))
	if rec.Code != http.StatusOK || len(hits) != 1 {
		t.Fatalf("own path: status %d, upstream hits %v", rec.Code, hits)
	}
}
