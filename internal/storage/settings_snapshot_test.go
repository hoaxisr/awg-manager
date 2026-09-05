package storage

import (
	"fmt"
	"testing"
)

func newLoadedStore(t *testing.T) *SettingsStore {
	t.Helper()
	s := NewSettingsStore(t.TempDir())
	if _, err := s.Load(); err != nil {
		t.Fatalf("seed Load: %v", err)
	}
	return s
}

// Красный до фикса под -race: GetServerPeerSecret читает живую map без
// лока, SetServerPeerSecret пишет в неё же под локом — data race, а без
// -race это тот самый fatal concurrent map read and map write.
func TestGetServerPeerSecret_NoRaceWithWrites(t *testing.T) {
	s := newLoadedStore(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			_ = s.SetServerPeerSecret("Wireguard0", fmt.Sprintf("pk%d", i), ServerPeerSecret{PrivateKey: "x"})
		}
	}()
	for i := 0; i < 200000; i++ {
		s.GetServerPeerSecret("Wireguard0", "pk0")
		s.GetServerInterfaceMeta("Wireguard0")
	}
	<-done
}

// Snapshot — независимая копия: правка снапшота не видна кэшу.
// (Красный компиляционно: метода ещё нет.)
func TestSnapshot_IndependentCopy(t *testing.T) {
	s := newLoadedStore(t)
	if err := s.SetServerPeerSecret("Wireguard0", "pk", ServerPeerSecret{PrivateKey: "x"}); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	snap.ApiKey = "mutated"
	delete(snap.ServerPeerSecrets, "Wireguard0")
	live, _ := s.Get()
	if live.ApiKey == "mutated" {
		t.Fatal("снапшот разделяет скаляры с кэшем")
	}
	if _, ok := s.GetServerPeerSecret("Wireguard0", "pk"); !ok {
		t.Fatal("снапшот разделяет map'ы с кэшем")
	}
}

// RT19: узкий мутатор публикует НОВУЮ запись, а живую не трогает.
//
// Это и есть настоящая защита от «concurrent map read and map write», из-за
// которой в api появились Snapshot-вызовы: раз розданный указатель неизменен,
// маршалить его безопасно. Пин детерминированный — не зависит от того, поймал
// ли race-детектор окно: мутация «писать в живую map на месте» делает его
// красным и без `-race`.
//
// Тесты `internal/api/settings_race_test.go` (снесённые вместе с переносом
// инварианта сюда) эту защиту уже не пиновали ничем: подмена `Snapshot()` на `Get()` в handler'е
// проходила зелёной даже под `-race` — гонки там больше нет по построению.
func TestSetServerPeerSecret_PublishesCopyNotInPlace(t *testing.T) {
	s := newLoadedStore(t)
	if err := s.SetServerPeerSecret("Wireguard0", "pk1", ServerPeerSecret{PrivateKey: "a"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	handed, err := s.Get() // ровно то, что держит handler, пока маршалит ответ
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	before := len(handed.ServerPeerSecrets["Wireguard0"])

	if err := s.SetServerPeerSecret("Wireguard0", "pk2", ServerPeerSecret{PrivateKey: "b"}); err != nil {
		t.Fatalf("вторая запись: %v", err)
	}
	if got := len(handed.ServerPeerSecrets["Wireguard0"]); got != before {
		t.Fatalf("розданный кэш изменили на месте: было %d записей, стало %d", before, got)
	}
	fresh, err := s.Get()
	if err != nil {
		t.Fatalf("Get после записи: %v", err)
	}
	if _, ok := fresh.ServerPeerSecrets["Wireguard0"]["pk2"]; !ok {
		t.Fatal("новая запись не опубликована: свежий Get её не видит")
	}
}

// Вторая половина того же инварианта: узкий мутатор ApiKey тоже публикует
// НОВУЮ запись, а не правит розданную. Найдено ревью: перенос инварианта был
// сделан только для ServerPeerSecret, и мутация «писать ApiKey на месте»
// проходила зелёной во всём наборе, включая `-race`.
func TestSetApiKey_PublishesCopyNotInPlace(t *testing.T) {
	s := newLoadedStore(t)
	if err := s.SetApiKey("первый"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	handed, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := s.SetApiKey("второй"); err != nil {
		t.Fatalf("вторая запись: %v", err)
	}
	if handed.ApiKey != "первый" {
		t.Fatalf("розданный кэш изменили на месте: ApiKey стал %q", handed.ApiKey)
	}
	fresh, err := s.Get()
	if err != nil {
		t.Fatalf("Get после записи: %v", err)
	}
	if fresh.ApiKey != "второй" {
		t.Fatalf("новый ключ не опубликован: %q", fresh.ApiKey)
	}
}
