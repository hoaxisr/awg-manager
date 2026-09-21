package httpclient

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"
)

// MaxRedirectHops — единый предел перенаправлений для загрузок по адресу,
// который ввёл пользователь (подписки, панели, списки).
const MaxRedirectHops = 5

// lookupIP — seam для резолвера (подменяется в тестах).
var lookupIP = net.LookupIP

// ValidatePublicURL — ранний отказ на адресе, ведущем внутрь роутера.
//
// Приватные диапазоны (LAN) намеренно НЕ блокируются — сервер подписки в LAN
// легитимен. Блок только loopback/link-local/unspecified: закрывает RCI
// localhost:79 и метадату. DNS-rebinding закрыт BlockInternalDial: фактический
// IP каждого connect проверяется повторно, резолв здесь — ранний отказ и
// defense-in-depth.
func ValidatePublicURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("некорректный URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("URL должен быть http(s)")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("URL без хоста")
	}
	if strings.EqualFold(host, "localhost") {
		return errors.New("URL указывает на внутренний адрес")
	}
	ips, err := lookupIP(host)
	if err != nil {
		return fmt.Errorf("не удалось разрешить хост: %w", err)
	}
	for _, ip := range ips {
		if isInternalIP(ip) {
			return errors.New("URL указывает на внутренний адрес")
		}
	}
	return nil
}

// isInternalIP — seam: AllowInternalDialForTest снимает страж, чтобы тесты
// потребителей могли ходить на httptest (он живёт на loopback).
var isInternalIP = internalIP

func internalIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// AllowInternalDialForTest снимает страж внутренних адресов (ValidatePublicURL
// и BlockInternalDial) и возвращает восстановление. Только тесты: httptest
// слушает loopback, который страж закрывает намеренно. Вне go test — паника:
// рубильник SSRF-стража не должен держаться на договорённости. Не
// потокобезопасен: тест, дёргающий его посреди прогона, не должен быть
// параллельным.
func AllowInternalDialForTest() (restore func()) {
	if !testing.Testing() {
		panic("httpclient: AllowInternalDialForTest вызван вне go test")
	}
	orig := isInternalIP
	isInternalIP = func(net.IP) bool { return false }
	return func() { isInternalIP = orig }
}

// BlockInternalDial — Control диалера: проверяет фактически подключаемый IP в
// момент dial (после резолва, перед connect), закрывая DNS-rebinding.
func BlockInternalDial(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip != nil && isInternalIP(ip) {
		return errors.New("адрес резолвится во внутренний адрес")
	}
	return nil
}

// RedirectPolicy — CheckRedirect: предел хопов и запрет спуска https→http.
// guard (может быть nil) проверяет адрес каждого хопа.
//
// Спуск запрещён, а не перенаправление вообще: в адресе живёт токен, и после
// 302 на http он и заголовки уехали бы открытым текстом — net/http снимает
// чувствительные заголовки при смене ХОСТА, схема на это не влияет. Апгрейд
// и переход внутри https сервер использует сам.
func RedirectPolicy(maxHops int, guard func(string) error) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxHops {
			return fmt.Errorf("слишком много перенаправлений (предел %d)", maxHops)
		}
		// net/http зовёт CheckRedirect только с непустым via; функция
		// экспортирована, поэтому пустой via — не паника, а «нечего проверять».
		if len(via) > 0 {
			if prev := via[len(via)-1]; prev.URL.Scheme == "https" && req.URL.Scheme != "https" {
				return fmt.Errorf("перенаправление с https на %q отклонено: в адресе может жить токен", req.URL.Scheme)
			}
		}
		if guard != nil {
			return guard(req.URL.String())
		}
		return nil
	}
}

// NewPublicClient — клиент для загрузки по адресу, введённому пользователем:
// прямой выход, страж внутренних адресов на dial, политика редиректов.
// insecureTLS снимает проверку сертификата — только там, где это решено
// (панель Phobos на самоподписанном TLS, Q14).
//
// Транспорт — канонический NewTransport (таймауты рукопожатия, keep-alive
// снят, ForceAttemptHTTP2=false и пин ALPN http/1.1 — см. buildTransport о
// том, почему одного флага мало). ProxyDirect обязателен и это защита, а не
// умолчание: страж SSRF — Control диалера, он смотрит на адрес, который
// РЕАЛЬНО диалится. С прокси диалится прокси, а внутренний адрес уезжает ему
// строкой в запросе — страж молча перестаёт закрывать что-либо.
func NewPublicClient(timeout time.Duration, insecureTLS bool) *http.Client {
	tr, err := NewTransport(TransportConfig{Proxy: ProxyDirect})
	if err != nil {
		// NewTransport отказывает только на непустом ProxyURL.
		panic("httpclient: NewTransport без прокси: " + err.Error())
	}
	// Диалер без привязки к интерфейсу — тот же, что собрал NewTransport, плюс
	// Control со стражем. TLSClientConfig уже склонирован NewTransport.
	tr.DialContext = (&net.Dialer{Timeout: 10 * time.Second, Control: BlockInternalDial}).DialContext
	tr.TLSClientConfig.InsecureSkipVerify = insecureTLS //nolint:gosec // решение вызывающего (Q14)
	return &http.Client{
		Timeout:       timeout,
		Transport:     tr,
		CheckRedirect: RedirectPolicy(MaxRedirectHops, ValidatePublicURL),
	}
}
