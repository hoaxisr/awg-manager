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

// ErrMirrorUnavailable — зеркало не отдало рабочий origin: не ответило,
// ответило не 200 или вернуло страницу без пригодного мета-тега. Отдельный
// сентинел нужен, чтобы вызывающий отличал «зеркало недоступно» от «ключ
// отклонён» типом ошибки, а не разбором текста.
var ErrMirrorUnavailable = errors.New("зеркало Amnezia недоступно")

// DefaultMirrorTTL — срок жизни добытого origin. Хост в мета-теге одноразовый
// и ротируется, поэтому кэш живёт минутами, а не до перезапуска демона.
const DefaultMirrorTTL = 30 * time.Minute

// maxMirrorHTML ограничивает разбираемую страницу зеркала: цель — роутер со
// 128 МБ, страница CP — десятки килобайт (ср. maxVPNLinkJSON).
const maxMirrorHTML = 1 << 20

var (
	metaTagRe  = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	tagAttrRe  = regexp.MustCompile(`(?is)([a-z0-9_:-]+)\s*=\s*("[^"]*"|'[^']*'|[^\s"'>]+)`)
	errNoMeta  = errors.New(`на странице нет <meta name="mirror-to"> с data-link`)
	errNoValue = errors.New(`<meta name="mirror-to"> пришёл без data-link`)
)

// ParseMirrorTo достаёт рабочий origin CP из data-link мета-тега mirror-to.
// Порядок атрибутов и вид кавычек в живой странице не зафиксированы, поэтому
// тег разбирается по атрибутам, а не по подстроке. Результат обязан быть
// абсолютным https-адресом; хвостовой слэш срезается, чтобы склейка путей у
// вызывающего не давала двойного.
func ParseMirrorTo(html []byte) (string, error) {
	for _, tag := range metaTagRe.FindAll(html, -1) {
		attrs := parseTagAttrs(string(tag))
		if !strings.EqualFold(strings.TrimSpace(attrs["name"]), "mirror-to") {
			continue
		}
		return normalizeOrigin(attrs["data-link"])
	}
	return "", errNoMeta
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

func normalizeOrigin(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errNoValue
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("непригодный data-link %q: %v", s, err)
	}
	if !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		return "", fmt.Errorf("data-link обязан быть абсолютным https-адресом, получен %q", s)
	}
	return strings.TrimRight(s, "/"), nil
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

// NewMirror создаёт резолвер. ttl <= 0 означает DefaultMirrorTTL.
func NewMirror(client *http.Client, ttl time.Duration) *Mirror {
	return newMirrorWithClock(client, ttl, time.Now)
}

// newMirrorWithClock — конструктор с подменяемыми часами для тестов
// (ср. newReaderWithClock в internal/sys/httpdownload).
func newMirrorWithClock(client *http.Client, ttl time.Duration, now func() time.Time) *Mirror {
	if client == nil {
		client = http.DefaultClient
	}
	if ttl <= 0 {
		ttl = DefaultMirrorTTL
	}
	return &Mirror{client: client, ttl: ttl, now: now}
}

// Origin возвращает рабочий origin CP для указанного адреса зеркала.
// Кэш привязан к адресу: смена настройки промахивается мимо него сама.
func (m *Mirror) Origin(ctx context.Context, mirrorURL string) (string, error) {
	mirrorURL = strings.TrimSpace(mirrorURL)
	if mirrorURL == "" {
		return "", fmt.Errorf("%w: адрес зеркала не задан", ErrMirrorUnavailable)
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
		return "", fmt.Errorf("%w: непригодный адрес %q: %v", ErrMirrorUnavailable, mirrorURL, err)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: запрос к %s: %v", ErrMirrorUnavailable, mirrorURL, err)
	}
	defer resp.Body.Close()

	// Статус — до чтения тела: страница ошибки CDN бывает большой, а её
	// содержимое всё равно не источник origin.
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: %s ответило %d", ErrMirrorUnavailable, mirrorURL, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMirrorHTML+1))
	if err != nil {
		return "", fmt.Errorf("%w: чтение ответа %s: %v", ErrMirrorUnavailable, mirrorURL, err)
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
