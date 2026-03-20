package logging

// AppLogger is the interface for UI-visible logging.
type AppLogger interface {
	AppLog(level Level, group, subgroup, action, target, message string)
}

// ScopedLogger wraps AppLogger with fixed group and subgroup.
type ScopedLogger struct {
	appLogger AppLogger
	group     string
	subgroup  string
}

// NewScopedLogger creates a logger scoped to a group and subgroup.
// Safe to use with nil appLogger — all methods become no-ops.
func NewScopedLogger(appLogger AppLogger, group, subgroup string) *ScopedLogger {
	return &ScopedLogger{appLogger: appLogger, group: group, subgroup: subgroup}
}

// Info logs an operation result. Visible at INFO, FULL, DEBUG.
func (l *ScopedLogger) Info(action, target, message string) {
	if l == nil || l.appLogger == nil {
		return
	}
	l.appLogger.AppLog(LevelInfo, l.group, l.subgroup, action, target, message)
}

// Full logs a key intermediate step. Visible at FULL, DEBUG.
func (l *ScopedLogger) Full(action, target, message string) {
	if l == nil || l.appLogger == nil {
		return
	}
	l.appLogger.AppLog(LevelFull, l.group, l.subgroup, action, target, message)
}

// Debug logs detailed technical info. Visible at DEBUG only.
func (l *ScopedLogger) Debug(action, target, message string) {
	if l == nil || l.appLogger == nil {
		return
	}
	l.appLogger.AppLog(LevelDebug, l.group, l.subgroup, action, target, message)
}

// Warn logs an error or problem. Always visible regardless of level.
func (l *ScopedLogger) Warn(action, target, message string) {
	if l == nil || l.appLogger == nil {
		return
	}
	l.appLogger.AppLog(LevelWarn, l.group, l.subgroup, action, target, message)
}

// Error logs a critical error. Always visible regardless of level.
func (l *ScopedLogger) Error(action, target, message string) {
	if l == nil || l.appLogger == nil {
		return
	}
	l.appLogger.AppLog(LevelError, l.group, l.subgroup, action, target, message)
}
