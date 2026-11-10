package probeq

import (
	"fmt"
	"strings"
	"testing"
)

func take(t *testing.T, q *Queue, now int64) Task {
	t.Helper()
	task, ok := q.Next(now, true)
	if !ok {
		t.Fatalf("Next(%d) returned no task", now)
	}
	return task
}

func assertHost(t *testing.T, task Task, host string) {
	t.Helper()
	if task.Host != host {
		t.Fatalf("host = %q, want %q", task.Host, host)
	}
}

func assertAsk(t *testing.T, got, want Ask) {
	t.Helper()
	if got != want {
		t.Fatalf("ask = %d, want %d", got, want)
	}
}

func TestHourlyLimitPolicy(t *testing.T) {
	q := New()
	starts := StartsPerHour
	if starts == 0 {
		starts = 31
	}
	for i := 0; i < starts+1; i++ {
		assertAsk(t, q.Nominate(fmt.Sprintf("h%d.example", i), false, 1000), AskQueued)
	}
	for i := 0; i < starts; i++ {
		task := take(t, q, 1000+int64(i))
		q.Done(task.Host, 1000+int64(i))
	}
	if StartsPerHour == 0 {
		assertHost(t, take(t, q, 1100), fmt.Sprintf("h%d.example", starts))
		used, _ := q.Counts(1100)
		if used != 0 {
			t.Fatalf("used with disabled limit = %d", used)
		}
		return
	}
	if _, ok := q.Next(1100, true); ok {
		t.Fatal("probe ran after the hourly budget was spent")
	}
	if _, ok := q.Next(1000+3599, true); ok {
		t.Fatal("oldest start expired too early")
	}
	if _, ok := q.Next(1000+3600, true); !ok {
		t.Fatal("oldest start did not expire at 3600 seconds")
	}
}

func TestUserRequestJumpsQueue(t *testing.T) {
	q := New()
	q.Nominate("a.example", false, 10)
	q.Nominate("a.example", false, 10)
	q.Nominate("b.example", false, 10)
	assertAsk(t, q.Request("c.example", 10), AskQueued)
	assertAsk(t, q.Request("d.example", 11), AskQueued)
	assertHost(t, take(t, q, 20), "c.example")
	assertHost(t, take(t, q, 20), "d.example")
	assertHost(t, take(t, q, 20), "a.example")
	assertHost(t, take(t, q, 20), "b.example")
	if _, ok := q.Next(20, true); ok {
		t.Fatal("empty queue produced work")
	}
}

func TestEvictionRemovesOldestWatcherAndCallsMetadataOwner(t *testing.T) {
	q := New()
	var evicted []string
	q.SetEvictionHandler(func(host string) { evicted = append(evicted, host) })
	for i := 0; i < QueueCapacity+1; i++ {
		assertAsk(t, q.Nominate(fmt.Sprintf("h%d.example", i), false, 1), AskQueued)
	}
	if len(evicted) != 1 || evicted[0] != "h0.example" {
		t.Fatalf("evicted = %v, want h0.example", evicted)
	}
	_, queued := q.Counts(1)
	if queued != QueueCapacity {
		t.Fatalf("queued = %d, want %d", queued, QueueCapacity)
	}
	assertHost(t, take(t, q, 2), "h1.example")
}

func TestUserCanEvictWatcherButUserQueueIsBounded(t *testing.T) {
	q := New()
	for i := 0; i < QueueCapacity; i++ {
		q.Nominate(fmt.Sprintf("n%d.example", i), false, 1)
	}
	assertAsk(t, q.Request("user.example", 1), AskQueued)
	assertHost(t, take(t, q, 2), "user.example")

	q = New()
	for i := 0; i < QueueCapacity; i++ {
		assertAsk(t, q.Request(fmt.Sprintf("u%d.example", i), 1), AskQueued)
	}
	assertAsk(t, q.Request("more.example", 1), AskFull)
	assertAsk(t, q.Nominate("note.example", false, 1), AskFull)
}

func TestInFlightCoalescesAndDoneReleasesHost(t *testing.T) {
	q := New()
	q.Nominate("a.example", false, 1)
	assertHost(t, take(t, q, 1), "a.example")
	assertAsk(t, q.Nominate("a.example", false, 2), AskQueued)
	assertAsk(t, q.Request("a.example", 2), AskBusy)
	if _, ok := q.Next(3, true); ok {
		t.Fatal("in-flight host produced duplicate work")
	}
	q.Done("a.example", 4)
	assertAsk(t, q.Nominate("a.example", false, 5), AskQueued)
	assertHost(t, take(t, q, 5), "a.example")
}

func TestInFlightSlotExpiresAfterTwoMinutes(t *testing.T) {
	q := New()
	q.Nominate("a.example", false, 1000)
	take(t, q, 1000)
	if _, ok := q.Next(1119, true); ok {
		t.Fatal("live in-flight slot expired too early")
	}
	assertAsk(t, q.Nominate("a.example", false, 1120), AskQueued)
	assertHost(t, take(t, q, 1120), "a.example")
}

func TestEightInflightSlots(t *testing.T) {
	q := New()
	for i := 0; i < InflightCapacity+1; i++ {
		q.Nominate(fmt.Sprintf("h%d.example", i), false, 1000)
	}
	for i := 0; i < InflightCapacity; i++ {
		assertHost(t, take(t, q, 1000), fmt.Sprintf("h%d.example", i))
	}
	if _, ok := q.Next(1119, true); ok {
		t.Fatal("ninth probe ran with eight live slots")
	}
	used, queued := q.Counts(1119)
	wantUsed := InflightCapacity
	if StartsPerHour == 0 {
		wantUsed = 0
	}
	if used != wantUsed || queued != 1 {
		t.Fatalf("Counts = (%d, %d), want (%d, 1)", used, queued, wantUsed)
	}
	assertHost(t, take(t, q, 1120), "h8.example")
}

func TestHostSpacingPolicy(t *testing.T) {
	q := New()
	q.Nominate("a.example", false, 1000)
	take(t, q, 1000)
	q.Done("a.example", 1100)
	if HostSpacingSeconds == 0 {
		assertAsk(t, q.Request("a.example", 1101), AskQueued)
		return
	}
	assertAsk(t, q.Request("a.example", 1100+HostSpacingSeconds-1), AskBusy)
	assertAsk(t, q.Request("a.example", 1100+HostSpacingSeconds), AskQueued)
}

func TestNextWithoutNotesSkipsWatchers(t *testing.T) {
	q := New()
	q.Nominate("note.example", false, 1000)
	q.Request("user.example", 1000)
	assertHost(t, take(t, q, 1000), "user.example")
	// take enables notes; verify the actual gate with a fresh queue.
	q = New()
	q.Nominate("note.example", false, 1000)
	q.Request("user.example", 1000)
	if _, ok := q.Next(1000, false); !ok {
		t.Fatal("USER was not eligible with notes disabled")
	}
	if _, ok := q.Next(1000, false); ok {
		t.Fatal("watcher ran with notes disabled")
	}
	if _, ok := q.Next(1000, true); !ok {
		t.Fatal("watcher was not eligible with notes enabled")
	}
}

func TestNextHandsOutLatestNominationDomainFlag(t *testing.T) {
	q := New()
	q.Nominate("many.example", true, 1000)
	q.Request("many.example", 1000)
	task := take(t, q, 1000)
	if !task.ByDomain {
		t.Fatal("USER upgrade lost watcher domain flag")
	}
	q.Nominate("a.two.example", true, 1000)
	q.Nominate("a.two.example", false, 1001)
	task = take(t, q, 1001)
	if task.ByDomain {
		t.Fatal("latest nomination did not replace domain flag")
	}
	q.Request("user.example", 1002)
	if task = take(t, q, 1002); task.ByDomain {
		t.Fatal("standalone USER request acquired domain flag")
	}
}

func TestCoalescingUpgradesPriorityAndFIFOPosition(t *testing.T) {
	q := New()
	q.Nominate("a.example", false, 1)
	q.Nominate("b.example", false, 1)
	q.Request("c.example", 1)
	q.Request("c.example", 1)
	q.Request("b.example", 1)
	q.Nominate("a.example", true, 1)
	_, queued := q.Counts(1)
	if queued != 3 {
		t.Fatalf("queued = %d, want 3", queued)
	}
	assertHost(t, take(t, q, 2), "c.example")
	assertHost(t, take(t, q, 2), "b.example")
	task := take(t, q, 2)
	assertHost(t, task, "a.example")
	if !task.ByDomain {
		t.Fatal("latest watcher domain flag was lost")
	}
}

func TestCancelRemovesOnlyQueuedTask(t *testing.T) {
	q := New()
	q.Nominate("queued.example", false, 1)
	if !q.Cancel("queued.example") {
		t.Fatal("queued task was not cancelled")
	}
	if _, ok := q.Next(2, true); ok {
		t.Fatal("cancelled task was issued")
	}
	q.Nominate("running.example", false, 3)
	take(t, q, 3)
	if q.Cancel("running.example") {
		t.Fatal("in-flight task disappeared from queue cancellation")
	}
	q.Done("running.example", 4)
}

func TestHostValidationAndZeroValue(t *testing.T) {
	var q Queue
	assertAsk(t, q.Nominate("", false, 1), AskBusy)
	assertAsk(t, q.Request("", 1), AskBusy)
	assertAsk(t, q.Nominate(strings.Repeat("a", hostNameMax), false, 1), AskBusy)
	host := strings.Repeat("a", hostNameMax-1)
	assertAsk(t, q.Nominate(host, false, 1), AskQueued)
	assertHost(t, take(t, &q, 1), host)
	var nilQueue *Queue
	assertAsk(t, nilQueue.Nominate("a.example", false, 1), AskBusy)
	assertAsk(t, nilQueue.Request("a.example", 1), AskBusy)
	if _, ok := nilQueue.Next(1, true); ok {
		t.Fatal("nil queue produced work")
	}
	if used, queued := nilQueue.Counts(1); used != 0 || queued != 0 {
		t.Fatal("nil queue reported work")
	}
}

func TestStartAtZeroUsesSentinel(t *testing.T) {
	q := New()
	q.Nominate("a.example", false, 0)
	take(t, q, 0)
	if StartsPerHour == 0 {
		return
	}
	used, _ := q.Counts(3600)
	if used != 1 {
		t.Fatalf("used at 3600 = %d, want sentinel start", used)
	}
	used, _ = q.Counts(3601)
	if used != 0 {
		t.Fatalf("used at 3601 = %d, want expired", used)
	}
}
