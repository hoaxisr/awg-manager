package peersubnet

import (
	"context"
	"errors"
	"net"
	"reflect"
	"sort"
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
	// вызывающего (отключение клиента посреди Reconcile).
	cancelAfter string
	cancel      context.CancelFunc
	// absent — пира на интерфейсе нет (PeerAllowIPs → ErrPeerNotFound).
	absent bool
	// beforeOwner — хук перед ответом NetworkRouteOwner (запись, появившаяся
	// после снимка).
	beforeOwner func(n string)
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

func mustNet(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

func (f *fakeRouter) PeerAllowIPs(ctx context.Context, iface, pub string) ([]*net.IPNet, error) {
	if err := f.call(ctx, "read allow"); err != nil {
		return nil, err
	}
	if f.absent {
		return nil, ErrPeerNotFound
	}
	var keys []string
	for k := range f.allow {
		if strings.HasPrefix(k, iface+"|"+pub+"|") {
			keys = append(keys, strings.TrimPrefix(k, iface+"|"+pub+"|"))
		}
	}
	sort.Strings(keys)
	out := make([]*net.IPNet, 0, len(keys))
	for _, k := range keys {
		out = append(out, mustNet(k))
	}
	return out, nil
}

func (f *fakeRouter) InterfaceRoutes(ctx context.Context, iface string) ([]Route, error) {
	if err := f.call(ctx, "read routes"); err != nil {
		return nil, err
	}
	var keys []string
	for k := range f.routes {
		if strings.HasSuffix(k, "|"+iface) {
			keys = append(keys, strings.TrimSuffix(k, "|"+iface))
		}
	}
	sort.Strings(keys)
	out := make([]Route, 0, len(keys))
	for _, k := range keys {
		out = append(out, Route{Net: mustNet(k), Comment: f.routes[k+"|"+iface]})
	}
	return out, nil
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
	if err := f.call(ctx, "read owner "+n.String()); err != nil {
		return false, false, err
	}
	if f.beforeOwner != nil {
		f.beforeOwner(n.String())
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

// mutations — журнал без чтений состояния.
func (f *fakeRouter) mutations() []string {
	var out []string
	for _, c := range f.calls {
		if !strings.HasPrefix(c, "read ") {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeRouter) setAllow(nets ...string) {
	for _, n := range nets {
		f.allow[iface+"|"+pub+"|"+n] = true
	}
}

const (
	n77, n78, n79 = "192.168.77.0/24", "192.168.78.0/24", "192.168.79.0/24"
	iface, pub    = "Wireguard9", "5+0I/P0Vaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa="
	ours          = "awgm-peer:5+0I/P0V"
	tunnel32      = "10.9.9.2/32"
)

var tunnelHost = []net.IP{net.ParseIP("10.9.9.2")}

func TestReconcile_OrderAllowIPsThenRoutes(t *testing.T) {
	f := newFakeRouter()
	f.setAllow(tunnel32, n79)
	f.routes[n79+"|"+iface] = ours
	if err := Reconcile(context.Background(), f, iface, pub, tunnelHost, []string{n77, n78}); err != nil {
		t.Fatal(err)
	}
	want := []string{"allow+ " + n77, "allow+ " + n78, "allow- " + n79, "route+ " + n77, "route+ " + n78, "route- " + n79}
	if !reflect.DeepEqual(f.mutations(), want) {
		t.Fatalf("calls = %v", f.calls)
	}
	if !f.allow[iface+"|"+pub+"|"+n77] || f.allow[iface+"|"+pub+"|"+n79] || f.routes[n77+"|"+iface] != ours || f.routes[n79+"|"+iface] != "" {
		t.Fatalf("state: allow=%v routes=%v", f.allow, f.routes)
	}
}

// F509: расхождение роутера с хранилищем лечится сохранением того же списка.
// В записи [77], на роутере ещё сирота 78 (allow-ip и наш маршрут): снимается
// 78, по 77 — ни одного лишнего вызова.
func TestReconcile_HealsOrphan(t *testing.T) {
	f := newFakeRouter()
	f.setAllow(tunnel32, n77, n78)
	f.routes[n77+"|"+iface] = ours
	f.routes[n78+"|"+iface] = ours
	if err := Reconcile(context.Background(), f, iface, pub, tunnelHost, []string{n77}); err != nil {
		t.Fatal(err)
	}
	want := []string{"allow- " + n78, "route- " + n78}
	if !reflect.DeepEqual(f.mutations(), want) {
		t.Fatalf("calls = %v", f.calls)
	}
	if !f.allow[iface+"|"+pub+"|"+n77] || f.routes[n77+"|"+iface] != ours || f.allow[iface+"|"+pub+"|"+n78] || f.routes[n78+"|"+iface] != "" {
		t.Fatalf("state: allow=%v routes=%v", f.allow, f.routes)
	}
}

// Роутер потерял сеть (ни allow-ip, ни маршрута) — сохранение того же
// списка её восстанавливает; частичная потеря — только недостающее.
func TestReconcile_RestoresLost(t *testing.T) {
	f := newFakeRouter()
	f.setAllow(tunnel32, n78)
	if err := Reconcile(context.Background(), f, iface, pub, tunnelHost, []string{n77, n78}); err != nil {
		t.Fatal(err)
	}
	want := []string{"allow+ " + n77, "route+ " + n77, "route+ " + n78}
	if !reflect.DeepEqual(f.mutations(), want) {
		t.Fatalf("calls = %v", f.calls)
	}
}

// Туннельный /32 (старый и новый при смене адреса) — не сеть за клиентом:
// ни снятия allow-ip, ни снятия маршрута с меткой. Посторонний /32 — сеть.
func TestReconcile_TunnelHostsNeverTouched(t *testing.T) {
	const newTunnel32, hostNet = "10.9.9.3/32", "192.168.50.7/32"
	f := newFakeRouter()
	f.setAllow(tunnel32, newTunnel32, hostNet)
	f.routes[newTunnel32+"|"+iface] = ours
	hosts := []net.IP{net.ParseIP("10.9.9.2"), net.ParseIP("10.9.9.3")}
	if err := Reconcile(context.Background(), f, iface, pub, hosts, nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"allow- " + hostNet}
	if !reflect.DeepEqual(f.mutations(), want) {
		t.Fatalf("calls = %v", f.calls)
	}
}

// Вне области сетей за клиентом (IPv6, 0.0.0.0/0) allow-ips не трогаются:
// в RemoteSubnets их быть не может.
func TestReconcile_NonSubnetAllowIPsUntouched(t *testing.T) {
	f := newFakeRouter()
	f.setAllow(tunnel32, "0.0.0.0/0", "fd00::/64")
	if err := Reconcile(context.Background(), f, iface, pub, tunnelHost, nil); err != nil {
		t.Fatal(err)
	}
	if m := f.mutations(); len(m) != 0 {
		t.Fatalf("calls = %v", f.calls)
	}
}

// Правило 2: поверх чужой записи не встаём и своей не считаем — ни при
// добавлении, ни при снятии сети.
func TestReconcile_ForeignRouteNeverTouched(t *testing.T) {
	f := newFakeRouter()
	f.routes[n77+"|"+iface] = "manual"
	f.routes[n78+"|"+iface] = "awgm-peer:5+0I/P0Vx" // метка сверяется целиком
	if err := Reconcile(context.Background(), f, iface, pub, tunnelHost, []string{n77}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"allow+ " + n77}; !reflect.DeepEqual(f.mutations(), want) {
		t.Fatalf("calls = %v", f.calls)
	}
	f.calls = nil
	if err := Reconcile(context.Background(), f, iface, pub, tunnelHost, nil); err != nil {
		t.Fatal(err)
	}
	if want := []string{"allow- " + n77}; !reflect.DeepEqual(f.mutations(), want) {
		t.Fatalf("calls = %v", f.calls)
	}
	if f.routes[n77+"|"+iface] != "manual" || f.routes[n78+"|"+iface] != "awgm-peer:5+0I/P0Vx" {
		t.Fatalf("чужая запись тронута: %v", f.routes)
	}
}

// Отказ чтения состояния — ошибка до единой мутации (fail-closed).
func TestReconcile_ReadFailureNoMutation(t *testing.T) {
	for _, fail := range []string{"read allow", "read routes"} {
		f := newFakeRouter()
		f.setAllow(n79)
		f.failOn = []string{fail}
		if err := Reconcile(context.Background(), f, iface, pub, tunnelHost, []string{n77}); err == nil || len(f.mutations()) != 0 {
			t.Fatalf("%s: err=%v calls=%v", fail, err, f.calls)
		}
	}
}

// Откат возвращает роутер к состоянию ДО вызова: стоявшее раньше (allow-ip 77,
// наш маршрут 80) не снимается, снятое (allow-ip и маршрут 79) возвращается.
func TestReconcile_RollbackUndoesOnlyOwnChanges(t *testing.T) {
	const n80 = "192.168.80.0/24"
	f := newFakeRouter()
	f.setAllow(tunnel32, n77, n79, n80)
	f.routes[n79+"|"+iface] = ours
	f.routes[n80+"|"+iface] = ours
	f.failOn = []string{"route+ " + n78}
	err := Reconcile(context.Background(), f, iface, pub, tunnelHost, []string{n77, n78, n80})
	var rb *RollbackError
	if err == nil || errors.As(err, &rb) {
		t.Fatalf("err = %v", err)
	}
	// Шаги: allow+78, allow-79, route+77, route+78 (отказ). Откат обратным
	// порядком: снять маршрут 77, вернуть allow 79, снять allow 78.
	want := []string{"allow+ " + n78, "allow- " + n79, "route+ " + n77, "route+ " + n78,
		"route- " + n77, "allow+ " + n79, "allow- " + n78}
	if !reflect.DeepEqual(f.mutations(), want) {
		t.Fatalf("calls = %v", f.calls)
	}
	wantAllow := map[string]bool{}
	for _, n := range []string{tunnel32, n77, n79, n80} {
		wantAllow[iface+"|"+pub+"|"+n] = true
	}
	wantRoutes := map[string]string{n79 + "|" + iface: ours, n80 + "|" + iface: ours}
	if !reflect.DeepEqual(f.allow, wantAllow) || !reflect.DeepEqual(f.routes, wantRoutes) {
		t.Fatalf("состояние не восстановлено: allow=%v routes=%v", f.allow, f.routes)
	}
}

// Отказ на последнем шаге (снятие маршрута): откатываются все четыре списка,
// в том числе уже снятый наш маршрут возвращается.
func TestReconcile_RouteRemovalFailureRestoresRemovedRoute(t *testing.T) {
	const n80 = "192.168.80.0/24"
	f := newFakeRouter()
	f.setAllow(n79, n80)
	f.routes[n79+"|"+iface] = ours
	f.routes[n80+"|"+iface] = ours
	f.failOn = []string{"route- " + n80}
	if err := Reconcile(context.Background(), f, iface, pub, tunnelHost, []string{n77, n78}); err == nil {
		t.Fatal("нет ошибки")
	}
	m := f.mutations()
	tail := m[len(m)-7:]
	want := []string{"route- " + n78, "route- " + n77, "route+ " + n79, "allow+ " + n80, "allow+ " + n79, "allow- " + n78, "allow- " + n77}
	if !reflect.DeepEqual(tail, want) {
		t.Fatalf("rollback = %v", tail)
	}
	if len(f.allow) != 2 || len(f.routes) != 2 || f.routes[n79+"|"+iface] != ours || f.routes[n80+"|"+iface] != ours {
		t.Fatalf("состояние не восстановлено: allow=%v routes=%v", f.allow, f.routes)
	}
}

func TestReconcile_AllowIPFailureRollsBackInReverse(t *testing.T) {
	const n80, n81 = "192.168.80.0/24", "192.168.81.0/24"
	cases := []struct {
		name           string
		failOn         string
		have, desired  []string
		wantTail       []string
		wantAllowAfter []string
	}{
		{"allow+", "allow+ " + n80, nil, []string{n77, n78, n80},
			[]string{"allow- " + n78, "allow- " + n77}, nil},
		{"allow-", "allow- " + n81, []string{n79, n80, n81}, []string{n77, n78},
			[]string{"allow+ " + n80, "allow+ " + n79, "allow- " + n78, "allow- " + n77},
			[]string{n79, n80, n81}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeRouter()
			f.setAllow(tc.have...)
			f.failOn = []string{tc.failOn}
			if err := Reconcile(context.Background(), f, iface, pub, tunnelHost, tc.desired); err == nil {
				t.Fatal("нет ошибки")
			}
			m := f.mutations()
			if tail := m[len(m)-len(tc.wantTail):]; !reflect.DeepEqual(tail, tc.wantTail) {
				t.Fatalf("rollback = %v", m)
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

func TestReconcile_RollbackFailureIsRollbackError(t *testing.T) {
	f := newFakeRouter()
	f.failOn = []string{"route+ ", "allow- "}
	err := Reconcile(context.Background(), f, iface, pub, tunnelHost, []string{n77})
	var rb *RollbackError
	if !errors.As(err, &rb) || !strings.Contains(rb.Cause.Error(), "route+") || !strings.Contains(rb.Rollback.Error(), "allow-") {
		t.Fatalf("err = %v", err)
	}
}

func TestReconcile_InvalidCIDRBeforeAnyCall(t *testing.T) {
	f := newFakeRouter()
	if err := Reconcile(context.Background(), f, iface, pub, tunnelHost, []string{"nonsense"}); err == nil || len(f.calls) != 0 {
		t.Fatalf("err=%v calls=%v", err, f.calls)
	}
}

func TestReconcile_CancelledCallerCtxStillRollsBack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := newFakeRouter()
	f.setAllow(n79)
	f.routes[n79+"|"+iface] = ours
	f.cancelAfter, f.cancel = "route+ "+n77, cancel
	err := Reconcile(ctx, f, iface, pub, tunnelHost, []string{n77, n78})
	var rb *RollbackError
	if !errors.Is(err, context.Canceled) || errors.As(err, &rb) {
		t.Fatalf("err = %v", err)
	}
	m := f.mutations()
	tail := m[len(m)-4:]
	want := []string{"route- " + n77, "allow+ " + n79, "allow- " + n78, "allow- " + n77}
	if !reflect.DeepEqual(tail, want) {
		t.Fatalf("rollback = %v", tail)
	}
	if len(f.allow) != 1 || !f.allow[iface+"|"+pub+"|"+n79] || len(f.routes) != 1 || f.routes[n79+"|"+iface] != ours {
		t.Fatalf("состояние не восстановлено: allow=%v routes=%v", f.allow, f.routes)
	}
}

func TestRollbackError_UnwrapsToCause(t *testing.T) {
	f := newFakeRouter()
	f.failOn = []string{"route+ ", "allow- "}
	err := Reconcile(context.Background(), f, iface, pub, tunnelHost, []string{n77})
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

// Удаление пира снимает ВСЕ маршруты с его меткой, найденные на роутере, —
// хранилище о них может не знать (сирота после RollbackError). Чужие и
// метку-префикс не трогает.
func TestRemoveRoutes_SweepsAllOwnFromRouter(t *testing.T) {
	f := newFakeRouter()
	f.routes[n77+"|"+iface] = ours
	f.routes["192.168.79.5/32|"+iface] = ours
	f.routes[n78+"|"+iface] = "manual"
	f.routes["192.168.81.0/24|"+iface] = "awgm-peer:5+0I/P0Vx"
	f.routes[n77+"|Wireguard8"] = ours
	if err := RemoveRoutes(context.Background(), f, iface, pub); err != nil {
		t.Fatal(err)
	}
	want := []string{"route- " + n77, "route- 192.168.79.5/32"}
	if !reflect.DeepEqual(f.mutations(), want) {
		t.Fatalf("calls = %v", f.calls)
	}
	if len(f.routes) != 3 || f.routes[n78+"|"+iface] != "manual" || f.routes[n77+"|Wireguard8"] != ours {
		t.Fatalf("routes = %v", f.routes)
	}
}

func TestRemoveRoutes_FailClosed(t *testing.T) {
	f := newFakeRouter()
	f.routes[n77+"|"+iface] = ours
	f.failOn = []string{"read routes"}
	if err := RemoveRoutes(context.Background(), f, iface, pub); err == nil || len(f.mutations()) != 0 {
		t.Fatalf("чтение: err=%v calls=%v", err, f.calls)
	}
	f = newFakeRouter()
	f.routes[n77+"|"+iface] = ours
	f.routes[n78+"|"+iface] = ours
	f.failOn = []string{"route- " + n77}
	if err := RemoveRoutes(context.Background(), f, iface, pub); err == nil || len(f.mutations()) != 1 {
		t.Fatalf("снятие: err=%v calls=%v", err, f.calls)
	}
}

// M1: запись на (N, iface) появилась после снимка (веб-морда) — перед
// добавлением владение перечитывается: чужую не переписываем и своей не
// считаем (последующее снятие её не трогает).
func TestReconcile_RouteAppearedAfterSnapshot(t *testing.T) {
	f := newFakeRouter()
	f.beforeOwner = func(n string) {
		if n == n77 {
			f.routes[n77+"|"+iface] = "manual"
		}
	}
	if err := Reconcile(context.Background(), f, iface, pub, tunnelHost, []string{n77}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"allow+ " + n77}; !reflect.DeepEqual(f.mutations(), want) {
		t.Fatalf("calls = %v", f.calls)
	}
	f.beforeOwner, f.calls = nil, nil
	if err := Reconcile(context.Background(), f, iface, pub, tunnelHost, nil); err != nil {
		t.Fatal(err)
	}
	if f.routes[n77+"|"+iface] != "manual" {
		t.Fatalf("чужая запись тронута: %v", f.routes)
	}
}

// I1: пира на интерфейсе нет — ErrPeerNotFound до единой мутации: allow-ips
// на отсутствующий ключ NDMS создал бы пира-призрака.
func TestReconcile_PeerNotFound_NoMutation(t *testing.T) {
	f := newFakeRouter()
	f.absent = true
	err := Reconcile(context.Background(), f, iface, pub, tunnelHost, []string{n77})
	if !errors.Is(err, ErrPeerNotFound) || len(f.mutations()) != 0 {
		t.Fatalf("err=%v calls=%v", err, f.calls)
	}
}

// Отказ свежей проверки владения перед добавлением — откат сделанного.
func TestReconcile_OwnerCheckFailureRollsBack(t *testing.T) {
	f := newFakeRouter()
	f.setAllow(n79)
	f.routes[n79+"|"+iface] = ours
	f.failOn = []string{"read owner " + n78}
	if err := Reconcile(context.Background(), f, iface, pub, tunnelHost, []string{n77, n78}); err == nil {
		t.Fatal("нет ошибки")
	}
	m := f.mutations()
	want := []string{"allow+ " + n77, "allow+ " + n78, "allow- " + n79, "route+ " + n77,
		"route- " + n77, "allow+ " + n79, "allow- " + n78, "allow- " + n77}
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("calls = %v", m)
	}
	if len(f.allow) != 1 || len(f.routes) != 1 || f.routes[n79+"|"+iface] != ours {
		t.Fatalf("allow=%v routes=%v", f.allow, f.routes)
	}
}
