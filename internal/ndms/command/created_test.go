package command

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// hasDrop — среди команд есть снос name.
func hasDrop(posts []string, name string) bool {
	return slices.Contains(posts, `{"interface":{"`+name+`":{"no":true}}}`)
}

// F584: каждый путь создания под поздней регистрацией записи (стенд 30.09:
// список через 30 мс после ответа ещё без неё) — успех, запись на месте,
// сноса нет, E == 0, фантомов нет.
func TestCreate_LateRecord_Confirms(t *testing.T) {
	cases := map[string]func(*Commands) (string, error){
		"import": func(c *Commands) (string, error) {
			res, err := c.Wireguard.ImportWireguardConfig(context.Background(), []byte("conf"), "x.conf")
			return res.Created.Name(), err
		},
		"opkgtun": func(c *Commands) (string, error) {
			conf, err := c.Interfaces.CreateOpkgTun(context.Background(), "OpkgTun3", "t", freeFor(t, "opkgtun3"))
			return conf.Name(), err
		},
		"proxy": func(c *Commands) (string, error) {
			conf, err := c.Proxies.CreateProxy(context.Background(), "Proxy0", "d", "127.0.0.1", 1080, false)
			return conf.Name(), err
		},
	}
	for name, create := range cases {
		t.Run(name, func(t *testing.T) {
			cmds, f, q := newOracleCommands(t, nil)
			f.ExpectCreate("OpkgTun3", "Proxy0")
			q.Interfaces.SetCreatedBackoff(time.Millisecond, time.Millisecond, time.Millisecond)
			f.HideCreated(2)
			lists := f.ListCalls()
			got, err := create(cmds)
			if err != nil || got == "" || !f.Has(got) {
				t.Fatalf("got=%q err=%v", got, err)
			}
			if f.ListCalls()-lists < 3 || hasDrop(f.Posts, got) || f.E != 0 || f.Phantoms != 0 {
				t.Fatalf("lists=%d drop=%v E=%d phantoms=%d posts=%v", f.ListCalls()-lists, hasDrop(f.Posts, got), f.E, f.Phantoms, f.Posts)
			}
		})
	}
}

// layerHooksInFirstList — хуки слоя по созданному доставляются в стор, пока
// первый список подтверждения в полёте: след «NDMS знает запись».
func layerHooksInFirstList(f *query.FakeNDMS, q *query.Queries) {
	f.InList(func() {
		f.InList(nil)
		for _, h := range f.DrainHooks() {
			if h.Type == "iflayerchanged" {
				q.Interfaces.OnLayerChanged(h.ID, h.Layer, h.Level)
			}
		}
	})
}

// След есть, а в списках записи нет за всё ожидание — ErrNotListed и снос
// созданного по имени из ответа импорта: сироты нет, E == 0, фантомов нет.
func TestImport_NeverListed_ErrorAndDrop(t *testing.T) {
	cmds, f, q := newOracleCommands(t, nil)
	q.Interfaces.SetCreatedBackoff(time.Millisecond)
	f.HideCreated(100)
	layerHooksInFirstList(f, q)
	res, err := cmds.Wireguard.ImportWireguardConfig(context.Background(), []byte("conf"), "x.conf")
	if !errors.Is(err, query.ErrNotListed) || !strings.Contains(err.Error(), "Wireguard0") || res.Created != (query.Confirmed{}) {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if !hasDrop(f.Posts, "Wireguard0") || f.Has("Wireguard0") || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("has=%v E=%d phantoms=%d posts=%v", f.Has("Wireguard0"), f.E, f.Phantoms, f.Posts)
	}
}

// Ни списка, ни хуков — ошибка без сноса: знает ли NDMS запись, неизвестно,
// а снос отсутствующего — E. Созданное остаётся, E == 0.
func TestImport_NoTrace_ErrorNoDrop(t *testing.T) {
	cmds, f, q := newOracleCommands(t, nil)
	q.Interfaces.SetCreatedBackoff(time.Millisecond)
	f.HideCreated(100)
	res, err := cmds.Wireguard.ImportWireguardConfig(context.Background(), []byte("conf"), "x.conf")
	if !errors.Is(err, query.ErrNotSeen) || res.Created != (query.Confirmed{}) {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(f.Posts) != 1 || !f.Has("Wireguard0") || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("posts=%v has=%v E=%d phantoms=%d", f.Posts, f.Has("Wireguard0"), f.E, f.Phantoms)
	}
}

// Созданное сняли (ifdestroyed) во время ожидания, след был — ошибка без
// сноса: сносить нечего, `no interface` дал бы E.
func TestImport_DestroyedDuringWait_NoDrop(t *testing.T) {
	cmds, f, q := newOracleCommands(t, nil)
	q.Interfaces.SetCreatedBackoff(time.Millisecond, time.Millisecond)
	f.HideCreated(100)
	f.InList(func() {
		f.InList(nil)
		for _, h := range f.DrainHooks() {
			if h.Type == "iflayerchanged" {
				q.Interfaces.OnLayerChanged(h.ID, h.Layer, h.Level)
			}
		}
		f.Remove("Wireguard0")
		for _, h := range f.DrainHooks() {
			if h.Type == "ifdestroyed" {
				q.Interfaces.OnDestroyed(h.ID)
			}
		}
	})
	res, err := cmds.Wireguard.ImportWireguardConfig(context.Background(), []byte("conf"), "x.conf")
	if !errors.Is(err, query.ErrCreatedThenRemoved) || res.Created != (query.Confirmed{}) {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(f.Posts) != 1 || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("posts=%v E=%d phantoms=%d", f.Posts, f.E, f.Phantoms)
	}
}

// Список не прочитан (решение 4) — ошибка, после импорта ни одной команды:
// созданный интерфейс остаётся на роутере (снести без списка не можем).
func TestImport_ListError_NoCommands(t *testing.T) {
	cmds, f, q := newOracleCommands(t, nil)
	q.Interfaces.SetCreatedBackoff(time.Millisecond)
	f.FailList(errors.New("rci down"))
	res, err := cmds.Wireguard.ImportWireguardConfig(context.Background(), []byte("conf"), "x.conf")
	if err == nil || errors.Is(err, query.ErrNotListed) || res.Created != (query.Confirmed{}) {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(f.Posts) != 1 || !f.Has("Wireguard0") || f.Phantoms != 0 {
		t.Fatalf("posts=%v has=%v phantoms=%d", f.Posts, f.Has("Wireguard0"), f.Phantoms)
	}
}

// hiddenForeign — чужой name уже есть в NDMS, но ни списком, ни хуком ещё не
// показан (поздний ifcreated под нагрузкой, F574).
func hiddenForeign(f *query.FakeNDMS, name, typ string) {
	f.HideCreated(-1)
	f.Add(ndms.Interface{ID: name, Type: typ})
	f.HideCreated(0)
}

// F574: создание по имени, занятому чужой невидимой записью, — NDMS не
// ответил «interface created»: ErrNotCreated, ни настроек, ни подтверждения,
// ни сноса; чужая запись цела, E == 0.
func TestCreateInterface_Existing_ErrorNoConfigureNoDrop(t *testing.T) {
	_, f, q := newOracleCommands(t, nil)
	hiddenForeign(f, "Wireguard1", "Wireguard")
	posts, lists := len(f.Posts), f.ListCalls()
	payload := map[string]any{"interface": map[string]any{"Wireguard1": map[string]any{}}}
	c, err := CreateInterface(context.Background(), f, nil, q, payload, "Wireguard1", false)
	if !errors.Is(err, ErrNotCreated) || c != (query.Confirmed{}) {
		t.Fatalf("c=%v err=%v", c, err)
	}
	if len(f.Posts)-posts != 1 || f.ListCalls() != lists || !f.Has("Wireguard1") || f.E != 0 {
		t.Fatalf("posts=%v lists=%d has=%v E=%d", f.Posts[posts:], f.ListCalls()-lists, f.Has("Wireguard1"), f.E)
	}
}

// Создано — подтверждено, как раньше.
func TestCreateInterface_Created_Confirms(t *testing.T) {
	_, f, q := newOracleCommands(t, nil)
	f.ExpectCreate("Wireguard1")
	payload := map[string]any{"interface": map[string]any{"Wireguard1": map[string]any{}}}
	c, err := CreateInterface(context.Background(), f, nil, q, payload, "Wireguard1", false)
	if err != nil || c.Name() != "Wireguard1" || f.Phantoms != 0 {
		t.Fatalf("c=%v err=%v phantoms=%d", c, err, f.Phantoms)
	}
}

// Прокси (EnsureProxy шлёт создание и по своей записи) попал в чужую
// невидимую запись со следом в хуках: подтверждения нет, а сноса по имени
// нет тоже — ответ не сказал «created» (F574, F584 Minor-1).
func TestCreateProxy_ExistingUnlisted_NoDrop(t *testing.T) {
	cmds, f, q := newOracleCommands(t, nil)
	q.Interfaces.SetCreatedBackoff(time.Millisecond)
	hiddenForeign(f, "Proxy0", "Proxy")
	q.Interfaces.OnLayerChanged("Proxy0", "ctrl", "") // след: NDMS запись знает
	_, err := cmds.Proxies.CreateProxy(context.Background(), "Proxy0", "d", "127.0.0.1", 1080, false)
	if err == nil {
		t.Fatal("want error")
	}
	if hasDrop(f.Posts, "Proxy0") || !f.Has("Proxy0") || f.E != 0 {
		t.Fatalf("drop=%v has=%v E=%d posts=%v", hasDrop(f.Posts, "Proxy0"), f.Has("Proxy0"), f.E, f.Posts)
	}
}
