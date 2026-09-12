package httpclient

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TransportConfig configures a reusable *http.Transport for http.Client.
type TransportConfig struct {
	// Interface binds outgoing sockets to a kernel device (SO_BINDTODEVICE).
	Interface string

	// ProxyURL routes through an HTTP proxy, e.g. "http://127.0.0.1:1080".
	ProxyURL string

	// DNSServers are used for hostname resolution when Interface is set.
	DNSServers []string

	// Proxy — что делать с прокси из окружения (HTTP_PROXY/HTTPS_PROXY).
	// См. ProxyPolicy: нулевое значение — прежнее поведение.
	Proxy ProxyPolicy
}

// ProxyPolicy — отношение транспорта к прокси из окружения.
//
// Тип заведён потому, что умолчание здесь НЕ очевидно и один раз уже подвело:
// `NewTransport(TransportConfig{})` молча наследует прокси окружения, хотя в
// коде вызывающего слова «прокси» нет вовсе. Клиенту, которому нужен прямой
// выход, мало «не передавать ProxyURL» — снимать наследование надо ЯВНО.
//
// Значений ровно два, и третьего «наследовать явно» здесь СОЗНАТЕЛЬНО нет:
// оно было бы неотличимо от умолчания, а при заданном Interface — ещё и
// молча отменялось бы, потому что привязка к устройству запрещает прокси
// всегда. Значение, которое ничего не меняет, хуже отсутствия значения: оно
// даёт ложную уверенность.
type ProxyPolicy int

const (
	// ProxyInheritEnv — наследовать прокси окружения, когда ProxyURL пуст и
	// привязки к интерфейсу нет. Нулевое значение: поведение вызывающих,
	// написанных до появления типа, не меняется.
	ProxyInheritEnv ProxyPolicy = iota

	// ProxyDirect — прямой выход, что бы ни стояло в окружении.
	//
	// Явный ProxyURL сильнее: назвать адрес и тут же запретить его —
	// противоречие вызывающего, и молча выбрасывать НАЗВАННЫЙ адрес хуже,
	// чем уважить его.
	//
	// Привязка к интерфейсу (Interface) даёт прямой выход и без этого
	// значения — так было всегда: прокси увёл бы трафик мимо устройства.
	ProxyDirect
)

// NewTransport returns an http.Transport with optional interface binding
// and/or HTTP proxy. Caller owns the returned value; clone per-client if
// needed.
func NewTransport(cfg TransportConfig) (*http.Transport, error) {
	parsedProxy, err := parseProxyURL(cfg.ProxyURL)
	if err != nil {
		return nil, err
	}

	base := &http.Transport{
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     false,
	}

	c := &Client{baseTransport: base}
	tr := c.buildTransport(CallConfig{
		Interface:      cfg.Interface,
		DNSServers:     cfg.DNSServers,
		ConnectTimeout: 10 * time.Second,
		Proxy:          cfg.Proxy,
	}, parsedProxy)

	// Прямой выход сильнее умолчания, но слабее явного ProxyURL. Проверка
	// стоит после сборки и трогает только тот случай, где прокси взялся из
	// окружения сам.
	if cfg.Proxy == ProxyDirect && parsedProxy == nil {
		tr.Proxy = nil
	}
	return tr, nil
}

// parseProxyURL разбирает адрес прокси, заданный вызывающим. nil без ошибки —
// адрес не задан.
//
// Один разбор на ОБА входа пакета (NewTransport и Client.Do): расхождение
// между ними уже случалось — строгую проверку добавили в один, и тот же ввод
// вёл себя в двух местах по-разному.
//
// Пробелы снимаются ДО проверки на пустоту: адрес из одних пробелов — это
// «адрес не задан», а не «задан». Иначе url.Parse успешно разбирал "   " в
// URL без схемы и хоста, такой адрес считался НАЗВАННЫМ и подавлял
// запрошенный ProxyDirect — поле с защитным смыслом молча переставало
// действовать.
//
// Адрес без схемы или хоста — громкий отказ: url.Parse не отвергает
// "не-адрес", а такой «прокси» не работает ни для одного запроса и уехать
// молча не должен.
func parseProxyURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("httpclient: invalid proxy URL %q: %w", raw, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("httpclient: proxy URL %q has no scheme or host", raw)
	}
	return u, nil
}
