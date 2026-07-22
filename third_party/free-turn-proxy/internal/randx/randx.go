// Package randx - тонкая обёртка над crypto/rand с API в стиле math/rand.
// Используется там, где gosec G404 бьёт по math/rand: антифингерпринт-jitter,
// случайные задержки, выбор имён/паттернов. CSPRNG здесь не критичен по
// производительности, зато снимает шум линтера и убирает PRNG-предсказуемость.
package randx

import (
	"crypto/rand"
	"encoding/binary"
	"math/big"
)

// IntN возвращает равномерное случайное число из [0, n). Для n <= 0 возвращает 0.
func IntN(n int) int {
	if n <= 0 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

// Intn - алиас IntN для совместимости с math/rand.
func Intn(n int) int { return IntN(n) }

// Float64 возвращает число из [0.0, 1.0) с 53 битами мантиссы.
func Float64() float64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	u := binary.LittleEndian.Uint64(b[:]) >> 11
	return float64(u) / float64(1<<53)
}
