// Package logx - минимальный уровневый логгер поверх stdlib log.
// Вызывающий получает Logger и зовёт Debugf/Infof/Warnf/Errorf;
// Debugf управляется debug-флагом, остальные уровни всегда пишут.
package logx

import "log"

// Logger - интерфейс уровневого логирования.
// Debugf гейтится debug-флагом конструктора; остальные уровни печатают всегда.
type Logger interface {
	Debugf(format string, v ...any)
	Infof(format string, v ...any)
	Warnf(format string, v ...any)
	Errorf(format string, v ...any)
	// DebugEnabled сообщает, будет ли Debugf писать вывод. Hot-path
	// (счётчики статистики, условные ветки) используют это, чтобы не
	// делать работу, результат которой логгер всё равно отбросит.
	DebugEnabled() bool
}

type stdLogger struct {
	debug bool
}

// New возвращает Logger поверх stdlib log. При debug=false Debugf - no-op.
func New(debug bool) Logger {
	return &stdLogger{debug: debug}
}

// Nop возвращает Logger, отбрасывающий весь вывод. Полезно в тестах.
func Nop() Logger { return nopLogger{} }

func (l *stdLogger) Debugf(format string, v ...any) {
	if l.debug {
		log.Printf("[DEBUG] "+format, v...)
	}
}
func (*stdLogger) Infof(format string, v ...any)  { log.Printf("[INFO] "+format, v...) }
func (*stdLogger) Warnf(format string, v ...any)  { log.Printf("[WARN] "+format, v...) }
func (*stdLogger) Errorf(format string, v ...any) { log.Printf("[ERROR] "+format, v...) }
func (l *stdLogger) DebugEnabled() bool           { return l.debug }

// OrNop возвращает l, если он не nil, иначе Nop. Используется в конструкторах
// пакетов, принимающих nullable Logger.
func OrNop(l Logger) Logger {
	if l == nil {
		return Nop()
	}
	return l
}

type nopLogger struct{}

func (nopLogger) Debugf(string, ...any) {}
func (nopLogger) Infof(string, ...any)  {}
func (nopLogger) Warnf(string, ...any)  {}
func (nopLogger) Errorf(string, ...any) {}
func (nopLogger) DebugEnabled() bool    { return false }
