package main

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/opkgtun"
)

// Пул считает занятость ШИРЕ имени: живой источник собирает номера ещё и из
// awgm<N>/awg<N>, и sysinfo.ListSystemInterfaces прямо предупреждает, что класс
// имени там теряется. Для выдачи номера лишний занятый безопасен; здесь тот же
// перебор переворачивается — номер туннеля awgm10, за которым нет никакого
// opkgtun10, приехал бы пользователю кнопкой «удалить opkgtun10».
//
// Проверка показывает ТОЛЬКО интерфейсы OpkgTun.
func TestOrphanIfaces_SkipsNumbersWithoutAnOpkgTunEntity(t *testing.T) {
	// Номер 10 занят анонимом (для пула это сирота), но ни записи NDMS
	// OpkgTun10, ни устройства opkgtun10 нет — есть только awgm10.
	live := opkgtun.Source{Name: "живые интерфейсы", Read: func(context.Context) (opkgtun.Taken, error) {
		return opkgtun.Taken{10: opkgtun.LiveHolder(10)}, nil
	}}
	pool := opkgtun.NewPool(16, live)

	prev := kernelIfacePresent
	kernelIfacePresent = func(string) bool { return false }
	t.Cleanup(func() { kernelIfacePresent = prev })

	// ifaces == nil — записей NDMS нет вовсе.
	got, err := orphanIfaces(pool, nil)(context.Background())
	if err != nil {
		t.Fatalf("orphanIfaces() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("сироты = %+v, ждали пусто: за номером 10 нет сущности OpkgTun", got)
	}
}

// Обратная половина того же правила: устройство opkgtun10 в ядре ЕСТЬ, записи
// NDMS нет — это ровно тот случай, ради которого снос дотягивается до
// `ip link del`, и он обязан попасть в список.
func TestOrphanIfaces_KeepsKernelOnlyOpkgTun(t *testing.T) {
	live := opkgtun.Source{Name: "живые интерфейсы", Read: func(context.Context) (opkgtun.Taken, error) {
		return opkgtun.Taken{10: opkgtun.LiveHolder(10)}, nil
	}}
	pool := opkgtun.NewPool(16, live)

	prev := kernelIfacePresent
	kernelIfacePresent = func(name string) bool { return name == "opkgtun10" }
	t.Cleanup(func() { kernelIfacePresent = prev })

	got, err := orphanIfaces(pool, nil)(context.Background())
	if err != nil {
		t.Fatalf("orphanIfaces() error = %v", err)
	}
	if len(got) != 1 || got[0].Iface != "opkgtun10" {
		t.Fatalf("сироты = %+v, ждали opkgtun10", got)
	}
}
