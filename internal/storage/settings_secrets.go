package storage

import (
	"bytes"
	"regexp"
)

// secretJSONKeys — имена полей settings.json, несущих секрет ОТКРЫТЫМ текстом
// либо шифротекстом, который расшифровывается лежащим рядом .device-key.
//
// Список по именам полей, а не по путям в структуре: privateKey встречается на
// трёх уровнях (managedServers[], serverPeerSecrets[][], серверные интерфейсы),
// и перечисление путей пришлось бы править при каждом переносе поля, молча
// пропуская секрет до первого ревью. Имя поля переживает перенос.
//
// Список ДОЛЖЕН пополняться вместе с новым секретным полем настроек. Страж
// на это есть: TestSecretJSONKeys_CoverSettingsSecrets в settings_secrets_test.go
// краснеет, когда в типах появляется поле с секретным именем, которого здесь нет.
var secretJSONKeys = []string{
	"apiKey",
	"amneziaPremiumKeyCipher",
	"privateKey",
	"presharedKey",
}

// secretValuePattern собирается один раз: функция зовётся на каждой записи
// настроек, а компиляция регулярного выражения дороже самого поиска.
var secretValuePattern = func() *regexp.Regexp {
	alt := ""
	for i, k := range secretJSONKeys {
		if i > 0 {
			alt += "|"
		}
		alt += regexp.QuoteMeta(k)
	}
	// Значение берётся как есть до закрывающей кавычки: экранированных кавычек
	// в base64-ключах и в шифротексте не бывает, а «жадный» разбор JSON здесь
	// не нужен — нам достаточно самой строки, чтобы поискать её в новом файле.
	return regexp.MustCompile(`"(?:` + alt + `)"\s*:\s*"([^"]+)"`)
}()

// secretsIn возвращает значения всех секретных полей, найденные в JSON настроек.
// Разбора структуры здесь нет сознательно: файл может быть от другой версии
// схемы, и разбор его молча потерял бы поля, которых текущая структура уже
// (или ещё) не знает, — а на флеше они бы остались.
func secretsIn(settingsJSON []byte) [][]byte {
	matches := secretValuePattern.FindAllSubmatch(settingsJSON, -1)
	out := make([][]byte, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// secretsDropped — есть ли в prev секрет, которого нет в next.
//
// Именно «нет в next», а не «поле изменилось»: секрет, переехавший в другое
// поле, с флеша никуда не делся и второй записи не требует; секрет, пропавший
// целиком, требует. Сравнение по вхождению подстроки в новый файл, а не по
// парам «поле-значение», ровно поэтому.
func secretsDropped(prev, next []byte) bool {
	for _, secret := range secretsIn(prev) {
		if !bytes.Contains(next, secret) {
			return true
		}
	}
	return false
}
