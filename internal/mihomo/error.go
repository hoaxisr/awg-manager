package mihomo

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MihomoCompileError captures structured context when Mihomo configuration
// compilation fails static validation. Callers must not supply secret credentials;
// the error renderer sanitizes control characters, collapses whitespace, and enforces
// UTF-8-safe rune-length boundaries on all fields to prevent log injection and log flooding.
type MihomoCompileError struct {
	Source   string // Subsystem: "rule", "proxy-group", "listener", "rule-set", "dns", "provider", etc.
	Resource string // Identifier of the offending resource (e.g. group name, listener name, slot)
	Rule     string // Rule syntax string or spec if applicable
	Message  string // Human-readable explanation of why validation failed
}

func sanitizeErrorField(s string, maxRunes int) string {
	if s == "" {
		return ""
	}

	// 1. Replace control characters and newlines with spaces
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			b.WriteRune(' ')
		} else {
			b.WriteRune(r)
		}
	}
	clean := b.String()

	// 2. Collapse consecutive whitespace
	clean = strings.Join(strings.Fields(clean), " ")

	// 3. UTF-8-safe truncation by rune count
	if maxRunes > 3 && utf8.RuneCountInString(clean) > maxRunes {
		runes := []rune(clean)
		return string(runes[:maxRunes-3]) + "..."
	}

	return clean
}

func (e *MihomoCompileError) Error() string {
	if e == nil {
		return "<nil>"
	}
	var b strings.Builder
	b.WriteString("mihomo compile error")
	if e.Source != "" {
		b.WriteString(" [")
		b.WriteString(e.Source)
		b.WriteString("]")
	}
	if e.Resource != "" {
		b.WriteString(" resource=")
		b.WriteString(e.Resource)
	}
	if e.Rule != "" {
		b.WriteString(" rule=")
		b.WriteString(e.Rule)
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	return b.String()
}

// NewCompileError constructs a new sanitized MihomoCompileError.
func NewCompileError(source, resource, rule, message string) *MihomoCompileError {
	return &MihomoCompileError{
		Source:   sanitizeErrorField(source, 64),
		Resource: sanitizeErrorField(resource, 128),
		Rule:     sanitizeErrorField(rule, 256),
		Message:  sanitizeErrorField(message, 512),
	}
}

// CompileErrorf formats a message and returns a new sanitized MihomoCompileError.
func CompileErrorf(source, resource, rule, format string, args ...any) *MihomoCompileError {
	return NewCompileError(source, resource, rule, fmt.Sprintf(format, args...))
}
