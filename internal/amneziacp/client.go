package amneziacp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
)

// ErrNoKey — ключа подписки нет ни сохранённого, ни сессионного. Отдельный
// класс: вызывающему нужно попросить ключ у пользователя, а не повторять
// запрос.
var ErrNoKey = errors.New("ключ подписки Amnezia не задан")

// ErrKeyRejected — портал отказал в ключе (401/403/422). Статус портала наружу
// не транслируется: 401 от CP, отданный наружу как 401, разлогинивает панель.
var ErrKeyRejected = errors.New("ключ подписки Amnezia отклонён")

// ErrServiceUnavailable — до данных подписки не добраться: молчит портал, не
// резолвится зеркало, ответ неожиданной формы. Причина отказа обязана
// отличаться от «ключ отклонён» сентинелом, а не текстом и не статусом: по
// первой пользователя отправляют повторить, по второй — ввести другой ключ.
// Конкретика зеркала остаётся доступной по ErrMirrorUnavailable.
var ErrServiceUnavailable = errors.New("сервис Amnezia недоступен")

const (
	// maxCPBody ограничивает ответ портала. Живой account-info — единицы
	// килобайт, .conf — сотни байт; мегабайт даёт запас на рост, но не даёт
	// ответу в сотни мегабайт съесть память роутера со 128 МБ.
	maxCPBody = 1 << 20

	// maxAttempts — попытка и ровно один повтор. Повторы ограничены числом:
	// вечный 401 или вечно мёртвый хост обязаны заканчиваться ошибкой.
	maxAttempts = 2

	// sessionCookie — имя cookie сессии портала.
	sessionCookie = "v_sid"

	// browserUA — UA веб-приложения портала: с UA по умолчанию Go запрос
	// отвергает WAF перед CP.
	browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

	// loginRemember — значение remember в теле входа. Всегда true: сессию
	// держит демон, а не человек за браузером, и долгая cookie — это меньше
	// входов с роутера. С флагом «запомнить ключ на роутере» ничего общего не
	// имеет: там речь про хранение секрета у нас, здесь — про срок cookie у CP.
	loginRemember = true
)

// LogFunc — узкий колбэк журналирования: event ложится в поле события, detail
// — в сообщение (ср. logging.ScopedLogger.Info). Пакет не знает ни про
// журнал приложения, ни про его уровни.
type LogFunc func(event, detail string)

// recovery — подсказка, что делать после неудачной попытки.
type recovery int

const (
	recoveryNone    recovery = iota // повторять нечего
	recoverySession                 // cookie протухла: войти заново
	recoveryMirror                  // хост не отвечает: перерезолвить зеркало
)

// cpRequest описывает один вызов портала.
type cpRequest struct {
	event   string // имя операции для журнала
	method  string
	path    string
	referer string // путь Referer'а, как у веб-приложения портала
	payload []byte
}

// Client владеет парой «origin зеркала + сессия портала»: сам логинится
// сохранённым ключом, переживает ротацию хоста и протухший sid и не выпускает
// наружу ни ключ подписки, ни сессию.
//
// Адрес зеркала и ключ приходят геттерами, чтобы пакет не знал ни про
// настройки, ни про хранилище, и чтобы смена настройки подхватывалась без
// пересборки клиента.
type Client struct {
	http      *http.Client
	mirror    *Mirror
	mirrorURL func() string
	key       func() string
	logf      LogFunc

	mu     sync.Mutex
	origin string // хост, выдавший сессию
	sid    string
}

// NewClient собирает клиента. httpClient == nil подменяется собственным прямым
// клиентом: маршрут загрузок в этой линии не участвует. Геттеры и logf
// обязательны — без любого из них клиент нерабочий, и молчаливая подмена
// спрятала бы ошибку сборки зависимостей.
func NewClient(httpClient *http.Client, mirrorURL, key func() string, logf LogFunc) *Client {
	if httpClient == nil {
		httpClient = newDirectClient()
	}
	return &Client{
		http:      httpClient,
		mirror:    NewMirror(httpClient, 0),
		mirrorURL: mirrorURL,
		key:       key,
		logf:      logf,
	}
}

// newDirectClient — прямой клиент к зеркалу и порталу.
//
// База — httpclient.NewTransport ради пина HTTP/1.1 (ForceAttemptHTTP2=false):
// фронт зеркала — тот же CDN, что и у портала, и на h2 он отвечает EOF и
// «malformed HTTP response» — ровно та поломка, ради которой httpclient и
// заведён.
//
// Прокси из окружения, в отличие от снятого internal/api/amnezia_cp.go, не
// берётся: при заданном HTTPS_PROXY запрос ушёл бы через чужой прокси — мимо
// требования о регионе, ради которого зеркало и понадобилось. Снимать его
// нужно явно: httpclient.NewTransport ставит ProxyFromEnvironment сам, когда
// транспорт не привязан к интерфейсу.
func newDirectClient() *http.Client {
	tr, err := httpclient.NewTransport(httpclient.TransportConfig{})
	if err != nil || tr == nil {
		tr = &http.Transport{}
	}
	tr.Proxy = nil
	tr.DialContext = (&net.Dialer{
		Timeout:   12 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext
	tr.TLSHandshakeTimeout = 15 * time.Second
	tr.ResponseHeaderTimeout = 25 * time.Second
	tr.ExpectContinueTimeout = time.Second
	tr.IdleConnTimeout = 45 * time.Second
	tr.MaxIdleConnsPerHost = 8
	// Простаивающие TLS-сессии за CDN подвисают до собственного таймаута —
	// пользователь видел это как «каждая вставка ключа висит ~40 секунд».
	tr.DisableKeepAlives = true
	return &http.Client{Transport: tr, Timeout: 45 * time.Second}
}

// AccountInfo отдаёт данные подписки без ключа подписки внутри.
func (c *Client) AccountInfo(ctx context.Context) (json.RawMessage, error) {
	body, err := c.call(ctx, cpRequest{
		event:   "account-info",
		method:  http.MethodGet,
		path:    "/api/account-info",
		referer: "/ru",
	})
	if err != nil {
		return nil, err
	}
	return scrubAccountInfo(body)
}

// CountryConfig выдаёт .conf выбранной страны. Операция расходная — тратит
// слот устройств подписки, — поэтому сериализовать её вызовы обязан
// вызывающий: клиент про параллельные запросы пользователя не знает.
//
// Пустой код страны — ошибка вызывающего: он валидирует ввод до вызова,
// отдельного сентинела под это нет.
func (c *Client) CountryConfig(ctx context.Context, countryCode string) (string, error) {
	code := strings.ToLower(strings.TrimSpace(countryCode))
	if code == "" {
		return "", errors.New("amneziacp: код страны пуст")
	}
	payload, err := json.Marshal(map[string]string{"countryCode": code})
	if err != nil {
		return "", fmt.Errorf("%w: тело запроса конфига: %w", ErrServiceUnavailable, err)
	}
	body, err := c.call(ctx, cpRequest{
		event:   "download-config",
		method:  http.MethodPost,
		path:    "/api/download-config",
		referer: "/ru",
		payload: payload,
	})
	if err != nil {
		return "", err
	}
	return extractConf(body)
}

// CheckKey проверяет присланный ключ входом в портал. Неудача текущую сессию
// не трогает: пользователь мог ввести чужой ключ, уже работающая подписка от
// этого не обязана отваливаться. Успех, наоборот, сессию занимает — ключ
// сохраняется уже после проверки, и неудача сохранения не должна отменять
// состоявшийся вход.
func (c *Client) CheckKey(ctx context.Context, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return ErrNoKey
	}
	var lastErr error
	for attempt := 0; ; attempt++ {
		origin, err := c.resolve(ctx)
		if err != nil {
			return err
		}
		sid, rec, err := c.login(ctx, origin, key)
		if err == nil {
			c.adopt(origin, sid)
			return nil
		}
		lastErr = err
		if !c.again(ctx, rec, attempt) {
			return lastErr
		}
	}
}

// ResetSession выбрасывает сессию: следующий вызов войдёт заново.
func (c *Client) ResetSession() {
	c.mu.Lock()
	c.origin, c.sid = "", ""
	c.mu.Unlock()
}

// call выполняет запрос под сессией, восстанавливая её при протухании и
// перерезолвя зеркало при сетевом отказе.
func (c *Client) call(ctx context.Context, req cpRequest) ([]byte, error) {
	// Пустой ключ отсекается до любого похода в сеть — и к порталу, и к
	// зеркалу: резолв ради заведомо невозможного запроса бессмыслен.
	key := strings.TrimSpace(c.key())
	if key == "" {
		return nil, ErrNoKey
	}

	var lastErr error
	for attempt := 0; ; attempt++ {
		origin, err := c.resolve(ctx)
		if err != nil {
			return nil, err
		}
		sid, rec, err := c.session(ctx, origin, key)
		if err == nil {
			var body []byte
			body, rec, err = c.send(ctx, origin, sid, req)
			if err == nil {
				return body, nil
			}
		}
		lastErr = err
		if !c.again(ctx, rec, attempt) {
			return nil, lastErr
		}
	}
}

// again решает, делать ли повтор, и готовит к нему клиента. Отменённый
// контекст повтором не лечится.
func (c *Client) again(ctx context.Context, rec recovery, attempt int) bool {
	if rec == recoveryNone || attempt+1 >= maxAttempts || ctx.Err() != nil {
		return false
	}
	if rec == recoveryMirror {
		// Хост в мета-теге ротируется: мёртвый адрес обязан быть добыт заново,
		// а не дожить в кэше до конца TTL.
		c.mirror.Invalidate()
	}
	return true
}

func (c *Client) resolve(ctx context.Context) (string, error) {
	origin, err := c.mirror.Origin(ctx, c.mirrorURL())
	if err != nil {
		// Недоступное зеркало — тот же класс, что и молчащий портал. Своя
		// причина остаётся различимой по ErrMirrorUnavailable и соседям.
		return "", fmt.Errorf("%w: %w", ErrServiceUnavailable, err)
	}
	return origin, nil
}

// session отдаёт cookie для указанного хоста, входя при необходимости. Сессия
// хранится вместе с origin, который её выдал: cookie одного хоста зеркала на
// другом недействительна, поэтому смена origin обнуляет sid сама.
func (c *Client) session(ctx context.Context, origin, key string) (string, recovery, error) {
	c.mu.Lock()
	if c.origin == origin && c.sid != "" {
		sid := c.sid
		c.mu.Unlock()
		return sid, recoveryNone, nil
	}
	c.mu.Unlock()

	// Одновременный промах даёт два входа — это допустимо: вход чужой квоты не
	// тратит, а держать лок на время сетевого запроса дороже.
	sid, rec, err := c.login(ctx, origin, key)
	if err != nil {
		return "", rec, err
	}
	c.adopt(origin, sid)
	return sid, recoveryNone, nil
}

func (c *Client) adopt(origin, sid string) {
	c.mu.Lock()
	c.origin, c.sid = origin, sid
	c.mu.Unlock()
}

// dropSession выбрасывает сессию, если она всё ещё та, на которой случился
// отказ: параллельный вызов мог уже войти заново.
func (c *Client) dropSession(sid string) {
	c.mu.Lock()
	if c.sid == sid {
		c.origin, c.sid = "", ""
	}
	c.mu.Unlock()
}

func (c *Client) login(ctx context.Context, origin, key string) (string, recovery, error) {
	payload, err := json.Marshal(map[string]any{"vpnKey": key, "remember": loginRemember})
	if err != nil {
		return "", recoveryNone, fmt.Errorf("%w: тело входа: %w", ErrServiceUnavailable, err)
	}

	resp, rec, err := c.do(ctx, origin, "", cpRequest{
		event:   "login",
		method:  http.MethodPost,
		path:    "/api/login",
		referer: "/ru/login",
		payload: payload,
	})
	if err != nil {
		if errors.Is(err, ErrKeyRejected) {
			// Повторять вход тем же ключом смысла нет.
			rec = recoveryNone
		}
		return "", rec, err
	}
	defer resp.Body.Close()

	sid := sessionFromResponse(resp)
	if sid == "" {
		// 200 без cookie — неожиданная форма, а не успех: дальше пошли бы
		// запросы без сессии и отказ уже с чужой причиной.
		return "", recoveryNone, fmt.Errorf("%w: вход не вернул cookie сессии", ErrServiceUnavailable)
	}
	return sid, recoveryNone, nil
}

func (c *Client) send(ctx context.Context, origin, sid string, req cpRequest) ([]byte, recovery, error) {
	resp, rec, err := c.do(ctx, origin, sid, req)
	if err != nil {
		if rec == recoverySession {
			c.dropSession(sid)
		}
		return nil, rec, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCPBody+1))
	if err != nil {
		return nil, recoveryNone, fmt.Errorf("%w: чтение ответа %s: %w", ErrServiceUnavailable, req.path, err)
	}
	if len(body) > maxCPBody {
		return nil, recoveryNone, fmt.Errorf("%w: ответ %s больше %d байт", ErrServiceUnavailable, req.path, maxCPBody)
	}
	return body, recoveryNone, nil
}

// do шлёт один запрос к порталу и классифицирует отказ. Тело успешного ответа
// остаётся незакрытым — его закрывает вызывающий; при отказе тело не читается
// вовсе: наружу идёт наш текст, а страница ошибки CDN бывает большой.
func (c *Client) do(ctx context.Context, origin, sid string, req cpRequest) (*http.Response, recovery, error) {
	var body io.Reader
	if len(req.payload) > 0 {
		body = bytes.NewReader(req.payload)
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.method, origin+req.path, body)
	if err != nil {
		return nil, recoveryNone, fmt.Errorf("%w: запрос %s: %w", ErrServiceUnavailable, req.path, err)
	}
	httpReq.Header.Set("Accept", "*/*")
	httpReq.Header.Set("User-Agent", browserUA)
	// Origin и Referer строятся от резолвнутого адреса: константа здесь
	// означала бы запрос к прежнему адресу портала, который спека запрещает.
	httpReq.Header.Set("Origin", origin)
	httpReq.Header.Set("Referer", origin+req.referer)
	if len(req.payload) > 0 {
		httpReq.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	}
	if sid != "" {
		httpReq.Header.Set("Cookie", sessionCookie+"="+sid)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		// В журнал идут резолвнутый адрес и маршрут: при разборе жалобы
		// спрашивают именно их. Ключа и сессии здесь нет.
		c.logf(req.event, fmt.Sprintf("origin=%s route=direct %s %s network=%v", origin, req.method, req.path, err))
		return nil, recoveryMirror, fmt.Errorf("%w: %s %s: %w", ErrServiceUnavailable, req.method, req.path, err)
	}

	// Статус — до чтения тела (иначе большой ответ приезжает в память раньше,
	// чем выясняется, что он не нужен).
	c.logf(req.event, fmt.Sprintf("origin=%s route=direct %s %s cp_http=%d", origin, req.method, req.path, resp.StatusCode))
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, statusRecovery(resp.StatusCode), statusError(resp.StatusCode, req)
	}
	return resp, recoveryNone, nil
}

func statusRecovery(code int) recovery {
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		// Протухшая cookie: войти заново и повторить.
		return recoverySession
	}
	return recoveryNone
}

// statusError переводит статус портала в нашу причину отказа. Наружу статус не
// уходит: отозванный premium-ключ не должен разлогинивать панель.
func statusError(code int, req cpRequest) error {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity:
		// 422 портал отдаёт на непригодный ключ во входе — это тот же класс,
		// что 401/403, а не «сервис лежит».
		return fmt.Errorf("%w: %s %s", ErrKeyRejected, req.method, req.path)
	default:
		return fmt.Errorf("%w: %s %s ответил %d", ErrServiceUnavailable, req.method, req.path, code)
	}
}

func sessionFromResponse(resp *http.Response) string {
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			return strings.TrimSpace(c.Value)
		}
	}
	return ""
}

// subscriptionKeyFields — поля ответа портала, несущие сам ключ подписки.
// Живой ответ кладёт его рядом с остальными полями, в vpn_key; camelCase
// срезается заодно — строка кода против необратимой протечки ключа в браузер.
var subscriptionKeyFields = []string{"vpn_key", "vpnKey"}

// scrubAccountInfo вырезает ключ подписки из ответа портала. Работает и когда
// конверт data есть, и когда его нет. Неожиданная форма — ошибка, а не пустой
// объект: пустой каталог пользователь прочитает как «в подписке нет стран».
func scrubAccountInfo(raw []byte) (json.RawMessage, error) {
	// Разбор в map[string]json.RawMessage, а не в map[string]any: значения
	// уезжают наружу как пришли. Через any числа проехали бы float64 и
	// потеряли точность.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("%w: ответ account-info не разобран: %w", ErrServiceUnavailable, err)
	}
	fields := top
	if inner, ok := top["data"]; ok {
		fields = nil
		if err := json.Unmarshal(inner, &fields); err != nil {
			return nil, fmt.Errorf("%w: конверт data в account-info — не объект: %w", ErrServiceUnavailable, err)
		}
	}
	// JSON null разбирается в nil-мапу без ошибки: без этой проверки пустой
	// ответ дошёл бы до интерфейса как подписка без стран.
	if fields == nil {
		return nil, fmt.Errorf("%w: account-info без данных", ErrServiceUnavailable)
	}

	for _, field := range subscriptionKeyFields {
		delete(fields, field)
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("%w: сборка account-info: %w", ErrServiceUnavailable, err)
	}
	return out, nil
}

// errNoConf — внутренний признак «в этой строке конфигурации нет».
var errNoConf = errors.New("конфигурации нет")

// extractConf достаёт .conf из ответа портала. Сначала разбирается ответ,
// потом берётся значение поля, и только потом решается, .conf это или
// vpn://-ссылка: проверка подстроки на сыром ответе отдала бы наружу целиком
// JSON-конверт — он содержит [Interface] внутри экранированной строки.
func extractConf(raw []byte) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// Не JSON — портал отдал сам .conf или ссылку текстом.
		conf, cerr := confFromCandidate(string(raw))
		if cerr != nil {
			return "", fmt.Errorf("%w: ответ download-config не разобран: %w", ErrServiceUnavailable, err)
		}
		return conf, nil
	}
	for _, cand := range jsonStrings(v, nil) {
		if conf, err := confFromCandidate(cand); err == nil {
			return conf, nil
		}
	}
	return "", fmt.Errorf("%w: в ответе download-config нет конфигурации", ErrServiceUnavailable)
}

func confFromCandidate(s string) (string, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "vpn://") {
		conf, err := DecodeVPNLinkToConf(s)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(conf) == "" {
			return "", errNoConf
		}
		return conf, nil
	}
	if strings.Contains(s, "[Interface]") {
		return s, nil
	}
	return "", errNoConf
}

// jsonStrings собирает строковые значения ответа. Обход детерминированный:
// ключи объектов перебираются по порядку, а не по случайному порядку map —
// иначе выбор конфигурации из ответа с несколькими строками зависел бы от
// запуска.
func jsonStrings(v any, out []string) []string {
	switch x := v.(type) {
	case string:
		out = append(out, x)
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			out = jsonStrings(x[k], out)
		}
	case []any:
		for _, item := range x {
			out = jsonStrings(item, out)
		}
	}
	return out
}
