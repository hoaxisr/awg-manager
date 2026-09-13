package command

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

const probeComment = "awgm-obfuscator awg20"

// routeCommandsWithConfig — команды маршрутов, у которых running-config отдаёт
// заданные строки. Форма ответа роутера: {"message":[ …строки конфигурации… ]}.
// Строки пишутся как в running-config: `ip route …` — команда глобальная, без
// отступа (отступ там только у тела блоков `interface`).
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

// routePayload достаёт тело команды маршрута из полезной нагрузки.
func routePayload(t *testing.T, p any, key string) map[string]any {
	t.Helper()
	root, ok := p.(map[string]any)
	if !ok {
		t.Fatalf("не та форма запроса: %#v", p)
	}
	fam, ok := root[key].(map[string]any)
	if !ok {
		t.Fatalf("нет ключа %q: %#v", key, p)
	}
	r, ok := fam["route"].(map[string]any)
	if !ok {
		t.Fatalf("нет route: %#v", p)
	}
	return r
}

// Свои записи снимаются каждая своей парой (host, interface) — по подписи,
// которую поставил владелец.
func TestRemoveOwnHostRoute_RemovesOwnEntriesByInterface(t *testing.T) {
	cmds, poster := routeCommandsWithConfig(t,
		"ip route 203.0.113.5 PPPoE0 auto !"+probeComment,
		"ip route 203.0.113.5 Bridge0 auto !"+probeComment,
	)

	if err := cmds.RemoveOwnHostRoute(context.Background(), "203.0.113.5", probeComment); err != nil {
		t.Fatalf("снятие: %v", err)
	}

	posts := poster.Payloads()
	if len(posts) != 2 {
		t.Fatalf("ждали две парные команды, получили %d: %#v", len(posts), posts)
	}
	seen := map[string]bool{}
	for _, p := range posts {
		r := routePayload(t, p, "ip")
		if r["no"] != true || r["host"] != "203.0.113.5" {
			t.Fatalf("не снятие нашего адреса: %#v", r)
		}
		seen[r["interface"].(string)] = true
	}
	if !seen["PPPoE0"] || !seen["Bridge0"] {
		t.Fatalf("сняты не те интерфейсы: %v", seen)
	}
}

// Запись с чужой подписью — чужая: пользовательский статический маршрут на тот
// же адрес обязан пережить снятие нашего.
func TestRemoveOwnHostRoute_KeepsForeignEntry(t *testing.T) {
	cmds, poster := routeCommandsWithConfig(t,
		"ip route 203.0.113.5 PPPoE0 auto !"+probeComment,
		"ip route 203.0.113.5 Bridge0 auto !маршрут пользователя",
	)

	if err := cmds.RemoveOwnHostRoute(context.Background(), "203.0.113.5", probeComment); err != nil {
		t.Fatalf("снятие: %v", err)
	}

	posts := poster.Payloads()
	if len(posts) != 1 {
		t.Fatalf("ждали одну команду (только свою запись), получили %d: %#v", len(posts), posts)
	}
	if r := routePayload(t, posts[0], "ip"); r["interface"] != "PPPoE0" {
		t.Fatalf("снята чужая запись: %#v", r)
	}
}

// Подпись сверяется целиком: чужая запись, в комментарий которой наша подпись
// попала куском, не наша.
func TestRemoveOwnHostRoute_CommentMatchedWhole(t *testing.T) {
	cmds, poster := routeCommandsWithConfig(t,
		"ip route 203.0.113.5 Bridge0 auto !не "+probeComment+" вовсе",
	)

	if err := cmds.RemoveOwnHostRoute(context.Background(), "203.0.113.5", probeComment); err != nil {
		t.Fatalf("снятие: %v", err)
	}
	if n := len(poster.Payloads()); n != 0 {
		t.Fatalf("снята запись с чужим комментарием: %#v", poster.Payloads())
	}
}

// Снятие по одному адресу не трогает наши же записи по другим адресам.
func TestRemoveOwnHostRoute_OtherAddressesUntouched(t *testing.T) {
	cmds, poster := routeCommandsWithConfig(t,
		"ip route 203.0.113.5 PPPoE0 auto !"+probeComment,
		"ip route 198.51.100.7 PPPoE0 auto !"+probeComment,
	)

	if err := cmds.RemoveOwnHostRoute(context.Background(), "203.0.113.5", probeComment); err != nil {
		t.Fatalf("снятие: %v", err)
	}

	posts := poster.Payloads()
	if len(posts) != 1 {
		t.Fatalf("ждали одну команду, получили %d: %#v", len(posts), posts)
	}
	if r := routePayload(t, posts[0], "ip"); r["host"] != "203.0.113.5" {
		t.Fatalf("снят не тот адрес: %#v", r)
	}
}

// Форма через шлюз и network+mask: четвёртое поле там не интерфейс, такие
// записи не наши, даже если подпись совпала.
func TestRemoveOwnHostRoute_IgnoresNonHostForms(t *testing.T) {
	cmds, poster := routeCommandsWithConfig(t,
		"ip route 203.0.113.5 192.168.1.1 PPPoE0 auto !"+probeComment,
	)

	if err := cmds.RemoveOwnHostRoute(context.Background(), "203.0.113.5", probeComment); err != nil {
		t.Fatalf("снятие: %v", err)
	}
	if n := len(poster.Payloads()); n != 0 {
		t.Fatalf("снята запись формы «через шлюз»: %#v", poster.Payloads())
	}
}

// Битая или обрезанная строка конфигурации не роняет разбор.
func TestRemoveOwnHostRoute_SurvivesShortLines(t *testing.T) {
	cmds, poster := routeCommandsWithConfig(t,
		"", "ip", "ip route", "ip route 203.0.113.5", "!",
		"ip route 203.0.113.5 PPPoE0 auto !"+probeComment,
	)

	if err := cmds.RemoveOwnHostRoute(context.Background(), "203.0.113.5", probeComment); err != nil {
		t.Fatalf("снятие: %v", err)
	}
	if n := len(poster.Payloads()); n != 1 {
		t.Fatalf("ждали одну команду, получили %d: %#v", n, poster.Payloads())
	}
}

// Отказ снятия своей записи обязан доехать до вызывающего: молчаливый успех
// оставил бы маршрут на роутере, а туннель считал бы его снятым.
func TestRemoveOwnHostRoute_FailureReachesCaller(t *testing.T) {
	cmds, poster := routeCommandsWithConfig(t,
		"ip route 203.0.113.5 PPPoE0 auto !"+probeComment,
	)
	poster.SetError(errors.New("boom"))

	if err := cmds.RemoveOwnHostRoute(context.Background(), "203.0.113.5", probeComment); err == nil {
		t.Fatal("отказ снятия проглочен")
	}
}

// v6: своя форма и в конфигурации (prefix с /128), и в снятии.
func TestRemoveOwnHostRoute_V6UsesPrefixForm(t *testing.T) {
	cmds, poster := routeCommandsWithConfig(t,
		"ipv6 route 2001:db8::5/128 PPPoE0 auto !"+probeComment,
	)

	if err := cmds.RemoveOwnHostRoute(context.Background(), "2001:db8::5", probeComment); err != nil {
		t.Fatalf("снятие: %v", err)
	}

	posts := poster.Payloads()
	if len(posts) != 1 {
		t.Fatalf("ждали одну команду, получили %d", len(posts))
	}
	r := routePayload(t, posts[0], "ipv6")
	if r["prefix"] != "2001:db8::5/128" || r["interface"] != "PPPoE0" {
		t.Fatalf("v6-снятие не той формы: %#v", r)
	}
}

// Конфигурацию не прочитать — падаем на слепую форму: лучше снять лишнее, чем
// оставить свой маршрут на роутере.
func TestRemoveOwnHostRoute_UnreadableConfigFallsBackToBlind(t *testing.T) {
	cmds, poster := newTestRouteCommands(t) // FakeGetter без running-config

	if err := cmds.RemoveOwnHostRoute(context.Background(), "203.0.113.5", probeComment); err != nil {
		t.Fatalf("снятие: %v", err)
	}

	posts := poster.Payloads()
	if len(posts) != 1 {
		t.Fatalf("ждали слепую форму одной командой, получили %d", len(posts))
	}
	r := routePayload(t, posts[0], "ip")
	if _, hasIface := r["interface"]; hasIface {
		t.Fatalf("слепая форма не должна указывать интерфейс: %#v", r)
	}
}

// Пустая подпись — у вызывающего своей записи нет: прежняя слепая форма.
func TestRemoveOwnHostRoute_NoCommentIsBlind(t *testing.T) {
	cmds, poster := routeCommandsWithConfig(t,
		"ip route 203.0.113.5 PPPoE0 auto !"+probeComment,
	)

	if err := cmds.RemoveOwnHostRoute(context.Background(), "203.0.113.5", ""); err != nil {
		t.Fatalf("снятие: %v", err)
	}

	posts := poster.Payloads()
	if len(posts) != 1 {
		t.Fatalf("ждали одну слепую команду, получили %d", len(posts))
	}
	if _, hasIface := routePayload(t, posts[0], "ip")["interface"]; hasIface {
		t.Fatal("при пустой подписи снятие обязано быть слепым")
	}
}
