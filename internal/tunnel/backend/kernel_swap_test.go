package backend

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/netdev"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// swapRig — швы D-N1: тип устройства (kernelRun, `ip -d link show`),
// снос/создание (netdev runIP) и держатель — каждый в свой журнал.
type swapRig struct {
	mu      sync.Mutex
	link    []string // вызовы netdev.Swapper
	holders int      // обходы /proc
}

func (r *swapRig) links() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.link)
}

func (r *swapRig) scans() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.holders
}

func newSwapRig(t *testing.T, exists bool, held *HeldError) *swapRig {
	t.Helper()
	r := &swapRig{}
	oldRun, oldHolder := kernelRun, tunHolder
	kernelRun = func(context.Context, string, ...string) (*exec.Result, error) {
		return &exec.Result{Stdout: "tun"}, nil // не amneziawg
	}
	tunHolder = func(string) *HeldError {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.holders++
		return held
	}
	stubIfaceExists(t, exists)
	t.Cleanup(netdev.StubRunIP(func(_ context.Context, name string, args ...string) (*exec.Result, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.link = append(r.link, name+" "+strings.Join(args, " "))
		return &exec.Result{}, nil
	}))
	t.Cleanup(func() { kernelRun, tunHolder = oldRun, oldHolder })
	return r
}

// underRead держит список в полёте, запускает op и проверяет, что до
// отпускания барьера op не тронула ни одного устройства; возвращает ошибку op.
func underRead(t *testing.T, gate *netdev.SwapGate, r *swapRig, op func() error) error {
	t.Helper()
	release := gate.Read()
	done := make(chan error, 1)
	go func() { done <- op() }()
	time.Sleep(50 * time.Millisecond)
	if got := r.links(); len(got) != 0 {
		release()
		t.Fatalf("устройство тронуто при списке в полёте: %v", got)
	}
	release()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("операция не завершилась после отпускания барьера")
		return nil
	}
}

// D-N1: снос plain tun и создание amneziawg — одной подменой под барьером:
// при списке в полёте ip не зовётся, после — del, затем add.
// Мутация: Start мимо Hold (прямой runIP) → del до release, красный.
func TestKernelStart_SwapUnderHold(t *testing.T) {
	r := newSwapRig(t, true, nil)
	gate := &netdev.SwapGate{}
	if err := underRead(t, gate, r, func() error { return NewKernel(gate).Start(context.Background(), "opkgtun7") }); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/opt/sbin/ip link del dev opkgtun7",
		"/opt/sbin/ip link add dev opkgtun7 type amneziawg",
	}
	if !slices.Equal(r.links(), want) {
		t.Fatalf("calls = %v, want %v", r.links(), want)
	}
}

// N11: обход /proc — до барьера: при списке в полёте Start уже проверил
// держателя (читатели не ждут обхода /proc).
// Мутация: tunHolder внутрь fn Hold → не вызван до release, красный.
func TestKernelStart_HolderCheckOutsideHold(t *testing.T) {
	r := newSwapRig(t, true, nil)
	gate := &netdev.SwapGate{}
	release := gate.Read()
	done := make(chan error, 1)
	go func() { done <- NewKernel(gate).Start(context.Background(), "opkgtun7") }()
	deadline := time.Now().Add(time.Second)
	for r.scans() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	scans := r.scans()
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if scans != 1 {
		t.Fatalf("обходов /proc до отпускания барьера = %d, want 1", scans)
	}
}

// Stop: держатель — до барьера, снос — под ним.
// Мутация: Stop мимо Hold → del до release, красный.
func TestKernelStop_UnderHold(t *testing.T) {
	r := newSwapRig(t, true, nil)
	gate := &netdev.SwapGate{}
	if err := underRead(t, gate, r, func() error { return NewKernel(gate).Stop(context.Background(), "opkgtun7") }); err != nil {
		t.Fatal(err)
	}
	if want := []string{"/opt/sbin/ip link del dev opkgtun7"}; !slices.Equal(r.links(), want) {
		t.Fatalf("calls = %v, want %v", r.links(), want)
	}
}

// ReplaceWithTun: del, затем tuntap add — одной подменой; устройства нет —
// только tuntap add; чужой держатель — HeldError, ip не зовётся.
// Мутации: порядок tuntap→del → красный; del без проверки наличия → красный
// («нет устройства»); без гарда держателя → del чужого, красный.
func TestKernelReplaceWithTun_DelThenTuntap(t *testing.T) {
	ctx := context.Background()
	t.Run("устройство есть", func(t *testing.T) {
		r := newSwapRig(t, true, nil)
		gate := &netdev.SwapGate{}
		if err := underRead(t, gate, r, func() error { return NewKernel(gate).ReplaceWithTun(ctx, "opkgtun7") }); err != nil {
			t.Fatal(err)
		}
		want := []string{
			"/opt/sbin/ip link del dev opkgtun7",
			"/opt/sbin/ip tuntap add dev opkgtun7 mode tun",
		}
		if !slices.Equal(r.links(), want) {
			t.Fatalf("calls = %v, want %v", r.links(), want)
		}
	})
	t.Run("устройства нет", func(t *testing.T) {
		r := newSwapRig(t, false, nil)
		if err := NewKernel(&netdev.SwapGate{}).ReplaceWithTun(ctx, "opkgtun7"); err != nil {
			t.Fatal(err)
		}
		if want := []string{"/opt/sbin/ip tuntap add dev opkgtun7 mode tun"}; !slices.Equal(r.links(), want) {
			t.Fatalf("calls = %v, want %v", r.links(), want)
		}
	})
	t.Run("чужой держатель", func(t *testing.T) {
		r := newSwapRig(t, true, &HeldError{Iface: "opkgtun7", PID: 4242, Comm: "csqtt"})
		err := NewKernel(&netdev.SwapGate{}).ReplaceWithTun(ctx, "opkgtun7")
		var held *HeldError
		if !errors.As(err, &held) || held.PID != 4242 {
			t.Fatalf("err = %v, want *HeldError", err)
		}
		if got := r.links(); len(got) != 0 {
			t.Fatalf("ip вызван при чужом держателе: %v", got)
		}
	})
}

// StopIfPresent: устройства нет — nil и ни одного ip; есть — Stop.
// Мутация: без проверки наличия → del по отсутствующему, красный.
func TestKernelStopIfPresent(t *testing.T) {
	ctx := context.Background()
	r := newSwapRig(t, false, nil)
	if err := NewKernel(&netdev.SwapGate{}).StopIfPresent(ctx, "opkgtun7"); err != nil || len(r.links()) != 0 {
		t.Fatalf("err=%v calls=%v, want nil и без ip", err, r.links())
	}
	r = newSwapRig(t, true, nil)
	if err := NewKernel(&netdev.SwapGate{}).StopIfPresent(ctx, "opkgtun7"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"/opt/sbin/ip link del dev opkgtun7"}; !slices.Equal(r.links(), want) {
		t.Fatalf("calls = %v, want %v", r.links(), want)
	}
}

// Без барьера бэкенд не собирается.
// Мутация: убрать панику в NewKernel → красный.
func TestNewKernel_NilGatePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "SwapGate обязателен") {
			t.Fatalf("recover = %v", r)
		}
	}()
	NewKernel(nil)
}

// Recreate: живое устройство — del и add amneziawg одной подменой под
// барьером (R61).
// Мутация: Recreate через свой SwapGate (мимо общего) → del до release, красный.
func TestKernelRecreate_UnderHold(t *testing.T) {
	r := newSwapRig(t, true, nil)
	gate := &netdev.SwapGate{}
	if err := underRead(t, gate, r, func() error { return NewKernel(gate).Recreate(context.Background(), "opkgtun7") }); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/opt/sbin/ip link del dev opkgtun7",
		"/opt/sbin/ip link add dev opkgtun7 type amneziawg",
	}
	if !slices.Equal(r.links(), want) {
		t.Fatalf("calls = %v, want %v", r.links(), want)
	}
}

// L2 (решение владельца 06.10): первый `tuntap add` после del отказал —
// повтор в том же Hold: читатель списка, пришедший между попытками, ждёт
// до конца подмены и записи без устройства не видит (окна нет), ошибки нет.
// Мутация: без повтора → ошибка и два вызова ip, красный.
func TestKernelReplaceWithTun_TuntapRetryUnderSameHold(t *testing.T) {
	r := newSwapRig(t, true, nil)
	gate := &netdev.SwapGate{}
	var (
		mu          sync.Mutex
		tuntaps     int
		readerIn    atomic.Bool
		inAtRetry   bool
		readerAdmit = make(chan struct{})
	)
	t.Cleanup(netdev.StubRunIP(func(_ context.Context, name string, args ...string) (*exec.Result, error) {
		mu.Lock()
		defer mu.Unlock()
		r.mu.Lock()
		r.link = append(r.link, name+" "+strings.Join(args, " "))
		r.mu.Unlock()
		if args[0] != "tuntap" {
			return &exec.Result{}, nil
		}
		tuntaps++
		if tuntaps == 1 {
			go func() { // читатель приходит между попытками
				release := gate.Read()
				readerIn.Store(true)
				release()
				close(readerAdmit)
			}()
			time.Sleep(20 * time.Millisecond)
			return nil, errors.New("injected: tuntap")
		}
		inAtRetry = readerIn.Load()
		return &exec.Result{}, nil
	}))

	if err := NewKernel(gate).ReplaceWithTun(context.Background(), "opkgtun7"); err != nil {
		t.Fatalf("ReplaceWithTun: %v", err)
	}
	<-readerAdmit
	want := []string{
		"/opt/sbin/ip link del dev opkgtun7",
		"/opt/sbin/ip tuntap add dev opkgtun7 mode tun",
		"/opt/sbin/ip tuntap add dev opkgtun7 mode tun",
	}
	if !slices.Equal(r.links(), want) {
		t.Fatalf("calls = %v, want %v", r.links(), want)
	}
	if inAtRetry {
		t.Fatal("читатель списка вошёл между попытками — запись без устройства видна")
	}
}
