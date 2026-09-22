package api

import (
	"encoding/json"
	"github.com/hoaxisr/awg-manager/internal/sys/appver"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func geositeTreeJSON(truncated bool, paths ...string) string {
	type entry struct {
		Path string `json:"path"`
	}
	entries := make([]entry, 0, len(paths))
	for _, p := range paths {
		entries = append(entries, entry{Path: p})
	}
	b, _ := json.Marshal(map[string]any{"truncated": truncated, "tree": entries})
	return string(b)
}

func newGeositesTestHandler(t *testing.T, upstream http.HandlerFunc) (*SingboxGeositesHandler, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		upstream(w, r)
	}))
	t.Cleanup(srv.Close)
	h := NewSingboxGeositesHandler(nil)
	h.treeURL = srv.URL
	return h, &calls
}

// rewindGeositeAttempt отматывает дебаунс/кулдаун, чтобы тест мог сразу
// провоцировать следующий фетч.
func rewindGeositeAttempt(h *SingboxGeositesHandler, back time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastAttempt = h.lastAttempt.Add(-back)
}

func geositesList(t *testing.T, h *SingboxGeositesHandler, url string) (*httptest.ResponseRecorder, SingboxGeositesData) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, url, nil))
	var envelope struct {
		Success bool                `json:"success"`
		Data    SingboxGeositesData `json:"data"`
	}
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode response: %v", err)
		}
	}
	return rec, envelope.Data
}

// Парсинг листинга: geosite-*.srs → имена без префикса/суффикса,
// посторонние файлы отбрасываются, порядок — сортированный.
func TestGeositesList_ParsesAndSorts(t *testing.T) {
	h, calls := newGeositesTestHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(geositeTreeJSON(false,
			"geosite-youtube.srs",
			"geosite-category-ads-all.srs",
			"README.md",
			"geoip-cn.srs",
			"geosite-xiaomi@cn.srs",
			"geosite-.srs",
		)))
	})

	rec, data := geositesList(t, h, "/api/singbox/router/geosites/list")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	want := []string{"category-ads-all", "xiaomi@cn", "youtube"}
	if len(data.Names) != len(want) {
		t.Fatalf("names = %v, want %v", data.Names, want)
	}
	for i, n := range want {
		if data.Names[i] != n {
			t.Fatalf("names = %v, want %v", data.Names, want)
		}
	}
	if data.BaseURL == "" || data.FetchedAt == "" {
		t.Errorf("baseUrl/fetchedAt must be set, got %+v", data)
	}
	if data.Stale {
		t.Error("fresh fetch must not be stale")
	}
	if calls.Load() != 1 {
		t.Errorf("expected 1 upstream call, got %d", calls.Load())
	}

	// Повторный запрос — из кэша, без нового обращения к GitHub.
	if rec2, _ := geositesList(t, h, "/api/singbox/router/geosites/list"); rec2.Code != http.StatusOK {
		t.Fatalf("cached status %d", rec2.Code)
	}
	if calls.Load() != 1 {
		t.Errorf("cached call must not hit upstream, got %d calls", calls.Load())
	}

	// refresh=1 сразу после удачного фетча гасится дебаунсом…
	if rec3, _ := geositesList(t, h, "/api/singbox/router/geosites/list?refresh=1"); rec3.Code != http.StatusOK {
		t.Fatalf("debounced refresh status %d", rec3.Code)
	}
	if calls.Load() != 1 {
		t.Errorf("debounced refresh must not hit upstream, got %d calls", calls.Load())
	}

	// …а после паузы — форсирует перезапрос.
	rewindGeositeAttempt(h, geositeAttemptDebounce+time.Second)
	if rec4, _ := geositesList(t, h, "/api/singbox/router/geosites/list?refresh=1"); rec4.Code != http.StatusOK {
		t.Fatalf("refresh status %d", rec4.Code)
	}
	if calls.Load() != 2 {
		t.Errorf("refresh must hit upstream, got %d calls", calls.Load())
	}
}

// Сбой апстрима без кэша — 502; с кэшем — протухший список со stale=true.
// Кулдаун после неудачи не пускает следующий запрос к апстриму.
func TestGeositesList_UpstreamFailure(t *testing.T) {
	fail := true
	h, calls := newGeositesTestHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
			return
		}
		_, _ = w.Write([]byte(geositeTreeJSON(false, "geosite-google.srs")))
	})

	rec, _ := geositesList(t, h, "/api/singbox/router/geosites/list")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("no-cache failure: status %d, want 502", rec.Code)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 upstream call, got %d", calls.Load())
	}

	// Кулдаун: немедленный повтор не жжёт rate-limit, ошибка отдаётся из кэша.
	if rec2, _ := geositesList(t, h, "/api/singbox/router/geosites/list"); rec2.Code != http.StatusBadGateway {
		t.Fatalf("cooldown failure: status %d, want 502", rec2.Code)
	}
	if calls.Load() != 1 {
		t.Fatalf("cooldown must not hit upstream, got %d calls", calls.Load())
	}

	// Наполняем кэш (после паузы), затем ломаем апстрим и форсируем refresh —
	// кэш выживает и помечается stale.
	fail = false
	rewindGeositeAttempt(h, geositeFailCooldown+time.Second)
	if rec3, data := geositesList(t, h, "/api/singbox/router/geosites/list"); rec3.Code != http.StatusOK || data.Stale {
		t.Fatalf("prime cache: status %d stale %v", rec3.Code, data.Stale)
	}
	fail = true
	rewindGeositeAttempt(h, geositeAttemptDebounce+time.Second)
	rec4, data := geositesList(t, h, "/api/singbox/router/geosites/list?refresh=1")
	if rec4.Code != http.StatusOK {
		t.Fatalf("stale-cache fallback: status %d, want 200", rec4.Code)
	}
	if len(data.Names) != 1 || data.Names[0] != "google" {
		t.Errorf("stale names = %v", data.Names)
	}
	if !data.Stale {
		t.Error("failed refresh must mark the payload stale")
	}
}

// Пустой листинг и truncated-листинг — ошибка, а не пустой/частичный кэш.
func TestGeositesList_BadListingsAreErrors(t *testing.T) {
	payload := geositeTreeJSON(false, "README.md")
	h, _ := newGeositesTestHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(payload))
	})
	if rec, _ := geositesList(t, h, "/api/singbox/router/geosites/list"); rec.Code != http.StatusBadGateway {
		t.Fatalf("empty listing: status %d, want 502", rec.Code)
	}

	payload = geositeTreeJSON(true, "geosite-google.srs")
	rewindGeositeAttempt(h, geositeFailCooldown+time.Second)
	if rec, _ := geositesList(t, h, "/api/singbox/router/geosites/list"); rec.Code != http.StatusBadGateway {
		t.Fatalf("truncated listing: status %d, want 502", rec.Code)
	}
}

func TestGeositesList_MethodNotAllowed(t *testing.T) {
	h := NewSingboxGeositesHandler(nil)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodPost, "/api/singbox/router/geosites/list", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rec.Code)
	}
}

// TTL: протухший кэш обновляется без refresh=1.
func TestGeositesList_TTLExpiry(t *testing.T) {
	h, calls := newGeositesTestHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(geositeTreeJSON(false, "geosite-google.srs")))
	})
	if rec, _ := geositesList(t, h, "/api/singbox/router/geosites/list"); rec.Code != http.StatusOK {
		t.Fatal("prime")
	}
	h.mu.Lock()
	h.fetchedAt = time.Now().Add(-geositeCacheTTL - time.Minute)
	h.lastAttempt = time.Now().Add(-geositeCacheTTL - time.Minute)
	h.mu.Unlock()
	if rec, _ := geositesList(t, h, "/api/singbox/router/geosites/list"); rec.Code != http.StatusOK {
		t.Fatal("post-ttl")
	}
	if calls.Load() != 2 {
		t.Errorf("expired cache must re-fetch, got %d calls", calls.Load())
	}
}

// Warm-cache чтение не блокируется чужим долгим фетчем: пока refresh висит
// в апстриме, обычный запрос немедленно получает stale-копию.
func TestGeositesList_StaleWhileRevalidate(t *testing.T) {
	release := make(chan struct{})
	slow := false
	h, _ := newGeositesTestHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		if slow {
			<-release
		}
		_, _ = w.Write([]byte(geositeTreeJSON(false, "geosite-google.srs")))
	})
	if rec, _ := geositesList(t, h, "/api/singbox/router/geosites/list"); rec.Code != http.StatusOK {
		t.Fatal("prime")
	}

	slow = true
	// Протухший кэш: обычный запрос не пройдёт по fresh-пути и упрётся в
	// inflight-развилку — ровно её и проверяем.
	h.mu.Lock()
	h.fetchedAt = time.Now().Add(-geositeCacheTTL - time.Minute)
	h.mu.Unlock()
	rewindGeositeAttempt(h, geositeAttemptDebounce+time.Second)
	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		_, _ = geositesList(t, h, "/api/singbox/router/geosites/list?refresh=1")
	}()

	// Дожидаемся, пока refresh займёт inflight-слот.
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		busy := h.inflight != nil
		h.mu.Unlock()
		if busy {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("refresh never started")
		}
		time.Sleep(5 * time.Millisecond)
	}

	done := make(chan SingboxGeositesData, 1)
	go func() {
		_, data := geositesList(t, h, "/api/singbox/router/geosites/list")
		done <- data
	}()
	select {
	case data := <-done:
		if len(data.Names) != 1 || !data.Stale {
			t.Errorf("expected stale cached copy, got %+v", data)
		}
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("warm-cache read blocked behind the in-flight fetch")
	}
	close(release)
	<-refreshDone
}

// Каталог берётся с GitHub API: запрос обязан представляться версией панели,
// иначе API отвечает 403 на безымянные запросы.
func TestGeositesList_SendsAppUserAgent(t *testing.T) {
	seen := make(chan string, 1)
	h, _ := newGeositesTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(geositeTreeJSON(false, "geosite-ru.srs")))
	})
	geositesList(t, h, "/api/singbox/router/geosites/list")
	if got := <-seen; got != appver.UA() {
		t.Errorf("User-Agent = %q, want %q", got, appver.UA())
	}
}
