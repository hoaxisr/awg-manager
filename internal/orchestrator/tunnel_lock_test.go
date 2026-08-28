package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// Issue #426: the per-tunnel execution lock must be bounded. A wedged
// operation used to make every subsequent start/stop/replace request block
// on a bare mutex forever — the UI showed «Операция уже выполняется» until
// the daemon was restarted.
func TestLockTunnel_BusyFailsFastOnCtxCancel(t *testing.T) {
	o := &Orchestrator{}

	if err := o.lockTunnel(context.Background(), "tun_a", "test"); err != nil {
		t.Fatalf("first lock: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := o.lockTunnel(ctx, "tun_a", "test")
	if !errors.Is(err, tunnel.ErrOperationInProgress) {
		t.Fatalf("busy lock: err = %v, want ErrOperationInProgress", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("busy lock waited %s — must give up with the caller's ctx", elapsed)
	}

	// Release → the tunnel is lockable again (no leaked state).
	o.unlockTunnel("tun_a")
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := o.lockTunnel(ctx2, "tun_a", "test"); err != nil {
		t.Fatalf("relock after unlock: %v", err)
	}
	o.unlockTunnel("tun_a")
}

// Different tunnels never contend with each other.
func TestLockTunnel_IndependentPerTunnel(t *testing.T) {
	o := &Orchestrator{}
	if err := o.lockTunnel(context.Background(), "tun_a", "test"); err != nil {
		t.Fatalf("lock a: %v", err)
	}
	defer o.unlockTunnel("tun_a")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := o.lockTunnel(ctx, "tun_b", "test"); err != nil {
		t.Fatalf("lock b must not contend with a: %v", err)
	}
	o.unlockTunnel("tun_b")
}

// Double-unlock must be a harmless no-op.
func TestUnlockTunnel_Idempotent(t *testing.T) {
	o := &Orchestrator{}
	if err := o.lockTunnel(context.Background(), "tun_a", "test"); err != nil {
		t.Fatalf("lock: %v", err)
	}
	o.unlockTunnel("tun_a")
	o.unlockTunnel("tun_a") // must not panic or corrupt the semaphore
	if err := o.lockTunnel(context.Background(), "tun_a", "test"); err != nil {
		t.Fatalf("relock: %v", err)
	}
	o.unlockTunnel("tun_a")
}

// Issue #795: при отказе «операция уже выполняется» лог должен называть
// держателя. Запись владельца живёт ровно столько, сколько держится замок.
func TestLockTunnel_TracksHolder(t *testing.T) {
	o := &Orchestrator{}
	if err := o.lockTunnel(context.Background(), "awg11", "stop"); err != nil {
		t.Fatalf("lock: %v", err)
	}
	hAny, ok := o.tunnelLockOwner.Load("awg11")
	if !ok {
		t.Fatal("держатель не записан — отказ не сможет назвать виновника")
	}
	if h := hAny.(*lockHolder); h.owner != "stop" || h.since.IsZero() {
		t.Fatalf("держатель = %+v, ожидался owner=stop с непустым since", h)
	}

	o.unlockTunnel("awg11")
	if _, ok := o.tunnelLockOwner.Load("awg11"); ok {
		t.Fatal("запись держателя пережила освобождение — лог соврёт следующему отказу")
	}
}

// Имена событий уходят в лог держателя; безымянное событие даёт «event-7»,
// по которому нечего анализировать.
func TestEventTypeString_AllNamed(t *testing.T) {
	for et := EventBoot; et < eventTypeCount; et++ {
		if s := et.String(); strings.HasPrefix(s, "event-") {
			t.Fatalf("EventType(%d) без имени: %q", int(et), s)
		}
	}
}

// Отказ помечает держателя: по этому счётчику строка освобождения решает,
// была ли помеха. Без него Warn писался бы на каждое штатное долгое
// держание (boot/reconnect на медленном NDMS) и тонул бы в шуме.
func TestLockTunnel_RefusalMarksHolder(t *testing.T) {
	o := &Orchestrator{}
	if err := o.lockTunnel(context.Background(), "awg11", "stop"); err != nil {
		t.Fatalf("lock: %v", err)
	}
	defer o.unlockTunnel("awg11")

	hAny, _ := o.tunnelLockOwner.Load("awg11")
	h := hAny.(*lockHolder)
	if n := h.refused.Load(); n != 0 {
		t.Fatalf("до отказов счётчик = %d, ожидался 0", n)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := o.lockTunnel(ctx, "awg11", "delete"); !errors.Is(err, tunnel.ErrOperationInProgress) {
		t.Fatalf("ожидался отказ, получено: %v", err)
	}
	if n := h.refused.Load(); n != 1 {
		t.Fatalf("счётчик отказов = %d, ожидался 1", n)
	}
}
