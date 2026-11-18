package transport

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"sync/atomic"
)

// На KeeneticOS 5.01.C.9 (5.1.9) ndm отвечает на любой запрос к
// 127.0.0.1:79/rci 403 «0x1867, invalid query string», а команды выпуска
// токена в этой ветке нет (стенд KN-1810, 18.11.2026, F807). Тот же RCI
// доступен через SCGI-сокет ndm, за которым стоит nginx; параметры запроса —
// те, что nginx передаёт для /rci (/var/nginx/nginx.conf).
const (
	ndmSCGISocket    = "/var/run/ndm.scgi.socket"
	loopbackClosedXD = "0x1867"
)

// loopbackFallback ходит в RCI по HTTP, а после первого 403 0x1867 —
// через SCGI-сокет до конца жизни процесса.
// ponytail: переключение одностороннее; прошивка меняется только с ребутом,
// а он перезапускает демон.
type loopbackFallback struct {
	base    http.RoundTripper
	scgi    http.RoundTripper
	useSCGI atomic.Bool
}

func (t *loopbackFallback) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.useSCGI.Load() {
		return t.scgi.RoundTrip(req)
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusForbidden ||
		!strings.Contains(resp.Header.Get("X-Detail"), loopbackClosedXD) {
		return resp, err
	}
	t.useSCGI.Store(true)
	// Тело уже вычитано и переиграть его нечем — отдаём 403, следующий
	// запрос пойдёт через сокет.
	if req.Body != nil && req.GetBody == nil {
		return resp, nil
	}
	retry := req.Clone(req.Context())
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return resp, nil
		}
		retry.Body = body
	}
	resp.Body.Close()
	return t.scgi.RoundTrip(retry)
}

// scgiTransport — RCI-запрос в SCGI-сокет ndm, ответ в CGI-форме
// («Status: 200 OK», заголовки, пустая строка, тело).
type scgiTransport struct{ path string }

func (s scgiTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		body = b
	}

	var d net.Dialer
	conn, err := d.DialContext(req.Context(), "unix", s.path)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(req.Context(), func() { conn.Close() })
	closeAll := func() { stop(); conn.Close() }

	if _, err := conn.Write(scgiRequest(req, body)); err != nil {
		closeAll()
		return nil, err
	}

	br := bufio.NewReader(conn)
	hdr, err := textproto.NewReader(br).ReadMIMEHeader()
	if err != nil {
		closeAll()
		return nil, fmt.Errorf("scgi: read header: %w", err)
	}
	status := hdr.Get("Status")
	code, err := strconv.Atoi(strings.Fields(status + " x")[0])
	if err != nil {
		closeAll()
		return nil, fmt.Errorf("scgi: bad status %q", status)
	}
	hdr.Del("Status")
	contentLength := int64(-1)
	if n, err := strconv.ParseInt(hdr.Get("Content-Length"), 10, 64); err == nil {
		contentLength = n
	}
	return &http.Response{
		Status:        status,
		StatusCode:    code,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header(hdr),
		Body:          &scgiBody{Reader: br, close: closeAll},
		ContentLength: contentLength,
		Request:       req,
	}, nil
}

type scgiBody struct {
	io.Reader
	close func()
}

func (b *scgiBody) Close() error { b.close(); return nil }

// scgiRequest собирает netstring заголовков (CONTENT_LENGTH первым — так
// требует SCGI) и тело.
func scgiRequest(req *http.Request, body []byte) []byte {
	var h bytes.Buffer
	for _, kv := range [][2]string{
		{"CONTENT_LENGTH", strconv.Itoa(len(body))},
		{"SCGI", "1"},
		{"REQUEST_METHOD", req.Method},
		{"REQUEST_URI", req.URL.RequestURI()},
		{"NORMALIZED_URI", req.URL.Path},
		{"QUERY_STRING", req.URL.RawQuery},
		{"CONTENT_TYPE", req.Header.Get("Content-Type")},
		{"SERVER_PROTOCOL", "HTTP/1.1"},
		{"REMOTE_ADDR", "127.0.0.1"},
		{"SERVER_ADDR", "127.0.0.1"},
		{"SERVER_PORT", "79"},
		{"HTTP_HOST", req.URL.Host},
		{"REMOTE_IDENT", req.Header.Get("User-Agent")},
		{"SCGI_AGENT", "http/rci"},
		{"AUTH_ALLOWED", "1"},
	} {
		h.WriteString(kv[0])
		h.WriteByte(0)
		h.WriteString(kv[1])
		h.WriteByte(0)
	}
	var out bytes.Buffer
	fmt.Fprintf(&out, "%d:", h.Len())
	out.Write(h.Bytes())
	out.WriteByte(',')
	out.Write(body)
	return out.Bytes()
}

var rciTransport http.RoundTripper = &loopbackFallback{
	base: baseTransport,
	scgi: scgiTransport{path: ndmSCGISocket},
}
