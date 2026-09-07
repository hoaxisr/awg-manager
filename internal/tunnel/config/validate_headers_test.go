package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestValidateHeaderRanges(t *testing.T) {
	cases := []struct {
		name       string
		h          [4]string
		wantErr    string // "" = ok
		wantFmtErr bool   // ошибка формата, а не пересечения
	}{
		{"уникальные одиночные", [4]string{"10", "20", "30", "40"}, "", false},
		{"дефолт WG 1-4 уникален и допустим модулем", [4]string{"1", "2", "3", "4"}, "", false},
		{"все пустые = дефолт модуля 1-4, допустимо", [4]string{"", "", "", ""}, "", false},
		{"H1=2 при пустых остальных пересекается с дефолтом H2=2", [4]string{"2", "", "", ""}, "H1 и H2", false},
		{"диапазон накрывает дефолт пустого H4=4", [4]string{"1-10", "20", "30", ""}, "H1 и H4", false},
		{"равные одиночные — пересечение", [4]string{"10", "10", "30", "40"}, "H1 и H2", false},
		{"диапазоны без пересечения", [4]string{"5-100", "101-200", "201-300", "301-400"}, "", false},
		{"диапазон накрывает одиночное", [4]string{"5-100", "50", "201-300", "301-400"}, "H1 и H2", false},
		{"пересечение через один", [4]string{"5-100", "200-300", "90-110", "400"}, "H1 и H3", false},
		{"мусор — ошибка формата", [4]string{"abc", "2", "3", "4"}, "H1", true},
		{"битый диапазон — ошибка формата", [4]string{"10-x", "20", "30", "40"}, "H1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := &storage.AWGObfuscation{H1: tc.h[0], H2: tc.h[1], H3: tc.h[2], H4: tc.h[3]}
			err := ValidateHeaderRanges(o)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
			// Анализатор различает ошибку формата и пересечение по сентинелу.
			if got := errors.Is(err, ErrHeaderFormat); got != tc.wantFmtErr {
				t.Fatalf("errors.Is(err, ErrHeaderFormat) = %v, want %v (err: %v)", got, tc.wantFmtErr, err)
			}
		})
	}
}

func TestValidateObfuscation_CombinesBothRules(t *testing.T) {
	// Пересечение H ловится и без header protection.
	if err := ValidateObfuscation(&storage.AWGObfuscation{H1: "10", H2: "10", H3: "30", H4: "40"}); err == nil {
		t.Fatal("overlapping H must be rejected")
	}
	// Правило AWG3 срабатывает первым.
	err := ValidateObfuscation(&storage.AWGObfuscation{HeaderProtectionKey: "bm90LWEta2V5", H1: "10", H2: "20", H3: "30", H4: "40"})
	if err == nil || !strings.Contains(err.Error(), "HeaderProtectionKey") {
		t.Fatalf("want HeaderProtectionKey error, got %v", err)
	}
}
