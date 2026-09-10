package amneziacp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Фикстуры: только домены .test (RFC 2606) — репозиторий публичный.
const (
	fixtureOriginA = "https://k7m2q9.example.test"
	fixtureOriginB = "https://x3p8t5.example.test"
	fixtureOriginC = "https://z6w1r4.example.test"
)

// testTTL намеренно не равен DefaultMirrorTTL: реализация, забывшая про
// настраиваемый TTL и взявшая дефолт, обязана быть видна.
const testTTL = 7 * time.Minute

func mirrorPage(dataLink string) string {
	return `<!doctype html><html><head><meta charset="utf-8">` +
		`<meta name="mirror-to" data-link="` + dataLink + `">` +
		`<title>cp</title></head><body>ok</body></html>`
}

// fakeClock — подменяемые часы: тест двигает время сам, без time.Sleep.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)}
}

func TestParseMirrorTo(t *testing.T) {
	cases := []struct {
		name string
		html string
		want string
	}{
		{
			name: "порядок name→data-link",
			html: mirrorPage(fixtureOriginA),
			want: fixtureOriginA,
		},
		{
			name: "обратный порядок атрибутов",
			html: `<head><meta data-link="` + fixtureOriginB + `" name="mirror-to"></head>`,
			want: fixtureOriginB,
		},
		{
			name: "одинарные кавычки",
			html: `<meta name='mirror-to' data-link='` + fixtureOriginC + `'>`,
			want: fixtureOriginC,
		},
		{
			name: "лишние атрибуты и перевод строки внутри тега",
			html: "<meta\n  charset=\"utf-8\">\n<meta\n  id=\"m\"\n  name=\"mirror-to\"\n  data-link=\"" + fixtureOriginA + "\"\n  content=\"x\">",
			want: fixtureOriginA,
		},
		{
			name: "верхний регистр тега и атрибутов",
			html: `<META NAME="MIRROR-TO" DATA-LINK="` + fixtureOriginB + `">`,
			want: fixtureOriginB,
		},
		{
			name: "хвостовой слэш срезается",
			html: mirrorPage(fixtureOriginA + "/"),
			want: fixtureOriginA,
		},
		{
			name: "пробелы вокруг адреса срезаются",
			html: mirrorPage("  " + fixtureOriginC + "  "),
			want: fixtureOriginC,
		},
		{
			name: "нужный meta не первый",
			html: `<meta name="viewport" content="width=device-width"><meta name="mirror-to" data-link="` + fixtureOriginA + `">`,
			want: fixtureOriginA,
		},
		{name: "тега нет вовсе", html: `<html><head><title>cp</title></head></html>`},
		{name: "meta есть, data-link нет", html: `<meta name="mirror-to" content="` + fixtureOriginA + `">`},
		{name: "пустой data-link", html: mirrorPage("")},
		{name: "http вместо https", html: mirrorPage("http://k7m2q9.example.test")},
		{name: "https без хоста", html: mirrorPage("https://")},
		{name: "относительный адрес", html: mirrorPage("/cp/ru")},
		{name: "адрес без схемы", html: mirrorPage("k7m2q9.example.test")},
		{name: "пустой документ", html: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseMirrorTo([]byte(tc.html))
			if tc.want == "" {
				if err == nil {
					t.Fatalf("ожидалась ошибка, получен origin %q", got)
				}
				if got != "" {
					t.Fatalf("при ошибке origin обязан быть пустым, получен %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("неожиданная ошибка: %v", err)
			}
			if got != tc.want {
				t.Fatalf("origin = %q, ожидался %q", got, tc.want)
			}
		})
	}
}

// mirrorServer отдаёт на каждый запрос свой origin из origins по порядку,
// чтобы «сходил заново, но вернул старое» было видно отдельно от «не сходил».
func mirrorServer(t *testing.T, hits *atomic.Int64, origins ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(hits.Add(1)) - 1
		if n >= len(origins) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, mirrorPage(origins[n]))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestMirrorOriginCachesUntilTTLExpires(t *testing.T) {
	var hits atomic.Int64
	srv := mirrorServer(t, &hits, fixtureOriginA, fixtureOriginB)
	clock := newFakeClock()
	m := newMirrorWithClock(srv.Client(), testTTL, clock.now)

	got, err := m.Origin(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("первый резолв: %v", err)
	}
	if got != fixtureOriginA {
		t.Fatalf("первый origin = %q, ожидался %q", got, fixtureOriginA)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("после первого резолва походов %d, ожидался 1", n)
	}

	clock.advance(testTTL - time.Nanosecond)
	got, err = m.Origin(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("резолв внутри TTL: %v", err)
	}
	if got != fixtureOriginA {
		t.Fatalf("внутри TTL origin = %q, ожидался кэшированный %q", got, fixtureOriginA)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("внутри TTL походов %d, ожидался 1 (кэш не сработал)", n)
	}

	clock.advance(time.Nanosecond)
	got, err = m.Origin(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("резолв после TTL: %v", err)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("после TTL походов %d, ожидалось 2 (кэш не протух)", n)
	}
	if got != fixtureOriginB {
		t.Fatalf("после TTL origin = %q, ожидался свежий %q", got, fixtureOriginB)
	}
}

func TestMirrorOriginDefaultTTL(t *testing.T) {
	var hits atomic.Int64
	srv := mirrorServer(t, &hits, fixtureOriginA, fixtureOriginB)
	clock := newFakeClock()
	m := newMirrorWithClock(srv.Client(), 0, clock.now)

	if _, err := m.Origin(context.Background(), srv.URL); err != nil {
		t.Fatalf("первый резолв: %v", err)
	}
	clock.advance(DefaultMirrorTTL - time.Minute)
	if _, err := m.Origin(context.Background(), srv.URL); err != nil {
		t.Fatalf("резолв внутри дефолтного TTL: %v", err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("внутри дефолтного TTL походов %d, ожидался 1", n)
	}
	clock.advance(time.Minute)
	if _, err := m.Origin(context.Background(), srv.URL); err != nil {
		t.Fatalf("резолв после дефолтного TTL: %v", err)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("после дефолтного TTL походов %d, ожидалось 2", n)
	}
}

func TestMirrorOriginKeyedByMirrorURL(t *testing.T) {
	var hitsA, hitsB atomic.Int64
	srvA := mirrorServer(t, &hitsA, fixtureOriginA)
	srvB := mirrorServer(t, &hitsB, fixtureOriginB)
	clock := newFakeClock()
	m := newMirrorWithClock(srvA.Client(), testTTL, clock.now)

	gotA, err := m.Origin(context.Background(), srvA.URL)
	if err != nil {
		t.Fatalf("зеркало A: %v", err)
	}
	if gotA != fixtureOriginA {
		t.Fatalf("зеркало A дало %q, ожидался %q", gotA, fixtureOriginA)
	}

	// Смена адреса зеркала обязана промахнуться мимо кэша сама, без Invalidate.
	gotB, err := m.Origin(context.Background(), srvB.URL)
	if err != nil {
		t.Fatalf("зеркало B: %v", err)
	}
	if gotB != fixtureOriginB {
		t.Fatalf("зеркало B дало %q, ожидался %q (кэш не привязан к адресу)", gotB, fixtureOriginB)
	}
	if n := hitsB.Load(); n != 1 {
		t.Fatalf("походов к зеркалу B: %d, ожидался 1", n)
	}
}

// recordingBody считает Read'ы: свойство «статус проверяется до чтения тела»
// иначе проверить нечем — по возвращённой ошибке оба порядка неразличимы.
type recordingBody struct {
	r      io.Reader
	reads  *atomic.Int64
	closed *atomic.Bool
}

func (b *recordingBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	return b.r.Read(p)
}

func (b *recordingBody) Close() error {
	b.closed.Store(true)
	return nil
}

type stubTransport struct {
	fn func(*http.Request) *http.Response
}

func (t stubTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.fn(r), nil
}

func TestMirrorOriginChecksStatusBeforeReadingBody(t *testing.T) {
	var reads atomic.Int64
	var closed atomic.Bool
	// Тело валидное: реализация, которая сначала читает и парсит, а статус
	// смотрит только «если ничего не нашлось», отдаст этот origin наружу.
	body := mirrorPage(fixtureOriginA)
	client := &http.Client{Transport: stubTransport{fn: func(r *http.Request) *http.Response {
		return &http.Response{
			StatusCode:    http.StatusServiceUnavailable,
			Status:        "503 Service Unavailable",
			Header:        make(http.Header),
			ContentLength: int64(len(body)),
			Body: &recordingBody{
				r:      strings.NewReader(body),
				reads:  &reads,
				closed: &closed,
			},
			Request: r,
		}
	}}}

	m := newMirrorWithClock(client, testTTL, newFakeClock().now)
	got, err := m.Origin(context.Background(), "https://mirror-503.example.test/cp")
	if err == nil {
		t.Fatalf("503 с валидным мета-тегом обязан быть ошибкой, получен origin %q", got)
	}
	if got != "" {
		t.Fatalf("при 503 origin обязан быть пустым, получен %q", got)
	}
	if !errors.Is(err, ErrMirrorUnavailable) {
		t.Fatalf("ошибка не различима сентинелом: %v", err)
	}
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("в ошибке нет кода ответа (свойство наблюдаемости): %v", err)
	}
	if n := reads.Load(); n != 0 {
		t.Fatalf("тело прочитано %d раз до проверки статуса", n)
	}
	if !closed.Load() {
		t.Fatal("тело ответа не закрыто")
	}
}

func TestMirrorOriginDoesNotCacheFailure(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, mirrorPage(fixtureOriginC))
	}))
	defer srv.Close()

	clock := newFakeClock()
	m := newMirrorWithClock(srv.Client(), testTTL, clock.now)

	if got, err := m.Origin(context.Background(), srv.URL); err == nil {
		t.Fatalf("502 обязан быть ошибкой, получен origin %q", got)
	}

	// Время не двигаем: неудача не имеет права занять кэш на весь TTL.
	got, err := m.Origin(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("повтор после неудачи: %v", err)
	}
	if got != fixtureOriginC {
		t.Fatalf("повтор дал origin %q, ожидался %q (закэширована неудача)", got, fixtureOriginC)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("походов %d, ожидалось 2", n)
	}
}

func TestMirrorOriginNoMetaTagIsError(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `<html><head><title>cp</title></head><body>hi</body></html>`)
	}))
	defer srv.Close()

	m := newMirrorWithClock(srv.Client(), testTTL, newFakeClock().now)
	got, err := m.Origin(context.Background(), srv.URL)
	if err == nil {
		t.Fatalf("страница без мета-тега обязана быть ошибкой, получен origin %q", got)
	}
	if !errors.Is(err, ErrMirrorUnavailable) {
		t.Fatalf("ошибка не различима сентинелом: %v", err)
	}
}

func TestMirrorOriginRejectsEmptyMirrorURL(t *testing.T) {
	var calls atomic.Int64
	client := &http.Client{Transport: stubTransport{fn: func(r *http.Request) *http.Response {
		calls.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody, Request: r}
	}}}

	m := newMirrorWithClock(client, testTTL, newFakeClock().now)
	if got, err := m.Origin(context.Background(), "   "); err == nil {
		t.Fatalf("пустой адрес зеркала обязан быть ошибкой, получен origin %q", got)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("при пустом адресе зеркала сделано %d запросов", n)
	}
}

func TestMirrorInvalidateDropsCache(t *testing.T) {
	var hits atomic.Int64
	srv := mirrorServer(t, &hits, fixtureOriginA, fixtureOriginB)
	clock := newFakeClock()
	m := newMirrorWithClock(srv.Client(), testTTL, clock.now)

	if _, err := m.Origin(context.Background(), srv.URL); err != nil {
		t.Fatalf("первый резолв: %v", err)
	}
	m.Invalidate()

	got, err := m.Origin(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("резолв после Invalidate: %v", err)
	}
	if got != fixtureOriginB || hits.Load() != 2 {
		t.Fatalf("после Invalidate origin=%q походов=%d, ожидались %q и 2", got, hits.Load(), fixtureOriginB)
	}
}

// Инвалидация приходит, пока запрос ещё летит: origin, который резолв уже
// добыл, мёртв, и записывать его в кэш на весь TTL нельзя. Детерминированно:
// Invalidate зовём из хендлера сервера — он выполняется строго внутри резолва.
func TestMirrorInvalidateDuringResolveIsNotCached(t *testing.T) {
	var hits atomic.Int64
	var m *Mirror
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			m.Invalidate()
			_, _ = io.WriteString(w, mirrorPage(fixtureOriginA))
			return
		}
		_, _ = io.WriteString(w, mirrorPage(fixtureOriginB))
	}))
	defer srv.Close()

	clock := newFakeClock()
	m = newMirrorWithClock(srv.Client(), testTTL, clock.now)

	got, err := m.Origin(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("резолв с инвалидацией на лету: %v", err)
	}
	if got != fixtureOriginA {
		t.Fatalf("резолв вернул %q, ожидался только что добытый %q", got, fixtureOriginA)
	}

	// Время не двигаем: если результат осел в кэше, поход будет один.
	got, err = m.Origin(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("резолв после инвалидации: %v", err)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("походов %d, ожидалось 2 — мёртвый origin воскрес из кэша", n)
	}
	if got != fixtureOriginB {
		t.Fatalf("origin = %q, ожидался %q", got, fixtureOriginB)
	}
}

func TestMirrorOriginConcurrent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, mirrorPage(fixtureOriginA))
	}))
	defer srv.Close()

	m := NewMirror(srv.Client(), testTTL)
	var wg sync.WaitGroup
	for i := range 24 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%6 == 5 {
				m.Invalidate()
				return
			}
			got, err := m.Origin(context.Background(), srv.URL)
			if err != nil {
				t.Errorf("резолв %d: %v", i, err)
				return
			}
			if got != fixtureOriginA {
				t.Errorf("резолв %d дал %q, ожидался %q", i, got, fixtureOriginA)
			}
		}(i)
	}
	wg.Wait()
}
