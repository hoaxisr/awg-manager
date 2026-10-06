package ops

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
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
	// replaceErr — подмена сорвалась после del: устройства нет, tun не встал.
	replaceErr error

	StartCalls   []string
	StopCalls    []string
	ReplaceCalls []string
	stopPosts    []int // len(f.Posts) на момент каждого Stop
	startPosts   []int // len(f.Posts) на момент каждого Start
	replacePosts []int // len(f.Posts) на момент каждого ReplaceWithTun
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
	b.f.SetAmneziaWG(iface, amneziawg)
	b.amneziawg = amneziawg
}

func (b *deviceBackend) exists(iface string) bool {
	_, err := os.Stat(filepath.Join(b.root, iface))
	return err == nil
}

// Start — как backend.KernelBackend: живое amneziawg не трогает; иное
// устройство сносит и создаёт amneziawg одной подменой — оракул видит её
// как снятие и появление (под up-записью — 0ba1).
func (b *deviceBackend) Start(_ context.Context, iface string) error {
	b.StartCalls = append(b.StartCalls, iface)
	b.startPosts = append(b.startPosts, len(b.f.Posts))
	if b.exists(iface) && b.amneziawg {
		return nil
	}
	if b.exists(iface) {
		b.f.SetNetdev(iface, false)
	} else if err := os.Mkdir(filepath.Join(b.root, iface), 0o755); err != nil {
		return err
	}
	b.f.SetNetdev(iface, true)
	b.f.SetAmneziaWG(iface, true)
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
	b.f.SetAmneziaWG(iface, false)
	b.amneziawg = false
	return nil
}

func (b *deviceBackend) IsRunning(_ context.Context, iface string) (bool, int) {
	return b.exists(iface) && b.amneziawg, 0
}

func (b *deviceBackend) WaitReady(context.Context, string, time.Duration) error { return nil }

// ReplaceWithTun — plain tun на имени (как backend.KernelBackend): держатель
// не-amneziawg — HeldError, устройство не трогается. Подмена видна оракулу
// так, как идёт в ядре: живое устройство снято (del), затем tun (tuntap add).
func (b *deviceBackend) ReplaceWithTun(_ context.Context, iface string) error {
	b.ReplaceCalls = append(b.ReplaceCalls, iface)
	b.replacePosts = append(b.replacePosts, len(b.f.Posts))
	if b.held != nil && !b.amneziawg {
		return b.held
	}
	if b.exists(iface) {
		b.f.SetNetdev(iface, false)
		if b.replaceErr != nil {
			_ = os.Remove(filepath.Join(b.root, iface))
			b.amneziawg = false
			return b.replaceErr
		}
	} else if err := os.Mkdir(filepath.Join(b.root, iface), 0o755); err != nil {
		return err
	}
	b.f.SetNetdev(iface, true)
	b.f.SetAmneziaWG(iface, false)
	b.amneziawg = false
	return nil
}

func (b *deviceBackend) Recreate(ctx context.Context, iface string) error {
	return b.Start(ctx, iface)
}

func (b *deviceBackend) StopIfPresent(ctx context.Context, iface string) error {
	if !b.exists(iface) {
		return nil
	}
	return b.Stop(ctx, iface)
}

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

// confCalls — команды up/down записи OpkgTun10 по порядку как "имя/уровень":
// кредиты своих граней conf выдают именно они (ExpectConf, П8), своих
// ожиданий у оператора OS5 нет.
func confCalls(f *ndmsquery.FakeNDMS) []string {
	var out []string
	for _, p := range f.Posts {
		switch p {
		case postDown:
			out = append(out, "OpkgTun10/disabled")
		case `{"interface":{"OpkgTun10":{"up":true}}}`:
			out = append(out, "OpkgTun10/running")
		}
	}
	return out
}

// Наш снос записи — свой ifdestroyed: кредит выдан до POST (ExpectRemoval в
// deleteInterface), вердикт — в точке входа (П22); команд up/down Delete
// опущенной записи не шлёт.
func TestDelete_RecordOwnRemoval(t *testing.T) {
	f := ndmsquery.NewFakeNDMS(opkgTun10())
	o, _, _ := newOS5Oracle(t, f, &MockBackend{running: true})

	if err := o.Delete(context.Background(), &storage.AWGTunnel{ID: "awg10", Name: "Germany"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !o.queries.Interfaces.ClaimOwnDestroyed("OpkgTun10") || f.Has("OpkgTun10") || len(confCalls(f)) != 0 {
		t.Fatalf("кредит ifdestroyed нет, запись есть=%v или up/down %v; posts=%v",
			f.Has("OpkgTun10"), confCalls(f), f.Posts)
	}
	clean(t, f)
}

// Первый старт упал после создания записи — откат сносит созданную запись
// (свой ifdestroyed — по кредиту, ClaimOwnDestroyed), а не только опускает: иначе
// OpkgTun10 остаётся на роутере после неудачного старта (F560).
func TestColdStart_RollbackDeletesJustCreatedRecord(t *testing.T) {
	f := ndmsquery.NewFakeNDMS()
	f.ExpectCreate("OpkgTun10")
	o, _, _ := newOS5Oracle(t, f, &MockBackend{startError: errors.New("injected: backend")})

	if err := o.ColdStart(context.Background(), lifecycleCfg(t)); err == nil {
		t.Fatal("ColdStart: want ошибку бэкенда")
	}
	if f.Has("OpkgTun10") {
		t.Fatalf("созданная запись осталась после отката: %v", f.Posts)
	}
	if !o.queries.Interfaces.ClaimOwnDestroyed("OpkgTun10") {
		t.Fatalf("снос отката без кредита своего ifdestroyed; posts=%v", f.Posts)
	}
	clean(t, f)
}

// rejectAddressPoster — оракул, отвергающий установку ip address.
type rejectAddressPoster struct{ f *ndmsquery.FakeNDMS }

func (p rejectAddressPoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	if js, _ := json.Marshal(payload); strings.Contains(string(js), `"ip":{"address":`) {
		return nil, errors.New("injected: address")
	}
	return p.f.Post(ctx, payload)
}

const (
	postDown = `{"interface":{"OpkgTun10":{"up":false}}}`
	postNo   = `{"interface":{"OpkgTun10":{"no":true}}}`
)

// upTun10 — наша запись OpkgTun10 в State "up" (туннель работал).
func upTun10() ndms.Interface {
	r := opkgTun10()
	r.State = "up"
	return r
}

// tunThenRecord проверяет снятие C3a (стенд Task 59: 20/20 без C всех
// классов и без E): `interface down` (только если запись была up — wasUp),
// затем подмена устройства на plain tun, затем `no interface`; NDMS снимает
// tun сам. Команда down (кредит disabled) — одна и только при up-записи.
func tunThenRecord(t *testing.T, f *ndmsquery.FakeNDMS, be *deviceBackend, wasUp bool) {
	t.Helper()
	clean(t, f)
	del := slices.Index(f.Posts, postNo)
	if del < 0 {
		t.Fatalf("сноса записи нет: %v", f.Posts)
	}
	if !slices.Equal(be.ReplaceCalls, []string{"opkgtun10"}) || be.replacePosts[0] > del {
		t.Fatalf("tun не подставлен до сноса записи: replace=%v posts-at-replace=%v, снос — POST #%d; posts=%v", be.ReplaceCalls, be.replacePosts, del, f.Posts)
	}
	down := slices.Index(f.Posts, postDown)
	var downExp []string
	for _, c := range confCalls(f) {
		if strings.HasSuffix(c, "/disabled") {
			downExp = append(downExp, c)
		}
	}
	if wasUp {
		if down < 0 || down >= be.replacePosts[0] {
			t.Fatalf("down не до подмены: down — POST #%d, подмена при %d; posts=%v", down, be.replacePosts[0], f.Posts)
		}
		if len(downExp) != 1 {
			t.Fatalf("ожидания disabled = %v, want одно (InterfaceDown)", downExp)
		}
	} else if down >= 0 || len(downExp) != 0 {
		t.Fatalf("down по опущенной записи: posts=%v", f.Posts)
	}
	for _, at := range be.stopPosts {
		if at <= del {
			t.Fatalf("Stop до сноса записи: posts-at-stop=%v, снос — POST #%d", be.stopPosts, del)
		}
	}
	if f.Has("OpkgTun10") || be.exists("opkgtun10") {
		t.Fatalf("запись есть=%v устройство есть=%v", f.Has("OpkgTun10"), be.exists("opkgtun10"))
	}
}

// D-N2/F598: Delete работающего kernel-туннеля — C3a (down → tun → запись).
// Прежний порядок «устройство, затем запись» под up-записью давал 0ba1
// (K-0ba1 3/3), запись при живом amneziawg — 003b (K-003b 3/3).
func TestDelete_Running_TunThenRecord(t *testing.T) {
	f := ndmsquery.NewFakeNDMS(upTun10())
	be := newDeviceBackend(t, f)
	be.plug(t, "opkgtun10", true)
	o, _ := newOS5LifecycleOn(t, &recordingPoster{f: f}, f, be, true)

	if err := o.Delete(context.Background(), &storage.AWGTunnel{ID: "awg10", Name: "Germany"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	tunThenRecord(t, f, be, true)
}

// Delete остановленного (запись down, amneziawg жив) — та же
// последовательность без down: подмена под down-записью C не даёт.
func TestDelete_Stopped_SameSequence(t *testing.T) {
	r := opkgTun10()
	r.State = "down"
	f := ndmsquery.NewFakeNDMS(r)
	be := newDeviceBackend(t, f)
	be.plug(t, "opkgtun10", true)
	o, _ := newOS5LifecycleOn(t, &recordingPoster{f: f}, f, be, true)

	if err := o.Delete(context.Background(), &storage.AWGTunnel{ID: "awg10", Name: "Germany"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	tunThenRecord(t, f, be, false)
}

// HeldError (на имени tun чужой программы с держателем): подменить нельзя —
// `no interface` не шлётся (запись при живом чужом tun с держателем — C
// busy, X3), ошибка наружу: оркестратор оставит запись туннеля (fail-closed).
func TestDelete_Held_FailClosed(t *testing.T) {
	f := ndmsquery.NewFakeNDMS(upTun10())
	be := newDeviceBackend(t, f)
	be.plug(t, "opkgtun10", false)
	be.held = &backend.HeldError{Iface: "opkgtun10", PID: 4242, Comm: "sing-box"}
	o, _ := newOS5LifecycleOn(t, &recordingPoster{f: f}, f, be, true)

	err := o.Delete(context.Background(), &storage.AWGTunnel{ID: "awg10", Name: "Germany"})
	if err == nil || !strings.Contains(err.Error(), "4242") {
		t.Fatalf("err = %v, want отказ с pid держателя", err)
	}
	if slices.Contains(f.Posts, postNo) || !f.Has("OpkgTun10") || !be.exists("opkgtun10") || len(be.StopCalls) != 0 {
		t.Fatalf("posts=%v запись есть=%v устройство есть=%v stop=%v", f.Posts, f.Has("OpkgTun10"), be.exists("opkgtun10"), be.StopCalls)
	}
	clean(t, f)
}

// Снос записи отвергнут после подмены — ошибка наружу, запись туннеля
// остаётся; OpkgTun10 остаётся с plain tun (как после ребута) — ни 0ba1,
// ни 0767 до повторного Delete. Прежний остаток П2 (запись без устройства)
// ушёл вместе с device-first.
func TestDelete_RecordRefused_TunKept(t *testing.T) {
	f := ndmsquery.NewFakeNDMS(upTun10())
	be := newDeviceBackend(t, f)
	be.plug(t, "opkgtun10", true)
	o, _ := newOS5LifecycleOn(t, failDeletePoster{f}, f, be, true)

	err := o.Delete(context.Background(), &storage.AWGTunnel{ID: "awg10", Name: "Germany"})
	if err == nil || !strings.Contains(err.Error(), "injected: delete") {
		t.Fatalf("err = %v, want отказ сноса записи", err)
	}
	if !f.Has("OpkgTun10") || !be.exists("opkgtun10") || be.amneziawg || len(be.StopCalls) != 0 {
		t.Fatalf("запись есть=%v устройство есть=%v amneziawg=%v stop=%v", f.Has("OpkgTun10"), be.exists("opkgtun10"), be.amneziawg, be.StopCalls)
	}
	clean(t, f)
}

// Подмена сорвалась после del (tun не встал, например ip убит по
// SwapHoldMax): устройства нет — запись снимается, снос записи без
// устройства — 0 C (стенд Task 59); оставить её значило бы держать запись
// без устройства (0767 на каждом нашем списке) до повторного Delete.
func TestDelete_ReplaceFailedDeviceGone_RecordRemoved(t *testing.T) {
	r := opkgTun10()
	r.State = "down"
	f := ndmsquery.NewFakeNDMS(r)
	be := newDeviceBackend(t, f)
	be.plug(t, "opkgtun10", true)
	be.replaceErr = errors.New("injected: tuntap")
	o, _ := newOS5LifecycleOn(t, &recordingPoster{f: f}, f, be, true)

	if err := o.Delete(context.Background(), &storage.AWGTunnel{ID: "awg10", Name: "Germany"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if f.Has("OpkgTun10") || !slices.Contains(f.Posts, postNo) {
		t.Fatalf("запись есть=%v posts=%v", f.Has("OpkgTun10"), f.Posts)
	}
	clean(t, f)
}

// M1 (финальное ревью F595): устройство снято снаружи (`ip link del`/rmmod),
// запись осталась — `state: error` при `conf: running`, либо ещё `up`, пока
// NDMS не заметил (0ba1 через ~3 с). Delete — сразу `no interface`: ни
// down, ни подмены — подмена здесь голый `tuntap add`, NEWLINK под
// running-записью (стендом не снят), а снос записи без устройства — 0 C
// ×12 (стенд Task 59). Оракул этот C не моделирует, поэтому ассерт — по
// шагам: tun не создан, down не отправлен, `no interface` ушёл.
// Мутация: снять ветку «устройства нет» → ReplaceCalls непуст, красный.
func TestDelete_DeviceAbsent_NoTunCreated(t *testing.T) {
	for _, state := range []string{"error", "up"} {
		t.Run(state, func(t *testing.T) {
			r := opkgTun10()
			r.State = state
			f := ndmsquery.NewFakeNDMS(r)
			be := newDeviceBackend(t, f) // устройства opkgtun10 нет
			o, _ := newOS5LifecycleOn(t, &recordingPoster{f: f}, f, be, true)

			if err := o.Delete(context.Background(), &storage.AWGTunnel{ID: "awg10", Name: "Germany"}); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if len(be.ReplaceCalls) != 0 || be.exists("opkgtun10") {
				t.Fatalf("tun создан под записью без устройства: replace=%v устройство=%v", be.ReplaceCalls, be.exists("opkgtun10"))
			}
			if !slices.Equal(f.Posts, []string{postNo}) {
				t.Fatalf("posts=%v, want ровно [no interface] без down", f.Posts)
			}
			if f.Has("OpkgTun10") || !o.queries.Interfaces.ClaimOwnDestroyed("OpkgTun10") {
				t.Fatalf("запись есть=%v или нет кредита своего ifdestroyed", f.Has("OpkgTun10"))
			}
			clean(t, f)
		})
	}
}

// L2 (остаток F1): откат по существующей записи, `del` прошёл, `tuntap add`
// отказал — запись без устройства; оставленная до следующего Start, она
// давала бы 0767 на каждом нашем списке. Запись снимается (без устройства —
// 0 C, стенд Task 59), свой ifdestroyed — по карте; следующий Start создаёт
// её заново. Мутация: оставить запись при !deleteRecord → красный.
func TestRollback_ExistingRecord_TuntapFails_RecordRemoved(t *testing.T) {
	f := ndmsquery.NewFakeNDMS(upTun10())
	be := newDeviceBackend(t, f)
	be.plug(t, "opkgtun10", false) // tun NDMS после ребута
	be.replaceErr = errors.New("injected: tuntap")
	o, _ := newOS5LifecycleOn(t, &recordingPoster{f: f}, f, be, true)
	o.firewall = &MockFirewall{addError: errors.New("injected: firewall")}

	if err := o.ColdStart(context.Background(), lifecycleCfg(t)); err == nil || !strings.Contains(err.Error(), "injected: firewall") {
		t.Fatalf("ColdStart: err = %v, want отказ файрвола", err)
	}
	if be.exists("opkgtun10") || f.Has("OpkgTun10") || !slices.Contains(f.Posts, postNo) {
		t.Fatalf("устройство=%v запись=%v posts=%v, want ни устройства, ни записи", be.exists("opkgtun10"), f.Has("OpkgTun10"), f.Posts)
	}
	if !o.queries.Interfaces.ClaimOwnDestroyed("OpkgTun10") {
		t.Fatal("снос отката без кредита своего ifdestroyed")
	}
	clean(t, f)

	be.replaceErr = nil
	o.firewall = &MockFirewall{}
	f.ExpectCreate("OpkgTun10")
	if err := o.ColdStart(context.Background(), lifecycleCfg(t)); err != nil {
		t.Fatalf("следующий ColdStart: %v", err)
	}
	if !f.Has("OpkgTun10") || !be.exists("opkgtun10") || !be.amneziawg {
		t.Fatalf("запись=%v устройство=%v amneziawg=%v после повторного старта", f.Has("OpkgTun10"), be.exists("opkgtun10"), be.amneziawg)
	}
	clean(t, f)
}

// F560/F598: откат первого старта (запись создана этой попыткой и ни разу не
// была up) — tun, затем запись; down не шлётся.
func TestColdStart_Rollback_TunThenRecord(t *testing.T) {
	f := ndmsquery.NewFakeNDMS()
	f.ExpectCreate("OpkgTun10")
	be := newDeviceBackend(t, f)
	o, _ := newOS5LifecycleOn(t, rejectAddressPoster{f}, f, be, true)

	if err := o.ColdStart(context.Background(), lifecycleCfg(t)); err == nil || !strings.Contains(err.Error(), "injected: address") {
		t.Fatalf("ColdStart: err = %v, want отказ адреса", err)
	}
	tunThenRecord(t, f, be, false)
}

// Откат первого старта после InterfaceUp (упал файрвол): запись up — down,
// затем tun, затем запись (C3a). Без down подмена под up-записью — 0ba1.
func TestColdStart_RollbackAfterUp_DownTunThenRecord(t *testing.T) {
	f := ndmsquery.NewFakeNDMS()
	f.ExpectCreate("OpkgTun10")
	be := newDeviceBackend(t, f)
	o, _ := newOS5LifecycleOn(t, &recordingPoster{f: f}, f, be, true)
	o.firewall = &MockFirewall{addError: errors.New("injected: firewall")}

	if err := o.ColdStart(context.Background(), lifecycleCfg(t)); err == nil || !strings.Contains(err.Error(), "injected: firewall") {
		t.Fatalf("ColdStart: err = %v, want отказ файрвола", err)
	}
	// running от InterfaceUp + disabled от down отката.
	if !slices.Contains(confCalls(f), "OpkgTun10/running") {
		t.Fatalf("InterfaceUp не дошёл: %v", f.Posts)
	}
	tunThenRecord(t, f, be, true)
}

// F1 ревью Task 62: откат старта по существующей записи — запись остаётся,
// под ней plain tun (состояние как после ребута), следующий старт подменит.
// Прежний Stop оставлял запись без устройства: 0ba1 под up и 0767 на каждом
// нашем списке до следующего старта.
func TestRollback_ExistingRecord_ReplacedWithTun(t *testing.T) {
	for _, state := range []string{"up", "down"} {
		t.Run(state, func(t *testing.T) {
			r := opkgTun10()
			r.State = state
			f := ndmsquery.NewFakeNDMS(r)
			be := newDeviceBackend(t, f)
			be.plug(t, "opkgtun10", false) // tun NDMS после ребута
			o, _ := newOS5LifecycleOn(t, rejectAddressPoster{f}, f, be, true)

			if err := o.ColdStart(context.Background(), lifecycleCfg(t)); err == nil || !strings.Contains(err.Error(), "injected: address") {
				t.Fatalf("ColdStart: err = %v, want отказ адреса", err)
			}
			clean(t, f)
			if slices.Contains(f.Posts, postNo) || !f.Has("OpkgTun10") {
				t.Fatalf("запись снята: has=%v posts=%v", f.Has("OpkgTun10"), f.Posts)
			}
			if !slices.Equal(be.ReplaceCalls, []string{"opkgtun10"}) || !be.exists("opkgtun10") || be.amneziawg || len(be.StopCalls) != 0 {
				t.Fatalf("replace=%v устройство=%v amneziawg=%v stop=%v", be.ReplaceCalls, be.exists("opkgtun10"), be.amneziawg, be.StopCalls)
			}
			down := slices.Index(f.Posts, postDown)
			if (state == "up") != (down >= 0 && down < be.replacePosts[0]) {
				t.Fatalf("state=%s: down — POST #%d, подмена при %d; posts=%v", state, down, be.replacePosts[0], f.Posts)
			}
			disabled := 0
			for _, c := range confCalls(f) {
				if strings.HasSuffix(c, "/disabled") {
					disabled++
				}
			}
			if want := map[string]int{"up": 1, "down": 0}[state]; disabled != want {
				t.Fatalf("state=%s: down %d, want %d: %v", state, disabled, want, f.Posts)
			}
		})
	}
}

// F61-1: старт по существующей up-записи, под которой tun NDMS (ребут):
// down → подмена на amneziawg → InterfaceUp. Без down подмена под running —
// 0ba1 (C3b). Под down-записью и при живом amneziawg — без down.
func TestStart_ExistingUpRecordTun_DownSwapUp(t *testing.T) {
	const postUp = `{"interface":{"OpkgTun10":{"up":true}}}`
	for _, tc := range []struct {
		name      string
		state     string
		amneziawg bool
		wantDown  bool
	}{
		{"up+tun", "up", false, true},
		{"down+tun", "down", false, false},
		{"up+amneziawg", "up", true, false},
	} {
		for _, site := range []string{"ColdStart", "Reconcile"} {
			t.Run(site+"/"+tc.name, func(t *testing.T) {
				r := opkgTun10()
				r.State = tc.state
				f := ndmsquery.NewFakeNDMS(r)
				be := newDeviceBackend(t, f)
				be.plug(t, "opkgtun10", tc.amneziawg)
				o, _ := newOS5LifecycleOn(t, &recordingPoster{f: f}, f, be, true)

				run := o.ColdStart
				if site == "Reconcile" {
					run = o.Reconcile
				}
				if err := run(context.Background(), lifecycleCfg(t)); err != nil {
					t.Fatalf("%s: %v", site, err)
				}
				clean(t, f)
				down := slices.Index(f.Posts, postDown)
				if !tc.wantDown {
					if down >= 0 {
						t.Fatalf("down без нужды: %v", f.Posts)
					}
					return
				}
				up := slices.Index(f.Posts, postUp)
				if len(be.startPosts) != 1 || down < 0 || down >= be.startPosts[0] || up < be.startPosts[0] {
					t.Fatalf("порядок down → подмена → up: down=#%d start@%v up=#%d; posts=%v", down, be.startPosts, up, f.Posts)
				}
				want := []string{"OpkgTun10/disabled", "OpkgTun10/running"}
				if got := confCalls(f); !slices.Equal(got, want) {
					t.Fatalf("up/down %v, want %v", got, want)
				}
			})
		}
	}
}

// R61-1: Reconcile упал между нашим down и InterfaceUp (здесь — ip link mtu
// после подмены): запись осталась disabled у Running-туннеля. Следующий
// Reconcile (WAN-up, reconnect) обязан её поднять — запись не up.
// Мутация «InterfaceUp только при justCreated || downed» → красный.
func TestReconcile_FailAfterDown_NextReconcileUp(t *testing.T) {
	const postUp = `{"interface":{"OpkgTun10":{"up":true}}}`
	f := ndmsquery.NewFakeNDMS(upTun10())
	be := newDeviceBackend(t, f)
	be.plug(t, "opkgtun10", false)
	o, rec := newOS5LifecycleOn(t, &recordingPoster{f: f}, f, be, true)
	rec.failOn = "txqueuelen"

	if err := o.Reconcile(context.Background(), lifecycleCfg(t)); err == nil {
		t.Fatal("Reconcile: want отказ ip link")
	}
	if !slices.Contains(f.Posts, postDown) || slices.Contains(f.Posts, postUp) {
		t.Fatalf("первый проход: want down без up; posts=%v", f.Posts)
	}

	rec.failOn = ""
	before := len(f.Posts)
	if err := o.Reconcile(context.Background(), lifecycleCfg(t)); err != nil {
		t.Fatalf("второй Reconcile: %v", err)
	}
	if !slices.Contains(f.Posts[before:], postUp) {
		t.Fatalf("опущенная запись не поднята повторным Reconcile: %v", f.Posts[before:])
	}
	clean(t, f)
}

// downDeniedPoster — оракул, отвергающий `interface down`.
type downDeniedPoster struct{ f *ndmsquery.FakeNDMS }

func (p downDeniedPoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	if js, _ := json.Marshal(payload); string(js) == postDown {
		return nil, errors.New("injected: down")
	}
	return p.f.Post(ctx, payload)
}

// R61-2: down перед подменой не прошёл ни с одной попытки — подмены нет
// (под running это C3b, 0ba1), ошибка наружу, запись и tun не тронуты.
// Мутация «down всегда считается сделанным» → Start вызван → красный.
func TestStart_DownFails_NoSwap(t *testing.T) {
	for _, site := range []string{"ColdStart", "Reconcile"} {
		t.Run(site, func(t *testing.T) {
			f := ndmsquery.NewFakeNDMS(upTun10())
			be := newDeviceBackend(t, f)
			be.plug(t, "opkgtun10", false)
			o, _ := newOS5LifecycleOn(t, downDeniedPoster{f}, f, be, true)

			run := o.ColdStart
			if site == "Reconcile" {
				run = o.Reconcile
			}
			err := run(context.Background(), lifecycleCfg(t))
			if err == nil || !strings.Contains(err.Error(), "injected: down") {
				t.Fatalf("err = %v, want отказ down", err)
			}
			if len(be.StartCalls) != 0 || len(be.ReplaceCalls) != 0 || len(be.StopCalls) != 0 || !be.exists("opkgtun10") || be.amneziawg {
				t.Fatalf("устройство тронуто: start=%v replace=%v stop=%v", be.StartCalls, be.ReplaceCalls, be.StopCalls)
			}
			if !f.Has("OpkgTun10") || len(f.Posts) != 0 {
				t.Fatalf("запись есть=%v posts=%v, want запись цела и ни одного POST", f.Has("OpkgTun10"), f.Posts)
			}
			clean(t, f)
		})
	}
}

// downFailedKept — down отвергнут на всех попытках: ни подмены, ни `no
// interface`, запись и наше amneziawg как были (подмена под running — C3b).
func downFailedKept(t *testing.T, f *ndmsquery.FakeNDMS, be *deviceBackend) {
	t.Helper()
	if len(be.ReplaceCalls) != 0 || len(be.StopCalls) != 0 || slices.Contains(f.Posts, postNo) {
		t.Fatalf("после отказа down: replace=%v stop=%v posts=%v", be.ReplaceCalls, be.StopCalls, f.Posts)
	}
	if !f.Has("OpkgTun10") || !be.exists("opkgtun10") || !be.amneziawg {
		t.Fatalf("запись есть=%v устройство есть=%v amneziawg=%v", f.Has("OpkgTun10"), be.exists("opkgtun10"), be.amneziawg)
	}
	clean(t, f)
}

// Fix round 3: Delete работающего — down отказал ×3 → ошибка наружу
// (запись туннеля остаётся, повтор Delete пройдёт), устройство и запись
// NDMS не тронуты. Мутация «подменять несмотря на отказ down» → красный.
func TestDelete_DownFails_FailClosed(t *testing.T) {
	f := ndmsquery.NewFakeNDMS(upTun10())
	be := newDeviceBackend(t, f)
	be.plug(t, "opkgtun10", true)
	o, _ := newOS5LifecycleOn(t, downDeniedPoster{f}, f, be, true)

	err := o.Delete(context.Background(), &storage.AWGTunnel{ID: "awg10", Name: "Germany"})
	if err == nil || !strings.Contains(err.Error(), "injected: down") {
		t.Fatalf("err = %v, want отказ down", err)
	}
	downFailedKept(t, f, be)
}

// Fix round 3: откат старта, дошедшего до InterfaceUp (упал файрвол), —
// down отказал ×3 → ни подмены, ни сноса; ошибка старта остаётся главной.
// Обе ветки: запись создана этой попыткой и существовала (amneziawg жив).
// Мутация «подменять несмотря на отказ down» → красный.
func TestRollback_DownFails_FailClosed(t *testing.T) {
	for _, justCreated := range []bool{true, false} {
		t.Run(map[bool]string{true: "justCreated", false: "existing"}[justCreated], func(t *testing.T) {
			var f *ndmsquery.FakeNDMS
			if justCreated {
				f = ndmsquery.NewFakeNDMS()
				f.ExpectCreate("OpkgTun10")
			} else {
				f = ndmsquery.NewFakeNDMS(upTun10())
			}
			be := newDeviceBackend(t, f)
			if !justCreated {
				be.plug(t, "opkgtun10", true)
			}
			o, _ := newOS5LifecycleOn(t, downDeniedPoster{f}, f, be, true)
			o.firewall = &MockFirewall{addError: errors.New("injected: firewall")}

			err := o.ColdStart(context.Background(), lifecycleCfg(t))
			if err == nil || !strings.Contains(err.Error(), "injected: firewall") {
				t.Fatalf("err = %v, want отказ файрвола (ошибка старта — главная)", err)
			}
			downFailedKept(t, f, be)
		})
	}
}
