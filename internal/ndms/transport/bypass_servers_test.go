package transport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Отсутствующий раздел обязан приходить ошибкой и тогда, когда в то же окно
// батчера попало чужое чтение.
func TestAbsentServerSectionStaysNotFoundUnderBatching(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			// Чужое чтение батчер шлёт POST'ом; раздела сервера там быть не должно.
			if body, _ := io.ReadAll(r.Body); strings.Contains(string(body), "sstp-server") {
				t.Error("раздел сервера ушёл в батч")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(`[{}]`))
		case r.URL.Path == "/show/rc/sstp-server":
			w.WriteHeader(http.StatusNotFound)
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	c := NewWithURL(srv.URL, NewSemaphore(4))
	c.batcher = newBatcher(c, 50*time.Millisecond, 8, 8)
	c.batcher.Start()
	defer c.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = c.GetRaw(context.Background(), "/show/version")
	}()
	_, err := c.GetRaw(context.Background(), "/show/rc/sstp-server")
	wg.Wait()

	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusNotFound {
		t.Fatalf("err = %v, want HTTP 404", err)
	}
}
