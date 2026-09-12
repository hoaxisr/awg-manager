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
	direct := false
	tr, err := NewTransport(TransportConfig{ProxyFromEnv: &direct})
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
	direct := false
	tr, err := NewTransport(TransportConfig{
		ProxyURL:     "http://explicit.fixture.test:8080",
		ProxyFromEnv: &direct,
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
