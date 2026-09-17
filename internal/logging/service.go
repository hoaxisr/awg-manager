package logging

import (
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// repeatCoalesceWindow — окно схлопывания идентичных повторов: пока между
// одинаковыми записями проходит меньше этого времени, они копятся в одной
// строке журнала (LastSeen продлевает окно, так что непрерывный шум с любым
// периодом короче окна остаётся одной записью).
const repeatCoalesceWindow = 5 * time.Minute

// SettingsGetter provides logging configuration.
type SettingsGetter interface {
	IsLoggingEnabled() bool
	GetLoggingMaxAge() int
	GetLogLevel() string
	GetAppMaxEntries() int
	GetSingboxMaxEntries() int
}

// BufferStats reports observable state of one bucket's buffer.
type BufferStats struct {
	Bucket   Bucket
	Size     int
	Capacity int
	Oldest   time.Time
}

// Service provides application logging with two isolated buffers — one
// for app events (tunnel/routing/server/system) and one for sing-box
// forwarder events. Bucket routing happens in AppLog based on the entry's
// group; settings are applied eagerly on construction and on demand via
// ApplySettings (called by api/settings after a settings PUT).
type Service struct {
	settings      SettingsGetter
	appBuffer     *LogBuffer
	singboxBuffer *LogBuffer
	bus           *events.Bus
}

func NewService(settings SettingsGetter) *Service {
	s := &Service{
		settings:      settings,
		appBuffer:     NewLogBuffer(BucketApp),
		singboxBuffer: NewLogBuffer(BucketSingbox),
	}
	s.applySettingsLocked()
	return s
}

func (s *Service) Stop() {
	s.appBuffer.Stop()
	s.singboxBuffer.Stop()
}

// SetEventBus sets the event bus for SSE publishing.
func (s *Service) SetEventBus(bus *events.Bus) { s.bus = bus }

// ApplySettings re-reads the settings store and updates each buffer's
// MaxAge and MaxEntries. Call after a successful PUT /api/settings.
func (s *Service) ApplySettings() { s.applySettingsLocked() }

func (s *Service) applySettingsLocked() {
	if s.settings == nil {
		return
	}
	maxAge := s.settings.GetLoggingMaxAge()
	if maxAge > 0 {
		s.appBuffer.SetMaxAge(maxAge)
		s.singboxBuffer.SetMaxAge(maxAge)
	}
	if app := s.settings.GetAppMaxEntries(); app > 0 {
		s.appBuffer.SetMaxEntries(app)
	}
	if sb := s.settings.GetSingboxMaxEntries(); sb > 0 {
		s.singboxBuffer.SetMaxEntries(sb)
	}
}

func (s *Service) IsEnabled() bool {
	if s.settings == nil {
		return false
	}
	return s.settings.IsLoggingEnabled()
}

// Visible implements LevelGate: попадёт ли запись такого уровня в журнал.
// Ровно та же проверка, что делает AppLog первым делом, — вынесена, чтобы
// дорогой поставщик мог спросить ДО подготовки записи.
func (s *Service) Visible(level Level) bool {
	if !s.IsEnabled() {
		return false
	}
	return IsVisible(level, Level(s.settings.GetLogLevel()))
}

// AppLog implements AppLogger. Checks enabled + level filtering, then routes
// to the correct buffer based on the entry's group.
func (s *Service) AppLog(level Level, group, subgroup, action, target, message string) {
	if !s.IsEnabled() {
		return
	}
	configuredLevel := Level(s.settings.GetLogLevel())
	if !IsVisible(level, configuredLevel) {
		return
	}
	bucket := BucketForGroup(group)
	target_buf := s.bufferFor(bucket)
	if target_buf == nil {
		return
	}
	entry := LogEntry{
		Timestamp: time.Now(),
		Level:     string(level),
		Group:     group,
		Subgroup:  subgroup,
		Action:    action,
		Target:    target,
		Message:   message,
	}
	// Идентичный повтор в окне сворачивается в существующую запись (×N)
	// вместо новой строки — источник рекуррентного шума (периодические
	// проверки, зацикленные Warn) не может забить журнал.
	entry, _ = target_buf.CoalesceOrAdd(entry, repeatCoalesceWindow)
	// Публикуем, только если панель открыта хоть у кого-то. Буфер выше
	// наполнен в любом случае, а страница журнала при открытии и при
	// переподключении SSE забирает пропущенное ручкой /logs по since
	// (frontend/src/routes/+layout.svelte), так что зритель ничего не теряет.
	//
	// Цена публикации не в самой отправке: событие строится с двумя
	// форматированиями времени, а Publish идёт веерно ко ВСЕМ подписчикам
	// шины, включая пять вечных внутренних, — пять пробуждений горутин на
	// строку журнала при нуле открытых панелей.
	if s.bus != nil && s.bus.ClientCount() > 0 {
		// Формат должен побайтно совпадать с REST-DTO (api.logEntryDTO):
		// клиент сопоставляет SSE-повторы с загруженными строками по
		// составному ключу, включающему timestamp как строку.
		ev := events.LogEntryEvent{
			Timestamp: entry.Timestamp.UTC().Format(time.RFC3339Nano),
			Level:     entry.Level,
			Group:     entry.Group,
			Subgroup:  entry.Subgroup,
			Action:    entry.Action,
			Target:    entry.Target,
			Message:   entry.Message,
			Bucket:    string(bucket),
			Repeats:   entry.Repeats,
		}
		if entry.LastSeen != nil {
			ev.LastSeen = entry.LastSeen.UTC().Format(time.RFC3339Nano)
		}
		s.bus.Publish("log:entry", ev)
	}
}

// GetLogs returns entries from the specified bucket filtered by group,
// subgroup, level with pagination. A non-zero `since` restricts results
// to entries strictly after that time (SSE catch-up after reconnect).
// Returns the page slice and the total count of filtered entries.
func (s *Service) GetLogs(bucket Bucket, group, subgroup, level string, since time.Time, limit, offset int) ([]LogEntry, int) {
	var groups []string
	var subgroups []string
	if group != "" {
		groups = []string{group}
	}
	if subgroup != "" {
		subgroups = []string{subgroup}
	}
	return s.GetLogsMulti(bucket, groups, subgroups, level, since, limit, offset)
}

// GetLogsMulti returns entries from the specified bucket filtered by multiple
// groups/subgroups and level with pagination.
func (s *Service) GetLogsMulti(bucket Bucket, groups []string, subgroups []string, level string, since time.Time, limit, offset int) ([]LogEntry, int) {
	if limit <= 0 {
		limit = 200
	}
	buf := s.bufferFor(bucket)
	if buf == nil {
		return nil, 0
	}
	return buf.GetPaginatedMulti(groups, subgroups, level, since, limit, offset)
}

// Clear removes all entries from the specified bucket.
func (s *Service) Clear(bucket Bucket) {
	if buf := s.bufferFor(bucket); buf != nil {
		buf.Clear()
	}
}

// Stats reports size/capacity/oldest for the specified bucket.
func (s *Service) Stats(bucket Bucket) BufferStats {
	buf := s.bufferFor(bucket)
	if buf == nil {
		return BufferStats{Bucket: bucket}
	}
	return BufferStats{
		Bucket:   bucket,
		Size:     buf.Len(),
		Capacity: capacityFor(s.settings, bucket),
		Oldest:   buf.Oldest(),
	}
}

func (s *Service) bufferFor(bucket Bucket) *LogBuffer {
	switch bucket {
	case BucketSingbox:
		return s.singboxBuffer
	case BucketApp:
		return s.appBuffer
	}
	return nil
}

func capacityFor(settings SettingsGetter, bucket Bucket) int {
	if settings == nil {
		return defaultMaxEntriesFor(bucket)
	}
	switch bucket {
	case BucketSingbox:
		if v := settings.GetSingboxMaxEntries(); v > 0 {
			return v
		}
	case BucketApp:
		if v := settings.GetAppMaxEntries(); v > 0 {
			return v
		}
	}
	return defaultMaxEntriesFor(bucket)
}

// Len returns the total entry count across both buckets.
func (s *Service) Len() int { return s.appBuffer.Len() + s.singboxBuffer.Len() }

var _ AppLogger = (*Service)(nil)
