package logging

import (
	"sync"
	"time"
)

// SettingsGetter interface for getting logging settings.
type SettingsGetter interface {
	IsLoggingEnabled() bool
	GetLoggingMaxAge() int
}

// Service provides application logging functionality.
type Service struct {
	settings SettingsGetter
	buffer   *LogBuffer
	mu       sync.RWMutex
}

// NewService creates a new logging service.
func NewService(settings SettingsGetter) *Service {
	s := &Service{
		settings: settings,
		buffer:   NewLogBuffer(),
	}
	return s
}

// Stop stops the logging service.
func (s *Service) Stop() {
	s.buffer.Stop()
}

// IsEnabled returns whether logging is enabled.
func (s *Service) IsEnabled() bool {
	if s.settings == nil {
		return false
	}
	return s.settings.IsLoggingEnabled()
}

// Log adds an info-level log entry if logging is enabled.
func (s *Service) Log(category, action, target, message string) {
	s.LogWithLevel(LevelInfo, category, action, target, message, "")
}

// LogWarn adds a warning-level log entry if logging is enabled.
func (s *Service) LogWarn(category, action, target, message string) {
	s.LogWithLevel(LevelWarn, category, action, target, message, "")
}

// LogError adds an error-level log entry if logging is enabled.
func (s *Service) LogError(category, action, target, message, errMsg string) {
	s.LogWithLevel(LevelError, category, action, target, message, errMsg)
}

// LogWithLevel adds a log entry with specified level if logging is enabled.
func (s *Service) LogWithLevel(level, category, action, target, message, errMsg string) {
	if !s.IsEnabled() {
		return
	}

	// Update maxAge from settings if changed
	if s.settings != nil {
		maxAge := s.settings.GetLoggingMaxAge()
		if maxAge > 0 {
			s.buffer.SetMaxAge(maxAge)
		}
	}

	entry := LogEntry{
		Timestamp: time.Now(),
		Level:     level,
		Category:  category,
		Action:    action,
		Target:    target,
		Message:   message,
		Error:     errMsg,
	}

	s.buffer.Add(entry)
}

// GetLogs returns log entries with optional filtering.
func (s *Service) GetLogs(category, level string) []LogEntry {
	if category == "" && level == "" {
		return s.buffer.GetAll()
	}
	return s.buffer.GetFiltered(category, level)
}

// Clear removes all log entries.
func (s *Service) Clear() {
	s.buffer.Clear()
}

// Len returns the number of log entries.
func (s *Service) Len() int {
	return s.buffer.Len()
}
