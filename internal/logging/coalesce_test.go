package logging

import (
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
)

func testEntry(msg string, ts time.Time) LogEntry {
	return LogEntry{
		Timestamp: ts,
		Level:     "warn",
		Group:     GroupTunnel,
		Subgroup:  SubConnectivity,
		Action:    "http-check",
		Target:    "awg10",
		Message:   msg,
	}
}

func TestCoalesce_FoldsIdenticalRepeat(t *testing.T) {
	lb := NewLogBuffer(BucketApp)
	defer lb.Stop()

	base := time.Now()
	lb.Add(testEntry("fail", base))

	updated, ok := lb.CoalesceOrAdd(testEntry("fail", base.Add(30*time.Second)), 5*time.Minute)
	if !ok {
		t.Fatal("expected repeat to coalesce")
	}
	if updated.Repeats != 1 {
		t.Fatalf("Repeats = %d, want 1", updated.Repeats)
	}
	if updated.LastSeen == nil || !updated.LastSeen.Equal(base.Add(30*time.Second)) {
		t.Fatalf("LastSeen = %v, want %v", updated.LastSeen, base.Add(30*time.Second))
	}
	if !updated.Timestamp.Equal(base) {
		t.Fatalf("Timestamp must stay first-seen: %v != %v", updated.Timestamp, base)
	}
	if lb.Len() != 1 {
		t.Fatalf("Len = %d, want 1", lb.Len())
	}
}

func TestCoalesce_LastSeenExtendsWindow(t *testing.T) {
	lb := NewLogBuffer(BucketApp)
	defer lb.Stop()

	base := time.Now()
	lb.Add(testEntry("fail", base))

	// Повторы каждые 4 минуты при окне 5 минут: каждый следующий попадает
	// в окно от LastSeen предыдущего, хотя от Timestamp уже далеко.
	for i := 1; i <= 3; i++ {
		ts := base.Add(time.Duration(i) * 4 * time.Minute)
		updated, ok := lb.CoalesceOrAdd(testEntry("fail", ts), 5*time.Minute)
		if !ok {
			t.Fatalf("repeat %d must coalesce (window extends via LastSeen)", i)
		}
		if updated.Repeats != i {
			t.Fatalf("repeat %d: Repeats = %d", i, updated.Repeats)
		}
	}
	if lb.Len() != 1 {
		t.Fatalf("Len = %d, want 1", lb.Len())
	}
}

func TestCoalesce_ExpiredWindowStartsFresh(t *testing.T) {
	lb := NewLogBuffer(BucketApp)
	defer lb.Stop()

	base := time.Now()
	lb.Add(testEntry("fail", base))

	if _, ok := lb.CoalesceOrAdd(testEntry("fail", base.Add(6*time.Minute)), 5*time.Minute); ok {
		t.Fatal("repeat outside window must NOT coalesce")
	}
	if lb.Len() != 2 {
		t.Fatalf("expired repeat must be added as a fresh entry: Len = %d, want 2", lb.Len())
	}
}

func TestCoalesce_DifferentFieldsDoNotFold(t *testing.T) {
	lb := NewLogBuffer(BucketApp)
	defer lb.Stop()

	base := time.Now()
	lb.Add(testEntry("fail", base))

	other := testEntry("fail", base.Add(time.Second))
	other.Target = "awg11"
	if _, ok := lb.CoalesceOrAdd(other, 5*time.Minute); ok {
		t.Fatal("different target must not coalesce")
	}
	other = testEntry("different message", base.Add(time.Second))
	if _, ok := lb.CoalesceOrAdd(other, 5*time.Minute); ok {
		t.Fatal("different message must not coalesce")
	}
	other = testEntry("fail", base.Add(time.Second))
	other.Level = "error"
	if _, ok := lb.CoalesceOrAdd(other, 5*time.Minute); ok {
		t.Fatal("different level must not coalesce")
	}
	if lb.Len() != 4 {
		t.Fatalf("non-matching entries must be added: Len = %d, want 4", lb.Len())
	}
}

func TestCoalesce_InterleavedEntriesStillFold(t *testing.T) {
	lb := NewLogBuffer(BucketApp)
	defer lb.Stop()

	base := time.Now()
	lb.Add(testEntry("fail A", base))
	lb.Add(testEntry("fail B", base.Add(time.Second)))

	// Повтор A находит свою запись сквозь более новую B.
	updated, ok := lb.CoalesceOrAdd(testEntry("fail A", base.Add(2*time.Second)), 5*time.Minute)
	if !ok || updated.Message != "fail A" || updated.Repeats != 1 {
		t.Fatalf("interleaved repeat must coalesce into its own entry: ok=%v entry=%+v", ok, updated)
	}
	if lb.Len() != 2 {
		t.Fatalf("Len = %d, want 2", lb.Len())
	}
}

func TestServiceAppLog_CoalescesRepeats(t *testing.T) {
	s := NewService(&mockSettings{enabled: true, maxAge: 2, logLevel: "info", appMaxEntries: 100, sbMaxEntries: 100})
	defer s.Stop()

	for i := 0; i < 5; i++ {
		s.AppLog(LevelWarn, GroupTunnel, SubConnectivity, "http-check", "awg10", "HTTP check failed: timeout")
	}
	entries, total := s.GetLogs(BucketApp, "", "", "", time.Time{}, 10, 0)
	if total != 1 || len(entries) != 1 {
		t.Fatalf("total = %d, len = %d, want 1", total, len(entries))
	}
	if entries[0].Repeats != 4 {
		t.Fatalf("Repeats = %d, want 4", entries[0].Repeats)
	}
	if entries[0].LastSeen == nil {
		t.Fatal("LastSeen must be set on coalesced entry")
	}

	// Другая запись не сворачивается и не мешает следующему повтору свернуться.
	s.AppLog(LevelInfo, GroupTunnel, SubLifecycle, "start", "awg11", "Tunnel started")
	s.AppLog(LevelWarn, GroupTunnel, SubConnectivity, "http-check", "awg10", "HTTP check failed: timeout")
	_, total = s.GetLogs(BucketApp, "", "", "", time.Time{}, 10, 0)
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
}

// SSE-событие и REST-DTO должны выдавать побайтно одинаковую строку
// timestamp — клиент сопоставляет повторы с загруженными строками по
// составному ключу (см. frontend logs store keyOf).
func TestServiceAppLog_SSETimestampMatchesRESTFormat(t *testing.T) {
	s := NewService(&mockSettings{enabled: true, maxAge: 2, logLevel: "info", appMaxEntries: 100, sbMaxEntries: 100})
	defer s.Stop()
	bus := events.NewBus()
	s.SetEventBus(bus)
	// Клиентская подписка: она изображает открытую панель, а публикация
	// log:entry теперь идёт только при живом зрителе.
	_, ch, unsub := bus.SubscribeClient()
	defer unsub()

	s.AppLog(LevelWarn, GroupTunnel, SubConnectivity, "http-check", "awg10", "fail")
	s.AppLog(LevelWarn, GroupTunnel, SubConnectivity, "http-check", "awg10", "fail") // повтор

	entries, _ := s.GetLogs(BucketApp, "", "", "", time.Time{}, 1, 0)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	wantTS := entries[0].Timestamp.UTC().Format(time.RFC3339Nano)
	wantLastSeen := entries[0].LastSeen.UTC().Format(time.RFC3339Nano)

	var got []events.LogEntryEvent
	for i := 0; i < 2; i++ {
		select {
		case ev := <-ch:
			got = append(got, ev.Data.(events.LogEntryEvent))
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for log:entry event")
		}
	}
	if got[0].Timestamp != wantTS || got[1].Timestamp != wantTS {
		t.Fatalf("SSE timestamps %q/%q != REST format %q", got[0].Timestamp, got[1].Timestamp, wantTS)
	}
	if got[1].Repeats != 1 || got[1].LastSeen != wantLastSeen {
		t.Fatalf("repeat event: repeats=%d lastSeen=%q, want 1/%q", got[1].Repeats, got[1].LastSeen, wantLastSeen)
	}
}

// Публикация log:entry идёт ТОЛЬКО при открытой панели. Цена не в самой
// отправке: событие строится с двумя форматированиями времени, а Publish идёт
// веерно ко всем подписчикам шины, включая пять вечных внутренних, — пять
// пробуждений горутин на строку журнала при нуле зрителей. Буфер при этом
// наполняется всегда: страница журнала забирает пропущенное ручкой /logs.
func TestAppLog_PublishesOnlyWhenWatched(t *testing.T) {
	s := NewService(&mockSettings{enabled: true, maxAge: 2, logLevel: "info", appMaxEntries: 100, sbMaxEntries: 100})
	defer s.Stop()
	bus := events.NewBus()
	s.SetEventBus(bus)

	// Только ВНУТРЕННЯЯ подписка: зрителей нет.
	_, ch, unsub := bus.Subscribe()
	defer unsub()

	s.AppLog(LevelWarn, GroupTunnel, SubConnectivity, "http-check", "awg10", "fail")

	select {
	case ev := <-ch:
		t.Fatalf("событие %q опубликовано при нуле открытых панелей", ev.Type)
	case <-time.After(150 * time.Millisecond):
	}

	// Буфер наполнен несмотря на отсутствие зрителей.
	if entries, _ := s.GetLogs(BucketApp, "", "", "", time.Time{}, 10, 0); len(entries) != 1 {
		t.Fatalf("записей в буфере %d, ожидалась 1: буфер наполняется всегда", len(entries))
	}

	// Появился зритель — публикация возобновляется.
	_, clientCh, unsubClient := bus.SubscribeClient()
	defer unsubClient()
	s.AppLog(LevelWarn, GroupTunnel, SubConnectivity, "http-check", "awg11", "fail")

	select {
	case <-clientCh:
	case <-time.After(time.Second):
		t.Fatal("с открытой панелью событие не пришло")
	}
}

// Отсев поставщика (Visible) и отсев записи (AppLog) обязаны совпадать на всех
// уровнях. Разойдясь, они дадут худший исход: строку, отброшенную поставщиком
// до разбора, но нужную пользователю, — и в журнале её просто не будет.
func TestVisibleAgreesWithAppLog(t *testing.T) {
	levels := []Level{LevelError, LevelWarn, LevelInfo, LevelFull, LevelDebug}
	for _, configured := range levels {
		s := NewService(&mockSettings{enabled: true, maxAge: 2, logLevel: string(configured),
			appMaxEntries: 100, sbMaxEntries: 100})
		for _, entry := range levels {
			s.Clear(BucketApp)
			s.AppLog(entry, GroupTunnel, SubConnectivity, "act", "tgt", "msg")
			_, written := s.GetLogs(BucketApp, "", "", "", time.Time{}, 10, 0)

			if got := s.Visible(entry); got != (written > 0) {
				t.Errorf("порог %q, запись %q: Visible=%v, а записалось %d — проверки разошлись",
					configured, entry, got, written)
			}
		}
		s.Stop()
	}
}

// Журнал выключен целиком — не пишем и не разрешаем поставщику готовить запись.
func TestVisibleFalseWhenLoggingDisabled(t *testing.T) {
	s := NewService(&mockSettings{enabled: false, maxAge: 2, logLevel: "debug",
		appMaxEntries: 100, sbMaxEntries: 100})
	defer s.Stop()

	if s.Visible(LevelError) {
		t.Error("Visible=true при выключенном журнале")
	}
	s.AppLog(LevelError, GroupTunnel, SubConnectivity, "act", "tgt", "msg")
	if _, n := s.GetLogs(BucketApp, "", "", "", time.Time{}, 10, 0); n != 0 {
		t.Errorf("записей %d при выключенном журнале", n)
	}
}
