package amneziacp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ErrMirrorUnavailable — зеркало не отдало рабочий origin. Под него подпадают
// все отказы резолва: адрес зеркала не задан, адрес непригоден, запрос не
// удался, ответ не 200, тело не прочиталось, тело больше предела, на странице
// нет пригодного мета-тега. Отдельный сентинел нужен, чтобы вызывающий отличал
// «зеркало недоступно» от «ключ отклонён» типом ошибки, а не разбором текста;
// конкретную причину дают сентинелы ниже.
var ErrMirrorUnavailable = errors.New("зеркало Amnezia недоступно")

// ErrMirrorNotConfigured — адрес зеркала не задан. Класс отказа другой, чем у
// молчащего зеркала: здесь вызывающему нужно отправить пользователя в
// настройки, а не повторять запрос и не гнать принудительный ре-резолв.
// Обёрнут в ErrMirrorUnavailable, чтобы грубая проверка по общему сентинелу
// продолжала срабатывать.
var ErrMirrorNotConfigured = fmt.Errorf("%w: адрес зеркала не задан", ErrMirrorUnavailable)

// DefaultMirrorTTL — срок жизни добытого origin. Хост в мета-теге временный и
// ротируется, поэтому кэш живёт минутами, а не до перезапуска демона.
const DefaultMirrorTTL = 30 * time.Minute

// maxMirrorHTML ограничивает разбираемую страницу зеркала. Цель — роутер со
// 128 МБ: живая страница весит 881 байт (снята 2026-09-10), мегабайт даёт
// тысячекратный запас на её рост и на обёртки CDN, но не позволяет ответу в
// сотни мегабайт съесть память целиком.
const maxMirrorHTML = 1 << 20

// maxMirrorRedirects повторяет предел, который net/http применяет сам, пока
// CheckRedirect не задан. Как только политика задана, штатный предел
// выключается целиком — цепочку надо ограничивать своими руками, иначе
// зеркало, перенаправляющее на себя, крутит запрос до отмены контекста.
const maxMirrorRedirects = 10

var (
	metaTagRe = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	tagAttrRe = regexp.MustCompile(`(?is)([a-z0-9_:-]+)\s*=\s*("[^"]*"|'[^']*'|[^\s"'>]+)`)

	// ErrNoMirrorTag — на странице нет <meta name="mirror-to">.
	// ErrBadMirrorLink — тег есть, но его data-link непригоден как origin.
	// Экспортированы потому, что экспортирована ParseMirrorTo: вызывающий вне
	// пакета обязан отличать «тега нет» от «тег есть, ссылка непригодна» типом.
	ErrNoMirrorTag   = errors.New(`на странице нет <meta name="mirror-to">`)
	ErrBadMirrorLink = errors.New("непригодный data-link")
)

// ParseMirrorTo достаёт рабочий origin CP из data-link мета-тега mirror-to.
// Порядок атрибутов и вид кавычек в живой странице не зафиксированы, поэтому
// тег разбирается по атрибутам, а не по подстроке. Берётся первый подходящий
// тег и первое вхождение атрибута в нём — как в HTML-парсере браузера.
// Результат — origin в форме «схема://хост[:порт]»: см. normalizeOrigin.
func ParseMirrorTo(html []byte) (string, error) {
	for _, tag := range metaTagRe.FindAll(html, -1) {
		attrs := parseTagAttrs(string(tag))
		if !strings.EqualFold(attrs["name"], "mirror-to") {
			continue
		}
		return normalizeOrigin(attrs["data-link"])
	}
	return "", ErrNoMirrorTag
}

func parseTagAttrs(tag string) map[string]string {
	attrs := make(map[string]string)
	for _, m := range tagAttrRe.FindAllStringSubmatch(tag, -1) {
		name := strings.ToLower(m[1])
		value := m[2]
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') {
			value = value[1 : len(value)-1]
		}
		if _, dup := attrs[name]; !dup {
			attrs[name] = value
		}
	}
	return attrs
}

// normalizeOrigin приводит data-link к origin в смысле RFC 6454: схема, хост и
// порт, больше ничего. Потребитель клеит из него «origin + /api/…» и шлёт его
// же заголовком Origin, поэтому путь, запрос, фрагмент и user:pass@ — отказ, а
// не молчаливое отбрасывание: живое зеркало отдаёт чистый хост, и появление
// там пути обязано быть слышно. Хвостовые слэши путём не считаются и
// отбрасываются, чтобы склейка путей у вызывающего не давала двойного.
func normalizeOrigin(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf(`%w: <meta name="mirror-to"> пришёл без data-link`, ErrBadMirrorLink)
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("%w %q: %w", ErrBadMirrorLink, s, err)
	}
	// url.Parse уже привела схему к нижнему регистру — сравнение обычное.
	if u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("%w %q: обязан быть абсолютным https-адресом", ErrBadMirrorLink, s)
	}
	if strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("%w %q: origin — только схема, хост и порт", ErrBadMirrorLink, s)
	}
	return u.Scheme + "://" + u.Host, nil
}

// Mirror добывает и кэширует рабочий origin CP по адресу зеркала.
// Владелец кэша один — этот объект; поля под mu.
type Mirror struct {
	client *http.Client
	ttl    time.Duration
	now    func() time.Time

	mu        sync.Mutex
	mirrorURL string // адрес зеркала, которому принадлежит origin
	origin    string
	expiresAt time.Time
	// gen растёт на каждой инвалидации: резолв, начавшийся до неё, свой
	// результат в кэш не кладёт — иначе поздний ответ воскрешает мёртвый
	// origin на весь TTL.
	gen uint64
}

// NewMirror создаёт резолвер. client обязателен и подмены на nil не имеет:
// http.DefaultClient берёт прокси из окружения (при заданном HTTPS_PROXY
// резолв ушёл бы через чужой прокси — мимо требования о регионе, ради
// которого зеркало и понадобилось) и не имеет таймаута.
// ttl <= 0 означает DefaultMirrorTTL.
func NewMirror(client *http.Client, ttl time.Duration) *Mirror {
	return newMirrorWithClock(client, ttl, time.Now)
}

// newMirrorWithClock — конструктор с подменяемыми часами для тестов
// (ср. newReaderWithClock в internal/sys/httpdownload).
func newMirrorWithClock(client *http.Client, ttl time.Duration, now func() time.Time) *Mirror {
	if ttl <= 0 {
		ttl = DefaultMirrorTTL
	}
	return &Mirror{client: withoutSchemeDowngrade(client), ttl: ttl, now: now}
}

// withoutSchemeDowngrade копирует клиента и запрещает перенаправление, которое
// уводит с https на что-то другое.
//
// Следовать перенаправлениям зеркалу НУЖНО: адрес вводит пользователь, и
// хвостовой слэш с сокращателем приезжают именно ими. Оба остаются ВНУТРИ
// https: вход резолвера всегда https — ValidateAmneziaMirrorURL отвергает
// другую схему у присланного адреса, а EffectiveAmneziaMirrorURL подменяет
// непригодное хранимое дефолтом (internal/storage/settings.go). Но
// запрос к зеркалу не безобиден, хотя и уходит без тела и без секрета: со
// страницы приезжает ХОСТ, которому клиент затем шлёт ключ подписки. Разрешив
// спуск на http, мы отдаём назначение этого ключа тому, кто сидит на канале, —
// проверка «схема только https» в настройках (internal/storage/settings.go)
// обещает ровно обратное, и обход доказан пробой: https-зеркало → 302 →
// http-хост → страница с mirror-to → origin атакующего без единой ошибки.
//
// Переход https → https разрешён, апгрейд http → https — тоже: запрещён
// именно спуск, а не перенаправление. Апгрейда в производстве не бывает
// (вход всегда https, см. выше); граница проведена по спуску потому, что
// защищаемое свойство — «схема не понижается», а не «схема не меняется».
//
// Копия, а не правка переданного клиента: объект чужой, его политика на других
// путях — не наше дело (ср. withoutRedirects).
func withoutSchemeDowngrade(c *http.Client) *http.Client {
	// nil сохраняется как nil: фолбэка на http.DefaultClient здесь нет
	// намеренно (см. NewMirror), и подменять его копией пустого клиента —
	// значит завести тот самый фолбэк с прокси из окружения и без таймаута.
	if c == nil {
		return nil
	}
	dup := *c
	dup.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxMirrorRedirects {
			return fmt.Errorf("%w: больше %d перенаправлений", ErrMirrorUnavailable, maxMirrorRedirects)
		}
		if prev := via[len(via)-1]; prev.URL.Scheme == "https" && req.URL.Scheme != "https" {
			return fmt.Errorf("%w: перенаправление с https на %q — со страницы зеркала приезжает хост, которому мы шлём ключ подписки",
				ErrMirrorUnavailable, req.URL.Scheme)
		}
		return nil
	}
	return &dup
}

// Origin возвращает рабочий origin CP для указанного адреса зеркала.
// Кэш привязан к адресу: смена настройки промахивается мимо него сама.
func (m *Mirror) Origin(ctx context.Context, mirrorURL string) (string, error) {
	mirrorURL = strings.TrimSpace(mirrorURL)
	if mirrorURL == "" {
		return "", ErrMirrorNotConfigured
	}

	m.mu.Lock()
	if m.mirrorURL == mirrorURL && m.now().Before(m.expiresAt) {
		origin := m.origin
		m.mu.Unlock()
		return origin, nil
	}
	gen := m.gen
	m.mu.Unlock()

	origin, err := m.resolve(ctx, mirrorURL)
	if err != nil {
		// Неудача не кэшируется: следующий вызов обязан сходить заново.
		return "", err
	}

	m.mu.Lock()
	if m.gen == gen {
		m.mirrorURL, m.origin, m.expiresAt = mirrorURL, origin, m.now().Add(m.ttl)
	}
	m.mu.Unlock()
	return origin, nil
}

// Invalidate выбрасывает кэш и отменяет запись результата у резолвов,
// которые уже летят.
func (m *Mirror) Invalidate() {
	m.mu.Lock()
	m.mirrorURL, m.origin, m.expiresAt = "", "", time.Time{}
	m.gen++
	m.mu.Unlock()
}

func (m *Mirror) resolve(ctx context.Context, mirrorURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mirrorURL, nil)
	if err != nil {
		return "", fmt.Errorf("%w: непригодный адрес %q: %w", ErrMirrorUnavailable, mirrorURL, err)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		// Чужая ошибка заворачивается через %w, а не %v: иначе
		// context.Canceled не доезжает до вызывающего и «запрос отменён»
		// отличимо от «зеркало лежит» только по тексту.
		return "", fmt.Errorf("%w: запрос к %s: %w", ErrMirrorUnavailable, mirrorURL, err)
	}
	defer resp.Body.Close()

	// Статус — до чтения тела: страница ошибки CDN бывает большой, а её
	// содержимое всё равно не источник origin.
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: %s ответило %d", ErrMirrorUnavailable, mirrorURL, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMirrorHTML+1))
	if err != nil {
		return "", fmt.Errorf("%w: чтение ответа %s: %w", ErrMirrorUnavailable, mirrorURL, err)
	}
	if len(body) > maxMirrorHTML {
		return "", fmt.Errorf("%w: страница %s больше %d байт", ErrMirrorUnavailable, mirrorURL, maxMirrorHTML)
	}

	origin, err := ParseMirrorTo(body)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrMirrorUnavailable, mirrorURL, err)
	}
	return origin, nil
}
