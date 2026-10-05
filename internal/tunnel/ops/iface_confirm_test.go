package ops

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// opkgTun10 — наша запись туннеля awg10 (описание = имени из lifecycleCfg).
func opkgTun10() ndms.Interface {
	return ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun", Description: "Germany"}
}

// clean — оракул не видел ни E, ни C, ни фантомов.
func clean(t *testing.T, f *ndmsquery.FakeNDMS) {
	t.Helper()
	if f.E != 0 || f.C != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d C=%d фантомов=%d, want 0/0/0; posts=%v", f.E, f.C, f.Phantoms, f.Posts)
	}
}

// Записи OpkgTun10 в NDMS нет (снята снаружи): Delete не шлёт ни одной
// команды — `no interface` по отсутствующему даёт E в журнале ndm, — но
// kernel-устройство снимает и отвечает nil: запись туннеля удаляется.
func TestDelete_RecordGone_NoNDMSCommandsKernelStopped(t *testing.T) {
	f := ndmsquery.NewFakeNDMS()
	be := &MockBackend{running: true}
	o, _, _ := newOS5Oracle(t, f, be)

	if err := o.Delete(context.Background(), &storage.AWGTunnel{ID: "awg10", Name: "Germany"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды в NDMS при отсутствующей записи: %v", f.Posts)
	}
	if !slices.Equal(be.StopCalls, []string{"opkgtun10"}) {
		t.Fatalf("kernel-устройство не снято: %v", be.StopCalls)
	}
	clean(t, f)
}

// Запись есть — снимается одной командой, E и фантомов нет.
func TestDelete_Present_RemovesRecord(t *testing.T) {
	f := ndmsquery.NewFakeNDMS(opkgTun10())
	o, _, _ := newOS5Oracle(t, f, &MockBackend{running: true})

	if err := o.Delete(context.Background(), &storage.AWGTunnel{ID: "awg10", Name: "Germany"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if f.Has("OpkgTun10") {
		t.Fatalf("запись не снята: %v", f.Posts)
	}
	clean(t, f)
}

// Список не прочитан — «не знаем»: ни host-route, ни NDMS, ни
// kernel-устройство не трогаем (устройство снимается только перед сносом
// записи, а сносить её без списка нельзя), ошибка наружу — оркестратор
// оставит запись туннеля для повтора, туннель цел (F596, L4, N3).
func TestDelete_ListError_KeepsDevice(t *testing.T) {
	f := ndmsquery.NewFakeNDMS(opkgTun10())
	f.FailList(errors.New("injected: rci"))
	be := &MockBackend{running: true}
	o, _, rec := newOS5Oracle(t, f, be)

	err := o.Delete(context.Background(), &storage.AWGTunnel{ID: "awg10", Name: "Germany", ResolvedEndpointIP: "203.0.113.5"})
	if err == nil || !strings.Contains(err.Error(), "injected: rci") {
		t.Fatalf("err = %v, want отказ чтения списка", err)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды в NDMS без списка: %v", f.Posts)
	}
	if len(be.StopCalls) != 0 {
		t.Fatalf("kernel-устройство снято без списка: %v", be.StopCalls)
	}
	if len(rec.Calls) != 0 {
		t.Fatalf("снос без списка (host-route и пр.):\n%s", strings.Join(rec.Calls, "\n"))
	}
	if !f.Has("OpkgTun10") {
		t.Fatal("запись снята без списка")
	}
}

// failDeletePoster — оракул, отвергающий `no interface OpkgTun10`.
type failDeletePoster struct{ f *ndmsquery.FakeNDMS }

func (p failDeletePoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	if js, _ := json.Marshal(payload); string(js) == `{"interface":{"OpkgTun10":{"no":true}}}` {
		return nil, errors.New("injected: delete")
	}
	return p.f.Post(ctx, payload)
}

// Снос записи отвергнут — ошибка наружу: оркестратор не удалит запись
// туннеля, OpkgTun10 не останется на роутере без хозяина (F559, как nwg).
// Под записью к этому моменту plain tun (подмена идёт до сноса, C3a), а
// снятия устройства после отказа нет: запись не остаётся без устройства.
func TestDelete_DeleteRecordFails_Error(t *testing.T) {
	f := ndmsquery.NewFakeNDMS(opkgTun10())
	be := &MockBackend{running: true}
	o, _ := newOS5LifecycleOn(t, failDeletePoster{f}, f, be, true)

	err := o.Delete(context.Background(), &storage.AWGTunnel{ID: "awg10", Name: "Germany"})
	if err == nil || !strings.Contains(err.Error(), "injected: delete") {
		t.Fatalf("err = %v, want отказ сноса записи", err)
	}
	if !slices.Equal(be.ReplaceCalls, []string{"opkgtun10"}) || len(be.StopCalls) != 0 || len(be.StopIfPresentCalls) != 0 {
		t.Fatalf("replace=%v stop=%v stopIfPresent=%v, want [opkgtun10]/[]/[]", be.ReplaceCalls, be.StopCalls, be.StopIfPresentCalls)
	}
	clean(t, f)
}

// Stop снимает DNS, поставленный стартом (как OS4 Stop); записи нет —
// снимать не с чего, команды нет (F561).
func TestStop_ClearsAppliedDNS(t *testing.T) {
	const clear = `{"ip":{"name-server":{"address":"1.1.1.1","interface":"OpkgTun10","no":true}}}`
	for _, gone := range []bool{false, true} {
		f := ndmsquery.NewFakeNDMS(opkgTun10())
		o, _, _ := newOS5Oracle(t, f, &MockBackend{})
		cfg := lifecycleCfg(t)
		cfg.DNS = []string{"1.1.1.1"}
		if err := o.ColdStart(context.Background(), cfg); err != nil {
			t.Fatalf("ColdStart: %v", err)
		}
		if gone {
			f.Remove("OpkgTun10")
		}
		before := len(f.Posts)
		if err := o.Stop(context.Background(), "awg10", "Germany"); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if got := slices.Contains(f.Posts[before:], clear); got == gone {
			t.Fatalf("gone=%v: снятие DNS=%v; posts=%v", gone, got, f.Posts[before:])
		}
		clean(t, f)
	}
}

// rejectDNSPoster — оракул, отвергающий установку name-server 8.8.8.8.
type rejectDNSPoster struct{ f *ndmsquery.FakeNDMS }

func (p rejectDNSPoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	js, _ := json.Marshal(payload)
	if strings.Contains(string(js), `"address":"8.8.8.8","interface"`) && !strings.Contains(string(js), `"no":true`) {
		return nil, errors.New("injected: dns")
	}
	return p.f.Post(ctx, payload)
}

// SyncDNS упал на середине: трекинг — то, что реально стоит, и Stop снимает
// ровно это, не прежние (уже снятые) и не отвергнутые (F561, раунд 1).
func TestSyncDNS_PartialFailure_TrackingTruthful(t *testing.T) {
	f := ndmsquery.NewFakeNDMS(opkgTun10())
	o, _ := newOS5LifecycleOn(t, rejectDNSPoster{f}, f, &MockBackend{}, true)
	cfg := lifecycleCfg(t)
	cfg.DNS = []string{"1.1.1.1"}
	if err := o.ColdStart(context.Background(), cfg); err != nil {
		t.Fatalf("ColdStart: %v", err)
	}
	if err := o.SyncDNS(context.Background(), "awg10", []string{"9.9.9.9", "8.8.8.8"}); err == nil {
		t.Fatal("SyncDNS: ждали отказ")
	}
	before := len(f.Posts)
	if err := o.Stop(context.Background(), "awg10", "Germany"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	var cleared []string
	for _, p := range f.Posts[before:] {
		if strings.Contains(p, `"name-server"`) {
			cleared = append(cleared, p)
		}
	}
	want := `{"ip":{"name-server":{"address":"9.9.9.9","interface":"OpkgTun10","no":true}}}`
	if len(cleared) != 1 || cleared[0] != want {
		t.Fatalf("Stop снял %v, want только 9.9.9.9", cleared)
	}
	clean(t, f)
}

// Список не прочитан — устройство всё равно опущено, NDMS не тронут, ошибка.
func TestStop_ListError_LinkDownThenError(t *testing.T) {
	f := ndmsquery.NewFakeNDMS(opkgTun10())
	f.FailList(errors.New("injected: rci"))
	o, _, rec := newOS5Oracle(t, f, &MockBackend{running: true})

	err := o.Stop(context.Background(), "awg10", "Germany")
	if err == nil || !strings.Contains(err.Error(), "injected: rci") {
		t.Fatalf("err = %v, want отказ чтения списка", err)
	}
	if !hasCall(rec.Calls, "/opt/sbin/ip link set down dev opkgtun10") {
		t.Fatalf("устройство не опущено:\n%s", strings.Join(rec.Calls, "\n"))
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды в NDMS без списка: %v", f.Posts)
	}
}

// Старт по существующей записи: ровно одно чтение списка на весь поток —
// команды идут по одному доказательству, после них только точечные
// перечитывания известной записи (без E).
func TestColdStart_OneListThenPointReads(t *testing.T) {
	f := ndmsquery.NewFakeNDMS(opkgTun10())
	o, _, _ := newOS5Oracle(t, f, &MockBackend{})
	cfg := lifecycleCfg(t)
	cfg.DNS = []string{"1.1.1.1"}
	cfg.DefaultRoute = true

	if err := o.ColdStart(context.Background(), cfg); err != nil {
		t.Fatalf("ColdStart: %v", err)
	}
	if got := f.ListCalls(); got != 1 {
		t.Fatalf("чтений списка = %d, want 1", got)
	}
	if len(f.Posts) == 0 {
		t.Fatal("старт не отправил ни одной команды")
	}
	clean(t, f)
}

// Записи нет — ColdStart создаёт её намеренно (одна запись, без фантомов) и
// настраивает по доказательству, которое вернуло создание.
func TestColdStart_AbsentRecord_CreatedOnce(t *testing.T) {
	f := ndmsquery.NewFakeNDMS()
	f.ExpectCreate("OpkgTun10")
	o, _, _ := newOS5Oracle(t, f, &MockBackend{})

	if err := o.ColdStart(context.Background(), lifecycleCfg(t)); err != nil {
		t.Fatalf("ColdStart: %v", err)
	}
	if !slices.Equal(f.Created, []string{"OpkgTun10"}) {
		t.Fatalf("создано = %v, want [OpkgTun10]", f.Created)
	}
	if got := f.ListCalls(); got != 2 {
		t.Fatalf("чтений списка = %d, want 2 (проверка + подтверждение созданного)", got)
	}
	clean(t, f)
}

// Правка живого туннеля, чья запись снята снаружи: Set* — ErrInterfaceGone
// без единой команды, Remove* — nil (снимать нечего).
func TestSetters_RecordGone(t *testing.T) {
	for name, tc := range map[string]struct {
		call     func(o *OperatorOS5Impl) error
		wantGone bool
	}{
		"SetMTU":  {func(o *OperatorOS5Impl) error { return o.SetMTU(context.Background(), "awg10", 1400) }, true},
		"SyncDNS": {func(o *OperatorOS5Impl) error { return o.SyncDNS(context.Background(), "awg10", []string{"1.1.1.1"}) }, true},
		"SyncAddress": {func(o *OperatorOS5Impl) error {
			return o.SyncAddress(context.Background(), "awg10", "10.8.0.2", 32, "")
		}, true},
		"SetDefaultRoute":    {func(o *OperatorOS5Impl) error { return o.SetDefaultRoute(context.Background(), "awg10") }, true},
		"RemoveDefaultRoute": {func(o *OperatorOS5Impl) error { return o.RemoveDefaultRoute(context.Background(), "awg10") }, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := ndmsquery.NewFakeNDMS()
			o, _, _ := newOS5Oracle(t, f, &MockBackend{running: true})
			err := tc.call(o)
			if tc.wantGone {
				if !errors.Is(err, tunnel.ErrInterfaceGone) || !strings.Contains(err.Error(), "OpkgTun10") {
					t.Fatalf("err = %v, want ErrInterfaceGone с именем", err)
				}
			} else if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if len(f.Posts) != 0 {
				t.Fatalf("команды в NDMS при отсутствующей записи: %v", f.Posts)
			}
			clean(t, f)
		})
	}
}

// Туннель от KeeneticOS 4.x на OS5: правка отказывает errOS4Tunnel, не безымянной ErrInterfaceGone.
func TestSetMTU_OS4Tunnel_RefusedAsOS4(t *testing.T) {
	o, _, _ := newOS5Oracle(t, ndmsquery.NewFakeNDMS(), &MockBackend{})
	if err := o.SetMTU(context.Background(), "awgm5", 1400); err == nil || !strings.Contains(err.Error(), "KeeneticOS 4.x") {
		t.Fatalf("err = %v, want errOS4Tunnel", err)
	}
}

// R17: на OS4 DNS адресуется по имени ЯДРА (awgm0), в списке NDMS его нет —
// подтверждать списком нельзя: команда уходит как прежде, список не читается.
func TestOperatorOS4_DNSByKernelName_NoConfirm(t *testing.T) {
	f := ndmsquery.NewFakeNDMS()
	queries := ndmsquery.NewQueries(ndmsquery.Deps{Getter: f, Logger: ndmsquery.NopLogger(), IsOS5: func() bool { return false }})
	poster := &recordingPoster{}
	cmds := ndmscommand.NewCommands(ndmscommand.Deps{
		Poster:  poster,
		Queries: queries,
		Save:    ndmscommand.NewSaveCoordinator(poster, nil, time.Hour, time.Hour, 0, queries.RunningConfig),
		IsOS5:   func() bool { return false },
	})
	o := NewOperatorOS4(queries, cmds, &MockWGClient{}, &MockBackend{}, &MockFirewall{})
	o.ipRun = (&scriptedIPRun{linkShowErr: errors.New("device \"awgm0\" does not exist")}).run

	if err := o.Reconcile(context.Background(), tunnel.Config{ID: "awgm0", MTU: 1420, DNS: []string{"1.1.1.1"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := o.Stop(context.Background(), "awgm0", ""); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	for _, want := range []string{
		`{"ip":{"name-server":{"address":"1.1.1.1","interface":"awgm0"}}}`,
		`{"ip":{"name-server":{"address":"1.1.1.1","interface":"awgm0","no":true}}}`,
	} {
		if !hasPayload(poster.payloads, want) {
			t.Fatalf("нет %s: %v", want, poster.payloads)
		}
	}
	if got := f.ListCalls(); got != 0 {
		t.Fatalf("OS4 DNS читает список NDMS (%d раз) — имя ядра в нём не подтвердится", got)
	}
}

// Переименование остановленного туннеля, затем Start: хук слоя пришёл, пока
// список Confirm в полёте. Хук новее только в своих полях — описание берётся
// из списка, и гейт владения видит запись своей (F517).
func TestStart_RenameWithLayerHookDuringConfirm_RecordIsOurs(t *testing.T) {
	ctx := context.Background()
	f := ndmsquery.NewFakeNDMS(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun", Description: "Old"})
	o, _, _ := newOS5Oracle(t, f, &MockBackend{running: false})
	if _, err := o.queries.Interfaces.List(ctx); err != nil { // карта тёплая: «Old»
		t.Fatal(err)
	}
	f.Add(opkgTun10()) // наша правка описания → «Germany», хука нет
	f.InList(func() { o.queries.Interfaces.OnLayerChanged("OpkgTun10", "conf", "running") })

	cfg := lifecycleCfg(t)
	if _, _, _, err := o.ensureOpkgTunRecord(ctx, "start", cfg, tunnel.NewNames(cfg.ID)); err != nil {
		t.Fatalf("ensureOpkgTunRecord: %v", err)
	}
	clean(t, f)
}

// S4 F595 (стенд 30.09, сирота OpkgTun11, 2.4): имя переиспользовано, и
// ifdestroyed прежнего воплощения доходит, пока список confirmOpkgTun в
// полёте. Метка противоречит ответу — одно перечитывание; запись есть —
// устройство подменено на tun, DeleteOpkgTun уходит, сироты нет.
func TestDelete_StaleDestroyedInFlight_DeletesRecord(t *testing.T) {
	ctx := context.Background()
	f := ndmsquery.NewFakeNDMS(opkgTun10())
	be := &MockBackend{running: true}
	o, _, _ := newOS5Oracle(t, f, be)
	if _, err := o.queries.Interfaces.List(ctx); err != nil { // карта тёплая, как в проде
		t.Fatal(err)
	}
	f.Remove("OpkgTun10") // прежнее воплощение
	f.Add(opkgTun10())    // новое, без дренажа
	var once sync.Once
	f.InList(func() {
		once.Do(func() {
			for _, h := range f.HooksFor("OpkgTun10") {
				if h.Type == "ifdestroyed" {
					o.queries.Interfaces.OnDestroyed(h.ID) // устаревший хук — в полёте списка
				}
			}
		})
	})

	lists := f.ListCalls()
	if err := o.Delete(ctx, &storage.AWGTunnel{ID: "awg10", Name: "Germany"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !slices.Contains(f.Posts, `{"interface":{"OpkgTun10":{"no":true}}}`) || f.Has("OpkgTun10") {
		t.Fatalf("сирота: запись есть=%v; posts=%v", f.Has("OpkgTun10"), f.Posts)
	}
	if got := f.ListCalls() - lists; got != 2 {
		t.Fatalf("списков за Delete = %d, want 2 (ответ + перечитывание по противоречию)", got)
	}
	if !slices.Equal(be.ReplaceCalls, []string{"opkgtun10"}) {
		t.Fatalf("kernel-устройство: replace=%v, want [opkgtun10]", be.ReplaceCalls)
	}
	clean(t, f)
}
