package opkgtun

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

// src — источник-подстановка со счётчиком чтений: «ноль заявок не трогает
// источники» иначе не проверить.
type src struct {
	name  string
	take  Taken
	err   error
	reads int
}

func (s *src) source() Source {
	return Source{Name: s.name, Read: func(context.Context) (Taken, error) {
		s.reads++
		return s.take, s.err
	}}
}

func poolOf(t *testing.T, ceiling int, take Taken) *Pool {
	t.Helper()
	s := &src{name: "тест", take: take}
	return NewPool(ceiling, s.source())
}

func TestDigits(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"0", 0, true},
		{"7", 7, true},
		{"007", 7, true}, // ведущие нули — тот же номер: имя строится по числу
		{"49", 49, true},
		{"", 0, false},
		{"+5", 0, false}, // Atoi принял бы
		{"-5", 0, false}, // Atoi принял бы — и уехал бы в занятость номером −5
		{"٥", 0, false},  // unicode.IsDigit принял бы, Atoi — нет
		{"5m", 0, false},
		{"m5", 0, false},
		{"99999999999999999999", 0, false}, // переполнение
	}
	for _, c := range cases {
		got, ok := Digits(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("Digits(%q) = %d, %v; want %d, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestIndexOf(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"OpkgTun12", 12, true},
		{"opkgtun12", 12, true}, // ядро пишет строчными
		{"OpkgTun0", 0, true},
		{"OpkgTun+5", 0, false},
		{"OpkgTun", 0, false},
		{"Wireguard0", 0, false},
		{"awg12", 0, false},
	}
	for _, c := range cases {
		got, ok := IndexOf(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("IndexOf(%q) = %d, %v; want %d, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// Инвентарь конструкторов: ключ и строка для человека — то, ради чего тип
// существует, и разъехаться им незачем.
func TestHolderConstructors(t *testing.T) {
	cases := []struct {
		name string
		h    Holder
		key  string
		text string
	}{
		{"туннель", TunnelHolder("awg10", "дом"), "awg10", "туннель «дом»"},
		{"туннель без имени", TunnelHolder("awg10", ""), "awg10", "туннель awg10"},
		{"новичок", TunnelHolder("", "дом"), "", "туннель «дом»"},
		{"системный", SystemTunnelHolder("awg20", "сервер"), "awg20", "системный туннель «сервер»"},
		{"прокси половина", ProxyHolder("wdtt:vk", "raw", "vk"), "wdtt:vk/raw", "прокси vk"},
		{"прокси без поля", ProxyHolder("wdtt:vk", "", ""), "wdtt:vk", "прокси wdtt:vk"},
		{"режим роутера", RouterModeHolder("fakeip"), "router-mode", "режим роутера fakeip"},
		{"аноним", AnonHolder("живой интерфейс opkgtun12"), "", "живой интерфейс opkgtun12"},
	}
	for _, c := range cases {
		if c.h.k != c.key {
			t.Errorf("%s: ключ = %q, want %q", c.name, c.h.k, c.key)
		}
		if got := c.h.String(); got != c.text {
			t.Errorf("%s: строка = %q, want %q", c.name, got, c.text)
		}
	}
}

// Пустое поле даёт ГОЛЫЙ ключ записи, а не "ключ/": под голым ключом инстанс
// освобождает пины, и "wdtt:vk/" не совпал бы с ним молча.
func TestProxyHolder_EmptyFieldKeepsBareKey(t *testing.T) {
	if got := ProxyHolder("wdtt:vk", "", "").k; got != "wdtt:vk" {
		t.Fatalf("ключ = %q, want wdtt:vk", got)
	}
}

func TestHolder_SameOwner_EmptyKeyMatchesNothing(t *testing.T) {
	a, b := AnonHolder("живой opkgtun1"), AnonHolder("запись NDMS X")
	if a.sameOwner(b) || a.sameOwner(a) {
		t.Fatal("безключевые держатели не должны совпадать, в том числе сами с собой")
	}
	own := TunnelHolder("awg10", "дом")
	if !own.sameOwner(TunnelHolder("awg10", "переименован")) {
		t.Fatal("один идентификатор — один владелец, имя не при чём")
	}
}

// mergeAll: заявленный НАЗЫВАЕТ номер в оба порядка прихода, но анонима не
// вытесняет; два заявленных дают конфликт, свой дубль — нет.
func TestMergeAll_Permutations(t *testing.T) {
	claimed := TunnelHolder("awg10", "дом")
	anon := AnonHolder("живой интерфейс opkgtun10")
	other := ProxyHolder("wdtt:vk", "raw", "vk")

	name := func(o occupancy) Holder {
		t.Helper()
		h, busy := o.holder(10)
		if !busy {
			t.Fatal("номер 10 свободен")
		}
		return h
	}

	t.Run("аноним пришёл первым", func(t *testing.T) {
		got, cf := mergeAll([]Taken{{10: anon}, {10: claimed}})
		if name(got) != claimed || len(cf) != 0 {
			t.Fatalf("держатель = %v, конфликты = %v", name(got), cf)
		}
	})
	t.Run("заявленный пришёл первым", func(t *testing.T) {
		got, cf := mergeAll([]Taken{{10: claimed}, {10: anon}})
		if name(got) != claimed || len(cf) != 0 {
			t.Fatalf("держатель = %v, конфликты = %v", name(got), cf)
		}
	})
	t.Run("двое заявленных — конфликт, первый называет номер", func(t *testing.T) {
		got, cf := mergeAll([]Taken{{10: claimed}, {10: other}})
		if name(got) != claimed {
			t.Fatalf("номер называет %v, ждали первого", name(got))
		}
		if len(cf) != 1 || cf[0].Index != 10 || cf[0].A != claimed || cf[0].B != other {
			t.Fatalf("конфликты = %v", cf)
		}
	})
	t.Run("свой дубль — не конфликт", func(t *testing.T) {
		_, cf := mergeAll([]Taken{{10: claimed}, {10: TunnelHolder("awg10", "дом")}})
		if len(cf) != 0 {
			t.Fatalf("конфликты = %v", cf)
		}
	})
	// Несущее свойство: заявленный забирает у анонима ИМЯ номера, но не место в
	// занятости. Потеряйся аноним — строгий пин просителя, чья запись лежит на
	// том же номере, перестал бы отказывать (см. occupancy).
	t.Run("аноним переживает заявленного", func(t *testing.T) {
		got, _ := mergeAll([]Taken{{10: claimed}, {10: anon}})
		if len(got[10]) != 2 {
			t.Fatalf("держателей номера 10: %d, ждали обоих", len(got[10]))
		}
	})
}

// Строку держателя живого интерфейса видит пользователь в совете «освободите
// номер», и собирает её ОДНА функция — иначе поставщики разойдутся
// формулировками. Закрепляется побайтово: смена текста меняет то, что человек
// читает при исчерпании пула.
func TestLiveHolderText(t *testing.T) {
	h := LiveHolder(12)
	if got := h.String(); got != "живой интерфейс opkgtun12" {
		t.Errorf("держатель = %q", got)
	}
	if !h.anonymous() {
		t.Error("живое устройство обязано быть безключевым: оно знает только номер")
	}
}
