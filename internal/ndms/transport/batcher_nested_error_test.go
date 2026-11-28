package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Ответы сняты со стенда 5.02.B.5 (28.11.2026): отказ лежит на месте
// отсутствующего узла.
func TestBatchNestedStatusErrorIsReported(t *testing.T) {
	answers := map[string]string{
		"/show/rc/sstp-server":   `{"show":{"rc":{"status":[{"status":"error","code":"1179781","message":"not found: \"show/rc/sstp-server\""}]}}}`,
		"/show/rc/nosuch/deeper": `{"show":{"rc":{"nosuch":{"status":[{"status":"error","message":"not found: \"show/rc/nosuch/deeper\""}]}}}}`,
		"/show/rc/broken":        `{"show":{"rc":{"status":[{"status":"error","message":"argument parse error"}]}}}`,
		"/show/version":          `{"show":{"version":{"release":"5.02"}}}`,
	}
	paths := []string{"/show/rc/sstp-server", "/show/rc/nosuch/deeper", "/show/rc/broken", "/show/version"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var cmds []map[string]any
		if err := json.NewDecoder(r.Body).Decode(&cmds); err != nil {
			t.Errorf("ожидался батч: %v", err)
			return
		}
		out := "["
		for i, cmd := range cmds {
			raw, _ := json.Marshal(cmd)
			for _, p := range paths {
				want, _, _ := pathToCommand(p)
				if w, _ := json.Marshal(want); string(w) == string(raw) {
					if i > 0 {
						out += ","
					}
					out += answers[p]
				}
			}
		}
		_, _ = w.Write([]byte(out + "]"))
	}))
	defer srv.Close()

	c := NewWithURL(srv.URL, NewSemaphore(4))
	c.batcher = newBatcher(c, 200*time.Millisecond, len(paths), 8)
	c.batcher.Start()
	defer c.Close()

	bodies, errs := make([][]byte, len(paths)), make([]error, len(paths))
	var wg sync.WaitGroup
	for i, p := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bodies[i], errs[i] = c.GetRaw(context.Background(), p)
		}()
	}
	wg.Wait()

	for i := range 2 {
		var httpErr *HTTPError
		if !errors.As(errs[i], &httpErr) || httpErr.Status != http.StatusNotFound {
			t.Errorf("%s: err = %v, want HTTP 404", paths[i], errs[i])
		}
	}
	var appErr *NDMSAppError
	if !errors.As(errs[2], &appErr) || appErr.Message != "argument parse error" {
		t.Errorf("broken: err = %v, want NDMSAppError", errs[2])
	}
	if errs[3] != nil || string(bodies[3]) != `{"release":"5.02"}` {
		t.Errorf("соседнее чтение: %s, %v", bodies[3], errs[3])
	}
}
