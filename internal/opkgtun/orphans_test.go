package opkgtun

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Воспроизводит расклад с роутера: awg12/awg13 живы (запись + след), а на
// номерах 10 и 11 остался ОДИН след — запись NDMS и живое устройство без
// записи владельца.
func TestPool_Orphans_AnonymousOnlyIndices(t *testing.T) {
	records := &src{name: "записи туннелей", take: Taken{
		12: TunnelHolder("awg12", "Estonia"),
		13: TunnelHolder("awg13", "Germany"),
	}}
	ndms := &src{name: "записи NDMS", take: Taken{
		10: AnonHolder("запись NDMS OpkgTun10"),
		12: AnonHolder("запись NDMS OpkgTun12"),
		13: AnonHolder("запись NDMS OpkgTun13"),
	}}
	live := &src{name: "живые интерфейсы", take: Taken{
		10: LiveHolder(10),
		11: LiveHolder(11),
		12: LiveHolder(12),
	}}
	p := NewPool(16, records.source(), ndms.source(), live.source())

	got, err := p.Orphans(context.Background())
	if err != nil {
		t.Fatalf("Orphans() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("сирот = %v, ждали ровно 10 и 11", got)
	}
	if got[0].Index != 10 || got[1].Index != 11 {
		t.Errorf("индексы = %d, %d; ждали 10, 11 по возрастанию", got[0].Index, got[1].Index)
	}
}

// Номер, выданный прямо сейчас, ещё не дошёл до записи владельца. Показать
// его сиротой значит предложить снести интерфейс, который в эту секунду
// создают.
func TestPool_Orphans_SkipsOpenReservation(t *testing.T) {
	live := &src{name: "живые интерфейсы", take: Taken{}}
	p := NewPool(16, live.source())

	res := mustReserve(t, p, Want(TunnelHolder("awg14", "новый")))
	defer res.Close()
	n := res.Numbers()[0]
	live.take = Taken{n: LiveHolder(n)}

	got, err := p.Orphans(context.Background())
	if err != nil {
		t.Fatalf("Orphans() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("сироты = %v, ждали пусто: номер %d держит незакрытая резервация", got, n)
	}
}

// Недосчёт держателей — это ложная сирота, то есть предложение удалить живое.
// Поэтому отказ источника отказывает ответу целиком, а не отдаёт остаток.
func TestPool_Orphans_SourceErrorFailsWholeAnswer(t *testing.T) {
	good := &src{name: "живые интерфейсы", take: Taken{10: LiveHolder(10)}}
	bad := &src{name: "записи туннелей", err: errors.New("диск не читается")}
	p := NewPool(16, good.source(), bad.source())

	got, err := p.Orphans(context.Background())
	if err == nil {
		t.Fatalf("Orphans() = %v, ждали отказ", got)
	}
	if got != nil {
		t.Errorf("при отказе отдан список %v, ждали nil", got)
	}
}

// Резервация, ЗАКРЫВШАЯСЯ во время чтения источников, не должна делать номер
// сиротой.
//
// Владелец пишет запись на диск после выдачи номера, поэтому источники, уже
// прочитанные к этому моменту, его не видят, а снимок резерваций, взятый
// ПОСЛЕ, не видит закрытую резервацию. Без раннего снимка номер проваливается
// между двумя взглядами и приезжает пользователю как «удалить».
func TestPool_Orphans_SkipsReservationClosedDuringRead(t *testing.T) {
	live := &src{name: "живые интерфейсы", take: Taken{}}
	p := NewPool(16, live.source())

	res := mustReserve(t, p, Want(TunnelHolder("awg14", "новый")))
	n := res.Numbers()[0]

	// Источник закрывает резервацию ровно в момент чтения занятости: так
	// выглядит сосед, успевший дописать запись и отпустить номер, пока мы
	// обходим источники.
	closing := Source{Name: "закрывает соседа", Read: func(context.Context) (Taken, error) {
		res.Close()
		return Taken{n: LiveHolder(n)}, nil
	}}
	p2 := NewPool(16, closing)
	p2.mu.Lock()
	p2.reserved[n] = res
	p2.mu.Unlock()
	res.p = p2

	got, err := p2.Orphans(context.Background())
	if err != nil {
		t.Fatalf("Orphans() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("сироты = %v, ждали пусто: номер %d держала резервация, закрывшаяся при чтении", got, n)
	}
}

// Поздний снимок резерваций — не украшение: резервация, ОТКРЫВШАЯСЯ во время
// чтения источников, раннему снимку не видна, а её номер в занятости ещё не
// значится (владелец не дописал запись). Без позднего снимка он приезжает
// сиротой.
func TestPool_Orphans_SkipsReservationOpenedDuringRead(t *testing.T) {
	const n = 7
	var p *Pool

	// Соседа вносим в reserved НАПРЯМУЮ, а не вызовом Reserve: Reserve читает
	// те же источники, и вызов из источника вошёл бы в него повторно — тест
	// вис бы на втором захвате pick вместо того, чтобы проверять предмет.
	opening := Source{Name: "открывает соседа", Read: func(context.Context) (Taken, error) {
		p.mu.Lock()
		if _, already := p.reserved[n]; !already {
			p.reserved[n] = &Reservation{p: p, numbers: []int{n}, holders: []Holder{TunnelHolder("awg14", "новый")}}
		}
		p.mu.Unlock()
		return Taken{}, nil
	}}
	// Устройство в ядре уже есть — без позднего снимка номер выглядит сиротой.
	live := Source{Name: "живые интерфейсы", Read: func(context.Context) (Taken, error) {
		return Taken{n: LiveHolder(n)}, nil
	}}
	p = NewPool(16, opening, live)

	got, err := p.Orphans(context.Background())
	if err != nil {
		t.Fatalf("Orphans() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("сироты = %v, ждали пусто: номер %d держит резервация, открытая при чтении", got, n)
	}
}

// Перепроверка перед сносом ходит под семафором выбора — там окна нет вовсе.
// Тест сторожит САМ ФАКТ взятия семафора: без него удаление снова начнёт
// гоняться с выдачей номеров.
func TestPool_OrphansExclusive_TakesPickSemaphore(t *testing.T) {
	live := &src{name: "живые интерфейсы", take: Taken{}}
	p := NewPool(16, live.source())

	p.pick <- struct{}{} // семафор занят кем-то другим
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	if _, err := p.OrphansExclusive(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, ждали ожидание семафора до отмены по контексту", err)
	}
	<-p.pick

	if _, err := p.OrphansExclusive(context.Background()); err != nil {
		t.Fatalf("на свободном семафоре Orphans отказал: %v", err)
	}
}
