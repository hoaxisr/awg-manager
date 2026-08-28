package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/manager"
)

// fakeProxyRuntime — менеджер, у которого посев отказывает заданное число раз.
type fakeProxyRuntime struct {
	failures int
	boots    int
	booted   bool
	posted   []proxyrt.EventKind
}

func (f *fakeProxyRuntime) SeedInfo() manager.SeedInfo {
	return manager.SeedInfo{Booted: f.booted}
}

func (f *fakeProxyRuntime) Boot(context.Context) error {
	f.boots++
	if f.boots <= f.failures {
		return errors.New("RCI недоступен")
	}
	f.booted = true
	return nil
}

func (f *fakeProxyRuntime) PostAll(k proxyrt.EventKind) { f.posted = append(f.posted, k) }

// Н1: на ХОЛОДНОМ старте роутера RCI ещё мёртв, посев падает fail-closed и
// инстансы не поднимаются вовсе. Без повторной попытки рантайм так и остаётся
// не поднятым до перезапуска демона, а ведомость INPUT-портов через две минуты
// закрывает порты переживших процессов.
func TestProxyNudgeRetriesBootUntilSeeded(t *testing.T) {
	mgr := &fakeProxyRuntime{failures: 1}
	ctx := context.Background()

	if booted, err := proxyNudge(ctx, mgr, proxyrt.EventBoot); err == nil || booted {
		t.Fatalf("первая попытка обязана вернуть отказ посева: booted=%v err=%v", booted, err)
	}
	if mgr.boots != 1 {
		t.Fatalf("Boot вызван %d раз, ожидали 1", mgr.boots)
	}

	booted, err := proxyNudge(ctx, mgr, proxyrt.EventBoot)
	if err != nil {
		t.Fatalf("повторная попытка: %v", err)
	}
	if !booted {
		t.Fatal("повторная попытка обязана сообщить о поднятом рантайме")
	}
	if mgr.boots != 2 {
		t.Fatalf("Boot вызван %d раз, ожидали 2 — ретрая нет", mgr.boots)
	}
	if len(mgr.posted) != 0 {
		t.Fatalf("до успешного посева будить некого: %v", mgr.posted)
	}
}

// После состоявшегося посева повторять боот незачем: достаточно разбудить
// воркеров, причём ИМЕННО тем событием, которое пришло (роль читает его вид —
// wan-up и boot ведут к разным решениям).
func TestProxyNudgeWakesWorkersOnceBooted(t *testing.T) {
	mgr := &fakeProxyRuntime{booted: true}

	booted, err := proxyNudge(context.Background(), mgr, proxyrt.EventWANUp)
	if err != nil {
		t.Fatal(err)
	}
	if booted {
		t.Fatal("рантайм уже был поднят — повторный подъём сообщать нечем")
	}
	if mgr.boots != 0 {
		t.Fatalf("поднятый рантайм не пересеивают: Boot вызван %d раз", mgr.boots)
	}
	if len(mgr.posted) != 1 || mgr.posted[0] != proxyrt.EventWANUp {
		t.Fatalf("воркеров разбудили не тем событием: %v", mgr.posted)
	}
}

// Конструктор ведомости НЕ заводит окно: гейт прохода читает ссылку на
// менеджера, а её проставляют позже конструктора. Будильник, заведённый
// раньше записи, читал бы поле из своей горутины без синхронизации —
// формальная гонка. Страж структурный: вернуть завод в конструктор он не даст.
func TestProxyFWBookConstructorDoesNotArmGrace(t *testing.T) {
	armed := 0
	prev := proxyFWAfterFunc
	defer func() { proxyFWAfterFunc = prev }()
	proxyFWAfterFunc = func(time.Duration, func()) *time.Timer {
		armed++
		return nil
	}

	b := newProxyFWBook([]string{"wdtt-server:s1"}, func() bool { return true })
	if armed != 0 {
		t.Fatalf("конструктор завёл окно (%d раз) — будильник увидел бы недописанную ссылку", armed)
	}
	b.armGrace()
	if armed != 1 {
		t.Fatalf("armGrace завёл окно %d раз, ждали 1", armed)
	}
}

// Н1, вторая половина: до состоявшегося посева проход окна ожидания обязан
// быть отложен. Пустая ведомость в этот момент означает не «серверов нет», а
// «объявить их некому», и приведение к пустому объединению закрыло бы
// INPUT-порты процессов, переживших перезапуск демона.
func TestProxyFWBookGraceWaitsForSeed(t *testing.T) {
	fw := &fakeListenFW{live: liveSpecs(spec(56000, "udp"))}

	// Перехват будильника держится ВЕСЬ тест: перезавод окна внутри graceOver
	// иначе завёл бы настоящий двухминутный таймер, и тот выстрелил бы по
	// модели уже после конца теста.
	var fire func()
	rearmed := 0
	prev := proxyFWAfterFunc
	defer func() { proxyFWAfterFunc = prev }()
	proxyFWAfterFunc = func(d time.Duration, f func()) *time.Timer {
		if d != proxyFWGrace {
			t.Fatalf("будильник заведён на %v, а не на окно ожидания", d)
		}
		if fire != nil {
			rearmed++
		}
		fire = f
		return nil
	}
	booted := false
	b := newProxyFWBook([]string{"wdtt-server:s1"}, func() bool { return booted })
	b.armGrace()
	b.list = fw.list
	b.apply = fw.reconcile

	fire()
	if len(fw.applied) != 0 {
		t.Fatalf("до посева проход окна состоялся и закрыл порты: %v", fw.applied)
	}
	if rearmed != 1 {
		t.Fatalf("окно не перезаведено (%d): вычистка ничьих портов не состоится никогда", rearmed)
	}

	booted = true
	fire()
	if len(fw.applied) != 1 {
		t.Fatalf("после посева проход окна обязан состояться: %v", fw.applied)
	}
	if got := fw.lastApplied(); len(got) != 0 {
		t.Fatalf("порты мёртвого поколения не вычищены: %v", got)
	}
}
