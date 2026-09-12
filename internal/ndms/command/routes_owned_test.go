package command

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// routeCommandsWithConfig — команды маршрутов, у которых running-config отдаёт
// заданные строки. Форма ответа роутера: {"message":[ …строки конфигурации… ]}.
func routeCommandsWithConfig(t *testing.T, lines ...string) (*RouteCommands, *fakePoster) {
	t.Helper()
	fg := query.NewFakeGetter()
	quoted := make([]string, 0, len(lines))
	for _, l := range lines {
		quoted = append(quoted, `"`+l+`"`)
	}
	fg.SetJSON("/show/running-config", `{"message":[`+strings.Join(quoted, ",")+`]}`)
	poster := &fakePoster{}
	sc := NewSaveCoordinator(poster, &fakePublisher{}, 500*time.Millisecond, 5*time.Second, 0, nil)
	q := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	return NewRouteCommands(poster, sc, q), poster
}

// Наши записи снимаются каждая своей парой (host, interface) — по метке
// владения, которую ставит AddStaticRoute.
func TestRemoveHostRoute_RemovesOwnedEntriesByInterface(t *testing.T) {
	cmds, poster := routeCommandsWithConfig(t,
		"    ip route 203.0.113.5 PPPoE0 auto !awgm-obfuscator awg20",
		"    ip route 203.0.113.5 Bridge0 auto !awgm-obfuscator awg21",
	)

	if err := cmds.RemoveHostRoute(context.Background(), "203.0.113.5"); err != nil {
		t.Fatalf("снятие: %v", err)
	}

	posts := poster.Payloads()
	if len(posts) != 2 {
		t.Fatalf("ждали две парные команды, получили %d: %#v", len(posts), posts)
	}
	seen := map[string]bool{}
	for _, p := range posts {
		r := p.(map[string]any)["ip"].(map[string]any)["route"].(map[string]any)
		if r["no"] != true || r["host"] != "203.0.113.5" {
			t.Fatalf("не снятие нашего адреса: %#v", r)
		}
		seen[r["interface"].(string)] = true
	}
	if !seen["PPPoE0"] || !seen["Bridge0"] {
		t.Fatalf("сняты не те интерфейсы: %v", seen)
	}
}

// Запись без нашей метки — чужая: пользовательский статический маршрут на тот
// же адрес обязан пережить снятие нашего.
func TestRemoveHostRoute_KeepsForeignEntry(t *testing.T) {
	cmds, poster := routeCommandsWithConfig(t,
		"    ip route 203.0.113.5 PPPoE0 auto !awgm-obfuscator awg20",
		"    ip route 203.0.113.5 Bridge0 auto !мой маршрут",
	)

	if err := cmds.RemoveHostRoute(context.Background(), "203.0.113.5"); err != nil {
		t.Fatalf("снятие: %v", err)
	}

	posts := poster.Payloads()
	if len(posts) != 1 {
		t.Fatalf("ждали одну команду (только свою запись), получили %d: %#v", len(posts), posts)
	}
	r := posts[0].(map[string]any)["ip"].(map[string]any)["route"].(map[string]any)
	if r["interface"] != "PPPoE0" {
		t.Fatalf("снята чужая запись: %#v", r)
	}
}

// Своих записей нет — снимать нечего, слепой залп по адресу только снёс бы
// чужое. Для вызывающего это успех.
func TestRemoveHostRoute_NoOwnedEntriesSendsNothing(t *testing.T) {
	cmds, poster := routeCommandsWithConfig(t,
		"    ip route 203.0.113.5 Bridge0 auto !чужой маршрут",
	)

	if err := cmds.RemoveHostRoute(context.Background(), "203.0.113.5"); err != nil {
		t.Fatalf("снятие: %v", err)
	}

	if n := len(poster.Payloads()); n != 0 {
		t.Fatalf("по чужой записи ушло %d команд: %#v", n, poster.Payloads())
	}
}

// v6: своя форма и в конфигурации (prefix с /128), и в снятии.
func TestRemoveHostRoute_OwnedV6UsesPrefixForm(t *testing.T) {
	cmds, poster := routeCommandsWithConfig(t,
		"    ipv6 route 2001:db8::5/128 PPPoE0 auto !awgm-obfuscator awg20",
	)

	if err := cmds.RemoveHostRoute(context.Background(), "2001:db8::5"); err != nil {
		t.Fatalf("снятие: %v", err)
	}

	posts := poster.Payloads()
	if len(posts) != 1 {
		t.Fatalf("ждали одну команду, получили %d", len(posts))
	}
	r := posts[0].(map[string]any)["ipv6"].(map[string]any)["route"].(map[string]any)
	if r["prefix"] != "2001:db8::5/128" || r["interface"] != "PPPoE0" {
		t.Fatalf("v6-снятие не той формы: %#v", r)
	}
}
