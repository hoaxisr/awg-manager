package command

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// claimInPost — Poster над оракулом, который на `no interface` гасит кредит
// снятия ВНУТРИ POST: так хук приходит раньше разбора ответа.
type claimInPost struct {
	*query.FakeNDMS
	q       *query.Queries
	name    string
	claimed *bool
}

func (p claimInPost) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	if b, _ := json.Marshal(payload); strings.Contains(string(b), `"no":true`) {
		*p.claimed = p.q.Interfaces.ClaimOwnDestroyed(p.name)
	}
	return p.FakeNDMS.Post(ctx, payload)
}

// stubPoster отвечает body на всё.
type stubPoster struct{ body string }

func (p stubPoster) Post(context.Context, any) (json.RawMessage, error) {
	return json.RawMessage(p.body), nil
}

// deleteFixture — команды над poster, карта над оракулом с Wireguard1;
// Wireguard1 подтверждён. Save — debounce час: save-POST не летит.
func deleteFixture(t *testing.T, poster func(*query.FakeNDMS, *query.Queries) Poster) (*InterfaceCommands, *query.FakeNDMS, *query.Queries, query.Confirmed) {
	t.Helper()
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard1", Type: "Wireguard"})
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger()})
	p := poster(f, q)
	c := NewInterfaceCommands(p, NewSaveCoordinator(p, nil, time.Hour, time.Hour, 0, nil), q)
	conf, _, ok, err := q.Interfaces.Confirm(context.Background(), "Wireguard1")
	if err != nil || !ok {
		t.Fatalf("confirm: ok=%v err=%v", ok, err)
	}
	return c, f, q, conf
}

// Координатор только из конструктора: nil паникует сразу, а не в первом
// сносе. Мутация: снять гард в newMutator → паника не на конструкторе, красный.
func TestNewMutator_NilSave_Panics(t *testing.T) {
	q := query.NewQueries(query.Deps{Getter: query.NewFakeNDMS(), Logger: query.NopLogger()})
	f := query.NewFakeNDMS()
	for name, build := range map[string]func(){
		"NewInterfaceCommands": func() { NewInterfaceCommands(f, nil, q) },
		"NewProxyCommands":     func() { NewProxyCommands(f, nil, q) },
		"NewWireguardCommands": func() { NewWireguardCommands(f, nil, q) },
		"NewCommands":          func() { NewCommands(Deps{Poster: f, Queries: q}) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("nil SaveCoordinator принят")
				}
			}()
			build()
		})
	}
}

// П21: кредит снятия выдан ДО POST — хук, пришедший во время POST, свой.
// Исход Removed погашенный кредит не воскрешает.
// Мутация: выдавать кредит после POST → claimed=false, красный.
func TestDeleteInterface_CreditBeforePost(t *testing.T) {
	var claimed bool
	c, f, q, conf := deleteFixture(t, func(f *query.FakeNDMS, q *query.Queries) Poster {
		return claimInPost{FakeNDMS: f, q: q, name: "Wireguard1", claimed: &claimed}
	})
	if err := c.deleteInterface(context.Background(), conf, "delete Wireguard1"); err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("хук во время POST не узнан своим: кредит выдан после POST")
	}
	if f.Has("Wireguard1") || len(f.Posts) != 1 || f.Posts[0] != `{"interface":{"Wireguard1":{"no":true}}}` {
		t.Fatalf("has=%v posts=%v", f.Has("Wireguard1"), f.Posts)
	}
	if rec, _ := q.Interfaces.Get(context.Background(), "Wireguard1"); rec != nil {
		t.Fatal("снятое осталось в карте")
	}
	if q.Interfaces.ClaimOwnDestroyed("Wireguard1") {
		t.Fatal("после гашения кредит остался")
	}
}

// «unable to find» — цели достигнута: ошибки нет, запись забыта, кредита нет
// (ifdestroyed от нашей команды не будет). Мутация: не вызывать Absent →
// кредит висит, красный.
func TestDeleteInterface_MissingRecord_NoCredit(t *testing.T) {
	c, _, q, conf := deleteFixture(t, func(*query.FakeNDMS, *query.Queries) Poster {
		return stubPoster{body: `{"status":"error","message":"unable to find interface \"Wireguard1\""}`}
	})
	if err := c.deleteInterface(context.Background(), conf, "delete Wireguard1"); err != nil {
		t.Fatal(err)
	}
	if q.Interfaces.ClaimOwnDestroyed("Wireguard1") {
		t.Fatal("кредит за несостоявшееся снятие остался")
	}
	if rec, _ := q.Interfaces.Get(context.Background(), "Wireguard1"); rec != nil {
		t.Fatal("отсутствующее осталось в карте")
	}
}

// Отказ не из терпимых — ошибка, запись в карте, кредита нет.
func TestDeleteInterface_Refused_KeepsRecord(t *testing.T) {
	c, _, q, conf := deleteFixture(t, func(*query.FakeNDMS, *query.Queries) Poster {
		return stubPoster{body: `{"status":"error","message":"interface is busy"}`}
	})
	if err := c.deleteInterface(context.Background(), conf, "delete Wireguard1"); err == nil {
		t.Fatal("отказ принят")
	}
	if rec, _ := q.Interfaces.Get(context.Background(), "Wireguard1"); rec == nil {
		t.Fatal("отказ снятия убрал запись из карты")
	}
	if q.Interfaces.ClaimOwnDestroyed("Wireguard1") {
		t.Fatal("кредит за отказ остался")
	}
}

// Путь wireguard к deleteInterface: импорт создал, списком не показан —
// ErrNotListed, снос по имени из ответа импорта тем же deleteInterface (кредит
// снятия выдан). Без метода на ndmsMutator не компилируется.
func TestWireguardImport_NotListed_DroppedViaMutator(t *testing.T) {
	cmds, f, q := newOracleCommands(t, nil)
	q.Interfaces.SetCreatedBackoff()
	f.HideCreated(100)
	_, err := cmds.Wireguard.ImportWireguardConfig(context.Background(), []byte("conf"), "x.conf")
	if !errors.Is(err, query.ErrNotListed) {
		t.Fatalf("err=%v", err)
	}
	if !hasDrop(f.Posts, "Wireguard0") || !q.Interfaces.ClaimOwnDestroyed("Wireguard0") {
		t.Fatalf("снос не через deleteInterface: posts=%v", f.Posts)
	}
}

// D-N3: `no interface` не уходит, пока летит наше сохранение — только после
// ConfigurationSaved. Мутация: снять HoldForRemoval → `no` при сохранении в
// полёте, оракул E == 1, красный.
func TestDeleteInterface_WaitsFlight(t *testing.T) {
	c, f, _, conf := deleteFixture(t, func(f *query.FakeNDMS, _ *query.Queries) Poster { return f })
	sc := c.save
	sc.SetSaveTimings(5*time.Second, 5*time.Second, 0)
	sc.SetUptimeReader(func() float64 { return 100 })
	sc.OnBusState(true)
	t.Cleanup(func() { drainSC(t, sc) })
	f.HoldSaves()
	flushed := make(chan error, 1)
	go func() { flushed <- sc.Flush(context.Background()) }()
	waitFlight(t, sc)
	deleted := make(chan error, 1)
	go func() { deleted <- c.deleteInterface(context.Background(), conf, "delete Wireguard1") }()
	time.Sleep(50 * time.Millisecond)
	if !f.Has("Wireguard1") {
		t.Fatal("`no interface` ушёл при сохранении в полёте")
	}
	f.SaveSettled()
	sc.OnConfigurationSaved(104)
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	if err := <-flushed; err != nil {
		t.Fatal(err)
	}
	if f.E != 0 {
		t.Fatalf("E = %d", f.E)
	}
}

// Пауза после сноса — только когда запись снята: «unable to find» и отказ
// её не ставят. Мутация: NoteRemoval на всех исходах → красный.
func TestDeleteInterface_NotesRemoval(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want bool
	}{
		"removed": {`{}`, true},
		"absent":  {`{"status":"error","message":"unable to find interface \"Wireguard1\""}`, false},
		"refused": {`{"status":"error","message":"interface is busy"}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			c, _, _, conf := deleteFixture(t, func(*query.FakeNDMS, *query.Queries) Poster {
				return stubPoster{body: tc.body}
			})
			_ = c.deleteInterface(context.Background(), conf, "delete Wireguard1")
			c.save.mu.Lock()
			noted := !c.save.lastRemovalAt.IsZero()
			c.save.mu.Unlock()
			if noted != tc.want {
				t.Fatalf("lastRemovalAt поставлен=%v, ждали %v", noted, tc.want)
			}
		})
	}
}
