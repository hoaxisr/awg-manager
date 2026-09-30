package ops

import (
	"context"
	"errors"
	"slices"
	"strings"
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

// clean — оракул не видел ни E, ни фантомов.
func clean(t *testing.T, f *ndmsquery.FakeNDMS) {
	t.Helper()
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d фантомов=%d, want 0/0; posts=%v", f.E, f.Phantoms, f.Posts)
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

// Список не прочитан — «не знаем»: NDMS не трогаем, kernel-устройство
// снимаем, ошибка наружу (оркестратор оставит запись туннеля для повтора).
func TestDelete_ListError_FailsClosed(t *testing.T) {
	f := ndmsquery.NewFakeNDMS(opkgTun10())
	f.FailList(errors.New("injected: rci"))
	be := &MockBackend{running: true}
	o, _, _ := newOS5Oracle(t, f, be)

	err := o.Delete(context.Background(), &storage.AWGTunnel{ID: "awg10", Name: "Germany"})
	if err == nil || !strings.Contains(err.Error(), "injected: rci") {
		t.Fatalf("err = %v, want отказ чтения списка", err)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды в NDMS без списка: %v", f.Posts)
	}
	if !slices.Equal(be.StopCalls, []string{"opkgtun10"}) {
		t.Fatalf("kernel-устройство не снято: %v", be.StopCalls)
	}
	if !f.Has("OpkgTun10") {
		t.Fatal("запись снята без списка")
	}
}

// Записи нет — Stop опускает только своё устройство, `conf: disabled`
// ставить некому (команда по имени создала бы запись).
func TestStop_RecordGone_LinkDownOnly(t *testing.T) {
	f := ndmsquery.NewFakeNDMS()
	o, _, rec := newOS5Oracle(t, f, &MockBackend{running: true})

	if err := o.Stop(context.Background(), "awg10", "Germany"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !hasCall(rec.Calls, "/opt/sbin/ip link set down dev opkgtun10") {
		t.Fatalf("устройство не опущено:\n%s", strings.Join(rec.Calls, "\n"))
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды в NDMS при отсутствующей записи: %v", f.Posts)
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
