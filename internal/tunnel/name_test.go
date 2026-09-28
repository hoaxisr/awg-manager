package tunnel

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// Предел — в БАЙТАХ UTF-8, а не в рунах: 128 кириллических букв — ровно
// 256 байт, 129-я уже не влезает в описание записи NDMS.
func TestValidateName_ByteLimit(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"256 байт ASCII", strings.Repeat("a", 256), true},
		{"257 байт ASCII", strings.Repeat("a", 257), false},
		{"128 кириллических = 256 байт", strings.Repeat("ж", 128), true},
		{"129 кириллических = 258 байт", strings.Repeat("ж", 129), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateName(tc.in)
			if (err == nil) != tc.ok {
				t.Fatalf("ValidateName(%d байт) = %v, want ok=%v", len(tc.in), err, tc.ok)
			}
			if err != nil && (!errors.Is(err, ErrNameTooLong) || !strings.Contains(err.Error(), "длиннее 256 байт")) {
				t.Fatalf("отказ не типизирован или не для человека: %v", err)
			}
		})
	}
}

// Обрезка — по границе руны: срез на 256-м байте посреди «ж» оставил бы
// битый UTF-8 в описании записи.
func TestTruncateName_RuneBoundary(t *testing.T) {
	in := "a" + strings.Repeat("ж", 200) // 401 байт; байт 256 — середина руны
	got := TruncateName(in)
	if len(got) != 255 || !utf8.ValidString(got) || !strings.HasPrefix(in, got) {
		t.Fatalf("TruncateName: %d байт, valid=%v", len(got), utf8.ValidString(got))
	}
	if short := "Germany"; TruncateName(short) != short {
		t.Fatalf("короткое имя изменено")
	}
}
