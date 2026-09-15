package opkgtun

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mustReserve(t *testing.T, p *Pool, reqs ...Request) *Reservation {
	t.Helper()
	res, err := p.Reserve(context.Background(), reqs...)
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	return res
}

func TestNewPool_PanicsOnWiringDefects(t *testing.T) {
	ok := Source{Name: "тест", Read: func(context.Context) (Taken, error) { return nil, nil }}
	cases := []struct {
		name string
		call func()
	}{
		{"потолок ниже нуля", func() { NewPool(-1, ok) }},
		{"потолок выше прошивочного", func() { NewPool(maxCeiling+1, ok) }},
		{"без источников", func() { NewPool(16) }},
		{"источник без имени", func() { NewPool(16, Source{Read: ok.Read}) }},
		{"источник без чтения", func() { NewPool(16, Source{Name: "x"}) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("ждали панику")
				}
			}()
			c.call()
		})
	}
}

// Ноль валиден: пул из одного номера. Паниковать на нём значит запретить
// законную сборку.
func TestNewPool_ZeroCeilingIsValid(t *testing.T) {
	p := poolOf(t, 0, nil)
	res := mustReserve(t, p, Want(RouterModeHolder("fakeip")))
	if got := res.Numbers(); len(got) != 1 || got[0] != 0 {
		t.Fatalf("Numbers() = %v, want [0]", got)
	}
}

func TestReserve_OrderByRequester(t *testing.T) {
	t.Run("режим роутера идёт снизу", func(t *testing.T) {
		p := poolOf(t, 16, nil)
		res := mustReserve(t, p, Want(RouterModeHolder("fakeip")))
		if got := res.Numbers()[0]; got != 0 {
			t.Fatalf("номер = %d, want 0", got)
		}
	})
	t.Run("остальные с исторического окна", func(t *testing.T) {
		p := poolOf(t, 16, nil)
		res := mustReserve(t, p, Want(TunnelHolder("", "новый")))
		if got := res.Numbers()[0]; got != kernelWindowStart {
			t.Fatalf("номер = %d, want %d", got, kernelWindowStart)
		}
	})
	t.Run("нижняя половина позже своего окна", func(t *testing.T) {
		busy := Taken{}
		for n := kernelWindowStart; n <= 16; n++ {
			busy[n] = ProxyHolder("wdtt:x", "raw", "x")
		}
		p := poolOf(t, 16, busy)
		res := mustReserve(t, p, Want(TunnelHolder("", "новый")))
		if got := res.Numbers()[0]; got != 9 {
			t.Fatalf("номер = %d, want 9", got)
		}
	})
	t.Run("пол: 0 и 1 не достаются никому, кроме режимов роутера", func(t *testing.T) {
		busy := Taken{}
		for n := 2; n <= 16; n++ {
			busy[n] = ProxyHolder("wdtt:x", "raw", "x")
		}
		p := poolOf(t, 16, busy)
		if _, err := p.Reserve(context.Background(), Want(TunnelHolder("", "новый"))); !errors.Is(err, ErrExhausted) {
			t.Fatalf("error = %v, want ErrExhausted", err)
		}
		res := mustReserve(t, p, Want(RouterModeHolder("fakeip")))
		if got := res.Numbers()[0]; got != 0 {
			t.Fatalf("режиму роутера номер = %d, want 0", got)
		}
	})
}

func TestReserve_PinRules(t *testing.T) {
	self := ProxyHolder("wdtt:vk", "raw", "vk")
	cases := []struct {
		name string
		take Taken
		pin  int
		veto Taken
		want int // -1 = пин отвергнут, ушёл в перебор
	}{
		{"свободен", nil, 12, nil, 12},
		{"выше потолка", nil, 17, nil, -1},
		{"ниже нуля", nil, -1, nil, -1},
		{"аноним отдаётся", Taken{12: AnonHolder("живой интерфейс opkgtun12")}, 12, nil, 12},
		{"свой ключ отдаётся", Taken{12: ProxyHolder("wdtt:vk", "raw", "vk")}, 12, nil, 12},
		{"чужой заявленный не отдаётся", Taken{12: TunnelHolder("awg12", "дом")}, 12, nil, -1},
		{"вето выше пина", nil, 12, Taken{12: SystemTunnelHolder("awg12", "легаси")}, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := poolOf(t, 16, c.take)
			r := WantPinned(self, c.pin)
			if c.veto != nil {
				r = r.Excluding(c.veto)
			}
			got := mustReserve(t, p, r).Numbers()[0]
			switch {
			case c.want >= 0 && got != c.want:
				t.Fatalf("номер = %d, want %d", got, c.want)
			case c.want < 0 && got == c.pin:
				t.Fatalf("пин %d не должен был честиться", c.pin)
			}
		})
	}
}

// Спорный номер не отдаётся никому: две записи о нём спорят, и отдать его
// третьему значит назначить победителя жребием.
func TestReserve_ConflictedPinGrantedToNobody(t *testing.T) {
	a := &src{name: "записи", take: Taken{12: TunnelHolder("awg12", "дом")}}
	b := &src{name: "прокси", take: Taken{12: ProxyHolder("wdtt:vk", "raw", "vk")}}
	p := NewPool(16, a.source(), b.source())

	res := mustReserve(t, p, WantPinned(ProxyHolder("wdtt:other", "raw", "o"), 12))
	if got := res.Numbers()[0]; got == 12 {
		t.Fatal("спорный номер выдан пину")
	}
	if cf := res.Conflicts(); len(cf) != 1 || cf[0].Index != 12 {
		t.Fatalf("конфликты = %v", cf)
	}
	if got := res.Conflicts().String(); !strings.Contains(got, "OpkgTun12") {
		t.Fatalf("строка конфликта = %q", got)
	}
}

// Пины честятся раньше беспиновых, иначе беспиновая заявка заняла бы номер,
// на который ссылается permit пользователя. Проверяется при ОБОИХ порядках
// обхода: раскладка после пина у них разная.
func TestReserve_PinsBeforeUnpinned(t *testing.T) {
	cases := []struct {
		name    string
		self    Holder
		wantA   int
		pinnedB int
	}{
		{"проситель с историческим окном", TunnelHolder("", "новый"), 11, 10},
		{"режим роутера", RouterModeHolder("fakeip"), 0, 10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := poolOf(t, 16, nil)
			res := mustReserve(t, p,
				Want(c.self),
				WantPinned(ProxyHolder("wdtt:vk", "raw", "vk"), c.pinnedB))
			got := res.Numbers()
			if got[0] != c.wantA || got[1] != c.pinnedB {
				t.Fatalf("Numbers() = %v, want [%d %d]", got, c.wantA, c.pinnedB)
			}
		})
	}
}

// Отвергнутый пин уходит в перебор ВТОРОЙ фазой: иначе он занял бы номер, на
// который ещё не успел заявиться пин следующей заявки.
func TestReserve_RejectedPinDoesNotStealLaterPin(t *testing.T) {
	p := poolOf(t, 16, nil)
	res := mustReserve(t, p,
		WantPinned(ProxyHolder("a", "", "a"), 10),
		WantPinned(ProxyHolder("b", "", "b"), 10), // отвергнут: 10 уже выдан
		WantPinned(ProxyHolder("c", "", "c"), 11))
	got := res.Numbers()
	if got[0] != 10 || got[2] != 11 {
		t.Fatalf("Numbers() = %v; пины 10 и 11 должны были достаться первому и третьему", got)
	}
	if got[1] == 10 || got[1] == 11 {
		t.Fatalf("отвергнутый пин забрал чужой номер: %v", got)
	}
}

// Номер, выданный в ЭТОМ вызове, занят для остальных заявок — иначе две
// беспиновые получили бы один номер.
func TestReserve_GrantedInThisCallIsBusy(t *testing.T) {
	t.Run("две беспиновые", func(t *testing.T) {
		p := poolOf(t, 16, nil)
		got := mustReserve(t, p,
			Want(TunnelHolder("", "первый")),
			Want(TunnelHolder("", "второй"))).Numbers()
		if got[0] == got[1] {
			t.Fatalf("оба получили %d", got[0])
		}
	})
	t.Run("два одинаковых пина разных владельцев", func(t *testing.T) {
		p := poolOf(t, 16, nil)
		got := mustReserve(t, p,
			WantPinned(ProxyHolder("wdtt:a", "wg", "a"), 12),
			WantPinned(ProxyHolder("wdtt:a", "raw", "a"), 12)).Numbers()
		if got[0] != 12 {
			t.Fatalf("первый пин = %d, want 12", got[0])
		}
		if got[1] == 12 {
			t.Fatal("второй пин получил тот же номер")
		}
	})
}

func TestWantPinned_PanicsOnKeylessHolder(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("ждали панику: пин у безключевого невыразим")
		}
	}()
	WantPinned(TunnelHolder("", "новичок"), 12)
}

// Дубль ключа паникует ДО захвата pick: паника под ним встала бы очередью
// навсегда. Проверяется тем, что после паники пул продолжает выдавать.
func TestReserve_DuplicateKeyPanicsBeforePick(t *testing.T) {
	p := poolOf(t, 16, nil)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("ждали панику на дубле ключа")
			}
		}()
		self := ProxyHolder("wdtt:vk", "raw", "vk")
		_, _ = p.Reserve(context.Background(), Want(self), Want(self))
	}()
	if _, err := p.Reserve(context.Background(), Want(TunnelHolder("", "после"))); err != nil {
		t.Fatalf("после паники пул встал: %v", err)
	}
}

// Безключевые не дедупятся: новичков в одном вызове может быть сколько угодно.
func TestReserve_KeylessRequestsAreNotDeduped(t *testing.T) {
	p := poolOf(t, 16, nil)
	got := mustReserve(t, p,
		Want(TunnelHolder("", "a")),
		Want(TunnelHolder("", "b")),
		Want(TunnelHolder("", "c"))).Numbers()
	if len(got) != 3 || got[0] == got[1] || got[1] == got[2] {
		t.Fatalf("Numbers() = %v", got)
	}
}

func TestReserve_ZeroRequestsTouchesNothing(t *testing.T) {
	s := &src{name: "тест"}
	p := NewPool(16, s.source())
	res, err := p.Reserve(context.Background())
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if s.reads != 0 {
		t.Fatalf("источник прочитан %d раз при нуле заявок", s.reads)
	}
	if got := res.Numbers(); len(got) != 0 {
		t.Fatalf("Numbers() = %v", got)
	}
	res.Close() // не должен паниковать
}

// Ошибка источника — это «не смогли посмотреть», а не «свободно»: выдать
// номер по неполной занятости значит получить коллизию.
func TestReserve_SourceFailureGrantsNothing(t *testing.T) {
	good := &src{name: "записи", take: Taken{5: TunnelHolder("awg5", "дом")}}
	bad := &src{name: "ndms", err: errors.New("RCI молчит")}
	p := NewPool(16, good.source(), bad.source())

	_, err := p.Reserve(context.Background(), Want(TunnelHolder("", "новый")))
	if err == nil || !strings.Contains(err.Error(), "ndms") {
		t.Fatalf("error = %v; ждали отказ с именем источника", err)
	}
	if errors.Is(err, ErrExhausted) {
		t.Fatal("сбой источника не должен выглядеть исчерпанием пула")
	}
	p.mu.Lock()
	n := len(p.reserved)
	p.mu.Unlock()
	if n != 0 {
		t.Fatalf("после отказа в reserved осталось %d номеров", n)
	}
}

// pick обязан отпускаться на КАЖДОМ пути выхода: иначе встаёт не аллокатор, а
// все четыре подсистемы — до перезапуска демона.
func TestReserve_PickReleasedOnEveryFailurePath(t *testing.T) {
	full := Taken{}
	for n := 0; n <= 16; n++ {
		full[n] = ProxyHolder("wdtt:x", "raw", "x")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		name string
		run  func(p *Pool)
	}{
		{"сбой источника", func(p *Pool) {
			_, _ = p.Reserve(context.Background(), Want(TunnelHolder("", "x")))
		}},
		{"исчерпание", func(p *Pool) {
			_, _ = p.Reserve(context.Background(), Want(TunnelHolder("", "x")))
		}},
		{"отменённый контекст", func(p *Pool) {
			_, _ = p.Reserve(canceled, Want(TunnelHolder("", "x")))
		}},
		{"паника дубля ключа", func(p *Pool) {
			defer func() { _ = recover() }()
			self := ProxyHolder("k", "", "k")
			_, _ = p.Reserve(context.Background(), Want(self), Want(self))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &src{name: "тест", take: full}
			if c.name == "сбой источника" {
				s = &src{name: "тест", err: errors.New("сбой")}
			}
			p := NewPool(16, s.source())
			c.run(p)
			// Пул обязан принять следующую заявку: если pick не отпущен,
			// второй Reserve повиснет на нём до таймаута.
			// Контекст БЕЗ дедлайна намеренно: с дедлайном застрявший pick
			// выглядел бы как обычный отказ по таймауту, и тест прошёл бы.
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _ = p.Reserve(context.Background(), Want(RouterModeHolder("fakeip")))
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("pick не отпущен: следующий Reserve повис")
			}
		})
	}
}

// Отмена контекста и исчерпание пула — разные новости, и путать их нельзя:
// на первую надо повторить, на вторую — освободить номер.
func TestReserve_CanceledContextIsNotExhaustion(t *testing.T) {
	p := poolOf(t, 16, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Reserve(ctx, Want(TunnelHolder("", "x")))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrExhausted) {
		t.Fatal("отмена не должна выглядеть исчерпанием")
	}
}

// Отмена В ОЖИДАНИИ pick — тот же ответ. Ворота держат первый Reserve внутри
// чтения источника, пока второй ждёт очередь.
func TestReserve_CancelWhileWaitingForPick(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	s := Source{Name: "ворота", Read: func(context.Context) (Taken, error) {
		close(entered)
		<-release
		return nil, nil
	}}
	p := NewPool(16, s)

	go func() { _, _ = p.Reserve(context.Background(), Want(TunnelHolder("", "первый"))) }()
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	_, err := p.Reserve(ctx, Want(TunnelHolder("", "второй")))
	close(release)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// Ранний срез: пока A читает источники, B успевает закрыть свою резервацию.
// Номер B при этом всё равно не достаётся A — его запись уже на диске, но в
// снимок источников, прочитанный ДО закрытия, она попасть не успела.
//
// Этот же тест доказывает, что Close берёт только mu: если бы он ждал pick,
// он встал бы за A и тест повис бы.
func TestReserve_EarlySnapshotSurvivesConcurrentClose(t *testing.T) {
	// Ворота держат ВТОРОЕ чтение источника: первое обслуживает резервацию,
	// которую мы потом закроем на ходу.
	var reads atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	gate := Source{Name: "ворота", Read: func(context.Context) (Taken, error) {
		if reads.Add(1) == 2 {
			close(entered)
			<-release
		}
		return nil, nil
	}}
	p := NewPool(16, gate)

	held := mustReserve(t, p, WantPinned(ProxyHolder("wdtt:vk", "raw", "vk"), 10))

	var got []int
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, err := p.Reserve(context.Background(), Want(TunnelHolder("", "новый")))
		if err != nil {
			t.Errorf("Reserve() error = %v", err)
			return
		}
		got = res.Numbers()
	}()

	<-entered
	held.Close() // берёт только mu — иначе встал бы за владельцем pick, и тест повис
	close(release)
	<-done

	if len(got) != 1 || got[0] == 10 {
		t.Fatalf("Numbers() = %v; номер из раннего среза не должен был достаться", got)
	}
}

func TestReservation_Close(t *testing.T) {
	t.Run("двойной Close идемпотентен", func(t *testing.T) {
		p := poolOf(t, 16, nil)
		res := mustReserve(t, p, Want(TunnelHolder("", "x")))
		res.Close()
		res.Close()
		p.mu.Lock()
		n := len(p.reserved)
		p.mu.Unlock()
		if n != 0 {
			t.Fatalf("в reserved осталось %d", n)
		}
	})
	t.Run("nil безопасен", func(t *testing.T) {
		var res *Reservation
		res.Close()
		if got := res.Numbers(); got != nil {
			t.Fatalf("Numbers() = %v", got)
		}
		if got := res.Conflicts(); got != nil {
			t.Fatalf("Conflicts() = %v", got)
		}
	})
	t.Run("снимает только свои номера", func(t *testing.T) {
		// Две резервации ОДНОГО владельца на разных номерах: поздний Close
		// первой не должен снять номер, выданный второй.
		p := poolOf(t, 16, nil)
		self := ProxyHolder("wdtt:vk", "raw", "vk")
		first := mustReserve(t, p, WantPinned(self, 10))
		first.Close()
		second := mustReserve(t, p, WantPinned(self, 10))
		first.Close() // повторный, уже закрытой
		p.mu.Lock()
		owner := p.reserved[10]
		p.mu.Unlock()
		if owner != second {
			t.Fatal("закрытая резервация сняла чужую запись")
		}
		second.Close()
	})
}

// Резервация держит номер и для соседа, ещё не записавшего свою запись.
func TestReserve_OpenReservationIsBusyForOthers(t *testing.T) {
	p := poolOf(t, 16, nil)
	first := mustReserve(t, p, Want(TunnelHolder("", "первый")))
	defer first.Close()
	second := mustReserve(t, p, Want(TunnelHolder("", "второй")))
	defer second.Close()
	if first.Numbers()[0] == second.Numbers()[0] {
		t.Fatalf("оба получили %d", first.Numbers()[0])
	}
}

// «Всё или ничего»: если вторая заявка не нашла номера, первая тоже не
// записывается — иначе номер утёк бы до перезапуска.
func TestReserve_AllOrNothing(t *testing.T) {
	busy := Taken{}
	for n := 2; n <= 16; n++ {
		busy[n] = ProxyHolder("wdtt:x", "raw", "x")
	}
	delete(busy, 5) // ровно один свободный номер на две заявки
	p := poolOf(t, 16, busy)

	_, err := p.Reserve(context.Background(),
		Want(TunnelHolder("", "первый")),
		Want(TunnelHolder("", "второй")))
	if !errors.Is(err, ErrExhausted) {
		t.Fatalf("error = %v, want ErrExhausted", err)
	}
	p.mu.Lock()
	n := len(p.reserved)
	p.mu.Unlock()
	if n != 0 {
		t.Fatalf("после отказа в reserved осталось %d номеров", n)
	}
}

func TestExhausted_Message(t *testing.T) {
	take := Taken{
		2:  ProxyHolder("wdtt:a", "raw", "a"),
		3:  ProxyHolder("wdtt:b", "raw", "b"),
		4:  RouterModeHolder("fakeip"),
		5:  AnonHolder("живой интерфейс opkgtun5"),
		6:  ProxyHolder("wdtt:c", "raw", "c"),
		7:  ProxyHolder("wdtt:d", "raw", "d"),
		8:  ProxyHolder("wdtt:e", "raw", "e"),
		9:  TunnelHolder("awg9", "свой"),
		10: TunnelHolder("awg10", "тоже свой"),
	}
	e := &exhausted{
		self:      TunnelHolder("", "новый"),
		floor:     2,
		ceiling:   10,
		taken:     take,
		reserved:  Taken{11: ProxyHolder("wdtt:f", "raw", "f")},
		veto:      Taken{6: SystemTunnelHolder("awg6", "легаси")},
		conflicts: Conflicts{{Index: 4, A: RouterModeHolder("fakeip"), B: ProxyHolder("wdtt:g", "raw", "g")}},
	}
	got := e.Error()
	want := "нет свободного номера OpkgTun в диапазоне 2..10. " +
		"Чужие подсистемы: 2 — прокси a, 3 — прокси b, 4 — режим роутера fakeip, " +
		"5 — живой интерфейс opkgtun5, 6 — прокси c, и ещё 2 (прокси — 2). " +
		"Ваших (туннель): 2. " +
		"Заняты незавершённой операцией: 11 — прокси f. " +
		"Спорные номера, заявлены дважды: OpkgTun4: режим роутера fakeip и прокси g. " +
		"Освободите один номер"
	if got != want {
		t.Fatalf("Error() =\n%q\nwant\n%q", got, want)
	}
	if !errors.Is(e, ErrExhausted) {
		t.Fatal("errors.Is(ErrExhausted) = false")
	}
}

// Номер, свободный в пуле, но занятый ИДЕНТИФИКАТОРОМ, назван отдельно:
// удаление такой записи освобождает awgN, но не номер OpkgTunN.
func TestExhausted_VetoReportedSeparately(t *testing.T) {
	e := &exhausted{
		self:    TunnelHolder("", "новый"),
		floor:   2,
		ceiling: 3,
		taken:   Taken{2: ProxyHolder("wdtt:a", "raw", "a")},
		veto:    Taken{3: SystemTunnelHolder("awg3", "легаси")},
	}
	got := e.Error()
	if !strings.Contains(got, "занят идентификатор: 3 — системный туннель «легаси»") {
		t.Fatalf("Error() = %q", got)
	}
}

// Гонки: выдача и закрытие из нескольких горутин не должны отдавать один
// номер дважды.
func TestReserve_ConcurrentGrantsAreUnique(t *testing.T) {
	p := poolOf(t, 16, nil)
	const n = 8
	var mu sync.Mutex
	seen := map[int]bool{}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := p.Reserve(context.Background(), Want(TunnelHolder("", "x")))
			if err != nil {
				return
			}
			mu.Lock()
			got := res.Numbers()[0]
			if seen[got] {
				t.Errorf("номер %d выдан дважды", got)
			}
			seen[got] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
}

// Чужая незакрытая резервация не отдаётся и пину: номер уже выдан, а запись
// соседа ещё не легла на диск — по источникам он выглядит свободным.
func TestReserve_PinDoesNotTakeOpenReservation(t *testing.T) {
	p := poolOf(t, 16, nil)
	held := mustReserve(t, p, Want(TunnelHolder("", "сосед")))
	defer held.Close()
	busy := held.Numbers()[0]

	got := mustReserve(t, p, WantPinned(ProxyHolder("wdtt:vk", "raw", "vk"), busy)).Numbers()[0]
	if got == busy {
		t.Fatalf("пин забрал номер %d из незакрытой резервации соседа", busy)
	}
}

// Вето действует и в переборе, не только на пине: номер, свободный в пуле, но
// занятый идентификатором awgN, не должен достаться туннелю никаким путём.
func TestReserve_VetoAppliesToTraversal(t *testing.T) {
	veto := Taken{}
	for n := kernelWindowStart; n <= 16; n++ {
		veto[n] = SystemTunnelHolder("awg"+itoa(n), "легаси")
	}
	p := poolOf(t, 16, nil)
	got := mustReserve(t, p, Want(TunnelHolder("", "новый")).Excluding(veto)).Numbers()[0]
	if got >= kernelWindowStart {
		t.Fatalf("номер = %d; вето на %d..16 обойдено перебором", got, kernelWindowStart)
	}
	if got != 9 {
		t.Fatalf("номер = %d, want 9 (первый за вычетом вето)", got)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// Потолок arm/arm64 — 49, и верхний сегмент 17..потолок проходится только там.
// На mips его нет вовсе, поэтому все прочие тесты о нём молчат, а смена
// поведения «kernel на arm после awg16 → awg17» иначе не зафиксирована ничем.
func TestReserve_ReachesUpperWindowOnArm(t *testing.T) {
	busy := Taken{}
	for n := 2; n <= 16; n++ {
		busy[n] = ProxyHolder("wdtt:x"+itoa(n), "", "x")
	}
	p := poolOf(t, 49, busy)

	res := mustReserve(t, p, Want(TunnelHolder("", "новый")))
	defer res.Close()
	if got := res.Numbers()[0]; got != 17 {
		t.Fatalf("номер = %d, ждали 17: верхний сегмент окна не проходится", got)
	}
}

// Пол не мешает верхнему сегменту: исчерпав 10..49 и 9..2, проситель получает
// честный отказ, а не ноль.
func TestReserve_ExhaustsWholeArmWindow(t *testing.T) {
	busy := Taken{}
	for n := 2; n <= 49; n++ {
		busy[n] = ProxyHolder("wdtt:x"+itoa(n), "", "x")
	}
	p := poolOf(t, 49, busy)

	if _, err := p.Reserve(context.Background(), Want(TunnelHolder("", "новый"))); !errors.Is(err, ErrExhausted) {
		t.Fatalf("error = %v, want ErrExhausted", err)
	}
	// Режимам роутера остаются 0 и 1.
	res := mustReserve(t, p, Want(RouterModeHolder("fakeip")))
	defer res.Close()
	if got := res.Numbers()[0]; got != 0 {
		t.Fatalf("режиму роутера номер = %d, want 0", got)
	}
}

// Режим роутера поднимается ВЫШЕ девятки. Прежнее окно 0..9 было нашим
// делением, а не фактом прошивки, и снятие его — одно из решений #891: без
// этого единственный работающий режим голодал бы, когда нижнюю половину
// разобрали туннели и прокси, которым есть куда отступать.
func TestReserve_RouterModeRisesAboveLowerWindow(t *testing.T) {
	busy := Taken{}
	for n := 0; n <= 9; n++ {
		busy[n] = ProxyHolder("wdtt:x"+itoa(n), "", "x")
	}
	p := poolOf(t, 49, busy)

	res := mustReserve(t, p, Want(RouterModeHolder("fakeip")))
	defer res.Close()
	if got := res.Numbers()[0]; got != 10 {
		t.Fatalf("режиму роутера номер = %d, ждали 10: он не поднимается выше нижней половины", got)
	}
}

// Строгая претензия не перебивает безключевого держателя, обычная перебивает.
// Разница несущая: запись NDMS переживает удаление устройства и могла остаться
// от ЧУЖОГО интерфейса.
func TestReserve_StrictPinSparesAnonymous(t *testing.T) {
	anon := Taken{5: AnonHolder("запись NDMS OpkgTun5")}
	self := RouterModeHolder("policy-tun")

	strict := mustReserve(t, poolOf(t, 16, anon), WantPinnedStrict(self, 5))
	defer strict.Close()
	if strict.Numbers()[0] == 5 {
		t.Fatal("строгая претензия отобрала номер у безключевого держателя")
	}

	loose := mustReserve(t, poolOf(t, 16, anon), WantPinned(self, 5))
	defer loose.Close()
	if loose.Numbers()[0] != 5 {
		t.Fatal("обычный пин обязан перебивать безключевого: владение доказано вызывающим")
	}
}

// Свой номер строгая претензия забирает: совпадение ключей — это и есть
// доказательство.
func TestReserve_StrictPinTakesOwnNumber(t *testing.T) {
	self := RouterModeHolder("policy-tun")
	p := poolOf(t, 16, Taken{5: RouterModeHolder("fakeip-tun")})

	res := mustReserve(t, p, WantPinnedStrict(self, 5))
	defer res.Close()
	if res.Numbers()[0] != 5 {
		t.Fatal("строгая претензия не взяла номер собственного ключа")
	}
}

// Перебор не выходит за потолок ни у кого. Нижняя половина окна (9..2) идёт
// сверху вниз и потому не обрезалась потолком сама собой: на маленьком пуле
// проситель получал номер ВЫШЕ потолка, тогда как пин тот же потолок проверял.
// Расхождение выбора и пина — это и есть класс #891.
func TestReserve_NeverIssuesAboveCeiling(t *testing.T) {
	for _, ceiling := range []int{0, 1, 2, 3, 5, 8, 9, 16} {
		for _, self := range []Holder{
			ProxyHolder("wdtt-client:x", "", "x"),
			TunnelHolder("", "новый"),
			RouterModeHolder("fakeip-tun"),
		} {
			p := NewPool(ceiling, Source{
				Name: "пусто",
				Read: func(context.Context) (Taken, error) { return nil, nil },
			})
			res, err := p.Reserve(context.Background(), Want(self))
			if err != nil {
				continue // честное исчерпание — законный исход
			}
			got := res.Numbers()[0]
			res.Close()
			if got > ceiling {
				t.Errorf("потолок %d, просителю %q выдан номер %d", ceiling, self, got)
			}
		}
	}
}
