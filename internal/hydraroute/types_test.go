package hydraroute

import "testing"

// Отсутствие ключа ≠ выключено: нулевой ipsetMaxElem даёт стоковый дефолт (память
// проекта о дефолтах hrneo.conf), явное значение — как есть.
func TestConfig_EffectiveMaxElem(t *testing.T) {
	if got := (&Config{}).EffectiveMaxElem(); got != 262144 {
		t.Fatalf("дефолт = %d, want 262144", got)
	}
	if got := (&Config{IpsetMaxElem: -5}).EffectiveMaxElem(); got != 262144 {
		t.Fatalf("отрицательное = %d, want 262144", got)
	}
	if got := (&Config{IpsetMaxElem: 5000}).EffectiveMaxElem(); got != 5000 {
		t.Fatalf("явное = %d, want 5000", got)
	}
}
