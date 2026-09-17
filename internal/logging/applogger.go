package logging

// AppLogger is the interface for UI-visible logging.
type AppLogger interface {
	AppLog(level Level, group, subgroup, action, target, message string)
}

// LevelGate — НЕОБЯЗАТЕЛЬНАЯ способность логгера: сказать, попадёт ли запись
// такого уровня в журнал, НЕ строя саму запись.
//
// Нужна поставщикам, у которых подготовка записи дороже её выбрасывания.
// Образец — пересылка журнала sing-box: там на каждую строку движка идут
// разбор JSON и несколько регулярок, а уровень проверялся уже ПОСЛЕ, внутри
// AppLog. На уровне движка `info` sing-box пишет строку на соединение и на
// DNS-запрос, так что почти вся эта работа выбрасывалась.
//
// Отдельным интерфейсом, а не методом AppLogger: реализующих AppLogger много
// (в основном тестовые), и расширять его ради одного потребителя незачем.
// Потребитель делает type assertion; не поддержал — работает как раньше.
type LevelGate interface {
	Visible(level Level) bool
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

// At пишет запись указанного уровня. Нужен вызывающему, который уровень уже
// вычислил (и по нему же отсеивал), — иначе тот повторял бы switch по уровням
// второй раз, и две копии могли бы разойтись.
func (l *ScopedLogger) At(level Level, action, target, message string) {
	if l == nil || l.appLogger == nil {
		return
	}
	l.appLogger.AppLog(level, l.group, l.subgroup, action, target, message)
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
