package config

import (
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// headerProtectionMinPadding — размер nonce (12 байт), который модуль читает из
// начала Sx-паддинга: S1 для handshake initiation, S2 для response, S3 для
// cookie, S4 для транспорта. Если паддинг короче, отправитель и получатель
// берут разный nonce и пакеты отбрасываются целиком.
//
// Модуль отвергает setconf при S < 12, но только для значений, пришедших в том
// же запросе: конфиг вообще без S-ключей он принимает молча, и туннель встаёт
// мёртвым. Поэтому проверяем сами.
const headerProtectionMinPadding = 12

// ValidateAWG3 проверяет параметры AWG 3.0 на совместимость с модулем ядра.
func ValidateAWG3(o *storage.AWGObfuscation) error {
	if o == nil || o.HeaderProtectionKey == "" {
		return nil
	}
	for _, s := range []struct {
		name  string
		value int
	}{{"S1", o.S1}, {"S2", o.S2}, {"S3", o.S3}, {"S4", o.S4}} {
		if s.value < headerProtectionMinPadding {
			return fmt.Errorf("%s = %d: при заданном HeaderProtectionKey значения S1-S4 должны быть не меньше %d — из этих байт берётся nonce header protection",
				s.name, s.value, headerProtectionMinPadding)
		}
	}
	return nil
}
