// Package probeq schedules active autolist probes.
//
// It is a bounded USER/watcher queue with coalescing, optional technical rate
// limits and expiring in-flight slots. Verdicts are deliberately not retained:
// a new observation always earns a new probe unless it coalesces with the same
// host already queued or running. The zero value of Queue is ready to use.
package probeq

import "sync"

const (
	// StartsPerHour — потолок запусков за скользящий час. Значение 0 при
	// компиляции полностью отключает учёт и ограничение запусков.
	StartsPerHour    = 0
	QueueCapacity    = 64
	InflightCapacity = 8
	InflightSeconds  = 120
	// HostSpacingSeconds — пауза между ручными пробами одного хоста после
	// завершения предыдущей. Значение 0 при компиляции отключает паузу.
	HostSpacingSeconds = 0

	completionCapacity = QueueCapacity
	hostNameMax        = 254 // includes the terminator in the original C storage
)

// Ask is the outcome of an attempt to nominate or request a probe.
type Ask uint8

const (
	AskQueued Ask = iota
	AskBusy
	AskFull
)

// Task is a probe handed out by Next. ByDomain is supplied by the latest
// coalesced watcher nomination and is false for a standalone user request.
type Task struct {
	Host     string
	ByDomain bool
}

type priority uint8

const (
	priorityNote priority = iota
	priorityUser
)

type queueEntry struct {
	host     string
	notes    uint32
	kind     priority
	byDomain bool
	born     uint64
	order    uint64
	used     bool
}

type inflightEntry struct {
	host string
	at   int64
	used bool
}

type completionEntry struct {
	host string
	at   int64
	used bool
}

// Queue owns probe scheduling. onEvict lets the engine discard metadata together
// with an evicted queue entry; it is called while the queue lock is held.
type Queue struct {
	mu        sync.Mutex
	queue     [QueueCapacity]queueEntry
	inflight  [InflightCapacity]inflightEntry
	completed [completionCapacity]completionEntry
	starts    [StartsPerHour]int64
	seq       uint64
	onEvict   func(string)
}

// New returns an empty probe queue.
func New() *Queue { return &Queue{} }

// SetEvictionHandler installs the engine-owned metadata cleanup callback.
func (q *Queue) SetEvictionHandler(fn func(string)) {
	if q == nil {
		return
	}
	q.mu.Lock()
	q.onEvict = fn
	q.mu.Unlock()
}

func validHost(host string) bool {
	return host != "" && len(host) < hostNameMax
}

// usedHour counts starts strictly newer than now-3600 seconds. With the
// compile-time limit disabled there is deliberately no start-history storage.
func (q *Queue) usedHour(now int64) int {
	if StartsPerHour == 0 {
		return 0
	}
	used := 0
	for _, at := range q.starts {
		if at != 0 && at > now-3600 {
			used++
		}
	}
	return used
}

func (q *Queue) queueFind(host string) int {
	for i := range q.queue {
		if q.queue[i].used && q.queue[i].host == host {
			return i
		}
	}
	return -1
}

func (q *Queue) completionFind(host string) int {
	for i := range q.completed {
		if q.completed[i].used && q.completed[i].host == host {
			return i
		}
	}
	return -1
}

func (q *Queue) slotLive(slot int, now int64) bool {
	e := &q.inflight[slot]
	return e.used && e.at > now-InflightSeconds
}

func (q *Queue) isInflight(host string, now int64) bool {
	for i := range q.inflight {
		if q.slotLive(i, now) && q.inflight[i].host == host {
			return true
		}
	}
	return false
}

// queueSlot returns a free slot or the oldest watcher entry. Queued USER
// requests are never evicted by another request.
func (q *Queue) queueSlot() (index int, evicted string, ok bool) {
	oldest := -1
	for i := range q.queue {
		e := &q.queue[i]
		if !e.used {
			return i, "", true
		}
		if e.kind == priorityNote && (oldest < 0 || e.born < q.queue[oldest].born) {
			oldest = i
		}
	}
	if oldest < 0 {
		return 0, "", false
	}
	return oldest, q.queue[oldest].host, true
}

// add coalesces by exact host. USER upgrades an existing watcher entry; watcher
// signals never downgrade it. Eviction is reported to the metadata owner.
func (q *Queue) add(host string, kind priority) (Ask, string) {
	i := q.queueFind(host)
	evicted := ""
	if i < 0 {
		var ok bool
		i, evicted, ok = q.queueSlot()
		if !ok {
			return AskFull, ""
		}
		q.seq++
		q.queue[i] = queueEntry{
			host: host, kind: kind, born: q.seq, order: q.seq, used: true,
		}
	} else if kind > q.queue[i].kind {
		q.queue[i].kind = kind
		q.seq++
		q.queue[i].order = q.seq
	}
	if kind == priorityNote && q.queue[i].notes != ^uint32(0) {
		q.queue[i].notes++
	}
	return AskQueued, evicted
}

func (q *Queue) finishAdmission(ask Ask, evicted string) Ask {
	if evicted != "" && q.onEvict != nil {
		q.onEvict(evicted)
	}
	return ask
}

// Nominate adds watcher work without consulting any historical verdict. A live
// in-flight host coalesces without adding a duplicate queue entry.
func (q *Queue) Nominate(host string, byDomain bool, now int64) Ask {
	if q == nil || !validHost(host) {
		return AskBusy
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.isInflight(host, now) {
		return AskQueued
	}
	ask, evicted := q.add(host, priorityNote)
	if ask == AskQueued {
		q.queue[q.queueFind(host)].byDomain = byDomain
	}
	return q.finishAdmission(ask, evicted)
}

// Cancel removes queued work for host. An already running probe is left alone;
// the engine invalidates its result through the task generation instead.
func (q *Queue) Cancel(host string) bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	i := q.queueFind(host)
	if i < 0 {
		return false
	}
	q.queue[i].used = false
	return true
}

// Request adds USER-priority work. Only a live in-flight duplicate and the
// optional compile-time technical limits can suppress it.
func (q *Queue) Request(host string, now int64) Ask {
	if q == nil || !validHost(host) {
		return AskBusy
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.isInflight(host, now) {
		return AskBusy
	}
	if HostSpacingSeconds > 0 {
		if i := q.completionFind(host); i >= 0 && q.completed[i].at > now-HostSpacingSeconds {
			return AskBusy
		}
	}
	if StartsPerHour > 0 && q.usedHour(now) >= StartsPerHour {
		return AskFull
	}
	ask, evicted := q.add(host, priorityUser)
	return q.finishAdmission(ask, evicted)
}

// before reports whether a should be handed out before b. Watchers are ordered
// by descending evidence count and then oldest birth; USER requests are FIFO.
func before(a, b *queueEntry) bool {
	if a.kind != b.kind {
		return a.kind > b.kind
	}
	if a.kind == priorityNote {
		if a.notes != b.notes {
			return a.notes > b.notes
		}
		return a.born < b.born
	}
	return a.order < b.order
}

// Next hands out the highest-priority queued probe and records its start. It
// returns false when a nonzero hourly budget or all in-flight slots are spent.
func (q *Queue) Next(now int64, allowNotes bool) (Task, bool) {
	if q == nil {
		return Task{}, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if StartsPerHour > 0 && q.usedHour(now) >= StartsPerHour {
		return Task{}, false
	}
	best := -1
	for i := range q.queue {
		e := &q.queue[i]
		if !e.used || (!allowNotes && e.kind == priorityNote) {
			continue
		}
		if best < 0 || before(e, &q.queue[best]) {
			best = i
		}
	}
	if best < 0 {
		return Task{}, false
	}
	slot := 0
	for slot < InflightCapacity && q.slotLive(slot, now) {
		slot++
	}
	if slot == InflightCapacity {
		return Task{}, false
	}
	if StartsPerHour > 0 {
		ring := 0
		for i := 1; i < StartsPerHour; i++ {
			if q.starts[i] < q.starts[ring] {
				ring = i
			}
		}
		if now == 0 {
			q.starts[ring] = 1
		} else {
			q.starts[ring] = now
		}
	}
	e := &q.queue[best]
	q.inflight[slot] = inflightEntry{host: e.host, at: now, used: true}
	task := Task{Host: e.host, ByDomain: e.byDomain}
	e.used = false
	return task, true
}

// Done ends every in-flight slot for host. No verdict is stored. Completion time
// is recorded only when the optional manual host spacing was compiled in.
func (q *Queue) Done(host string, now int64) {
	if q == nil || !validHost(host) {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.inflight {
		if q.inflight[i].used && q.inflight[i].host == host {
			q.inflight[i].used = false
		}
	}
	if HostSpacingSeconds == 0 {
		return
	}
	i := q.completionFind(host)
	if i < 0 {
		oldest := 0
		for j := range q.completed {
			if !q.completed[j].used {
				i = j
				break
			}
			if q.completed[j].at < q.completed[oldest].at {
				oldest = j
			}
		}
		if i < 0 {
			i = oldest
		}
	}
	q.completed[i] = completionEntry{host: host, at: now, used: true}
}

// Counts returns starts in the current hour and distinct queued hosts. UsedHour
// is zero when the compile-time limit is disabled. In-flight work is not queued.
func (q *Queue) Counts(now int64) (usedHour, queued int) {
	if q == nil {
		return 0, 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.queue {
		if q.queue[i].used {
			queued++
		}
	}
	return q.usedHour(now), queued
}
