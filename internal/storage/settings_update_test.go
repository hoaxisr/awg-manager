package storage

import (
	"errors"
	"testing"
)

// Ради чего всё: снимок, взятый вызывающим ДО узкой записи, затирал бы её.
// Update берёт копию под локом в момент коммита, поэтому запись узкого
// мутатора доживает до диска.
func TestUpdate_KeepsNarrowMutatorWrite(t *testing.T) {
	s := newLoadedStore(t)

	stale, err := s.Get() // вызывающий держит указатель с этого момента
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stale.OpkgTun != nil {
		t.Fatal("непустая запись владения в свежем сторе")
	}

	if err := s.SetOpkgTunState(&OpkgTunState{Mode: "policytun", Index: 7}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}

	if err := s.Update(func(cur *Settings) error {
		cur.SingboxRouter.Enabled = true
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	live, err := s.Get()
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if live.OpkgTun == nil || live.OpkgTun.Index != 7 {
		t.Fatal("Update затёр запись узкого мутатора")
	}
	if !live.SingboxRouter.Enabled {
		t.Fatal("Update не записал своё поле")
	}

	// Перечитать с диска: публикация в кэш и запись файла обязаны совпадать.
	fresh := NewSettingsStore(s.DataDir())
	onDisk, err := fresh.Load()
	if err != nil {
		t.Fatalf("Load from disk: %v", err)
	}
	if onDisk.OpkgTun == nil || onDisk.OpkgTun.Index != 7 || !onDisk.SingboxRouter.Enabled {
		t.Fatal("на диске не оба изменения")
	}
}

// Публикуется копия: объект, который вызывающий получил до Update, не
// меняется под ним.
func TestUpdate_PublishesCopy(t *testing.T) {
	s := newLoadedStore(t)
	before, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := s.Update(func(cur *Settings) error {
		cur.ApiKey = "new"
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if before.ApiKey == "new" {
		t.Fatal("Update правит живой объект по месту, а не публикует копию")
	}
	after, _ := s.Get()
	if after.ApiKey != "new" {
		t.Fatal("копия не опубликована в кэш")
	}
}

// Ошибка мутатора отменяет запись целиком — на диск и в кэш не уходит ничего.
func TestUpdate_MutatorErrorSkipsSave(t *testing.T) {
	s := newLoadedStore(t)
	boom := errors.New("boom")
	if err := s.Update(func(cur *Settings) error {
		cur.ApiKey = "half-written"
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("ожидалась ошибка мутатора, получено: %v", err)
	}
	live, _ := s.Get()
	if live.ApiKey == "half-written" {
		t.Fatal("отменённая мутация уехала в кэш")
	}
	fresh := NewSettingsStore(s.DataDir())
	onDisk, err := fresh.Load()
	if err != nil {
		t.Fatalf("Load from disk: %v", err)
	}
	if onDisk.ApiKey == "half-written" {
		t.Fatal("отменённая мутация уехала на диск")
	}
}

// Под -race: Update, узкий мутатор и Snapshot одновременно.
func TestUpdate_NoRaceWithNarrowMutatorAndSnapshot(t *testing.T) {
	s := newLoadedStore(t)
	if err := s.SetOpkgTunState(&OpkgTunState{Mode: "fakeip", Index: 3}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	writers := make(chan struct{}, 2)
	go func() {
		defer func() { writers <- struct{}{} }()
		for i := 0; i < 200; i++ {
			_ = s.SetOpkgTunState(&OpkgTunState{Mode: "fakeip", Index: i})
		}
	}()
	go func() {
		defer func() { writers <- struct{}{} }()
		for i := 0; i < 200; i++ {
			_ = s.Update(func(cur *Settings) error {
				cur.SingboxRouter.Enabled = i%2 == 0
				return nil
			})
		}
	}()
	for i := 0; i < 400; i++ {
		if _, err := s.Snapshot(); err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
	}
	<-writers
	<-writers
}
