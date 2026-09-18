// Package openfluxlink — ссылки openflux:// и разбор клиентских ссылок.
//
// Ссылка несёт канал связи (транспорт + адрес документа-релея) и его
// параметры: клиент OpenFlux и выходная нода соединяются ЧЕРЕЗ релей, поэтому
// «адрес сервера» в привычном смысле у подсистемы нет — совместимость сторон
// держат транспорт, канал, кодек и общий секрет. Кодирование — то же, что у
// freeturn://: base64url (без паддинга) поверх JSON.
package openfluxlink

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// LinkScheme — префикс наших ссылок.
const LinkScheme = "openflux://"

// LinkPayload — JSON внутри openflux://. Имена полей — флаги upstream
// (main.go), а не наши: ссылку читает владелец телефона/ПК руками, и знакомые
// имена читаются быстрее.
type LinkPayload struct {
	V         int    `json:"v"`
	Role      string `json:"role,omitempty"`      // client|exit — чьё это подключение
	Transport string `json:"transport,omitempty"` // yandex|vyandex|oneme|cupsonline|mailru
	URL       string `json:"url,omitempty"`       // документ-канал
	MaxToken  string `json:"maxToken,omitempty"`  // транспорт oneme
	MaxUID    string `json:"maxUid,omitempty"`    // транспорт oneme
	Mode      string `json:"mode,omitempty"`      // l3|l4 — ТОЛЬКО у выхода
	Codec     string `json:"codec,omitempty"`     // batched|legacy
	Key       string `json:"key,omitempty"`       // общий секрет AES-256-GCM
	Name      string `json:"name,omitempty"`      // человеческая пометка, на процесс не влияет
}

// EncodeLink строит openflux://-ссылку: base64url без паддинга поверх JSON.
func EncodeLink(p LinkPayload) (string, error) {
	if p.V == 0 {
		p.V = 1
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return LinkScheme + base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeLink разбирает openflux://-ссылку (и голый base64-тела — фронт
// подставляет то, что скопировали). Ошибки — текстом для пользователя.
func DecodeLink(link string) (LinkPayload, error) {
	var p LinkPayload
	body := strings.TrimSpace(link)
	body = strings.TrimPrefix(body, LinkScheme)
	if body == "" {
		return p, fmt.Errorf("пустая ссылка")
	}
	body = strings.TrimRight(body, "=")
	body = strings.NewReplacer("-", "+", "_", "/").Replace(body)
	if pad := len(body) % 4; pad != 0 {
		body += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return p, fmt.Errorf("не удалось декодировать base64: %w", err)
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, fmt.Errorf("не удалось разобрать JSON: %w", err)
	}
	if p.V == 0 {
		p.V = 1
	}
	return p, nil
}
