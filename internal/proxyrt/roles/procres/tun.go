package procres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
)

// TunLink — срез control.Link для передачи дескриптора.
type TunLink interface {
	ProcessLink
	AttachTun(ctx context.Context, iface string, f *os.File) error
	DetachTun(ctx context.Context) error
}

// TunOpener открывает TUN-дескриптор существующего интерфейса.
// Прод — OpenTunFD (tun_linux.go): IFF_TUN|IFF_NO_PI, без IFF_VNET_HDR,
// неблокирующий — контракт §5.3 протокола.
type TunOpener func(name string) (*os.File, error)

// TunHandoff — ресурс передачи TUN-дескриптора raw-клиенту.
//
// Наблюдение — из снимка Link: process-ресурс стоит в декларации раньше и
// обновил его в этом же прогоне. Отдельный запрос state здесь был бы вторым
// походом в сокет за то же знание.
type TunHandoff struct {
	id    proxyrt.ResourceID
	link  TunLink
	open  TunOpener
	now   func() time.Time
	iface string
}

func NewTunHandoff(id proxyrt.ResourceID, link TunLink, open TunOpener, now func() time.Time) *TunHandoff {
	if now == nil {
		now = time.Now
	}
	return &TunHandoff{id: id, link: link, open: open, now: now}
}

func (t *TunHandoff) SetDesired(iface string) { t.iface = iface }

func (t *TunHandoff) ID() proxyrt.ResourceID { return t.id }

func (t *TunHandoff) Observe(context.Context) (proxyrt.Observation, error) {
	snap, ok := t.link.Snapshot()
	if !ok || t.now().Sub(snap.At) > snapMaxAge {
		return proxyrt.Observation{Known: false, Detail: "нет свежего снимка состояния процесса"}, nil
	}
	if snap.State.Tun == nil {
		// Отсутствие необязательного поля = «неизвестно», не «нет» (§5.2).
		return proxyrt.Observation{Known: false, Detail: "процесс не сообщил состояние TUN"}, nil
	}
	return proxyrt.Observation{
		Known:  true,
		Exists: snap.State.Tun.Attached,
		Attrs: map[string]string{
			"iface":    snap.State.Tun.Iface,
			"attached": strconv.FormatBool(snap.State.Tun.Attached),
		},
	}, nil
}

func (t *TunHandoff) Plan(obs proxyrt.Observation) []proxyrt.Step {
	if obs.Exists && obs.Attrs["iface"] == t.iface {
		return nil
	}
	attach := proxyrt.Step{Resource: t.id, Op: "attach",
		Args: map[string]string{"iface": t.iface}, Reason: "дескриптор не прикреплён"}
	if obs.Exists {
		// Смена интерфейса: detach -> дождаться -> attach (§5.3). Оба шага в
		// одном плане: detach синхронен (ok в ответе), Apply предусловий не
		// перепроверяет (§4.5 спеки).
		return []proxyrt.Step{
			{Resource: t.id, Op: "detach", Reason: "прикреплён к " + obs.Attrs["iface"] + ", нужен " + t.iface},
			attach,
		}
	}
	return []proxyrt.Step{attach}
}

func (t *TunHandoff) Apply(ctx context.Context, s proxyrt.Step) error {
	switch s.Op {
	case "detach":
		return t.link.DetachTun(ctx)
	case "attach":
		return t.attach(ctx)
	default:
		return fmt.Errorf("неизвестный шаг %q", s.Op)
	}
}

func (t *TunHandoff) attach(ctx context.Context) error {
	f, err := t.open(t.iface)
	if err != nil {
		if errors.Is(err, syscall.EBUSY) {
			// Дескриптор держит кто-то ещё (второй TUNSETIFF на single-queue
			// tun даёт EBUSY). Спросить state: attached — шаг уже выполнен.
			return t.confirmAttached(ctx, fmt.Errorf("TUNSETIFF %s: EBUSY, а процесс не подтверждает attach", t.iface))
		}
		return fmt.Errorf("открытие TUN %s: %w", t.iface, err)
	}
	defer f.Close() // копия дескриптора у процесса живёт своя
	err = t.link.AttachTun(ctx, t.iface, f)
	if err == nil {
		return nil
	}
	var pe *awgmproto.Error
	if errors.As(err, &pe) && pe.Code == awgmproto.CodeBusy {
		// «Уже прикреплён» — ретраить вслепую нельзя, сверяемся по state.
		return t.confirmAttached(ctx, err)
	}
	return err
}

func (t *TunHandoff) confirmAttached(ctx context.Context, cause error) error {
	octx, cancel := context.WithTimeout(ctx, observeTimeout)
	defer cancel()
	st, serr := t.link.State(octx)
	if serr == nil && st.Tun != nil && st.Tun.Attached && st.Tun.Iface == t.iface {
		return nil
	}
	return cause
}

func (t *TunHandoff) RecheckAfter() time.Duration { return 0 }
