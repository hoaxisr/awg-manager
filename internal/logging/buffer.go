package logging

import (
	"time"

	"github.com/hoaxisr/awg-manager/internal/logbuf"
)

const (
	defaultMaxAge        = 2 * time.Hour
	defaultAppMaxEntries = 5000
	defaultSBMaxEntries  = 5000
)

// LogBuffer stores app log entries with automatic cleanup.
// Thin wrapper over logbuf.Buffer[LogEntry] — see internal/logbuf for
// the shared ring + TTL + goroutine-safe storage machinery.
//
// Each Service owns one buffer per Bucket so a noisy stream (sing-box)
// cannot evict history from another stream (app).
type LogBuffer struct {
	bucket Bucket
	inner  *logbuf.Buffer[LogEntry]
}

// NewLogBuffer creates a new log buffer for the given bucket. Defaults
// (MaxAge / MaxEntries) are bucket-specific; the Service overrides them
// from settings on construction via SetMaxAge / SetMaxEntries.
func NewLogBuffer(bucket Bucket) *LogBuffer {
	return &LogBuffer{
		bucket: bucket,
		inner: logbuf.New(logbuf.Options[LogEntry]{
			MaxAge:     defaultMaxAge,
			MaxEntries: defaultMaxEntriesFor(bucket),
			// Эффективное время записи — последний повтор: активно
			// повторяющаяся схлопнутая запись не должна выселяться TTL-очисткой
			// по давнему первому появлению.
			TimestampOf:  effectiveTime,
			SetTimestamp: func(e *LogEntry, t time.Time) { e.Timestamp = t },
		}),
	}
}

// DefaultCapacity is the ring size used when settings carry no override
// for bucket — the value Stats(bucket).Capacity reports on a fresh box.
func DefaultCapacity(bucket Bucket) int { return defaultMaxEntriesFor(bucket) }

func defaultMaxEntriesFor(bucket Bucket) int {
	if bucket == BucketSingbox {
		return defaultSBMaxEntries
	}
	return defaultAppMaxEntries
}

// Bucket returns which bucket this buffer belongs to.
func (lb *LogBuffer) Bucket() Bucket { return lb.bucket }

// Add adds a new log entry to the buffer.
func (lb *LogBuffer) Add(entry LogEntry) { lb.inner.Add(entry) }

// GetAll returns all log entries, newest first.
func (lb *LogBuffer) GetAll() []LogEntry { return lb.inner.GetAll() }

// GetFiltered returns log entries matching group/subgroup/level, newest first.
// Empty string for any field means "no constraint on that field".
func (lb *LogBuffer) GetFiltered(group, subgroup, level string) []LogEntry {
	return lb.inner.Filter(matcher(group, subgroup, level, time.Time{}))
}

// GetPaginated returns filtered entries with pagination, newest first,
// plus the total count of filtered entries. A non-zero `since` restricts
// the result to entries whose Timestamp is strictly after `since`
// (used for SSE catch-up after a reconnect).
func (lb *LogBuffer) GetPaginated(group, subgroup, level string, since time.Time, limit, offset int) ([]LogEntry, int) {
	return lb.inner.FilterPage(matcher(group, subgroup, level, since), limit, offset)
}

// GetPaginatedMulti returns filtered entries with pagination, newest first,
// plus the total count of filtered entries, using multi-select group/subgroup
// filters. Empty slices mean "no constraint" for that field.
func (lb *LogBuffer) GetPaginatedMulti(groups, subgroups []string, level string, since time.Time, limit, offset int) ([]LogEntry, int) {
	return lb.inner.FilterPage(matcherMulti(groups, subgroups, level, since), limit, offset)
}

// coalesceScanLimit ограничивает поиск дубликата последними записями —
// при debug-флуде окно может содержать сотни записей, сканировать весь
// буфер на каждую запись незачем.
const coalesceScanLimit = 300

// effectiveTime — время последней активности записи: LastSeen для
// схлопнутых повторов, иначе Timestamp (первое появление).
func effectiveTime(e LogEntry) time.Time {
	if e.LastSeen != nil {
		return *e.LastSeen
	}
	return e.Timestamp
}

// CoalesceOrAdd сворачивает идентичный повтор в недавнюю существующую
// запись (совпадают level/group/subgroup/action/target/message, последнее
// появление не старше window), иначе добавляет новую — атомарно, под одним
// локом буфера: параллельные одинаковые записи не могут задвоиться. У
// свёрнутой записи Repeats++ и LastSeen; Timestamp и позиция не меняются.
// Возвращает сохранённую запись и признак сворачивания.
func (lb *LogBuffer) CoalesceOrAdd(entry LogEntry, window time.Duration) (LogEntry, bool) {
	now := entry.Timestamp
	if now.IsZero() {
		now = time.Now()
	}
	cutoff := now.Add(-window)
	return lb.inner.UpsertRecent(coalesceScanLimit,
		func(e LogEntry) bool {
			if e.Level != entry.Level || e.Group != entry.Group || e.Subgroup != entry.Subgroup ||
				e.Action != entry.Action || e.Target != entry.Target || e.Message != entry.Message {
				return false
			}
			return effectiveTime(e).After(cutoff)
		},
		func(e *LogEntry) {
			e.Repeats++
			t := now
			e.LastSeen = &t
		},
		entry)
}

// Clear removes all entries.
func (lb *LogBuffer) Clear() { lb.inner.Clear() }

// SetMaxAge updates the maximum age for log entries (hours).
func (lb *LogBuffer) SetMaxAge(hours int) { lb.inner.SetMaxAge(hours) }

// SetMaxEntries updates the size cap. Trims immediately if exceeded.
func (lb *LogBuffer) SetMaxEntries(n int) { lb.inner.SetMaxEntries(n) }

// Stop stops the cleanup goroutine.
func (lb *LogBuffer) Stop() { lb.inner.Stop() }

// Len returns the number of entries in the buffer.
func (lb *LogBuffer) Len() int { return lb.inner.Len() }

// Oldest returns the timestamp of the oldest entry, or zero if empty.
func (lb *LogBuffer) Oldest() time.Time {
	all := lb.inner.GetAll()
	if len(all) == 0 {
		return time.Time{}
	}
	// GetAll returns newest-first — the last element is the oldest.
	return all[len(all)-1].Timestamp
}

// matcher builds the group/subgroup/level/since composite predicate once so
// Filter/FilterPage don't recompute the closure shape per entry. A zero
// `since` disables the timestamp cutoff.
func matcher(group, subgroup, level string, since time.Time) func(LogEntry) bool {
	return func(e LogEntry) bool {
		// Отсечка по последней активности: схлопнутая запись, повторившаяся
		// после since, попадает в catch-up и освежает счётчик у клиента.
		if !since.IsZero() && !effectiveTime(e).After(since) {
			return false
		}
		if group != "" && e.Group != group {
			return false
		}
		if subgroup != "" && e.Subgroup != subgroup {
			return false
		}
		if level != "" && !IsVisible(Level(e.Level), Level(level)) {
			return false
		}
		return true
	}
}

func matcherMulti(groups, subgroups []string, level string, since time.Time) func(LogEntry) bool {
	groupSet := make(map[string]struct{}, len(groups))
	for _, g := range groups {
		if g != "" {
			groupSet[g] = struct{}{}
		}
	}

	subgroupSet := make(map[string]struct{}, len(subgroups))
	for _, s := range subgroups {
		if s != "" {
			subgroupSet[s] = struct{}{}
		}
	}

	return func(e LogEntry) bool {
		if !since.IsZero() && !effectiveTime(e).After(since) {
			return false
		}
		if len(groupSet) > 0 {
			if _, ok := groupSet[e.Group]; !ok {
				return false
			}
		}
		if len(subgroupSet) > 0 {
			if _, ok := subgroupSet[e.Subgroup]; !ok {
				return false
			}
		}
		if level != "" && !IsVisible(Level(e.Level), Level(level)) {
			return false
		}
		return true
	}
}
