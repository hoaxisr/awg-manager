package amneziacp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

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

// ErrOutcomeUnknown — РАСХОДНЫЙ запрос до портала дошёл, а чем он у портала
// кончился, мы не знаем. Дороги сюда три, общее у них одно: слот устройства
// подписки МОГ быть списан, и повтор стоит пользователю второго слота.
//
//   - Перенаправление в ответе: 3xx означает либо «веб-приложение гонит на
//     страницу входа» (запрос не обработан), либо «ручка отдаёт подписанную
//     ссылку» (обработан) — различить нечем, см. statusRecovery.
//   - Сетевой отказ ПОСЛЕ того, как запрос ушёл в сеть: обрыв до ответа,
//     таймаут, отмена контекста в полёте. Портал мог обработать запрос и
//     потерять соединение на ответе.
//   - Ответ успеха, из которого конфигурации не собрать: нераспознанный
//     конверт, эхо ключа вместо конфигурации, тело больше предела, обрыв
//     чтения тела.
//
// Третья дорога прежде утверждала потраченный слот как ФАКТ и имела ради этого
// свой сентинел. Основанием было только «статус 2xx», а основание негодное:
// между нами и порталом стоит WAF/CDN (ради него и заведён browserUA), и его
// интерстишл с кодом 200 попадает ровно сюда; адрес портала к тому же
// резолвится с зеркала, которое задаёт сам пользователь, — любой отвечающий
// 200 хост порождал ложное «слот потрачен». Отличать известный исход от
// неизвестного оказалось нечем, и сентинел остался один: совет пользователю у
// всех трёх дорог всё равно общий — посмотреть счётчик устройств у портала.
//
// НЕ обёрнут в ErrServiceUnavailable сознательно: смысл того — «повторите», а
// здесь вызывающий обязан сказать человеку «проверьте, не выдалась ли
// конфигурация», а не «попробуйте ещё раз» (F200).
var ErrOutcomeUnknown = errors.New("исход запроса к Amnezia неизвестен")

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

	// eventMirrorResolve — имя операции резолва зеркала в журнале. Своя
	// операция, а не операция запроса: резолв идёт до того, как станет
	// известно, к какой ручке портала собирались.
	eventMirrorResolve = "mirror-resolve"

	// defaultRemember — значение remember в теле входа, пока явного входа не
	// было. true: сессию держит демон, а не человек за браузером, и долгая
	// cookie — это меньше входов с роутера. С флагом «запомнить ключ на
	// роутере» ничего общего не имеет: там речь про хранение секрета у нас,
	// здесь — про срок cookie у CP.
	defaultRemember = true
)

// LogFunc — узкий колбэк журналирования: event ложится в поле события, detail
// — в сообщение (ср. logging.ScopedLogger.Info). Пакет не знает ни про
// журнал приложения, ни про его уровни.
type LogFunc func(event, detail string)

// recovery — подсказка, что делать после неудачной попытки.
type recovery int

const (
	recoveryNone     recovery = iota // повторять нечего
	recoverySession                  // cookie протухла: войти заново
	recoveryRedirect                 // перенаправление: сессию сбросить, но исход запроса неизвестен
	recoveryMirror                   // хост не отвечает: перерезолвить зеркало
)

// cpRequest описывает один вызов портала.
type cpRequest struct {
	event   string // имя операции для журнала
	method  string
	path    string
	referer string // путь Referer'а, как у веб-приложения портала
	payload []byte
	// repeatable == false запрещает повтор запроса после сетевого отказа:
	// портал мог успеть обработать запрос до обрыва, и повтор расходной ручки
	// съест второй слот устройства подписки. Нулевое значение — запрет:
	// повторяемость объявляется явно.
	repeatable bool
}

// MirrorURLFunc отдаёт адрес зеркала, SubscriptionKeyFunc — ключ подписки.
// Имена типов — документация сигнатуры, а не защита: func-литерал приводится к
// любому из них, поэтому перепутанные местами геттеры компилируются. Поймать
// перестановку можно только чтением вызова.
type MirrorURLFunc func() string

// SubscriptionKeyFunc — см. MirrorURLFunc.
type SubscriptionKeyFunc func() string

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
	mirrorURL MirrorURLFunc
	key       SubscriptionKeyFunc
	logf      LogFunc

	mu     sync.Mutex
	origin string // хост, выдавший сессию
	keyID  string // отпечаток ключа, которым сессия получена
	sid    string
	// remember — срок cookie, которого просил ПОСЛЕДНИЙ УДАВШИЙСЯ явный вход
	// (CheckKey). Неявный ре-логин при протухшей сессии повторяет его: сессия
	// у демона одна, спросить пользователя в этот момент не у кого, а
	// подставить туда своё значение — значит молча отменить его выбор.
	remember bool
}

// NewClient собирает клиента. httpClient == nil подменяется собственным прямым
// клиентом: маршрут загрузок в этой линии не участвует. Геттеры и logf
// обязательны, и их отсутствие — паника на сборке зависимостей: иначе
// nil-журнал уронит демон на первом же запросе пользователя, а не при запуске.
func NewClient(httpClient *http.Client, mirrorURL MirrorURLFunc, key SubscriptionKeyFunc, logf LogFunc) *Client {
	if mirrorURL == nil {
		panic("amneziacp.NewClient: геттер адреса зеркала обязателен")
	}
	if key == nil {
		panic("amneziacp.NewClient: геттер ключа подписки обязателен")
	}
	if logf == nil {
		panic("amneziacp.NewClient: журнал обязателен")
	}
	if httpClient == nil {
		httpClient = newDirectClient()
	}
	return &Client{
		http:      withoutRedirects(httpClient),
		mirror:    NewMirror(httpClient, 0),
		mirrorURL: mirrorURL,
		key:       key,
		logf:      logf,
		remember:  defaultRemember,
	}
}

// withoutRedirects копирует клиента и запрещает следовать редиректам. Политика
// нужна на пути к порталу: на 307/308 Go переигрывает тело запроса — ключ
// подписки уехал бы на хост из Location, — а на 301/302 документ чужого хоста
// приехал бы как ответ портала. С запретом 3xx доезжает до проверки статуса и
// становится отказом. На путь к зеркалу политика не ставится: запрос туда без
// тела и без секрета, а адрес зеркала вводит пользователь — апгрейд протокола,
// хвостовой слэш и сокращатель приезжают перенаправлением, и запрет сломал бы
// резолв на ровном месте.
//
// Копия, а не правка переданного клиента: объект чужой, его политика — не наше
// дело. Хранилище cookie в копию не берётся: сессию мы ставим заголовком сами,
// а жившая в хранилище вызывающего cookie дописалась бы к нему второй.
func withoutRedirects(c *http.Client) *http.Client {
	dup := *c
	dup.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	dup.Jar = nil
	return &dup
}

// newDirectClient — прямой клиент к зеркалу и порталу.
//
// База — httpclient.NewTransport ради пина HTTP/1.1 (ForceAttemptHTTP2=false).
// Пин нужен из-за портала: на h2 он отвечает EOF и «malformed HTTP response» —
// ровно та поломка, ради которой httpclient и заведён. Зеркало лежит на
// хранилище Google и с h2 работает, общего фронта у них нет; один транспорт на
// оба пути — потому что клиент один, а не потому что фронт общий.
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
	body, _, err := c.call(ctx, cpRequest{
		event:   "account-info",
		method:  http.MethodGet,
		path:    "/api/account-info",
		referer: "/ru",
		// Чтение ничего не тратит: повтор после сетевого отказа безопасен.
		repeatable: true,
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
	body, key, err := c.call(ctx, cpRequest{
		event:   "download-config",
		method:  http.MethodPost,
		path:    "/api/download-config",
		referer: "/ru",
		payload: payload,
		// repeatable не ставится сознательно: портал мог выдать конфиг и
		// потерять соединение на ответе, а повтор съел бы второй слот.
	})
	if err != nil {
		return "", err
	}
	return extractConf(body, key)
}

// CheckKey проверяет присланный ключ входом в портал. Неудача текущую сессию
// не трогает: пользователь мог ввести чужой ключ, уже работающая подписка от
// этого не обязана отваливаться. Успех, наоборот, сессию занимает — ключ
// сохраняется уже после проверки, и неудача сохранения не должна отменять
// состоявшийся вход.
//
// remember — срок cookie, которого просит пользователь; сюда он приходит
// аргументом, а не константой, потому что это его выбор, а не наш. Значение
// запоминается ТОЛЬКО на удавшемся входе: отвергнутый ключ не должен менять
// политику сессии, которая уже работает.
func (c *Client) CheckKey(ctx context.Context, key string, remember bool) error {
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
		sid, rec, err := c.login(ctx, origin, key, remember)
		if err == nil {
			c.adopt(origin, keyFingerprint(key), sid)
			c.setRemember(remember)
			return nil
		}
		lastErr = err
		// Вход ничего не тратит: повторяем его наравне с чтением.
		if !c.again(ctx, rec, attempt, true) {
			return lastErr
		}
	}
}

// ResetSession выбрасывает сессию: следующий вызов войдёт заново.
func (c *Client) ResetSession() {
	c.mu.Lock()
	c.origin, c.keyID, c.sid = "", "", ""
	c.mu.Unlock()
}

// call выполняет запрос под сессией, восстанавливая её при протухании и
// перерезолвя зеркало при сетевом отказе. Вторым значением отдаётся ключ,
// которым запрос сделан: разбору ответа он нужен, чтобы не принять эхо ключа
// за данные, а повторное чтение геттера дало бы уже другое значение.
func (c *Client) call(ctx context.Context, req cpRequest) ([]byte, string, error) {
	// Пустой ключ отсекается до любого похода в сеть — и к порталу, и к
	// зеркалу: резолв ради заведомо невозможного запроса бессмыслен.
	key := strings.TrimSpace(c.key())
	if key == "" {
		return nil, "", ErrNoKey
	}

	var lastErr error
	for attempt := 0; ; attempt++ {
		origin, err := c.resolve(ctx)
		if err != nil {
			return nil, "", err
		}
		// Отказ резолва и отказ входа повторяемы независимо от самого запроса:
		// до расходной ручки портала дело ещё не дошло.
		repeatable := true
		sid, rec, err := c.session(ctx, origin, key)
		if err == nil {
			var body []byte
			body, rec, err = c.send(ctx, origin, sid, req)
			if err == nil {
				return body, key, nil
			}
			repeatable = req.repeatable
		}
		lastErr = err
		if !c.again(ctx, rec, attempt, repeatable) {
			return nil, "", lastErr
		}
	}
}

// again решает, делать ли повтор, и готовит к нему клиента.
//
// Порядок проверок значим. Отменённый контекст повтором не лечится и кэш
// адреса не трогает: кэш общий, и пользователь, закрывший вкладку, не должен
// ломать резолв остальным. Мёртвый хост, наоборот, выбрасывается из кэша даже
// когда повтора не будет (расходный запрос, исчерпанные попытки) — иначе
// следующая попытка пользователя пойдёт на тот же труп до конца TTL.
//
// repeatable запрещает повтор везде, где исход запроса неизвестен: после
// сетевого отказа (портал мог обработать расходный запрос и потерять
// соединение на ответе) и после перенаправления (302 на подписанную ссылку
// скачивания — форма успеха, которую наш запрет редиректов превращает в
// отказ). Исключение одно — recoverySession: отказ авторизации определённый
// ответ, портал запрос отверг и слот не потратил, а без повтора протухшая
// сессия читается пользователем как отклонённый ключ.
func (c *Client) again(ctx context.Context, rec recovery, attempt int, repeatable bool) bool {
	if rec == recoveryNone || ctx.Err() != nil {
		return false
	}
	if rec == recoveryMirror {
		// Хост в мета-теге ротируется: мёртвый адрес обязан быть добыт заново,
		// а не дожить в кэше до конца TTL.
		c.mirror.Invalidate()
	}
	if rec != recoverySession && !repeatable {
		return false
	}
	return attempt+1 < maxAttempts
}

func (c *Client) resolve(ctx context.Context) (string, error) {
	mirrorURL := c.mirrorURL()
	origin, err := c.mirror.Origin(ctx, mirrorURL)
	if err != nil {
		// Отказ резолва — самая вероятная жалоба в этой линии, и разбирать её
		// без строки в журнале не по чему. Адрес зеркала секретом не является.
		c.logf(eventMirrorResolve, fmt.Sprintf("mirror=%s route=direct resolve=%v", mirrorURL, err))
		// Недоступное зеркало — тот же класс, что и молчащий портал. Своя
		// причина остаётся различимой по ErrMirrorUnavailable и соседям.
		return "", fmt.Errorf("%w: %w", ErrServiceUnavailable, err)
	}
	return origin, nil
}

// session отдаёт cookie для указанного хоста, входя при необходимости. Сессия
// хранится вместе с origin, который её выдал, и с отпечатком ключа, которым
// получена: cookie одного хоста зеркала на другом недействительна, а сессия
// чужого ключа показала бы каталог чужой подписки и потратила бы её слот.
// Поэтому смена любого из двух обнуляет sid сама.
func (c *Client) session(ctx context.Context, origin, key string) (string, recovery, error) {
	id := keyFingerprint(key)
	c.mu.Lock()
	if c.origin == origin && c.keyID == id && c.sid != "" {
		sid := c.sid
		c.mu.Unlock()
		return sid, recoveryNone, nil
	}
	c.mu.Unlock()

	// Одновременный промах даёт два входа — это допустимо: вход чужой квоты не
	// тратит, а держать лок на время сетевого запроса дороже.
	sid, rec, err := c.login(ctx, origin, key, c.rememberPolicy())
	if err != nil {
		return "", rec, err
	}
	c.adopt(origin, id, sid)
	return sid, recoveryNone, nil
}

// setRemember / rememberPolicy — политика срока cookie под тем же локом, что
// и сессия: явный вход её задаёт, неявный ре-логин повторяет.
func (c *Client) setRemember(v bool) {
	c.mu.Lock()
	c.remember = v
	c.mu.Unlock()
}

func (c *Client) rememberPolicy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remember
}

func (c *Client) adopt(origin, keyID, sid string) {
	c.mu.Lock()
	c.origin, c.keyID, c.sid = origin, keyID, sid
	c.mu.Unlock()
}

// keyFingerprint — отпечаток ключа подписки. Хранится вместо самого ключа:
// секрет и так живёт у владельца геттера, класть его в объект второй раз
// незачем. Наружу отпечаток не отдаётся.
func keyFingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// dropSession выбрасывает сессию, если она всё ещё та, на которой случился
// отказ: параллельный вызов мог уже войти заново.
func (c *Client) dropSession(sid string) {
	c.mu.Lock()
	if c.sid == sid {
		c.origin, c.keyID, c.sid = "", "", ""
	}
	c.mu.Unlock()
}

func (c *Client) login(ctx context.Context, origin, key string, remember bool) (string, recovery, error) {
	payload, err := json.Marshal(map[string]any{"vpnKey": key, "remember": remember})
	if err != nil {
		return "", recoveryNone, fmt.Errorf("%w: тело входа: %w", ErrServiceUnavailable, err)
	}

	resp, rec, err := c.do(ctx, origin, "", cpRequest{
		event:   "login",
		method:  http.MethodPost,
		path:    "/api/login",
		referer: "/ru/login",
		payload: payload,
		// Вход ничего не тратит — повторять его можно. Объявляется явно,
		// потому что нулевое значение поля означает запрет, а повторяют вход
		// оба вызывающих (CheckKey и call передают repeatable=true своими
		// руками). Читает поле statusError: без этой строки перенаправление
		// на входе уехало бы наружу как «исход неизвестен», хотя слот
		// подписки вход не тратит.
		repeatable: true,
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
		if rec == recoverySession || rec == recoveryRedirect {
			c.dropSession(sid)
		}
		return nil, rec, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCPBody+1))
	if err != nil {
		return nil, recoveryNone, fmt.Errorf("%w: чтение ответа %s: %w", outcomeSentinel(req), req.path, err)
	}
	if len(body) > maxCPBody {
		return nil, recoveryNone, fmt.Errorf("%w: ответ %s больше %d байт", outcomeSentinel(req), req.path, maxCPBody)
	}
	return body, recoveryNone, nil
}

// outcomeSentinel — причина отказа там, где запрос УЖЕ уехал в портал, а чем
// он у портала кончился, мы не знаем.
//
// У расходной (неповторяемой) ручки такой отказ обязан нести
// ErrOutcomeUnknown: слот устройства подписки мог быть списан, и пользователь,
// прочитавший «попробуйте позже», тратит второй. У повторяемых ручек повтор
// безвреден — чтение слота не тратит, — и причина остаётся прежней.
func outcomeSentinel(req cpRequest) error {
	if req.repeatable {
		return ErrServiceUnavailable
	}
	return ErrOutcomeUnknown
}

// do шлёт один запрос к порталу и классифицирует отказ. Тело успешного ответа
// остаётся незакрытым — его закрывает вызывающий; при отказе тело не читается
// вовсе: наружу идёт наш текст, а страница ошибки CDN бывает большой.
func (c *Client) do(ctx context.Context, origin, sid string, req cpRequest) (*http.Response, recovery, error) {
	var body io.Reader
	if len(req.payload) > 0 {
		body = bytes.NewReader(req.payload)
	}
	// sent — ушёл ли запрос в сеть целиком. Единственный доступный нам признак
	// того, что портал запрос ВИДЕЛ: после этого момента любой сетевой отказ
	// (обрыв до ответа, таймаут, отмена контекста в полёте) оставляет исход
	// расходной операции неизвестным. Признак берётся у транспорта, а не
	// угадывается по тексту ошибки: *url.Error одинаков и для мёртвого хоста,
	// и для обрыва на чтении ответа.
	var sent atomic.Bool
	traced := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				sent.Store(true)
			}
		},
	})
	httpReq, err := http.NewRequestWithContext(traced, req.method, origin+req.path, body)
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
		c.logf(req.event, fmt.Sprintf("origin=%s route=direct %s %s sent=%t network=%v", origin, req.method, req.path, sent.Load(), err))
		// Запрос, успевший уйти в сеть, портал МОГ обработать и потерять
		// соединение на ответе: у расходной ручки это стоило слота устройства
		// подписки, и звать пользователя повторить нельзя (F200). Не ушедший
		// запрос — мёртвый хост, отказ соединения, отмена до записи — не видел
		// никто, и повтор там безопасен и правилен.
		sentinel := ErrServiceUnavailable
		if sent.Load() {
			sentinel = outcomeSentinel(req)
		}
		return nil, recoveryMirror, fmt.Errorf("%w: %s %s: %w", sentinel, req.method, req.path, err)
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
	if code == http.StatusUnauthorized {
		// Протухшая cookie: войти заново и повторить.
		//
		// 403 сюда НЕ входит, хотя причина отказа у них общая (см.
		// statusError): 401 — «кто ты», то есть правдоподобно истёкшая
		// сессия, а 403 — «нельзя», то есть политика или исчерпанная квота.
		// Вход заново её не меняет, а повтор расходной ручки после него
		// стоит второго слота устройства подписки.
		return recoverySession
	}
	if code/100 == 3 {
		// Перенаправление означает одно из двух, и различить их мы не можем:
		// либо веб-приложение гонит на страницу входа (тогда cookie протухла),
		// либо ручка отвечает ссылкой (тогда запрос обработан, и у расходной
		// ручки слот уже потрачен). Отсюда своя подсказка, а не recoverySession
		// и не recoveryNone: сессию роняем на случай первого — иначе мёртвая
		// cookie доживёт в кэше до перезапуска демона, повторяя тот же ответ на
		// каждый вызов; повтор запрещаем на случай второго — иначе расходный
		// запрос уходит дважды. Повтор разрешён только там, где он безвреден,
		// то есть у повторяемых ручек.
		return recoveryRedirect
	}
	return recoveryNone
}

// statusError переводит статус портала в нашу причину отказа. Наружу статус не
// уходит: отозванный premium-ключ не должен разлогинивать панель.
//
// Перенаправление у НЕповторяемой (то есть расходной) ручки — свой класс:
// повторять его нельзя, потому что 302 мог быть формой успеха и слот уже
// потрачен. У повторяемых ручек этого различия нет — там повтор безвреден, и
// 3xx остаётся обычным «сервис недоступен».
func statusError(code int, req cpRequest) error {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity:
		// 422 портал отдаёт на непригодный ключ во входе — это тот же класс,
		// что 401/403, а не «сервис лежит».
		return fmt.Errorf("%w: %s %s", ErrKeyRejected, req.method, req.path)
	}
	if code/100 == 3 {
		return fmt.Errorf("%w: %s %s ответил %d", outcomeSentinel(req), req.method, req.path, code)
	}
	return fmt.Errorf("%w: %s %s ответил %d", ErrServiceUnavailable, req.method, req.path, code)
}

func sessionFromResponse(resp *http.Response) string {
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			return strings.TrimSpace(c.Value)
		}
	}
	return ""
}

// vpnLinkScheme — префикс ссылки подписки. Ключ подписки Amnezia сам является
// vpn://-ссылкой, поэтому значение — признак секрета наравне с именем поля
// (см. subscriptionKeyFields): все имена живого ответа перечислить нельзя, а
// «vpn://» узнаётся само.
const vpnLinkScheme = "vpn://"

// decodeJSON разбирает ответ портала с сохранением точности чисел: через
// обычный any большие целые проехали бы float64 и потеряли значение (живой
// ответ несёт счётчики). Хвост после первого значения — отказ: ответ портала
// это один документ, а не поток.
func decodeJSON(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("лишние данные после JSON-документа")
	}
	return v, nil
}

// scrubAccountInfo вырезает ключ подписки из ответа портала. Работает и когда
// конверт data есть, и когда его нет. Неожиданная и пустая форма — ошибка, а
// не пустой объект: пустой каталог пользователь прочитает как «в подписке нет
// стран».
func scrubAccountInfo(raw []byte) (json.RawMessage, error) {
	doc, err := decodeJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: ответ account-info не разобран: %w", ErrServiceUnavailable, err)
	}
	fields, ok := doc.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: account-info — не объект", ErrServiceUnavailable)
	}
	if inner, wrapped := fields["data"]; wrapped {
		if fields, ok = inner.(map[string]any); !ok {
			// JSON null разбирается в nil-значение, а не в объект: без этой
			// ветки пустой ответ дошёл бы до интерфейса как подписка без стран.
			return nil, fmt.Errorf("%w: конверт data в account-info — не объект", ErrServiceUnavailable)
		}
	}
	// Пустота проверяется ПОСЛЕ скраба: ответ, от которого после вычистки
	// ничего не осталось, наружу уходить не должен — пустой каталог
	// пользователь прочитает как «в подписке нет стран».
	scrubSecrets(fields)
	if len(fields) == 0 {
		return nil, fmt.Errorf("%w: account-info без данных", ErrServiceUnavailable)
	}

	out, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("%w: сборка account-info: %w", ErrServiceUnavailable, err)
	}
	return out, nil
}

// subscriptionKeyFields — имена полей, несущих сам ключ подписки. Чёрный список
// живёт поверх поиска по значению, а не вместо него: портал волен отдать ключ
// обрезанным или в своей кодировке, схемы в значении тогда нет, а имя поля то
// же. Обратное тоже верно — имена живого ответа перечислить нельзя, — поэтому
// нужны оба признака.
//
// Имена — в нижнем регистре: сопоставление регистронезависимо (см.
// scrubSecrets), как и у поиска поля конфигурации, потому что регистр имени
// выбирает портал. Запись с заглавной буквой в этом списке была бы мёртвой.
var subscriptionKeyFields = []string{"vpn_key", "vpnkey"}

// secretMarker заменяет вырезанный секрет. Замена, а не удаление: удаление
// уносит поле со свободным текстом целиком и сдвигает индексы массива, по
// которым фронт считает длину.
const secretMarker = "[вырезано]"

// scrubSecrets вычищает ключ подписки из разобранного ответа портала на месте.
// Копии нет сознательно: значение только что разобрано здесь же, владелец у
// него один, а вторая копия дерева — самый дорогой путь этой фичи на роутере
// со 128 МБ. Удаление ключей во время обхода map спецификация Go разрешает:
// удалённые записи просто не выдаются.
//
// Обход рекурсивный, признаков два — имя поля и значение: ключ течёт вложенным
// объектом, элементом массива, переименованным полем, вложенным конвертом и
// текстом сообщения об ошибке.
func scrubSecrets(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, item := range x {
			if slices.Contains(subscriptionKeyFields, strings.ToLower(k)) {
				delete(x, k)
				continue
			}
			if s, isStr := item.(string); isStr {
				x[k] = maskSecret(s)
				continue
			}
			scrubSecrets(item)
		}
	case []any:
		for i, item := range x {
			if s, isStr := item.(string); isStr {
				x[i] = maskSecret(s)
				continue
			}
			scrubSecrets(item)
		}
	}
}

// maskSecret заменяет маркером каждую vpn://-ссылку в строке, оставляя
// остальной текст на месте.
//
// Один проход со сборкой результата, а не пересборка всей строки на каждом
// вхождении: тело приезжает с адреса, который задаёт пользователь, вход не
// доверенный, а целевое железо — MIPS-роутер. Проход эквивалентен поиску с
// начала после каждой замены: до первого вхождения ссылок нет по построению, а
// маркер новых не создаёт — ни сам, ни на стыках.
func maskSecret(s string) string {
	at := strings.Index(s, vpnLinkScheme)
	if at < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for at >= 0 {
		b.WriteString(s[:at])
		b.WriteString(secretMarker)
		// Ссылка кончается там, где начинается пробел: в base64url его нет.
		rest := s[at:]
		if i := strings.IndexFunc(rest, unicode.IsSpace); i >= 0 {
			s = rest[i:]
		} else {
			s = ""
		}
		at = strings.Index(s, vpnLinkScheme)
	}
	b.WriteString(s)
	return b.String()
}

// errNoConf — внутренний признак «в этой строке конфигурации нет».
var errNoConf = errors.New("конфигурации нет")

// confFields — имена полей JSON-ответа, несущих конфигурацию. Живой
// download-config отдаёт .conf текстом (см. extractConf), но конверт мог бы
// прийти от другой ручки или другого тарифа. Сканировать все строки ответа
// нельзя: ключ подписки сам валидная vpn://-ссылка, и его эхо в ответе-ошибке
// уехало бы пользователю как конфигурация — чужой регион и приватный ключ всей
// подписки в файле туннеля.
//
// Имя одно и живым ответом НЕ подтверждено: в текстовой форме полей нет вовсе,
// «config» здесь — самая вероятная догадка. Список из нескольких придуманных
// имён был бы конфигурируемостью под форму, которой не существует.
var confFields = []string{"config"}

// extractConf достаёт .conf из ответа портала.
//
// Живая форма (снята 2026-09-11) — не JSON: готовый .conf с заголовком из
// комментариев, в одном из которых лежит сам ключ подписки. Поэтому порядок
// такой: сперва пробуем разобрать JSON (валидный .conf в JSON не разбирается,
// а вот JSON-конверт содержит «[Interface]» внутри экранированной строки — и
// проверка подстроки на сыром ответе отдала бы наружу весь конверт), и только
// не разобрав — читаем тело как .conf или как ссылку.
//
// key — ключ, которым сделан запрос: его эхо конфигурацией не считается.
func extractConf(raw []byte, key string) (string, error) {
	doc, err := decodeJSON(raw)
	if err != nil {
		conf, cerr := confFromCandidate(string(raw), key)
		if cerr != nil {
			return "", fmt.Errorf("%w: ответ download-config не разобран: %w", ErrOutcomeUnknown, err)
		}
		return conf, nil
	}
	for _, cand := range confCandidates(doc, nil) {
		if conf, err := confFromCandidate(cand, key); err == nil {
			return conf, nil
		}
	}
	return "", fmt.Errorf("%w: в ответе download-config нет конфигурации", ErrOutcomeUnknown)
}

func confFromCandidate(s, key string) (string, error) {
	s = strings.TrimSpace(s)
	// Эхо ключа подписки конфигурацией не является, даже когда разбирается как
	// ссылка: в нём вся подписка, а не выбранная страна.
	if keyEcho(s, key) {
		return "", errNoConf
	}
	if strings.HasPrefix(s, vpnLinkScheme) {
		conf, err := DecodeVPNLinkToConf(s)
		if err != nil {
			return "", err
		}
		conf = withoutKeyLines(conf)
		if conf == "" {
			return "", errNoConf
		}
		return conf, nil
	}
	if strings.Contains(s, "[Interface]") {
		return withoutKeyLines(s), nil
	}
	return "", errNoConf
}

// keyEcho сообщает, что кандидат несёт наш же ключ подписки, а не
// конфигурацию страны.
//
// Сравнение идёт по СОДЕРЖИМОМУ ссылки, а не по её написанию. Равенство строк
// закрывало ровно одну запись из многих, и обход доказан зондом: ту же ссылку
// портал волен вернуть в обычном алфавите base64 вместо URL-безопасного
// (base64URLDecode сам мапит '-'→'+' и '_'→'/'), с хвостовыми '=' или без них,
// с неканоническими битами в последнем символе, в другом регистре схемы, с
// обрамляющими пробелами — байты те же, строка другая, и наружу уезжала
// конфигурация всей подписки вместе с её приватным ключом.
//
// Сравниваются байты полезной нагрузки — то, ВО ЧТО декодируется текст
// ссылки; это закрывает весь класс форм её записи. Пересжатие или переупаковка
// нагрузки (те же данные, другие байты) им не закрыты: портал возвращает то,
// что мы ему прислали, и вводить ради непронаблюдённой формы сравнение
// распакованных конфигураций — значит завести новый отказ, при котором
// законная конфигурация страны, совпавшая с конфигурацией подписки, перестанет
// выдаваться.
func keyEcho(candidate, key string) bool {
	if key == "" {
		return false
	}
	candRaw, candOK := vpnLinkPayload(candidate)
	keyRaw, keyOK := vpnLinkPayload(key)
	if !candOK || !keyOK {
		// Разобрать нечего — сравнивать остаётся написание, приведённое к
		// общему виду. Отказ по умолчанию закрытый: сомнительное совпадение
		// лучше засчитать эхом, чем выдать ключ подписки за конфигурацию.
		return strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(key))
	}
	return bytes.Equal(candRaw, keyRaw)
}

// vpnLinkPayload отдаёт полезную нагрузку vpn://-ссылки. Схема сравнивается
// без учёта регистра: её пишет портал, а не мы.
func vpnLinkPayload(s string) ([]byte, bool) {
	s = strings.TrimSpace(s)
	if len(s) <= len(vpnLinkScheme) || !strings.EqualFold(s[:len(vpnLinkScheme)], vpnLinkScheme) {
		return nil, false
	}
	raw, err := base64URLDecode(s[len(vpnLinkScheme):])
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	return raw, true
}

// withoutKeyLines вырезает из .conf строки с ключом подписки. Живой
// download-config отдаёт заголовок вида «# VPN Key: vpn://…» перед
// [Interface], и без вырезания ключ всей подписки лёг бы в файл туннеля на
// флеш и в предпросмотр в интерфейсе. Режется строка целиком: «vpn://» бывает
// в этом файле только в комментарии-заголовке, значением параметра WireGuard
// или AWG такая строка не бывает.
func withoutKeyLines(conf string) string {
	if !strings.Contains(conf, vpnLinkScheme) {
		return strings.TrimSpace(conf)
	}
	lines := strings.Split(conf, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.Contains(line, vpnLinkScheme) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// confCandidates собирает значения полей из confFields. Обход
// детерминированный: ключи объектов перебираются по порядку, а не по
// случайному порядку map — иначе выбор конфигурации из ответа с несколькими
// полями зависел бы от запуска.
func confCandidates(v any, out []string) []string {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			if s, isStr := x[k].(string); isStr {
				if slices.Contains(confFields, strings.ToLower(k)) {
					out = append(out, s)
				}
				continue
			}
			out = confCandidates(x[k], out)
		}
	case []any:
		for _, item := range x {
			out = confCandidates(item, out)
		}
	}
	return out
}
