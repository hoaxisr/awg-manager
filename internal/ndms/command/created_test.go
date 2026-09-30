package command

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

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

// Записи нет за всё ожидание — ошибка ErrNotListed и снос созданного по
// имени из ответа импорта: сироты нет, E == 0, фантомов нет.
func TestImport_NeverListed_ErrorAndDrop(t *testing.T) {
	cmds, f, q := newOracleCommands(t, nil)
	q.Interfaces.SetCreatedBackoff(time.Millisecond)
	f.HideCreated(-1)
	res, err := cmds.Wireguard.ImportWireguardConfig(context.Background(), []byte("conf"), "x.conf")
	if !errors.Is(err, query.ErrNotListed) || !strings.Contains(err.Error(), "Wireguard0") || res.Created != (query.Confirmed{}) {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if !hasDrop(f.Posts, "Wireguard0") || f.Has("Wireguard0") || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("has=%v E=%d phantoms=%d posts=%v", f.Has("Wireguard0"), f.E, f.Phantoms, f.Posts)
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
