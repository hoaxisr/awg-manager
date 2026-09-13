package httpclient

import (
	"net/http"
	"net/url"
	"reflect"
	"testing"
)

// Прокси из окружения наследуется молча, когда транспорт не привязан к
// интерфейсу. Это умолчание НЕ очевидно: в коде вызывающего слова «прокси»
// может не быть вовсе, а запрос всё равно уйдёт через чужой хост. Тест
// закрепляет его как договор, а не как случайность.
func TestNewTransport_EnvProxyInheritedWhenUnbound(t *testing.T) {
	tr, err := NewTransport(TransportConfig{})
	if err != nil {
		t.Fatalf("транспорт: %v", err)
	}
	if tr.Proxy == nil {
		t.Fatal("прокси окружения не унаследован — умолчание изменилось молча")
	}
	// Сверяется ТОЖДЕСТВО функции, а не её поведение на подставленном
	// окружении: http.ProxyFromEnvironment читает переменные ОДИН раз на
	// процесс (sync.Once внутри net/http), поэтому t.Setenv действует только
	// если этот тест окажется первым в прогоне. Проверка поведением проходила
	// в одиночку и падала в общем прогоне — то есть зависела от порядка.
	if !sameFunc(tr.Proxy, http.ProxyFromEnvironment) {
		t.Fatal("Proxy — не http.ProxyFromEnvironment: наследование окружения снято молча")
	}
}

// sameFunc сравнивает функции по адресу кода: оператора == для func в Go нет.
func sameFunc(a, b func(*http.Request) (*url.URL, error)) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

// Явный отказ снимает наследование. Ради этого поле и заведено: клиенту
// портала Amnezia нужен ПРЯМОЙ выход с роутера — иначе запрос уйдёт через
// чужой хост мимо требования о регионе, ради которого зеркало и понадобилось.
func TestNewTransport_EnvProxyRefusedExplicitly(t *testing.T) {
	tr, err := NewTransport(TransportConfig{Proxy: ProxyDirect})
	if err != nil {
		t.Fatalf("транспорт: %v", err)
	}
	if tr.Proxy != nil {
		u, _ := tr.Proxy(mustReq(t, "https://example.test/x"))
		t.Fatalf("прямой выход не получен: прокси = %v", u)
	}
}

// Явный адрес прокси сильнее явного отказа: назвать адрес и тут же запретить
// его — противоречие вызывающего, и молча выбрасывать НАЗВАННЫЙ адрес хуже,
// чем уважить его.
func TestNewTransport_ExplicitProxyURLWinsOverRefusal(t *testing.T) {
	tr, err := NewTransport(TransportConfig{
		ProxyURL: "http://explicit.fixture.test:8080",
		Proxy:    ProxyDirect,
	})
	if err != nil {
		t.Fatalf("транспорт: %v", err)
	}
	if tr.Proxy == nil {
		t.Fatal("названный адрес прокси выброшен")
	}
	u, err := tr.Proxy(mustReq(t, "https://example.test/x"))
	if err != nil {
		t.Fatalf("выбор прокси: %v", err)
	}
	if u == nil || u.Host != "explicit.fixture.test:8080" {
		t.Fatalf("прокси = %v, ожидался explicit.fixture.test:8080", u)
	}
}

// Привязка к интерфейсу означает выход через конкретное устройство, и прокси
// окружения увёл бы трафик мимо него. Это уже было в коде — закрепляем.
func TestNewTransport_BoundInterfaceNeverUsesEnvProxy(t *testing.T) {
	tr, err := NewTransport(TransportConfig{Interface: "nwg0"})
	if err != nil {
		t.Fatalf("транспорт: %v", err)
	}
	if tr.Proxy != nil {
		u, _ := tr.Proxy(mustReq(t, "https://example.test/x"))
		t.Fatalf("привязанный транспорт пошёл через прокси окружения: %v", u)
	}
}

func mustReq(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("запрос: %v", err)
	}
	return req
}

// Привязка к интерфейсу запрещает прокси ВСЕГДА, в том числе когда вызывающий
// просит наследовать. Место было слепым: буквальная реализация «явного
// наследования» уводила туннельный трафик через прокси окружения мимо
// устройства, и набор тестов молчал.
func TestNewTransport_BoundInterfaceIgnoresInheritRequest(t *testing.T) {
	tr, err := NewTransport(TransportConfig{Interface: "nwg0", Proxy: ProxyInheritEnv})
	if err != nil {
		t.Fatalf("транспорт: %v", err)
	}
	if tr.Proxy != nil {
		t.Fatal("привязанный транспорт получил прокси — трафик уйдёт мимо устройства")
	}
}

// Адрес прокси из одних пробелов — это «адрес не задан», а не «задан». Иначе
// такая строка считается НАЗВАННЫМ адресом и отменяет запрошенный прямой
// выход: поле с защитным смыслом молча перестаёт действовать.
//
// Проверяются ОБА входа пакета: расхождение между ними уже случалось —
// строгий разбор добавили в NewTransport и забыли про Client.Do.
func TestProxyURL_BlankDoesNotDefeatDirect(t *testing.T) {
	tr, err := NewTransport(TransportConfig{ProxyURL: "   ", Proxy: ProxyDirect})
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	if tr.Proxy != nil {
		u, _ := tr.Proxy(mustReq(t, "https://example.test/x"))
		t.Fatalf("NewTransport: прямой выход отменён пустым адресом: %v", u)
	}

	c := &Client{baseTransport: &http.Transport{}}
	parsed, err := parseProxyURL("   ")
	if err != nil {
		t.Fatalf("parseProxyURL: %v", err)
	}
	if got := c.buildTransport(CallConfig{Proxy: ProxyDirect}, parsed); got.Proxy != nil {
		u, _ := got.Proxy(mustReq(t, "https://example.test/x"))
		t.Fatalf("Client.Do: прямой выход отменён пустым адресом: %v", u)
	}
}

// Негодный адрес прокси — громкий отказ, а не тихий нерабочий транспорт.
// url.Parse не отвергает строку без схемы и хоста, и такой «прокси» валил бы
// каждый запрос уже в рантайме, попутно подавляя ProxyDirect.
func TestProxyURL_UnusableIsRefused(t *testing.T) {
	for _, bad := range []string{"не-адрес", "garbage", "//host-without-scheme"} {
		if _, err := NewTransport(TransportConfig{ProxyURL: bad}); err == nil {
			t.Errorf("NewTransport принял %q как адрес прокси", bad)
		}
		if _, err := parseProxyURL(bad); err == nil {
			t.Errorf("parseProxyURL принял %q — тот же ввод пройдёт через Client.Do", bad)
		}
	}
}

// Второй вход — Client.Do через CallConfig — тоже умеет требовать прямой
// выход. Он шире NewTransport: через него ходят диагностика, пробы связи и
// измерение «прямого» IP.
func TestBuildTransport_CallConfigHonoursDirect(t *testing.T) {
	c := &Client{baseTransport: &http.Transport{}}

	inherit := c.buildTransport(CallConfig{}, nil)
	if inherit.Proxy == nil {
		t.Fatal("умолчание CallConfig изменилось: прокси окружения больше не наследуется")
	}
	direct := c.buildTransport(CallConfig{Proxy: ProxyDirect}, nil)
	if direct.Proxy != nil {
		t.Fatal("CallConfig{Proxy: ProxyDirect} не даёт прямого выхода")
	}
}
