package peersubnet

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
)

// fakeRouter — роутер в памяти: allow-ips, маршруты с комментариями, журнал
// вызовов и инъекция отказа по подстроке имени вызова.
type fakeRouter struct {
	allow  map[string]bool   // iface|pubkey|cidr
	routes map[string]string // cidr|iface → comment
	calls  []string
	failOn []string
	// injected — ошибки, выданные по failOn, в порядке выдачи.
	injected []error
	// cancelAfter/cancel: после успешного вызова с таким именем отменить ctx
	// вызывающего (отключение клиента посреди Apply).
	cancelAfter string
	cancel      context.CancelFunc
}

func newFakeRouter() *fakeRouter {
	return &fakeRouter{allow: map[string]bool{}, routes: map[string]string{}}
}

// call ведёт себя как транспорт RCI: на отменённом ctx каждый вызов падает
// с ctx.Err().
func (f *fakeRouter) call(ctx context.Context, name string) error {
	f.calls = append(f.calls, name)
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, s := range f.failOn {
		if strings.Contains(name, s) {
			e := errors.New("boom: " + name)
			f.injected = append(f.injected, e)
			return e
		}
	}
	if f.cancel != nil && name == f.cancelAfter {
		f.cancel()
	}
	return nil
}

func (f *fakeRouter) AddAllowIP(ctx context.Context, iface, pub string, n *net.IPNet) error {
	if err := f.call(ctx, "allow+ "+n.String()); err != nil {
		return err
	}
	f.allow[iface+"|"+pub+"|"+n.String()] = true
	return nil
}

func (f *fakeRouter) RemoveAllowIP(ctx context.Context, iface, pub string, n *net.IPNet) error {
	if err := f.call(ctx, "allow- "+n.String()); err != nil {
		return err
	}
	delete(f.allow, iface+"|"+pub+"|"+n.String())
	return nil
}

func (f *fakeRouter) NetworkRouteOwner(ctx context.Context, n *net.IPNet, iface, comment string) (bool, bool, error) {
	if err := f.call(ctx, "owner? "+n.String()); err != nil {
		return false, false, err
	}
	c, ok := f.routes[n.String()+"|"+iface]
	return ok, ok && c == comment, nil
}

func (f *fakeRouter) AddNetworkRoute(ctx context.Context, n *net.IPNet, iface, comment string) error {
	if err := f.call(ctx, "route+ "+n.String()); err != nil {
		return err
	}
	f.routes[n.String()+"|"+iface] = comment
	return nil
}

func (f *fakeRouter) RemoveOwnNetworkRoute(ctx context.Context, n *net.IPNet, iface, comment string) (bool, error) {
	if err := f.call(ctx, "route- "+n.String()); err != nil {
		return false, err
	}
	if c, ok := f.routes[n.String()+"|"+iface]; ok && c == comment {
		delete(f.routes, n.String()+"|"+iface)
		return true, nil
	}
	return false, nil
}

const (
	n77, n78, n79 = "192.168.77.0/24", "192.168.78.0/24", "192.168.79.0/24"
	iface, pub    = "Wireguard9", "5+0I/P0Vaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa="
	ours          = "awgm-peer:5+0I/P0V"
)

func TestApply_OrderAllowIPsThenRoutes(t *testing.T) {
	f := newFakeRouter()
	f.allow[iface+"|"+pub+"|"+n79] = true
	f.routes[n79+"|"+iface] = ours
	if err := Apply(context.Background(), f, iface, pub, []string{n77, n78}, []string{n79}); err != nil {
		t.Fatal(err)
	}
	want := []string{"allow+ " + n77, "allow+ " + n78, "allow- " + n79, "owner? " + n77, "route+ " + n77, "owner? " + n78, "route+ " + n78, "route- " + n79}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls = %v", f.calls)
	}
	if !f.allow[iface+"|"+pub+"|"+n77] || f.allow[iface+"|"+pub+"|"+n79] || f.routes[n77+"|"+iface] != ours || f.routes[n79+"|"+iface] != "" {
		t.Fatalf("state: allow=%v routes=%v", f.allow, f.routes)
	}
}

// Правило 2: поверх существующей записи не встаём и своей не считаем —
// ни при добавлении, ни при последующем удалении сети.
func TestApply_ExistingRouteSkippedAndNeverOwned(t *testing.T) {
	f := newFakeRouter()
	f.routes[n77+"|"+iface] = "manual"
	if err := Apply(context.Background(), f, iface, pub, []string{n77}, nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "route+") {
			t.Fatalf("маршрут поверх чужого: %v", f.calls)
		}
	}
	if err := Apply(context.Background(), f, iface, pub, nil, []string{n77}); err != nil {
		t.Fatal(err)
	}
	if f.routes[n77+"|"+iface] != "manual" {
		t.Fatal("чужая запись снята")
	}
}

func TestApply_RouteFailureRollsBackInReverse(t *testing.T) {
	f := newFakeRouter()
	f.allow[iface+"|"+pub+"|"+n79] = true
	f.routes[n79+"|"+iface] = ours
	f.failOn = []string{"route+ " + n78}
	err := Apply(context.Background(), f, iface, pub, []string{n77, n78}, []string{n79})
	var rb *RollbackError
	if err == nil || errors.As(err, &rb) {
		t.Fatalf("err = %v", err)
	}
	// Отказ на добавлении маршрута 78: до снятия 79-го дело не дошло. Откат —
	// снять наш 77-й маршрут, вернуть allow 79, снять allow 78 и 77.
	tail := f.calls[len(f.calls)-4:]
	want := []string{"route- " + n77, "allow+ " + n79, "allow- " + n78, "allow- " + n77}
	if !reflect.DeepEqual(tail, want) {
		t.Fatalf("rollback = %v", tail)
	}
	if len(f.allow) != 1 || !f.allow[iface+"|"+pub+"|"+n79] || len(f.routes) != 1 || f.routes[n79+"|"+iface] != ours {
		t.Fatalf("состояние не восстановлено: allow=%v routes=%v", f.allow, f.routes)
	}
}

// Отказ на последнем шаге (снятие маршрута): откатываются все четыре списка,
// в том числе уже снятый наш маршрут 79 возвращается.
func TestApply_RouteRemovalFailureRestoresRemovedRoute(t *testing.T) {
	const n80 = "192.168.80.0/24"
	f := newFakeRouter()
	for _, n := range []string{n79, n80} {
		f.allow[iface+"|"+pub+"|"+n] = true
		f.routes[n+"|"+iface] = ours
	}
	f.failOn = []string{"route- " + n80}
	err := Apply(context.Background(), f, iface, pub, []string{n77, n78}, []string{n79, n80})
	var rb *RollbackError
	if err == nil || errors.As(err, &rb) {
		t.Fatalf("err = %v", err)
	}
	tail := f.calls[len(f.calls)-7:]
	want := []string{"route- " + n78, "route- " + n77, "route+ " + n79, "allow+ " + n80, "allow+ " + n79, "allow- " + n78, "allow- " + n77}
	if !reflect.DeepEqual(tail, want) {
		t.Fatalf("rollback = %v", tail)
	}
	if len(f.allow) != 2 || len(f.routes) != 2 || f.routes[n79+"|"+iface] != ours || f.routes[n80+"|"+iface] != ours {
		t.Fatalf("состояние не восстановлено: allow=%v routes=%v", f.allow, f.routes)
	}
}

func TestApply_RollbackFailureIsRollbackError(t *testing.T) {
	f := newFakeRouter()
	f.failOn = []string{"route+ ", "allow- "}
	err := Apply(context.Background(), f, iface, pub, []string{n77}, nil)
	var rb *RollbackError
	if !errors.As(err, &rb) || !strings.Contains(rb.Cause.Error(), "route+") || !strings.Contains(rb.Rollback.Error(), "allow-") {
		t.Fatalf("err = %v", err)
	}
}

func TestApply_InvalidCIDRBeforeAnyCall(t *testing.T) {
	f := newFakeRouter()
	if err := Apply(context.Background(), f, iface, pub, []string{"nonsense"}, nil); err == nil || len(f.calls) != 0 {
		t.Fatalf("err=%v calls=%v", err, f.calls)
	}
}

func TestRemoveRoutes_OwnOnlyAndFailClosed(t *testing.T) {
	f := newFakeRouter()
	f.routes[n77+"|"+iface] = ours
	f.routes[n78+"|"+iface] = "manual"
	if err := RemoveRoutes(context.Background(), f, iface, pub, []string{n77, n78}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.routes[n77+"|"+iface]; ok || f.routes[n78+"|"+iface] != "manual" {
		t.Fatalf("routes = %v", f.routes)
	}
	f = newFakeRouter()
	f.routes[n77+"|"+iface] = ours
	f.failOn = []string{"route- " + n77}
	if err := RemoveRoutes(context.Background(), f, iface, pub, []string{n77, n78}); err == nil || len(f.calls) != 1 {
		t.Fatalf("fail-closed: err=%v calls=%v", err, f.calls)
	}
}

func TestApply_CancelledCallerCtxStillRollsBack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := newFakeRouter()
	f.allow[iface+"|"+pub+"|"+n79] = true
	f.routes[n79+"|"+iface] = ours
	f.cancelAfter, f.cancel = "route+ "+n77, cancel
	err := Apply(ctx, f, iface, pub, []string{n77, n78}, []string{n79})
	var rb *RollbackError
	if !errors.Is(err, context.Canceled) || errors.As(err, &rb) {
		t.Fatalf("err = %v", err)
	}
	tail := f.calls[len(f.calls)-4:]
	want := []string{"route- " + n77, "allow+ " + n79, "allow- " + n78, "allow- " + n77}
	if !reflect.DeepEqual(tail, want) {
		t.Fatalf("rollback = %v", tail)
	}
	if len(f.allow) != 1 || !f.allow[iface+"|"+pub+"|"+n79] || len(f.routes) != 1 || f.routes[n79+"|"+iface] != ours {
		t.Fatalf("состояние не восстановлено: allow=%v routes=%v", f.allow, f.routes)
	}
}

func TestApply_AllowIPFailureRollsBackInReverse(t *testing.T) {
	const n80, n81 = "192.168.80.0/24", "192.168.81.0/24"
	cases := []struct {
		name           string
		failOn         string
		added, removed []string
		wantTail       []string
		wantAllowAfter []string
	}{
		{"allow+", "allow+ " + n80, []string{n77, n78, n80}, nil,
			[]string{"allow- " + n78, "allow- " + n77}, nil},
		{"allow-", "allow- " + n81, []string{n77, n78}, []string{n79, n80, n81},
			[]string{"allow+ " + n80, "allow+ " + n79, "allow- " + n78, "allow- " + n77},
			[]string{n79, n80, n81}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeRouter()
			for _, n := range tc.removed {
				f.allow[iface+"|"+pub+"|"+n] = true
			}
			f.failOn = []string{tc.failOn}
			if err := Apply(context.Background(), f, iface, pub, tc.added, tc.removed); err == nil {
				t.Fatal("нет ошибки")
			}
			tail := f.calls[len(f.calls)-len(tc.wantTail):]
			if !reflect.DeepEqual(tail, tc.wantTail) {
				t.Fatalf("rollback = %v", f.calls)
			}
			if len(f.allow) != len(tc.wantAllowAfter) {
				t.Fatalf("allow = %v", f.allow)
			}
			for _, n := range tc.wantAllowAfter {
				if !f.allow[iface+"|"+pub+"|"+n] {
					t.Fatalf("allow = %v", f.allow)
				}
			}
		})
	}
}

func TestApply_OwnerCheckFailureRollsBackAllowIPs(t *testing.T) {
	f := newFakeRouter()
	f.allow[iface+"|"+pub+"|"+n79] = true
	f.routes[n79+"|"+iface] = ours
	f.failOn = []string{"owner? " + n78}
	if err := Apply(context.Background(), f, iface, pub, []string{n77, n78}, []string{n79}); err == nil {
		t.Fatal("нет ошибки")
	}
	tail := f.calls[len(f.calls)-4:]
	want := []string{"route- " + n77, "allow+ " + n79, "allow- " + n78, "allow- " + n77}
	if !reflect.DeepEqual(tail, want) {
		t.Fatalf("rollback = %v", tail)
	}
	if len(f.allow) != 1 || len(f.routes) != 1 || f.routes[n79+"|"+iface] != ours {
		t.Fatalf("allow=%v routes=%v", f.allow, f.routes)
	}
}

// Наш маршрут, стоявший до Apply (сирота прошлого сбоя), пропускается и
// откатом не снимается: откат возвращает роутер к состоянию ДО вызова.
func TestApply_PreexistingOwnRouteSurvivesRollback(t *testing.T) {
	f := newFakeRouter()
	f.routes[n77+"|"+iface] = ours
	f.failOn = []string{"route+ " + n78}
	if err := Apply(context.Background(), f, iface, pub, []string{n77, n78}, nil); err == nil {
		t.Fatal("нет ошибки")
	}
	for _, c := range f.calls {
		if c == "route+ "+n77 || c == "route- "+n77 {
			t.Fatalf("наш прежний маршрут тронут: %v", f.calls)
		}
	}
	if f.routes[n77+"|"+iface] != ours {
		t.Fatalf("routes = %v", f.routes)
	}
}

func TestRollbackError_UnwrapsToCause(t *testing.T) {
	f := newFakeRouter()
	f.failOn = []string{"route+ ", "allow- "}
	err := Apply(context.Background(), f, iface, pub, []string{n77}, nil)
	var rb *RollbackError
	if !errors.As(err, &rb) || len(f.injected) == 0 || !errors.Is(err, f.injected[0]) {
		t.Fatalf("err = %v injected = %v", err, f.injected)
	}
	for _, e := range f.injected[1:] {
		if errors.Is(err, e) {
			t.Fatalf("ошибка отката видна через Unwrap: %v", e)
		}
	}
}
