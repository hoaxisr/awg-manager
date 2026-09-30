package ops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/netdev"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
)

// deviceBackend — бэкенд, чьи устройства видны и netdev (каталоги во
// временном netdev.SysClassNet), и оракулу (FakeNDMS.SetNetdev): живое
// opkgtunN там — то, из-за чего NDMS отвергает создание записи (F569).
// amneziawg=false — на имени plain tun (NDMS или чужая программа); held —
// держатель, из-за которого Stop отказывает, как backend.KernelBackend.
type deviceBackend struct {
	root      string
	f         *ndmsquery.FakeNDMS
	amneziawg bool
	held      *backend.HeldError

	StartCalls []string
	StopCalls  []string
	stopPosts  []int // len(f.Posts) на момент каждого Stop
}

func newDeviceBackend(t *testing.T, f *ndmsquery.FakeNDMS) *deviceBackend {
	t.Helper()
	root := t.TempDir()
	old := netdev.SysClassNet
	netdev.SysClassNet = root
	t.Cleanup(func() { netdev.SysClassNet = old })
	return &deviceBackend{root: root, f: f}
}

// plug — устройство iface появилось (amneziawg — наше).
func (b *deviceBackend) plug(t *testing.T, iface string, amneziawg bool) {
	t.Helper()
	if err := os.Mkdir(filepath.Join(b.root, iface), 0o755); err != nil {
		t.Fatal(err)
	}
	b.f.SetNetdev(iface, true)
	b.amneziawg = amneziawg
}

func (b *deviceBackend) exists(iface string) bool {
	_, err := os.Stat(filepath.Join(b.root, iface))
	return err == nil
}

func (b *deviceBackend) Start(_ context.Context, iface string) error {
	b.StartCalls = append(b.StartCalls, iface)
	if !b.exists(iface) {
		if err := os.Mkdir(filepath.Join(b.root, iface), 0o755); err != nil {
			return err
		}
	}
	b.f.SetNetdev(iface, true)
	b.amneziawg = true
	return nil
}

func (b *deviceBackend) Stop(_ context.Context, iface string) error {
	b.StopCalls = append(b.StopCalls, iface)
	b.stopPosts = append(b.stopPosts, len(b.f.Posts))
	if b.held != nil && !b.amneziawg {
		return b.held
	}
	_ = os.Remove(filepath.Join(b.root, iface))
	b.f.SetNetdev(iface, false)
	b.amneziawg = false
	return nil
}

func (b *deviceBackend) IsRunning(_ context.Context, iface string) (bool, int) {
	return b.exists(iface) && b.amneziawg, 0
}

func (b *deviceBackend) WaitReady(context.Context, string, time.Duration) error { return nil }

// F569: работающий туннель, запись OpkgTun10 снята снаружи, наше amneziawg
// живо. ColdStart сносит устройство ДО создания записи, создание голое —
// ни C, ни E, ни фантомов; устройство поднимается заново.
func TestColdStart_AfterExternalRemoval_RecreatesRecord(t *testing.T) {
	f := ndmsquery.NewFakeNDMS(opkgTun10())
	be := newDeviceBackend(t, f)
	be.plug(t, "opkgtun10", true)
	f.Remove("OpkgTun10")
	f.ExpectCreate("OpkgTun10")
	poster := &recordingPoster{f: f}
	o, _ := newOS5LifecycleOn(t, poster, f, be, true)

	if err := o.ColdStart(context.Background(), lifecycleCfg(t)); err != nil {
		t.Fatalf("ColdStart: %v", err)
	}
	if !slices.Equal(be.StopCalls, []string{"opkgtun10"}) || be.stopPosts[0] != 0 {
		t.Fatalf("устройство не снесено до создания: stop=%v posts-at-stop=%v", be.StopCalls, be.stopPosts)
	}
	if len(f.Posts) == 0 || f.Posts[0] != `{"interface":{"OpkgTun10":{}}}` {
		t.Fatalf("первый POST не голое создание: %v", f.Posts)
	}
	if f.E != 0 || f.C != 0 || f.Phantoms != 0 || !slices.Equal(f.Created, []string{"OpkgTun10"}) {
		t.Fatalf("E=%d C=%d Phantoms=%d Created=%v; posts=%v", f.E, f.C, f.Phantoms, f.Created, f.Posts)
	}
	if !slices.Equal(be.StartCalls, []string{"opkgtun10"}) {
		t.Fatalf("устройство не поднято заново: %v", be.StartCalls)
	}
}

// Записи нет, на имени plain tun, который держит чужая программа: отказ с
// держателем, ни одного POST — создание всё равно отвергли бы (C).
func TestColdStart_DeviceHeldByForeign_NoCreate(t *testing.T) {
	f := ndmsquery.NewFakeNDMS()
	f.ExpectCreate("OpkgTun10")
	be := newDeviceBackend(t, f)
	be.plug(t, "opkgtun10", false)
	be.held = &backend.HeldError{Iface: "opkgtun10", PID: 4242, Comm: "sing-box"}
	poster := &recordingPoster{f: f}
	o, _ := newOS5LifecycleOn(t, poster, f, be, true)

	err := o.ColdStart(context.Background(), lifecycleCfg(t))
	if err == nil || !strings.Contains(err.Error(), "4242") || !strings.Contains(err.Error(), "OpkgTun10") {
		t.Fatalf("err = %v, want отказ с pid держателя", err)
	}
	if len(f.Posts) != 0 || f.C != 0 || f.Has("OpkgTun10") || len(be.StartCalls) != 0 {
		t.Fatalf("posts=%v C=%d start=%v", f.Posts, f.C, be.StartCalls)
	}
}

// Записи нет, наше amneziawg живо — Stop сносит устройство: правило «записи
// нет ⇒ нашего устройства нет» (иначе следующий старт упрётся в C, F569).
func TestStop_RecordGone_RemovesDevice(t *testing.T) {
	f := ndmsquery.NewFakeNDMS()
	be := newDeviceBackend(t, f)
	be.plug(t, "opkgtun10", true)
	o, _ := newOS5LifecycleOn(t, &recordingPoster{f: f}, f, be, true)

	if err := o.Stop(context.Background(), "awg10", "Germany"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !slices.Equal(be.StopCalls, []string{"opkgtun10"}) || be.exists("opkgtun10") {
		t.Fatalf("устройство не снесено: %v", be.StopCalls)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды в NDMS при отсутствующей записи: %v", f.Posts)
	}
	clean(t, f)
}

// Записи нет, на имени чужой tun (не amneziawg) — не трогаем (F500/#935).
func TestStop_RecordGone_ForeignTun_Untouched(t *testing.T) {
	f := ndmsquery.NewFakeNDMS()
	be := newDeviceBackend(t, f)
	be.plug(t, "opkgtun10", false)
	o, rec := newOS5LifecycleOn(t, &recordingPoster{f: f}, f, be, true)

	if err := o.Stop(context.Background(), "awg10", "Germany"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(be.StopCalls) != 0 || !be.exists("opkgtun10") || len(rec.Calls) != 0 {
		t.Fatalf("чужое устройство тронуто: stop=%v ip=%v", be.StopCalls, rec.Calls)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды в NDMS: %v", f.Posts)
	}
}

// orderedNotifier — ожидания хуков с числом POST на момент регистрации.
type orderedNotifier struct {
	f     *ndmsquery.FakeNDMS
	calls []string
}

func (n *orderedNotifier) ExpectHook(name, level string) {
	n.calls = append(n.calls, name+"/"+level+"@"+strconv.Itoa(len(n.f.Posts)))
}

// Наш снос записи ждёт свой ifdestroyed ДО POST: иначе оркестратор принял
// бы его за внешнее снятие (Task 35).
func TestDelete_RegistersDestroyedExpectation(t *testing.T) {
	f := ndmsquery.NewFakeNDMS(opkgTun10())
	o, _, _ := newOS5Oracle(t, f, &MockBackend{running: true})
	hn := &orderedNotifier{f: f}
	o.SetHookNotifier(hn)

	if err := o.Delete(context.Background(), &storage.AWGTunnel{ID: "awg10", Name: "Germany"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !slices.Contains(hn.calls, "OpkgTun10/destroyed@0") || f.Has("OpkgTun10") {
		t.Fatalf("ожидание destroyed до POST не зарегистрировано: %v; posts=%v", hn.calls, f.Posts)
	}
	clean(t, f)
}

// Первый старт упал после создания записи — откат сносит созданную запись
// (с ожиданием её ifdestroyed до POST), а не только опускает: иначе
// OpkgTun10 остаётся на роутере после неудачного старта (F560).
func TestColdStart_RollbackDeletesJustCreatedRecord(t *testing.T) {
	f := ndmsquery.NewFakeNDMS()
	f.ExpectCreate("OpkgTun10")
	o, _, _ := newOS5Oracle(t, f, &MockBackend{startError: errors.New("injected: backend")})
	hn := &orderedNotifier{f: f}
	o.SetHookNotifier(hn)

	if err := o.ColdStart(context.Background(), lifecycleCfg(t)); err == nil {
		t.Fatal("ColdStart: want ошибку бэкенда")
	}
	if f.Has("OpkgTun10") {
		t.Fatalf("созданная запись осталась после отката: %v", f.Posts)
	}
	if !slices.Contains(hn.calls, "OpkgTun10/destroyed@"+strconv.Itoa(len(f.Posts)-1)) {
		t.Fatalf("ожидание destroyed до POST сноса не зарегистрировано: %v; posts=%v", hn.calls, f.Posts)
	}
	clean(t, f)
}
