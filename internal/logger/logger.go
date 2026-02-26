package logger

import (
	"fmt"
	"os"
	"time"
)

// Logger writes structured log messages to stderr.
type Logger struct {
	component string
}

// Config for logger initialization.
type Config struct {
	Level     string
	FilePath  string
	MaxRecent int
}

// New creates a new logger.
func New(cfg Config) (*Logger, error) {
	return &Logger{}, nil
}

// WithComponent returns a logger with a component prefix.
func (l *Logger) WithComponent(name string) *Logger {
	return &Logger{component: name}
}

// Close does nothing.
func (l *Logger) Close() error {
	return nil
}

func (l *Logger) log(level, msg string) {
	prefix := ""
	if l.component != "" {
		prefix = "[" + l.component + "] "
	}
	fmt.Fprintf(os.Stderr, "%s %s %s%s\n", time.Now().Format("15:04:05"), level, prefix, msg)
}

func (l *Logger) Debug(msg string, fields ...map[string]interface{}) { l.log("DBG", msg) }
func (l *Logger) Info(msg string, fields ...map[string]interface{})  { l.log("INF", msg) }
func (l *Logger) Warn(msg string, fields ...map[string]interface{})  { l.log("WRN", msg) }
func (l *Logger) Error(msg string, fields ...map[string]interface{}) { l.log("ERR", msg) }

func (l *Logger) Debugf(format string, args ...interface{}) { l.log("DBG", fmt.Sprintf(format, args...)) }
func (l *Logger) Infof(format string, args ...interface{})  { l.log("INF", fmt.Sprintf(format, args...)) }
func (l *Logger) Warnf(format string, args ...interface{})  { l.log("WRN", fmt.Sprintf(format, args...)) }
func (l *Logger) Errorf(format string, args ...interface{}) { l.log("ERR", fmt.Sprintf(format, args...)) }
