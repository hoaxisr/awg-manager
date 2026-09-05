package transport

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// RT7: фастпат — ПРОД-ПУТЬ каждого одиночного чтения RCI, и до сих пор он не
// исполнялся ни одним тестом.
//
// Причина была записана прямо в коде: «Tests его не вызывают, чтобы остаться
// на POST batch (mock handlers только POST поддерживают)». То есть удобство
// фикстуры определяло, какой путь проверяется, — а проверялся при этом не тот,
// которым ходит продукт. Мутация «фастпат не ходит в роутер и отдаёт пустое
// тело» оставляла весь `./internal/ndms/...` зелёным.
func newFastPathBatcher(t *testing.T, handler http.HandlerFunc, window time.Duration) (*Batcher, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	cli := NewWithURL(srv.URL+"/", NewSemaphore(30))
	b := newBatcher(cli, window, 64, 256)
	b.EnableFastPath() // как в прод-конструкторе transport.New()
	b.Start()
	return b, func() { b.Close(); srv.Close() }
}

func TestFastPath_SinglePathGoesDirectGET(t *testing.T) {
	var mu sync.Mutex
	var methods, paths []string
	b, done := newFastPathBatcher(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"release":"5.01"}`))
	}, 5*time.Millisecond)
	defer done()

	body, err := b.Submit(context.Background(), "show/version")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Тело доезжает до вызывающего дословно: фастпат ничего не оборачивает.
	if got := strings.TrimSpace(string(body)); got != `{"release":"5.01"}` {
		t.Fatalf("тело %q — не то, что отдал роутер", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(methods) != 1 || methods[0] != http.MethodGet {
		t.Fatalf("методы %v — одиночное чтение обязано идти GET-ом, а не POST", methods)
	}
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "/show/version") {
		t.Fatalf("пути %v — запрошен не тот ресурс", paths)
	}
}

// Схлопывание на фастпате: N вызывающих одного пути обязаны получить ОДИН
// поход в роутер и каждый — свой ответ. Иначе смысл батчера теряется именно
// там, где он чаще всего работает.
func TestFastPath_CoalescesCallersOfSamePath(t *testing.T) {
	var mu sync.Mutex
	gets := 0
	b, done := newFastPathBatcher(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gets++
		mu.Unlock()
		time.Sleep(5 * time.Millisecond) // окно, в котором копятся остальные
		_, _ = w.Write([]byte(`{"ok":true}`))
	}, 20*time.Millisecond)
	defer done()

	const callers = 5
	var wg sync.WaitGroup
	errs := make([]error, callers)
	bodies := make([][]byte, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bodies[i], errs[i] = b.Submit(context.Background(), "show/interface")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("вызывающий %d получил отказ: %v", i, err)
		}
		if string(bodies[i]) != `{"ok":true}` {
			t.Fatalf("вызывающий %d получил %q", i, bodies[i])
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if gets != 1 {
		t.Fatalf("походов в роутер %d, ждали 1: пятеро спрашивали один путь", gets)
	}
}

// Отказ уровня приложения приходит В ТЕЛЕ ответа с кодом 200 — на фастпате
// его обязан распознать ExtractError, иначе вызывающий примет текст ошибки за
// данные.
func TestFastPath_AppErrorInBodyBecomesError(t *testing.T) {
	b, done := newFastPathBatcher(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","message":"нет такой команды"}`))
	}, 5*time.Millisecond)
	defer done()

	if _, err := b.Submit(context.Background(), "show/nope"); err == nil {
		t.Fatal("отказ в теле ответа обязан стать ошибкой, а не данными")
	} else if !strings.Contains(err.Error(), "нет такой команды") {
		t.Fatalf("причина отказа потеряна: %v", err)
	}
}

// Несколько РАЗНЫХ путей фастпату не отдаются — они идут батчем через POST.
func TestFastPath_MultiplePathsStillGoBatch(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	b, done := newFastPathBatcher(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`[{"a":1},{"b":2}]`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}, 30*time.Millisecond)
	defer done()

	var wg sync.WaitGroup
	for _, p := range []string{"show/version", "show/interface"} {
		wg.Add(1)
		go func(p string) { defer wg.Done(); _, _ = b.Submit(context.Background(), p) }(p)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	for _, m := range methods {
		if m == http.MethodGet {
			t.Fatalf("разные пути ушли GET-ом поштучно: %v", methods)
		}
	}
}

// Распаковка обёртки batch-ответа: NDMS заворачивает элемент в дерево путей,
// а вызывающий обязан получить тот же shape, что дал бы прямой GET. Без
// распаковки он получил бы обёртку — и «ноль интерфейсов» при живом роутере.
func TestBatch_UnwrapsPathTreeToInnerValue(t *testing.T) {
	// Отвечаем ПО СОСТАВУ запроса, а не по позиции: порядок, в котором
	// горутины успели подать пути, недетерминирован, и ответ «по индексу»
	// делал бы тест то зелёным, то красным без причины.
	b, done := newTestBatcher(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "want POST", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var batch []map[string]any
		if err := json.Unmarshal(body, &batch); err != nil {
			http.Error(w, "bad batch", http.StatusBadRequest)
			return
		}
		out := make([]string, 0, len(batch))
		for _, item := range batch {
			if show, ok := item["show"].(map[string]any); ok {
				if _, ok := show["version"]; ok {
					out = append(out, `{"show":{"version":{"release":"5.01"}}}`)
					continue
				}
				if _, ok := show["system"]; ok {
					out = append(out, `{"show":{"system":{"uptime":"42"}}}`)
					continue
				}
			}
			out = append(out, `{}`)
		}
		_, _ = w.Write([]byte("[" + strings.Join(out, ",") + "]"))
	}, 30*time.Millisecond)
	defer done()

	var wg sync.WaitGroup
	var mu sync.Mutex
	got := map[string]string{}
	for _, p := range []string{"show/version", "show/system"} {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			body, err := b.Submit(context.Background(), p)
			if err != nil {
				t.Errorf("%s: %v", p, err)
				return
			}
			mu.Lock()
			got[p] = strings.TrimSpace(string(body))
			mu.Unlock()
		}(p)
	}
	wg.Wait()

	if got["show/version"] != `{"release":"5.01"}` {
		t.Errorf("show/version отдан обёрнутым: %s", got["show/version"])
	}
	if got["show/system"] != `{"uptime":"42"}` {
		t.Errorf("show/system отдан обёрнутым: %s", got["show/system"])
	}
}
