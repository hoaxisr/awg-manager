package procres

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/control"
)

// fakeTunLink — Snapshot + журнал attach/detach.
type fakeTunLink struct {
	fakeLink
	attached  []string
	detached  int
	attachErr error
}

func (f *fakeTunLink) AttachTun(_ context.Context, iface string, fd *os.File) error {
	if f.attachErr != nil {
		return f.attachErr
	}
	f.attached = append(f.attached, iface)
	return nil
}

func (f *fakeTunLink) DetachTun(context.Context) error {
	f.detached++
	return nil
}

func snapWithTun(iface string, attached bool) *control.Snapshot {
	return &control.Snapshot{
		State: awgmproto.State{Tun: &awgmproto.TunState{Iface: iface, Attached: attached}},
		At:    time.Now(),
	}
}

func pipeOpener(t *testing.T) TunOpener {
	// Настоящего TUN в тестах нет; для контракта ресурса достаточно любого fd.
	return func(string) (*os.File, error) {
		r, w, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		w.Close()
		return r, nil
	}
}

func TestTunHandoffAttachesWhenDetached(t *testing.T) {
	link := &fakeTunLink{fakeLink: fakeLink{snap: snapWithTun("opkgtun18", false)}}
	h := NewTunHandoff("tun_handoff", link, pipeOpener(t), time.Now)
	h.SetDesired("opkgtun18")

	obs, err := h.Observe(context.Background())
	if err != nil || !obs.Known || obs.Exists {
		t.Fatalf("obs=%+v err=%v: ожидали Known, не attached", obs, err)
	}
	steps := h.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "attach" {
		t.Fatalf("ожидали attach, получили %v", steps)
	}
	if err := h.Apply(context.Background(), steps[0]); err != nil {
		t.Fatal(err)
	}
	if len(link.attached) != 1 || link.attached[0] != "opkgtun18" {
		t.Fatalf("attach не дошёл: %v", link.attached)
	}
}

func TestTunHandoffSettledWhenAttachedToWantedIface(t *testing.T) {
	link := &fakeTunLink{fakeLink: fakeLink{snap: snapWithTun("opkgtun18", true)}}
	h := NewTunHandoff("tun_handoff", link, pipeOpener(t), time.Now)
	h.SetDesired("opkgtun18")
	obs, _ := h.Observe(context.Background())
	if !obs.Exists {
		t.Fatalf("attached=true обязан быть Exists: %+v", obs)
	}
	if steps := h.Plan(obs); len(steps) != 0 {
		t.Fatalf("дрейфа нет: %v", steps)
	}
}

func TestTunHandoffRenumberDetachesFirst(t *testing.T) {
	// Ренумерация OpkgTun17 -> OpkgTun18: detach -> attach (§5.3).
	link := &fakeTunLink{fakeLink: fakeLink{snap: snapWithTun("opkgtun17", true)}}
	h := NewTunHandoff("tun_handoff", link, pipeOpener(t), time.Now)
	h.SetDesired("opkgtun18")
	obs, _ := h.Observe(context.Background())
	steps := h.Plan(obs)
	if len(steps) != 2 || steps[0].Op != "detach" || steps[1].Op != "attach" {
		t.Fatalf("ожидали detach+attach, получили %v", steps)
	}
}

func TestTunHandoffStaleSnapshotIsUnknown(t *testing.T) {
	old := snapWithTun("opkgtun18", false)
	old.At = time.Now().Add(-2 * time.Minute)
	link := &fakeTunLink{fakeLink: fakeLink{snap: old}}
	h := NewTunHandoff("tun_handoff", link, pipeOpener(t), time.Now)
	h.SetDesired("opkgtun18")
	obs, _ := h.Observe(context.Background())
	if obs.Known {
		t.Fatalf("протухший снимок обязан давать Unknown: %+v", obs)
	}
}

func TestTunHandoffOpenerEBUSYChecksState(t *testing.T) {
	// Собственный TUNSETIFF вернул EBUSY: дескриптор держит кто-то ещё.
	// Спросить state; attached==true — шаг выполнен (§5.3).
	link := &fakeTunLink{fakeLink: fakeLink{
		st:   awgmproto.State{Tun: &awgmproto.TunState{Iface: "opkgtun18", Attached: true}},
		snap: snapWithTun("opkgtun18", false),
	}}
	h := NewTunHandoff("tun_handoff", link, func(string) (*os.File, error) {
		return nil, syscall.EBUSY
	}, time.Now)
	h.SetDesired("opkgtun18")
	if err := h.Apply(context.Background(), proxyrt.Step{Resource: "tun_handoff", Op: "attach"}); err != nil {
		t.Fatalf("EBUSY при attached=true — шаг выполнен, а не отказ: %v", err)
	}
}

func TestTunHandoffBusyReplyChecksState(t *testing.T) {
	link := &fakeTunLink{fakeLink: fakeLink{
		st:   awgmproto.State{Tun: &awgmproto.TunState{Iface: "opkgtun18", Attached: true}},
		snap: snapWithTun("opkgtun18", false),
	}}
	link.attachErr = &awgmproto.Error{Code: awgmproto.CodeBusy, Msg: "уже прикреплён"}
	h := NewTunHandoff("tun_handoff", link, pipeOpener(t), time.Now)
	h.SetDesired("opkgtun18")
	if err := h.Apply(context.Background(), proxyrt.Step{Resource: "tun_handoff", Op: "attach"}); err != nil {
		t.Fatalf("busy при attached=true — шаг выполнен: %v", err)
	}
}

func TestTunHandoffBadRequestIsFailure(t *testing.T) {
	// bad-request (несовпадение имени, §5.3) — Failed без слепого ретрая.
	link := &fakeTunLink{fakeLink: fakeLink{snap: snapWithTun("opkgtun18", false)}}
	link.attachErr = &awgmproto.Error{Code: awgmproto.CodeBadRequest, Msg: "ожидали tun opkgtun18"}
	link.err = errors.New("state недоступен")
	h := NewTunHandoff("tun_handoff", link, pipeOpener(t), time.Now)
	h.SetDesired("opkgtun18")
	if err := h.Apply(context.Background(), proxyrt.Step{Resource: "tun_handoff", Op: "attach"}); err == nil {
		t.Fatal("bad-request обязан доехать отказом")
	}
}

// Проверка контракта: TunHandoff обязан быть настоящим proxyrt.Resource.
var _ proxyrt.Resource = (*TunHandoff)(nil)
