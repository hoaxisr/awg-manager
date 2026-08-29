package storage

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// seedTunnel кладёт запись через Create — сидинг фикстуры, не предмет пина.
func seedTunnel(t *testing.T, s *AWGTunnelStore, tun *AWGTunnel) {
	t.Helper()
	if err := s.Create(tun); err != nil {
		t.Fatalf("seed Create: %v", err)
	}
}

func readRaw(t *testing.T, dir, id string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatalf("read %s.json: %v", id, err)
	}
	return data
}

// П1. Две параллельные правки РАЗНЫХ полей одной записи обязаны сойтись на
// диске обе. Прежде вызывающий читал запись сам, вне лока, и писал снимок
// через Save: правка, приехавшая, пока он работал со снимком (секунды RCI),
// затиралась — тот самый lost update, ради которого заведена транзакция.
//
// Конструкция детерминирована взаимоисключением, а не таймингом: A паркуется
// ВНУТРИ мутатора, держа dir-lock; B стартует только после этого. На
// исправленном коде B заперт локом, poll просто истекает, обе правки ложатся
// последовательно. На дефектном (чтение снимка ДО захвата) B успевает
// записаться за миллисекунды, poll это видит, и A затирает его снимком.
func TestAWGStoreUpdate_ConcurrentEditsDoNotLoseUpdates(t *testing.T) {
	store, dir := newTestAWGStore(t)
	seedTunnel(t, store, &AWGTunnel{ID: "awg10", Name: "old"})

	entered := make(chan struct{})
	release := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := store.Update("awg10", func(tun *AWGTunnel) error {
			close(entered)
			<-release
			tun.Name = "edited-by-A"
			return nil
		}); err != nil {
			t.Errorf("Update A: %v", err)
		}
	}()

	<-entered
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := store.Update("awg10", func(tun *AWGTunnel) error {
			tun.Interface.DNS = "9.9.9.9"
			return nil
		}); err != nil {
			t.Errorf("Update B: %v", err)
		}
	}()

	// Ждём появления правки B на диске, пока A запаркован. На исправленном
	// коде она появиться не может — 300 мс это верхняя граница ожидания, а не
	// гарантия чего-либо.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(filepath.Join(dir, "awg10.json")); err == nil &&
			bytes.Contains(data, []byte("9.9.9.9")) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	wg.Wait()

	got, err := store.Get("awg10")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "edited-by-A" {
		t.Errorf("Name = %q, want edited-by-A (правка A потеряна)", got.Name)
	}
	if got.Interface.DNS != "9.9.9.9" {
		t.Errorf("Interface.DNS = %q, want 9.9.9.9 (правка B потеряна)", got.Interface.DNS)
	}
}

// П2. Create на занятый ID отказывает и НЕ трогает байты существующей записи:
// молчаливая перезапись унесла бы вместе с записью её ключи.
func TestAWGStoreCreate_RefusesExistingID(t *testing.T) {
	store, dir := newTestAWGStore(t)
	seedTunnel(t, store, &AWGTunnel{
		ID:        "awg10",
		Name:      "original",
		Interface: AWGInterface{PrivateKey: "SECRET-KEY"},
	})
	before := readRaw(t, dir, "awg10")

	err := store.Create(&AWGTunnel{ID: "awg10", Name: "intruder"})
	if err == nil {
		t.Fatal("Create на занятый ID вернул nil, want ошибку")
	}

	if after := readRaw(t, dir, "awg10"); !bytes.Equal(before, after) {
		t.Errorf("файл перезаписан отказавшим Create:\nbefore=%s\nafter=%s", before, after)
	}
}

// П2b. Пустой ID — отказ. Иначе на диск ложится файл ".json": List его не
// видит (нет имени), а удалить обычным путём нечем.
func TestAWGStoreCreate_RefusesEmptyID(t *testing.T) {
	store, dir := newTestAWGStore(t)

	if err := store.Create(&AWGTunnel{Name: "no-id"}); err == nil {
		t.Fatal("Create с пустым ID вернул nil, want ошибку")
	}

	if _, err := os.Stat(filepath.Join(dir, ".json")); err == nil {
		t.Error("Create с пустым ID записал файл \".json\"")
	}
}

// П3. Записи нет — Update отказывает с ErrNotFound и файла не создаёт. Отдать
// мутатору нулевую запись значило бы дать ему воскресить удалённый туннель
// дефолтами.
func TestAWGStoreUpdate_NotFound(t *testing.T) {
	store, dir := newTestAWGStore(t)

	called := false
	err := store.Update("awg10", func(tun *AWGTunnel) error {
		called = true
		tun.Name = "resurrected"
		return nil
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update err = %v, want ErrNotFound", err)
	}
	// Сентинел стора и доменный — ОДИН объект, а не одноимённые двойники:
	// слой, взявший не тот пакет, иначе получил бы молча ложное «не найдено»
	// (тексты совпадают, идентичность нет, компилятор не ловит).
	if !errors.Is(err, tunnel.ErrNotFound) {
		t.Fatalf("Update err = %v, не опознаётся как tunnel.ErrNotFound", err)
	}
	if called {
		t.Error("мутатор вызван на отсутствующей записи")
	}
	if _, err := os.Stat(filepath.Join(dir, "awg10.json")); err == nil {
		t.Error("Update создал файл отсутствующего туннеля")
	}
}

// П4. Битый JSON — отказ, байты нетронуты. Проглотить ошибку разбора значило
// бы затереть восстановимую запись (с ключами) дефолтами.
func TestAWGStoreUpdate_CorruptFailsClosed(t *testing.T) {
	store, dir := newTestAWGStore(t)
	seedTunnel(t, store, &AWGTunnel{ID: "awg10", Name: "original"})

	corrupt := []byte(`{"id": "awg10", "name": "orig`)
	if err := os.WriteFile(filepath.Join(dir, "awg10.json"), corrupt, 0644); err != nil {
		t.Fatal(err)
	}

	called := false
	err := store.Update("awg10", func(tun *AWGTunnel) error {
		called = true
		tun.Name = "clobbered"
		return nil
	})
	if err == nil {
		t.Fatal("Update на битом JSON вернул nil, want ошибку")
	}
	if called {
		t.Error("мутатор вызван на нечитаемой записи")
	}
	if after := readRaw(t, dir, "awg10"); !bytes.Equal(corrupt, after) {
		t.Errorf("битый файл переписан: %s", after)
	}
}

// П5. ErrNoChange от мутатора — «менять нечего»: nil наружу и НИ ОДНОГО байта
// на диск, даже если мутатор успел что-то присвоить своей копии.
func TestAWGStoreUpdate_ErrNoChangeSkipsWrite(t *testing.T) {
	store, dir := newTestAWGStore(t)
	seedTunnel(t, store, &AWGTunnel{ID: "awg10", Name: "original"})
	before := readRaw(t, dir, "awg10")

	if err := store.Update("awg10", func(tun *AWGTunnel) error {
		tun.Name = "must-not-persist"
		return ErrNoChange
	}); err != nil {
		t.Fatalf("Update err = %v, want nil на ErrNoChange", err)
	}

	if after := readRaw(t, dir, "awg10"); !bytes.Equal(before, after) {
		t.Errorf("файл переписан при ErrNoChange:\nbefore=%s\nafter=%s", before, after)
	}
}

// П6. Ошибка мутатора отменяет запись целиком: наружу ошибка, файл прежний.
func TestAWGStoreUpdate_MutatorErrorAbortsWrite(t *testing.T) {
	store, dir := newTestAWGStore(t)
	seedTunnel(t, store, &AWGTunnel{ID: "awg10", Name: "original"})
	before := readRaw(t, dir, "awg10")

	boom := errors.New("boom")
	err := store.Update("awg10", func(tun *AWGTunnel) error {
		tun.Name = "must-not-persist"
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Update err = %v, want boom", err)
	}

	if after := readRaw(t, dir, "awg10"); !bytes.Equal(before, after) {
		t.Errorf("файл переписан при ошибке мутатора:\nbefore=%s\nafter=%s", before, after)
	}
}
