package logger

import "fmt"

// Logger is a no-op logger kept for interface compatibility.
// Runtime logs go to in-memory loggingService (visible in UI).
// stderr is reserved for Go runtime panics only.
type Logger struct {
	component string
}

// New creates a new logger.
func New() *Logger {
	return &Logger{}
}

// WithComponent returns a logger with a component prefix.
func (l *Logger) WithComponent(name string) *Logger {
	return &Logger{component: name}
}

// Close does nothing.
func (l *Logger) Close() error {
	return nil
}

func (l *Logger) log(level, msg string) {}

func (l *Logger) Debug(msg string, fields ...map[string]interface{}) { l.log("DBG", msg) }
func (l *Logger) Info(msg string, fields ...map[string]interface{})  { l.log("INF", msg) }
func (l *Logger) Warn(msg string, fields ...map[string]interface{})  { l.log("WRN", msg) }
func (l *Logger) Error(msg string, fields ...map[string]interface{}) { l.log("ERR", msg) }

func (l *Logger) Debugf(format string, args ...interface{}) { l.log("DBG", fmt.Sprintf(format, args...)) }
func (l *Logger) Infof(format string, args ...interface{})  { l.log("INF", fmt.Sprintf(format, args...)) }
func (l *Logger) Warnf(format string, args ...interface{})  { l.log("WRN", fmt.Sprintf(format, args...)) }
func (l *Logger) Errorf(format string, args ...interface{}) { l.log("ERR", fmt.Sprintf(format, args...)) }
