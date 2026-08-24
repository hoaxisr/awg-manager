package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instance"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/ndmsres"
)

var errRouter = errors.New("роутер не ответил")

// fakeRC — running-config фикстурой. Блоки — как в живом rc: заголовок без
// отступа, содержимое с отступом.
type fakeRC struct {
	lines []string
	err   error
}

func (f fakeRC) Lines(context.Context) ([]string, error) { return f.lines, f.err }

type fakeIfaces struct {
	list []ndms.Interface
	err  error
}

func (f fakeIfaces) List(context.Context) ([]ndms.Interface, error) { return f.list, f.err }

type fakePolicyList struct {
	list []ndms.Policy
	err  error
}

func (f fakePolicyList) List(context.Context) ([]ndms.Policy, error) { return f.list, f.err }

// fakeDefaultRoute записывает ИМЯ, с которым позвали, — подмена
// SetDefaultRoute на RemoveDefaultRoute компилируется (сигнатуры совпадают),
// поэтому сверяется и метод, и аргумент.
type fakeDefaultRoute struct {
	names []string
	err   error
}

func (f *fakeDefaultRoute) SetDefaultRoute(_ context.Context, name string) error {
	f.names = append(f.names, name)
	return f.err
}

// fakeSweepCommands — ndmsres.Commands с записью удалений. Встроенный
// интерфейс nil: прочих методов уборщик не зовёт, вызов упал бы паникой.
type fakeSweepCommands struct {
	ndmsres.Commands
	deleted []string
	err     error
}

func (f *fakeSweepCommands) DeleteOpkgTun(_ context.Context, name string) error {
	f.deleted = append(f.deleted, name)
	return f.err
}

type fakeResolver struct {
	asked  []string
	byName map[string]string
}

func (f *fakeResolver) ResolveSystemName(_ context.Context, ndmsName string) string {
	f.asked = append(f.asked, ndmsName)
	return f.byName[ndmsName]
}

// rcFixture — три блока и обе печатные формы кандидатуры default route.
//
// Порядок блоков значим: OpkgTun18 идёт ДО блока с `ip global`, поэтому
// «утечка» блока (потерянный сброс признака на неотступной строке) окрасит
// кейс HasIPGlobal(OpkgTun18) в красный.
func rcFixture() fakeRC {
	return fakeRC{lines: []string{
		"interface OpkgTun18",
		"  description AWGM WDTT Raw Client: Имя",
		"  ip access-group _WEBADMIN_OpkgTun18 in",
		"interface OpkgTun19",
		"  no ip global",
		"  no ip access-group _WEBADMIN_OpkgTun19 in",
		"interface OpkgTun20",
		"  ip global order 3",
		"ip route default OpkgTun20",
		"ip route default interface OpkgTun18",
	}}
}

func TestRcHasInterfaceLine(t *testing.T) {
	q := proxyNDMSQuery{rc: rcFixture()}
	cases := []struct {
		name  string
		check func() (bool, error)
		want  bool
	}{
		{"ip global в своём блоке", func() (bool, error) { return q.HasIPGlobal(context.Background(), "OpkgTun20") }, true},
		{"ip global чужого блока не течёт", func() (bool, error) { return q.HasIPGlobal(context.Background(), "OpkgTun18") }, false},
		{"no ip global — не совпадает", func() (bool, error) { return q.HasIPGlobal(context.Background(), "OpkgTun19") }, false},
		{"ACL точным совпадением", func() (bool, error) { return q.HasPermitAllACL(context.Background(), "OpkgTun18") }, true},
		{"no ip access-group — не совпадает", func() (bool, error) { return q.HasPermitAllACL(context.Background(), "OpkgTun19") }, false},
		{"ACL чужого блока не считается", func() (bool, error) { return q.HasPermitAllACL(context.Background(), "OpkgTun20") }, false},
		{"кандидатура default route", func() (bool, error) { return q.HasDefaultRoute(context.Background(), "OpkgTun20") }, true},
		{"кандидатура во второй печатной форме", func() (bool, error) { return q.HasDefaultRoute(context.Background(), "OpkgTun18") }, true},
		{"кандидатуры нет", func() (bool, error) { return q.HasDefaultRoute(context.Background(), "OpkgTun19") }, false},
	}
	for _, c := range cases {
		got, err := c.check()
		if err != nil || got != c.want {
			t.Fatalf("%s: %v %v", c.name, got, err)
		}
	}
}

// Ошибка чтения running-config обязана дойти до вызывающего: «не смогли
// посмотреть» — не то же самое, что «нет».
func TestRcQueriesPropagateError(t *testing.T) {
	q := proxyNDMSQuery{rc: fakeRC{err: errRouter}}
	for name, check := range map[string]func() (bool, error){
		"HasIPGlobal":     func() (bool, error) { return q.HasIPGlobal(context.Background(), "OpkgTun18") },
		"HasPermitAllACL": func() (bool, error) { return q.HasPermitAllACL(context.Background(), "OpkgTun18") },
		"HasDefaultRoute": func() (bool, error) { return q.HasDefaultRoute(context.Background(), "OpkgTun18") },
	} {
		if _, err := check(); !errors.Is(err, errRouter) {
			t.Fatalf("%s проглотил ошибку: %v", name, err)
		}
	}
}

func TestIfaceFactsFromList(t *testing.T) {
	q := proxyNDMSQuery{ifaces: fakeIfaces{list: []ndms.Interface{
		{ID: "OpkgTun17", Description: "чужой", SecurityLevel: "private", Address: "10.1.1.1", Mask: "255.0.0.0", MTU: 1400, ConfLayer: "running"},
		{
			ID: "OpkgTun18", Description: "AWGM WDTT Raw Client: Имя",
			SecurityLevel: "public", Address: "10.70.0.5", Mask: "255.255.255.255",
			MTU: 1300, ConfLayer: "running",
		},
		{ID: "OpkgTun20", Description: "выключенный", SecurityLevel: "public", MTU: 1500, ConfLayer: "disabled"},
	}}}

	f, ok, err := q.Iface(context.Background(), "OpkgTun18")
	if err != nil || !ok {
		t.Fatalf("%+v %v %v", f, ok, err)
	}
	want := ndmsres.IfaceFacts{
		Description: "AWGM WDTT Raw Client: Имя", SecurityLevel: "public",
		Address: "10.70.0.5", Mask: "255.255.255.255", MTU: 1300, AdminUp: true,
	}
	if !reflect.DeepEqual(f, want) {
		t.Fatalf("поля перепутаны или потеряны:\n got %+v\nwant %+v", f, want)
	}

	if f, _, _ := q.Iface(context.Background(), "OpkgTun20"); f.AdminUp {
		t.Fatal("ConfLayer=disabled — это AdminUp=false")
	}
	if _, ok, _ := q.Iface(context.Background(), "OpkgTun19"); ok {
		t.Fatal("отсутствие в списке = подтверждённое «нет»")
	}
	q = proxyNDMSQuery{ifaces: fakeIfaces{err: errRouter}}
	if _, ok, err := q.Iface(context.Background(), "OpkgTun18"); ok || !errors.Is(err, errRouter) {
		t.Fatalf("ошибка списка обязана дойти: %v %v", ok, err)
	}
}

func TestOpkgTunIndex(t *testing.T) {
	for _, c := range []struct {
		in string
		n  int
		ok bool
	}{
		{"OpkgTun18", 18, true},
		{"OpkgTun0", 0, true},
		{"awg10", 0, false},
		{"Bridge0", 0, false},
		{"OpkgTun", 0, false},
		{"OpkgTun1x", 0, false},
		{"OpkgTun-1", 0, false},
	} {
		n, ok := opkgTunIndex(c.in)
		if n != c.n || ok != c.ok {
			t.Fatalf("%s: %d %v", c.in, n, ok)
		}
	}
}

func TestLivePermitsForSkipsDenied(t *testing.T) {
	fn := livePermitsFor(fakePolicyList{list: []ndms.Policy{
		{Name: "P1", Interfaces: []ndms.PermittedIface{{Name: "OpkgTun18"}}},
		{Name: "P2", Interfaces: []ndms.PermittedIface{{Name: "OpkgTun18", Denied: true}}},
		{Name: "P3", Interfaces: []ndms.PermittedIface{{Name: "OpkgTun19"}}},
	}})
	got, err := fn(context.Background(), "OpkgTun18")
	if err != nil || len(got) != 1 || got[0] != "P1" {
		t.Fatalf("%v %v (denied и чужие — мимо)", got, err)
	}
	if _, err := livePermitsFor(fakePolicyList{err: errRouter})(context.Background(), "OpkgTun18"); !errors.Is(err, errRouter) {
		t.Fatalf("ошибка списка политик обязана дойти: %v", err)
	}
}

func TestProxySweepScannerPrefixAndLabel(t *testing.T) {
	s := proxySweepScanner{ifaces: fakeIfaces{list: []ndms.Interface{
		{ID: "OpkgTun18", Description: "AWGM WDTT Raw Client: Имя"},
		{ID: "OpkgTun19", Description: "чужое описание"},
		{ID: "Bridge0", Description: "AWGM WDTT"}, // не OpkgTun — мимо
	}}}
	got, err := s.Scan(context.Background(), instance.SweepLabels())
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].Name != "OpkgTun18" || got[0].Label != "AWGM WDTT Raw Client: Имя" {
		t.Fatalf("Label обязан быть ПРОЧИТАННЫМ описанием, не константой: %+v", got[0])
	}
	if _, err := (proxySweepScanner{ifaces: fakeIfaces{err: errRouter}}).Scan(context.Background(), instance.SweepLabels()); !errors.Is(err, errRouter) {
		t.Fatalf("ошибка списка обязана дойти, иначе уборщик решит, что сирот нет: %v", err)
	}
}

// Кандидатура пишется ИМЕННО SetDefaultRoute и ИМЕННО именем интерфейса:
// у RemoveDefaultRoute та же сигнатура, подмена собралась бы молча.
func TestEnsureDefaultRouteCandidacyArgs(t *testing.T) {
	fr := &fakeDefaultRoute{}
	if err := (proxyNDMSCommands{routes: fr}).EnsureDefaultRouteCandidacy(context.Background(), "OpkgTun18"); err != nil {
		t.Fatalf("%v", err)
	}
	if !reflect.DeepEqual(fr.names, []string{"OpkgTun18"}) {
		t.Fatalf("SetDefaultRoute позван с %v", fr.names)
	}
	fr = &fakeDefaultRoute{err: errRouter}
	if err := (proxyNDMSCommands{routes: fr}).EnsureDefaultRouteCandidacy(context.Background(), "OpkgTun18"); !errors.Is(err, errRouter) {
		t.Fatalf("ошибка записи кандидатуры обязана дойти: %v", err)
	}
}

// Сирота сносится по ИМЕНИ, не по описанию: оба поля строковые, подмена
// собралась бы молча и снесла бы не тот интерфейс (или ничего).
func TestProxySweepRemoverDeletesByName(t *testing.T) {
	fc := &fakeSweepCommands{}
	res := proxyrt.OwnedResource{Label: "AWGM WDTT Raw Client: Имя", Name: "OpkgTun18"}
	if err := (proxySweepRemover{cmds: fc}).Remove(context.Background(), res); err != nil {
		t.Fatalf("%v", err)
	}
	if !reflect.DeepEqual(fc.deleted, []string{"OpkgTun18"}) {
		t.Fatalf("DeleteOpkgTun позван с %v", fc.deleted)
	}
	fc = &fakeSweepCommands{err: errRouter}
	if err := (proxySweepRemover{cmds: fc}).Remove(context.Background(), res); !errors.Is(err, errRouter) {
		t.Fatalf("ошибка сноса обязана дойти: %v", err)
	}
}

func TestProxyKernelWANResolvesName(t *testing.T) {
	r := &fakeResolver{byName: map[string]string{"ISP": "eth3", "Blank": "  "}}
	fn := proxyKernelWAN(r)

	got, err := fn(context.Background(), "ISP")
	if err != nil || got != "eth3" {
		t.Fatalf("%q %v", got, err)
	}
	if !reflect.DeepEqual(r.asked, []string{"ISP"}) {
		t.Fatalf("спрошено %v, а должно быть NDMS-имя ISP", r.asked)
	}
	if _, err := fn(context.Background(), "Wireguard9"); err == nil {
		t.Fatal("неизвестное kernel-имя — ошибка, а не пустая строка")
	}
	if _, err := fn(context.Background(), "Blank"); err == nil {
		t.Fatal("пробельное kernel-имя — тоже ошибка")
	}
}
