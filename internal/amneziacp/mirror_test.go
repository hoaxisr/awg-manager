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
	"testing/iotest"
	"time"
)

// Фикстуры: только домены .test (RFC 2606) — репозиторий публичный.
const (
	fixtureOriginA = "https://k7m2q9.example.test"
	fixtureOriginB = "https://x3p8t5.example.test"
	fixtureOriginC = "https://z6w1r4.example.test"
)

// realMirrorPage повторяет форму живой страницы зеркала (снята 2026-09-10,
// 881 байт): нужный тег не первый, а перед ним стоит тег-обманка
// original-url с само-закрывающимся `/>`. Адреса — .test, кроме
// cp.amnezia.org в обманке: фолбэк на этот адрес спека запрещает, поэтому
// data-link у обманки — та самая ссылка, которую резолвер обязан не взять.
const realMirrorPage = `<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width,initial-scale=1"/>
<meta name="description" content="Amnezia"/>
<meta name="original-url" data-link="https://cp.amnezia.org"/>
<meta name="mirror-to" data-link="` + fixtureOriginA + `">
<title>Amnezia</title>
</head>
<body><div id="app"></div></body>
</html>`

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
		name    string
		html    string
		want    string
		wantErr error // причина отказа; nil — случай положительный
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
			name: "хвостовые слэши отбрасываются",
			html: mirrorPage(fixtureOriginA + "//"),
			want: fixtureOriginA,
		},
		{
			name: "пробелы вокруг адреса срезаются",
			html: mirrorPage("  " + fixtureOriginC + "  "),
			want: fixtureOriginC,
		},
		{
			name: "верхний регистр схемы приводится к нижнему",
			html: mirrorPage("HTTPS://k7m2q9.example.test"),
			want: fixtureOriginA,
		},
		{
			name: "порт сохраняется",
			html: mirrorPage(fixtureOriginB + ":8443"),
			want: fixtureOriginB + ":8443",
		},
		{
			// Живая страница: тег-обманка original-url стоит ПЕРЕД нужным и
			// несёт cp.amnezia.org. Резолвер, сопоставляющий любой тег с
			// data-link, вернёт здесь запрещённый спекой адрес, а не origin.
			name: "живая страница зеркала с тегом-обманкой",
			html: realMirrorPage,
			want: fixtureOriginA,
		},
		{
			name: "нужный meta не первый",
			html: `<meta name="viewport" content="width=device-width"><meta name="mirror-to" data-link="` + fixtureOriginA + `">`,
			want: fixtureOriginA,
		},
		{
			// Дубль атрибута: как в HTML-парсере браузера, выигрывает первый.
			name: "дубль data-link — берётся первый",
			html: `<meta name="mirror-to" data-link="` + fixtureOriginA + `" data-link="` + fixtureOriginB + `">`,
			want: fixtureOriginA,
		},
		{
			// Два подходящих тега: берётся первый, а не последний.
			name: "два тега mirror-to — берётся первый",
			html: `<meta name="mirror-to" data-link="` + fixtureOriginA + `"><meta name="mirror-to" data-link="` + fixtureOriginB + `">`,
			want: fixtureOriginA,
		},
		{name: "тега нет вовсе", html: `<html><head><title>cp</title></head></html>`, wantErr: ErrNoMirrorTag},
		{name: "пустой документ", html: "", wantErr: ErrNoMirrorTag},
		// Сопоставление имени атрибута точное: браузер тег с пробелами внутри
		// значения name тоже не сопоставил бы. Строка держит это решение —
		// без неё возврат подрезки пробелов не заметил бы ни один тест.
		{name: "пробелы внутри значения name", html: `<meta name=" mirror-to " data-link="` + fixtureOriginA + `">`, wantErr: ErrNoMirrorTag},
		{name: "meta есть, data-link нет", html: `<meta name="mirror-to" content="` + fixtureOriginA + `">`, wantErr: ErrBadMirrorLink},
		{name: "пустой data-link", html: mirrorPage(""), wantErr: ErrBadMirrorLink},
		{name: "http вместо https", html: mirrorPage("http://k7m2q9.example.test"), wantErr: ErrBadMirrorLink},
		{name: "https без хоста", html: mirrorPage("https://"), wantErr: ErrBadMirrorLink},
		{name: "относительный адрес", html: mirrorPage("/cp/ru"), wantErr: ErrBadMirrorLink},
		{name: "адрес без схемы", html: mirrorPage("k7m2q9.example.test"), wantErr: ErrBadMirrorLink},
		// Origin по RFC 6454 — только схема, хост и порт: всё остальное
		// в data-link отвергается, а не отбрасывается молча.
		{name: "путь", html: mirrorPage(fixtureOriginA + "/cp"), wantErr: ErrBadMirrorLink},
		{name: "запрос", html: mirrorPage(fixtureOriginA + "/cp?m-path=/ru/"), wantErr: ErrBadMirrorLink},
		// Запрос без пути: у самого адреса зеркала форма именно такая. Без
		// этой строки страж запроса снимался незаметно — в остальных
		// фикстурах запрос идёт вместе с путём, и его ловил страж пути.
		{name: "запрос без пути", html: mirrorPage(fixtureOriginA + "?m-path=/ru"), wantErr: ErrBadMirrorLink},
		{name: "пустой запрос", html: mirrorPage(fixtureOriginA + "?"), wantErr: ErrBadMirrorLink},
		{name: "фрагмент", html: mirrorPage(fixtureOriginA + "#top"), wantErr: ErrBadMirrorLink},
		{name: "userinfo", html: mirrorPage("https://u:p@k7m2q9.example.test"), wantErr: ErrBadMirrorLink},
		{name: "адрес не разбирается", html: mirrorPage("https://[::1"), wantErr: ErrBadMirrorLink},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseMirrorTo([]byte(tc.html))
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("ожидалась ошибка, получен origin %q", got)
				}
				if got != "" {
					t.Fatalf("при ошибке origin обязан быть пустым, получен %q", got)
				}
				// Причина обязана быть различима сентинелом: вызывающий вне
				// пакета отличает «тега нет» от «ссылка непригодна» типом.
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("причина отказа %v, ожидалась %v", err, tc.wantErr)
				}
				// И различима между собой: один сентинел под двумя именами
				// проверку на присутствие переживает, а вызывающего не спасает.
				other := ErrNoMirrorTag
				if errors.Is(tc.wantErr, ErrNoMirrorTag) {
					other = ErrBadMirrorLink
				}
				if errors.Is(err, other) {
					t.Fatalf("причина совпала и с %v, и с %v: %v", tc.wantErr, other, err)
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

// Фолбэка на http.DefaultClient нет: он берёт прокси из окружения и не имеет
// таймаута. Проверка белого ящика — снаружи подмена nil на дефолт невидима:
// резолв через неё точно так же ходит и точно так же отвечает.
func TestNewMirrorKeepsNilClient(t *testing.T) {
	if m := NewMirror(nil, testTTL); m.client != nil {
		t.Fatalf("nil-клиент подменён на %v", m.client)
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

// hugeBody отдаёт заданное число байт и считает отданные. Нужен потому, что
// снятие io.LimitReader по возвращённой ошибке неотличимо: проверка длины
// поймает и тело, прочитанное целиком, — но память к тому моменту уже съедена.
type hugeBody struct {
	left   int
	served *atomic.Int64
	closed *atomic.Bool
}

func (b *hugeBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		return 0, io.EOF
	}
	n := min(len(p), b.left)
	for i := range p[:n] {
		p[i] = 'x'
	}
	b.left -= n
	b.served.Add(int64(n))
	return n, nil
}

func (b *hugeBody) Close() error {
	b.closed.Store(true)
	return nil
}

func (b *recordingBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	return b.r.Read(p)
}

func (b *recordingBody) Close() error {
	b.closed.Store(true)
	return nil
}

// roundTripFunc — транспорт для стабов. Возвращает и ответ, и ошибку: стаб,
// который умеет только ответ, не может вести себя как настоящий транспорт на
// отменённом контексте и на разрыве соединения.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// okResponse — 200 с указанным телом; тело считает Read'ы и закрытие.
func okResponse(r *http.Request, body string, reads *atomic.Int64, closed *atomic.Bool) *http.Response {
	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Header:        make(http.Header),
		ContentLength: int64(len(body)),
		Body:          &recordingBody{r: strings.NewReader(body), reads: reads, closed: closed},
		Request:       r,
	}
}

func TestMirrorOriginChecksStatusBeforeReadingBody(t *testing.T) {
	var reads atomic.Int64
	var closed atomic.Bool
	// Тело валидное: реализация, которая сначала читает и парсит, а статус
	// смотрит только «если ничего не нашлось», отдаст этот origin наружу.
	body := mirrorPage(fixtureOriginA)
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
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
		}, nil
	})}

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

func TestMirrorOriginRejectsEmptyMirrorURL(t *testing.T) {
	var calls atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody, Request: r}, nil
	})}

	m := newMirrorWithClock(client, testTTL, newFakeClock().now)
	got, err := m.Origin(context.Background(), "   ")
	if err == nil {
		t.Fatalf("пустой адрес зеркала обязан быть ошибкой, получен origin %q", got)
	}
	// Ненастроенный адрес и молчащее зеркало — разные классы: первый значит
	// «зайдите в настройки», второй — «повторите позже» с ре-резолвом.
	if !errors.Is(err, ErrMirrorNotConfigured) {
		t.Fatalf("ошибка не различима сентинелом ErrMirrorNotConfigured: %v", err)
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
	// Резолвер попадает в хендлер через канал, а не через переменную,
	// которой присваивают уже после запуска сервера: связь «сначала
	// присвоили, потом хендлер прочитал» держится каналом, а не тем, когда
	// именно net/http вызовет хендлер.
	ready := make(chan *Mirror, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			(<-ready).Invalidate()
			_, _ = io.WriteString(w, mirrorPage(fixtureOriginA))
			return
		}
		_, _ = io.WriteString(w, mirrorPage(fixtureOriginB))
	}))
	defer srv.Close()

	clock := newFakeClock()
	m := newMirrorWithClock(srv.Client(), testTTL, clock.now)
	ready <- m

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

// Что этот тест проверяет: отсутствие гонки под -race и то, что каждый
// параллельный вызов получает корректный origin. Чего он НЕ проверяет и не
// должен: единственности похода на промахе кэша. Одновременные промахи здесь
// допустимы сознательно — резолв это GET статической страницы зеркала, чужую
// квоту он не тратит, потребитель у резолвера один, и объединение
// одновременных запросов было бы абстракцией без названной проблемы.
// Счётчик походов зафиксирован рамками [1, число резолвящих горутин], чтобы
// это решение было видно в тесте, а не подразумевалось.
func TestMirrorOriginConcurrent(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, mirrorPage(fixtureOriginA))
	}))
	defer srv.Close()

	const goroutines, resolvers = 24, 20 // каждая шестая горутина инвалидирует
	m := NewMirror(srv.Client(), testTTL)
	var wg sync.WaitGroup
	for i := range goroutines {
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

	if n := hits.Load(); n < 1 || n > resolvers {
		t.Fatalf("походов к зеркалу %d, ожидались 1..%d", n, resolvers)
	}
}

// Предел размера страницы — единственная защита от «зеркало ответило 200 и
// отдало сотни мегабайт» на роутере со 128 МБ. Ровно на пределе страница
// обязана разбираться, на байт больше — отказ; пара ловит и off-by-one.
func TestMirrorOriginRejectsOversizedPage(t *testing.T) {
	page := mirrorPage(fixtureOriginA)
	if len(page) > maxMirrorHTML {
		t.Fatalf("фикстура %d байт уже больше предела %d", len(page), maxMirrorHTML)
	}
	// Набивка — комментарий: она не может случайно стать вторым мета-тегом.
	pad := func(total int) string {
		filler := total - len(page) - len("<!---->")
		return page + "<!--" + strings.Repeat("x", filler) + "-->"
	}

	cases := []struct {
		name    string
		size    int
		wantErr bool
	}{
		{name: "ровно на пределе", size: maxMirrorHTML},
		{name: "на байт больше предела", size: maxMirrorHTML + 1, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := pad(tc.size)
			if len(body) != tc.size {
				t.Fatalf("тело %d байт, ожидалось %d", len(body), tc.size)
			}
			var reads atomic.Int64
			var closed atomic.Bool
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return okResponse(r, body, &reads, &closed), nil
			})}

			m := newMirrorWithClock(client, testTTL, newFakeClock().now)
			got, err := m.Origin(context.Background(), "https://mirror-size.example.test/cp")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("страница в %d байт обязана быть отвергнута, получен origin %q", tc.size, got)
				}
				if got != "" {
					t.Fatalf("при превышении предела origin обязан быть пустым, получен %q", got)
				}
				if !errors.Is(err, ErrMirrorUnavailable) {
					t.Fatalf("ошибка не различима сентинелом: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("страница ровно в предел обязана разбираться: %v", err)
			}
			if got != fixtureOriginA {
				t.Fatalf("origin = %q, ожидался %q", got, fixtureOriginA)
			}
		})
	}
}

// Предел обязан обрывать чтение, а не только отвергать результат: зеркало,
// отдающее гигабайты, не должно доехать до памяти роутера целиком.
func TestMirrorOriginStopsReadingAtLimit(t *testing.T) {
	const bodySize = 8 * maxMirrorHTML
	var served atomic.Int64
	var closed atomic.Bool
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Header:        make(http.Header),
			ContentLength: bodySize,
			Body:          &hugeBody{left: bodySize, served: &served, closed: &closed},
			Request:       r,
		}, nil
	})}

	m := newMirrorWithClock(client, testTTL, newFakeClock().now)
	got, err := m.Origin(context.Background(), "https://mirror-huge.example.test/cp")
	if err == nil {
		t.Fatalf("страница в %d байт обязана быть отвергнута, получен origin %q", bodySize, got)
	}
	if !errors.Is(err, ErrMirrorUnavailable) {
		t.Fatalf("ошибка не различима сентинелом: %v", err)
	}
	if n := served.Load(); n > maxMirrorHTML+1 {
		t.Fatalf("прочитано %d байт при пределе %d: чтение не оборвано", n, maxMirrorHTML)
	}
	if !closed.Load() {
		t.Fatal("тело ответа не закрыто")
	}
}

// Причина отмены обязана доезжать до вызывающего типом: иначе «запрос
// отменён» от «зеркало лежит» он отличит только разбором текста.
func TestMirrorOriginPreservesCancellation(t *testing.T) {
	var calls atomic.Int64
	// Транспорт ведёт себя как настоящий: на отменённом контексте отдаёт
	// ошибку контекста, а не ответ. Стаб, который r.Context() не смотрит,
	// подмену %w на %v в обёртке поймать не может.
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		return okResponse(r, mirrorPage(fixtureOriginA), new(atomic.Int64), new(atomic.Bool)), nil
	})}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	m := newMirrorWithClock(client, testTTL, newFakeClock().now)
	got, err := m.Origin(ctx, "https://mirror-cancel.example.test/cp")
	if err == nil {
		t.Fatalf("отменённый контекст обязан быть ошибкой, получен origin %q", got)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("причина отмены потеряна при обёртке: %v", err)
	}
	if !errors.Is(err, ErrMirrorUnavailable) {
		t.Fatalf("ошибка не различима сентинелом: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("походов к транспорту %d, ожидался 1", n)
	}
}

// Все пути отказа обязаны быть различимы сентинелом и не отдавать origin.
// Таблица нужна потому, что снятие %w на непокрытом пути тесты переживало.
func TestMirrorOriginFailurePathsAreDistinguishable(t *testing.T) {
	page := mirrorPage(fixtureOriginA)

	cases := []struct {
		name      string
		mirrorURL string
		transport roundTripFunc
		wantErr   error
	}{
		{
			name:      "адрес зеркала не задан",
			mirrorURL: "   ",
			wantErr:   ErrMirrorNotConfigured,
		},
		{
			name:      "непригодный адрес зеркала",
			mirrorURL: "://зеркала-нет",
			wantErr:   ErrMirrorUnavailable,
		},
		{
			name:      "транспорт отказал",
			mirrorURL: "https://mirror-dead.example.test/cp",
			transport: func(*http.Request) (*http.Response, error) {
				return nil, errors.New("соединение разорвано")
			},
			wantErr: ErrMirrorUnavailable,
		},
		{
			name:      "ответ не 200",
			mirrorURL: "https://mirror-503.example.test/cp",
			transport: func(r *http.Request) (*http.Response, error) {
				resp := okResponse(r, page, new(atomic.Int64), new(atomic.Bool))
				resp.StatusCode, resp.Status = http.StatusServiceUnavailable, "503 Service Unavailable"
				return resp, nil
			},
			wantErr: ErrMirrorUnavailable,
		},
		{
			name:      "тело не читается",
			mirrorURL: "https://mirror-truncated.example.test/cp",
			transport: func(r *http.Request) (*http.Response, error) {
				resp := okResponse(r, page, new(atomic.Int64), new(atomic.Bool))
				resp.Body = io.NopCloser(iotest.ErrReader(errors.New("соединение оборвалось на теле")))
				return resp, nil
			},
			wantErr: ErrMirrorUnavailable,
		},
		{
			name:      "страница без мета-тега",
			mirrorURL: "https://mirror-nometa.example.test/cp",
			transport: func(r *http.Request) (*http.Response, error) {
				return okResponse(r, `<html><head><title>cp</title></head></html>`, new(atomic.Int64), new(atomic.Bool)), nil
			},
			wantErr: ErrNoMirrorTag,
		},
		{
			name:      "мета-тег с непригодной ссылкой",
			mirrorURL: "https://mirror-badlink.example.test/cp",
			transport: func(r *http.Request) (*http.Response, error) {
				return okResponse(r, mirrorPage("https://k7m2q9.example.test/cp?m-path=/ru/"), new(atomic.Int64), new(atomic.Bool)), nil
			},
			wantErr: ErrBadMirrorLink,
		},
	}

	// Конкретные причины отказа. Ожидаемая обязана срабатывать, соседние —
	// нет: проверка только на присутствие переживает и сентинел-синоним, и
	// чужую причину, подставленную на пути. wantErr, равный общему сентинелу,
	// значит «конкретной причины на этом пути нет» — тогда молчат все три.
	specific := []error{ErrMirrorNotConfigured, ErrNoMirrorTag, ErrBadMirrorLink}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transport := tc.transport
			if transport == nil {
				transport = func(r *http.Request) (*http.Response, error) {
					t.Fatalf("на этом пути запрос делать нельзя")
					return nil, nil
				}
			}
			m := newMirrorWithClock(&http.Client{Transport: transport}, testTTL, newFakeClock().now)
			got, err := m.Origin(context.Background(), tc.mirrorURL)
			if err == nil {
				t.Fatalf("ожидалась ошибка, получен origin %q", got)
			}
			if got != "" {
				t.Fatalf("при ошибке origin обязан быть пустым, получен %q", got)
			}
			if !errors.Is(err, ErrMirrorUnavailable) {
				t.Fatalf("ошибка не различима общим сентинелом: %v", err)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("причина отказа %v, ожидалась %v", err, tc.wantErr)
			}
			for _, other := range specific {
				if other == tc.wantErr {
					continue
				}
				if errors.Is(err, other) {
					t.Fatalf("причина совпала и с ожидаемой %v, и с чужой %v: %v", tc.wantErr, other, err)
				}
			}
		})
	}
}

// Тело закрывается и на успешном пути: без этого соединение утекает на
// каждом резолве, а не только на отказе.
func TestMirrorOriginClosesBodyOnSuccess(t *testing.T) {
	var reads atomic.Int64
	var closed atomic.Bool
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return okResponse(r, mirrorPage(fixtureOriginA), &reads, &closed), nil
	})}

	m := newMirrorWithClock(client, testTTL, newFakeClock().now)
	got, err := m.Origin(context.Background(), "https://mirror-ok.example.test/cp")
	if err != nil {
		t.Fatalf("резолв: %v", err)
	}
	if got != fixtureOriginA {
		t.Fatalf("origin = %q, ожидался %q", got, fixtureOriginA)
	}
	if !closed.Load() {
		t.Fatal("тело ответа не закрыто на успешном пути")
	}
}

// Спуск с https на http при резолве зеркала — отказ, а не молчаливое
// следование. Ценность не в самом запросе (он без тела и без секрета), а в
// том, ЧТО с него приезжает: хост, которому клиент затем шлёт ключ подписки.
// Проверка настроек обещает «схема только https» ровно поэтому; без запрета
// обещание обходится одним 302 (доказано пробой при ревью P007).
func TestMirrorOriginRejectsSchemeDowngrade(t *testing.T) {
	var attackerHits atomic.Int64
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerHits.Add(1)
		_, _ = io.WriteString(w, mirrorPage(fixtureOriginC))
	}))
	t.Cleanup(attacker.Close)

	entry := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL, http.StatusFound)
	}))
	t.Cleanup(entry.Close)

	m := newMirrorWithClock(entry.Client(), testTTL, newFakeClock().now)
	origin, err := m.Origin(context.Background(), entry.URL)
	if err == nil {
		t.Fatalf("спуск на http принят, origin = %q", origin)
	}
	if !errors.Is(err, ErrMirrorUnavailable) {
		t.Fatalf("класс ошибки не «зеркало недоступно»: %v", err)
	}
	if n := attackerHits.Load(); n != 0 {
		t.Fatalf("http-хост всё-таки опрошен (%d раз) — запрет не сработал", n)
	}
}

// Апгрейд и переход внутри https запрещать нельзя: адрес зеркала вводит
// пользователь, и перенаправлением приезжают и хвостовой слэш, и сокращатель.
// Тест держит границу узкой — запрещён спуск, а не редирект вообще.
func TestMirrorOriginFollowsSchemeUpgrade(t *testing.T) {
	var hits atomic.Int64
	target := mirrorServer(t, &hits, fixtureOriginA)

	entry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusMovedPermanently)
	}))
	t.Cleanup(entry.Close)

	m := newMirrorWithClock(target.Client(), testTTL, newFakeClock().now)
	got, err := m.Origin(context.Background(), entry.URL)
	if err != nil {
		t.Fatalf("резолв через перенаправление: %v", err)
	}
	if got != fixtureOriginA {
		t.Fatalf("origin = %q, ожидался %q", got, fixtureOriginA)
	}
}

// Своя политика редиректов выключает штатный предел net/http целиком, поэтому
// предел обязан быть восстановлен руками: зеркало, перенаправляющее на себя,
// иначе крутит запрос до отмены контекста.
func TestMirrorOriginStopsEndlessRedirects(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	m := newMirrorWithClock(srv.Client(), testTTL, newFakeClock().now)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	origin, err := m.Origin(ctx, srv.URL)
	if err == nil {
		t.Fatalf("бесконечная цепочка принята, origin = %q", origin)
	}
	if !errors.Is(err, ErrMirrorUnavailable) {
		t.Fatalf("класс ошибки не «зеркало недоступно»: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("цепочку остановил таймаут теста, а не предел перенаправлений")
	}
	if n := hits.Load(); n > maxMirrorRedirects+1 {
		t.Fatalf("походов %d, предел %d не соблюдён", n, maxMirrorRedirects)
	}
}
