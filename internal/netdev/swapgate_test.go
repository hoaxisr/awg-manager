package netdev

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// quiet — сколько ждём, чтобы сказать «не вернулся»: заблокированный вызов
// за это время не вернётся, свободный — вернётся за микросекунды.
const quiet = 50 * time.Millisecond

// recordIP подменяет runIP журналом argv.
func recordIP(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	t.Cleanup(StubRunIP(func(_ context.Context, name string, args ...string) (*exec.Result, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, name+" "+strings.Join(args, " "))
		return &exec.Result{}, nil
	}))
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(calls)
	}
}

// returnsWithin — fn вернулась за d.
func returnsWithin(d time.Duration, fn func()) bool {
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// Пока идёт подмена, читатель списка ждёт; отпустили — проходит.
// Мутация: Read без RLock → Read возвращается внутри Hold, красный.
func TestSwapGate_ReadersWaitForHold(t *testing.T) {
	var g SwapGate
	entered, unblock := make(chan struct{}), make(chan struct{})
	held := make(chan error, 1)
	go func() {
		held <- g.Hold(context.Background(), func(Swapper) error {
			close(entered)
			<-unblock
			return nil
		})
	}()
	<-entered

	read := make(chan struct{})
	go func() { g.Read()(); close(read) }()
	select {
	case <-read:
		t.Fatal("Read вернулся, пока fn держит барьер")
	case <-time.After(quiet):
	}

	close(unblock)
	if err := <-held; err != nil {
		t.Fatal(err)
	}
	select {
	case <-read:
	case <-time.After(time.Second):
		t.Fatal("Read не прошёл после отпускания барьера")
	}
}

// Подмена ждёт список в полёте: fn не начинается, пока читатель не отпустил.
// Мутация: Hold без Lock → fn входит при живом читателе, красный.
func TestSwapGate_HoldWaitsForInflightReader(t *testing.T) {
	var g SwapGate
	release := g.Read()
	entered := make(chan struct{})
	held := make(chan error, 1)
	go func() {
		held <- g.Hold(context.Background(), func(Swapper) error { close(entered); return nil })
	}()
	select {
	case <-entered:
		t.Fatal("fn вошла при списке в полёте")
	case <-time.After(quiet):
	}
	release()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("fn не вошла после отпускания читателя")
	}
	if err := <-held; err != nil {
		t.Fatal(err)
	}
}

// Зависшая подмена отпускает барьер по потолку: ctx fn истекает через
// SwapHoldMax, Hold возвращается, читатели проходят.
// Мутация: без WithTimeout → fn ждёт вечно, тест падает по таймауту.
func TestSwapGate_HoldBounded(t *testing.T) {
	var g SwapGate
	start := time.Now()
	var fnErr error
	ok := returnsWithin(SwapHoldMax+time.Second, func() {
		fnErr = g.Hold(context.Background(), func(sw Swapper) error {
			<-sw.Context().Done()
			return sw.Context().Err()
		})
	})
	if !ok {
		t.Fatalf("Hold не вернулся за SwapHoldMax+1s")
	}
	if took := time.Since(start); took < SwapHoldMax {
		t.Fatalf("Hold вернулся за %v, раньше потолка %v", took, SwapHoldMax)
	}
	if !errors.Is(fnErr, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", fnErr)
	}
	if !returnsWithin(time.Second, func() { g.Read()() }) {
		t.Fatal("читатель не прошёл после истечения потолка")
	}
}

// N4: обрыв запроса вызывающего не рвёт подмену — ctx fn живой, даже если
// ctx вызывающего отменён до Hold.
// Мутация: без WithoutCancel → fn видит Done сразу, красный.
func TestSwapGate_HoldSurvivesCallerCancel(t *testing.T) {
	var g SwapGate
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := g.Hold(ctx, func(sw Swapper) error {
		select {
		case <-sw.Context().Done():
			return sw.Context().Err()
		default:
		}
		if _, ok := sw.Context().Deadline(); !ok {
			return errors.New("у ctx подмены нет потолка")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("fn увидела отменённый ctx вызывающего: %v", err)
	}
}

// DeleteLink — тот же Hold: при списке в полёте `ip` не зовётся.
// Мутация: DeleteLink мимо Hold (прямой runIP) → del до release, красный.
func TestSwapGate_DeleteLink_UnderHold(t *testing.T) {
	calls := recordIP(t)
	var g SwapGate
	release := g.Read()
	done := make(chan error, 1)
	go func() { done <- g.DeleteLink(context.Background(), "opkgtun9") }()
	time.Sleep(quiet)
	if got := calls(); len(got) != 0 {
		t.Fatalf("ip вызван при списке в полёте: %v", got)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if want := []string{"/opt/sbin/ip link del dev opkgtun9"}; !slices.Equal(calls(), want) {
		t.Fatalf("calls = %v, want %v", calls(), want)
	}
}

// Формы команд Swapper — те, что проверены на стенде (П3/П4).
// Мутация: `mode tun` убрать из TuntapAdd → красный.
func TestSwapper_CommandForms(t *testing.T) {
	calls := recordIP(t)
	var g SwapGate
	err := g.Hold(context.Background(), func(sw Swapper) error {
		return errors.Join(sw.LinkDel("opkgtun3"), sw.LinkAdd("opkgtun3", "amneziawg"), sw.TuntapAdd("opkgtun3"))
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/opt/sbin/ip link del dev opkgtun3",
		"/opt/sbin/ip link add dev opkgtun3 type amneziawg",
		"/opt/sbin/ip tuntap add dev opkgtun3 mode tun",
	}
	if !slices.Equal(calls(), want) {
		t.Fatalf("calls = %v, want %v", calls(), want)
	}
}

// Отказ ip доходит до вызывающего с именем устройства и stderr.
// Мутация: run глотает ошибку → nil, красный.
func TestSwapper_ErrorCarriesStderr(t *testing.T) {
	t.Cleanup(StubRunIP(func(context.Context, string, ...string) (*exec.Result, error) {
		return &exec.Result{Stderr: "RTNETLINK answers: File exists", ExitCode: 2}, errors.New("exit status 2")
	}))
	var g SwapGate
	err := g.Hold(context.Background(), func(sw Swapper) error { return sw.LinkAdd("opkgtun3", "amneziawg") })
	if err == nil || !strings.Contains(err.Error(), "opkgtun3") || !strings.Contains(err.Error(), "File exists") {
		t.Fatalf("err = %v", err)
	}
}

// Swapper без ctx (собранный мимо Hold) — паника с текстом.
// Мутация: Context без проверки nil → паники нет, красный.
func TestSwapper_OutsideHoldPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil || !strings.Contains(r.(string), "Swapper вне Hold") {
			t.Fatalf("recover = %v, want паника «Swapper вне Hold»", r)
		}
	}()
	(&swapper{}).Context()
}

// Сбежавший Swapper: сохранён из fn и вызван после возврата Hold — ошибка
// ErrSwapperDone, `ip` не запускается (иначе снос мимо барьера).
// Мутация: убрать проверку done в run → ip вызван, красный.
func TestSwapper_EscapedAfterHold(t *testing.T) {
	calls := recordIP(t)
	var g SwapGate
	var saved Swapper
	if err := g.Hold(context.Background(), func(sw Swapper) error { saved = sw; return nil }); err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"LinkDel":   func() error { return saved.LinkDel("opkgtun3") },
		"LinkAdd":   func() error { return saved.LinkAdd("opkgtun3", "amneziawg") },
		"TuntapAdd": func() error { return saved.TuntapAdd("opkgtun3") },
	} {
		if err := call(); !errors.Is(err, ErrSwapperDone) {
			t.Errorf("%s после Hold: err = %v, want ErrSwapperDone", name, err)
		}
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("ip вызван сбежавшим Swapper: %v", got)
	}
}
