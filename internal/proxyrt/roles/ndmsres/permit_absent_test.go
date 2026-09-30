package ndmsres_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/ndmsres"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/roletest"
)

// permit-all снимается ТОЛЬКО с существующих интерфейсов из списка; отсутствующий
// интерфейс — не отказ и не шаг (create-on-reference, как у policy_exit).
func TestPermitAbsent_RemovesOnlyFromExistingListed(t *testing.T) {
	ctx := context.Background()
	n := roletest.NewNDMS()
	for _, name := range []string{"OpkgTun17", "OpkgTun19"} {
		if err := n.CreateOpkgTunWithSecurityLevel(ctx, name, "x", "private"); err != nil {
			t.Fatal(err)
		}
		if err := n.SetPermitAllACL(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	// OpkgTun23 — в списке, но в NDMS его нет.
	r := ndmsres.NewPermitAbsent("permit_absent", n, n)
	r.SetDesired([]string{"OpkgTun17", "OpkgTun19", "OpkgTun23"}, nil)

	obs, err := r.Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Exists {
		t.Fatal("permit-all стоит на двух половинах, Exists обязан быть false")
	}
	steps := r.Plan(obs)
	if len(steps) != 2 {
		t.Fatalf("шагов %d, ожидали 2 (по одному на половину): %+v", len(steps), steps)
	}
	for _, s := range steps {
		if err := r.Apply(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"OpkgTun17", "OpkgTun19"} {
		if n.ExitOf(name).PermitAll {
			t.Errorf("permit-all остался на %s", name)
		}
	}
	obs, _ = r.Observe(ctx)
	if !obs.Exists || len(r.Plan(obs)) != 0 {
		t.Fatalf("после снятия желаемое обязано сходиться: %+v", obs)
	}
}

// Пустой список и интерфейсы без permit-all — сходится без шагов.
func TestPermitAbsent_NothingToDoConverges(t *testing.T) {
	ctx := context.Background()
	n := roletest.NewNDMS()
	if err := n.CreateOpkgTunWithSecurityLevel(ctx, "OpkgTun17", "x", "private"); err != nil {
		t.Fatal(err)
	}
	r := ndmsres.NewPermitAbsent("permit_absent", n, n)
	r.SetDesired([]string{"OpkgTun17", ""}, nil)
	obs, err := r.Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !obs.Exists || len(r.Plan(obs)) != 0 {
		t.Fatalf("ожидали сходимость без шагов: %+v", obs)
	}
	if got := r.Plan(proxyrt.Observation{Known: true, Exists: false, Attrs: map[string]string{"present": ""}}); len(got) != 0 {
		t.Fatalf("пустой present не даёт шагов: %+v", got)
	}
}

// Неэкспонированная половина теряет и ip global: стенд 2026-09-06 — после
// снятия тумблера ExposeToPolicies WG-половина оставалась `ip global`,
// снимать было некому (policy_exit аддитивен и из ведомости уходит).
func TestPermitAbsent_ClearsIPGlobalOnUnexposed(t *testing.T) {
	ctx := context.Background()
	n := roletest.NewNDMS()
	for _, name := range []string{"OpkgTun0", "OpkgTun1"} {
		if err := n.CreateOpkgTunWithSecurityLevel(ctx, name, "x", "private"); err != nil {
			t.Fatal(err)
		}
		if err := n.SetIPGlobal(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	// OpkgTun1 несёт оба остатка, OpkgTun0 — только ip global.
	if err := n.SetPermitAllACL(ctx, "OpkgTun1"); err != nil {
		t.Fatal(err)
	}
	r := ndmsres.NewPermitAbsent("permit_absent", n, n)
	r.SetDesired([]string{"OpkgTun0", "OpkgTun1"}, []string{"OpkgTun0", "OpkgTun1"})

	obs, err := r.Observe(ctx)
	if err != nil || obs.Exists {
		t.Fatalf("ожидалось расхождение: err=%v obs=%+v", err, obs)
	}
	var ops []string
	for _, s := range r.Plan(obs) {
		ops = append(ops, s.Op+":"+s.Args["name"])
		if err := r.Apply(ctx, s); err != nil {
			t.Fatalf("apply %v: %v", s, err)
		}
	}
	want := []string{"remove-acl:OpkgTun1", "clear-ip-global:OpkgTun0", "clear-ip-global:OpkgTun1"}
	if !slices.Equal(ops, want) {
		t.Fatalf("шаги: %v, ожидалось %v", ops, want)
	}
	for _, name := range []string{"OpkgTun0", "OpkgTun1"} {
		if g, _ := n.HasIPGlobal(ctx, name); g {
			t.Errorf("%s: ip global остался", name)
		}
	}
	if obs, _ := r.Observe(ctx); !obs.Exists {
		t.Fatalf("после снятия ресурс не сошёлся: %+v", obs)
	}
}

// Интерфейс в списке permit, но НЕ в списке global: `ip global` не трогаем.
// Это выключенный инстанс с включённым тумблером — повторный `ip global` при
// включении сбросил бы permit пользователя в политиках в deny (стенд
// 2026-09-06), поэтому списки разные намеренно.
func TestPermitAbsent_KeepsIPGlobalOutsideGlobalList(t *testing.T) {
	ctx := context.Background()
	n := roletest.NewNDMS()
	if err := n.CreateOpkgTunWithSecurityLevel(ctx, "OpkgTun0", "x", "private"); err != nil {
		t.Fatal(err)
	}
	if err := n.SetIPGlobal(ctx, "OpkgTun0"); err != nil {
		t.Fatal(err)
	}
	r := ndmsres.NewPermitAbsent("permit_absent", n, n)
	r.SetDesired([]string{"OpkgTun0"}, nil)

	obs, err := r.Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !obs.Exists || len(r.Plan(obs)) != 0 {
		t.Fatalf("ip global вне списка global — не расхождение и не шаг: %+v", obs)
	}
	if g, _ := n.HasIPGlobal(ctx, "OpkgTun0"); !g {
		t.Fatal("ip global снят у интерфейса вне списка global")
	}
}

// failingNDMS — модель NDMS, у которой снятие ACL отказывает. `roletest.NDMS`
// отказывать не умеет, поэтому прощающая ветка Apply (auto-delete уже
// каскадировал список — цель достигнута чужими руками) без этой заглушки
// непроверяема.
type failingNDMS struct {
	*roletest.NDMS
	removeErr error
	has       bool
	hasErr    error
}

func (n *failingNDMS) RemovePermitAllACL(context.Context, string) error { return n.removeErr }

func (n *failingNDMS) HasPermitAllACL(context.Context, string) (bool, error) {
	return n.has, n.hasErr
}

// Отказ снятия при уже пропавшей привязке — не отказ ресурса: желаемое
// «permit-all нет» достигнуто, и провал шага только заблокировал бы цепочку.
func TestPermitAbsent_ApplyForgivesRemoveWhenACLAlreadyGone(t *testing.T) {
	ctx := context.Background()
	n := &failingNDMS{NDMS: roletest.NewNDMS(), removeErr: errors.New("no such acl"), has: false}
	r := ndmsres.NewPermitAbsent("permit_absent", n, n)
	step := proxyrt.Step{Resource: "permit_absent", Op: "remove-acl",
		Args: map[string]string{"name": "OpkgTun17"}}
	if err := r.Apply(ctx, step); err != nil {
		t.Fatalf("привязки уже нет — Apply обязан считать цель достигнутой: %v", err)
	}
}

// Если перепроверка сама не отвечает, прощать нечего: отказ снятия уходит
// наверх и оборачивает исходную ошибку команды.
func TestPermitAbsent_ApplyFailsWhenRecheckUnavailable(t *testing.T) {
	ctx := context.Background()
	removeErr := errors.New("RCI недоступен")
	n := &failingNDMS{NDMS: roletest.NewNDMS(), removeErr: removeErr,
		hasErr: errors.New("и посмотреть нечем")}
	r := ndmsres.NewPermitAbsent("permit_absent", n, n)
	step := proxyrt.Step{Resource: "permit_absent", Op: "remove-acl",
		Args: map[string]string{"name": "OpkgTun17"}}
	err := r.Apply(ctx, step)
	if err == nil {
		t.Fatal("перепроверка не ответила — прощать нечего, ждали ошибку")
	}
	if !errors.Is(err, removeErr) {
		t.Fatalf("ошибка не оборачивает отказ снятия: %v", err)
	}
	if !strings.Contains(err.Error(), "OpkgTun17") {
		t.Fatalf("в ошибке нет имени интерфейса: %v", err)
	}
}
