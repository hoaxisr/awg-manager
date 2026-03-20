package logging

import (
	"sync"
	"time"
)

// SettingsGetter provides logging configuration.
type SettingsGetter interface {
	IsLoggingEnabled() bool
	GetLoggingMaxAge() int
	GetLogLevel() string
}

// Service provides application logging.
type Service struct {
	settings SettingsGetter
	buffer   *LogBuffer
	mu       sync.RWMutex
}

func NewService(settings SettingsGetter) *Service {
	return &Service{
		settings: settings,
		buffer:   NewLogBuffer(),
	}
}

func (s *Service) Stop() { s.buffer.Stop() }

func (s *Service) IsEnabled() bool {
	if s.settings == nil {
		return false
	}
	return s.settings.IsLoggingEnabled()
}

// AppLog implements AppLogger. Checks enabled + level filtering.
func (s *Service) AppLog(level Level, group, subgroup, action, target, message string) {
	if !s.IsEnabled() {
		return
	}
	configuredLevel := Level(s.settings.GetLogLevel())
	if !IsVisible(level, configuredLevel) {
		return
	}
	if s.settings != nil {
		if maxAge := s.settings.GetLoggingMaxAge(); maxAge > 0 {
			s.buffer.SetMaxAge(maxAge)
		}
	}
	s.buffer.Add(LogEntry{
		Timestamp: time.Now(),
		Level:     string(level),
		Group:     group,
		Subgroup:  subgroup,
		Action:    action,
		Target:    target,
		Message:   message,
	})
}

// GetLogs returns entries filtered by group, subgroup, level with pagination.
// Returns the page slice and the total count of filtered entries.
func (s *Service) GetLogs(group, subgroup, level string, limit, offset int) ([]LogEntry, int) {
	if limit <= 0 {
		limit = 200
	}
	return s.buffer.GetPaginated(group, subgroup, level, limit, offset)
}

func (s *Service) Clear()  { s.buffer.Clear() }
func (s *Service) Len() int { return s.buffer.Len() }

var _ AppLogger = (*Service)(nil)
