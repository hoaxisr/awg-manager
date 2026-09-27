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
}

func newFakeRouter() *fakeRouter {
	return &fakeRouter{allow: map[string]bool{}, routes: map[string]string{}}
}

func (f *fakeRouter) call(name string) error {
	f.calls = append(f.calls, name)
	for _, s := range f.failOn {
		if strings.Contains(name, s) {
			return errors.New("boom: " + name)
		}
	}
	return nil
}

func (f *fakeRouter) AddAllowIP(_ context.Context, iface, pub string, n *net.IPNet) error {
	if err := f.call("allow+ " + n.String()); err != nil {
		return err
	}
	f.allow[iface+"|"+pub+"|"+n.String()] = true
	return nil
}

func (f *fakeRouter) RemoveAllowIP(_ context.Context, iface, pub string, n *net.IPNet) error {
	if err := f.call("allow- " + n.String()); err != nil {
		return err
	}
	delete(f.allow, iface+"|"+pub+"|"+n.String())
	return nil
}

func (f *fakeRouter) NetworkRouteOwner(_ context.Context, n *net.IPNet, iface, comment string) (bool, bool, error) {
	if err := f.call("owner? " + n.String()); err != nil {
		return false, false, err
	}
	c, ok := f.routes[n.String()+"|"+iface]
	return ok, ok && c == comment, nil
}

func (f *fakeRouter) AddNetworkRoute(_ context.Context, n *net.IPNet, iface, comment string) error {
	if err := f.call("route+ " + n.String()); err != nil {
		return err
	}
	f.routes[n.String()+"|"+iface] = comment
	return nil
}

func (f *fakeRouter) RemoveOwnNetworkRoute(_ context.Context, n *net.IPNet, iface, comment string) (bool, error) {
	if err := f.call("route- " + n.String()); err != nil {
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
