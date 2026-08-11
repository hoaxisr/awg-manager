package proxysup

import (
	"testing"
	"time"
)

func TestBackoffGrowsAndResets(t *testing.T) {
	b := NewBackoff(30*time.Second, 15*time.Minute)
	now := time.Unix(1000, 0)

	if !b.Allow("a", now) {
		t.Fatal("первая попытка должна быть разрешена")
	}
	b.Fail("a", now)
	if b.Allow("a", now.Add(29*time.Second)) {
		t.Fatal("до истечения base повтор запрещён")
	}
	if !b.Allow("a", now.Add(30*time.Second)) {
		t.Fatal("после base повтор разрешён")
	}

	// Вторая неудача — окно вдвое шире.
	b.Fail("a", now)
	if b.Allow("a", now.Add(59*time.Second)) {
		t.Fatal("после второй неудачи окно должно быть 60 с")
	}

	b.Success("a")
	if !b.Allow("a", now) {
		t.Fatal("успех сбрасывает счётчик")
	}
}

func TestBackoffCap(t *testing.T) {
	b := NewBackoff(30*time.Second, 2*time.Minute)
	now := time.Unix(1000, 0)
	for i := 0; i < 20; i++ {
		b.Fail("a", now)
	}
	if b.Allow("a", now.Add(2*time.Minute-time.Second)) {
		t.Fatal("задержка не должна опускаться ниже потолка")
	}
	if !b.Allow("a", now.Add(2*time.Minute)) {
		t.Fatal("задержка не должна превышать потолок")
	}
}

func TestBackoffIndependentIDs(t *testing.T) {
	b := NewBackoff(30*time.Second, time.Minute)
	now := time.Unix(1000, 0)
	b.Fail("a", now)
	if !b.Allow("b", now) {
		t.Fatal("состояние не должно протекать между id")
	}
}
