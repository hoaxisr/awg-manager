package awg3endpoint

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var tagRe = regexp.MustCompile(`^[\p{L}\p{N} ._-]+$`)

// awg-подмножество, нужное ТОЛЬКО для инвариантов; остальное — passthrough.
type awgShape struct {
	Type    string `json:"type"`
	PrivKey string `json:"private_key"`
	S1      int    `json:"s1"`
	S2      int    `json:"s2"`
	S3      int    `json:"s3"`
	S4      int    `json:"s4"`
	HPK     string `json:"header_protection_key"`
	Peers   []struct {
		PublicKey string `json:"public_key"`
		Address   string `json:"address"`
	} `json:"peers"`
}

// Parse разворачивает RouteBox-envelope, валидирует инварианты и тег, и
// возвращает Record с непрозрачным Endpoint (весь awg-объект as-is).
func Parse(raw []byte, tag string, existingTags map[string]bool) (Record, error) {
	// envelope {success,data} → data
	var env struct {
		Success *bool           `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	body := raw
	if json.Unmarshal(raw, &env) == nil && env.Success != nil {
		// Явный RouteBox-конверт (есть поле success). success=false или пустой
		// data — это ошибка/пустой ответ RouteBox, а не endpoint: даём понятную
		// ошибку вместо проваливания в анмаршал конверта как endpoint (иначе
		// вводящий в заблуждение ErrNotAwg).
		if !*env.Success || len(env.Data) == 0 {
			return Record{}, fmt.Errorf("RouteBox-ответ без data (success=%v)", *env.Success)
		}
		body = env.Data
	}

	var a awgShape
	if err := json.Unmarshal(body, &a); err != nil {
		return Record{}, fmt.Errorf("невалидный JSON: %w", err)
	}
	if a.Type != "awg" {
		return Record{}, ErrNotAwg
	}
	if strings.TrimSpace(a.PrivKey) == "" {
		return Record{}, ErrMissingKey
	}
	if len(a.Peers) == 0 || a.Peers[0].PublicKey == "" || a.Peers[0].Address == "" {
		return Record{}, ErrMissingPeer
	}
	if a.HPK != "" && (a.S1 < 8 || a.S2 < 8 || a.S3 < 8 || a.S4 < 8) {
		return Record{}, ErrHeaderProtectionS
	}

	t := strings.TrimSpace(tag)
	if t == "" || !tagRe.MatchString(t) || existingTags[t] {
		return Record{}, fmt.Errorf("%w: %q", ErrTag, tag)
	}

	// endpoint — исходный body без изменений (passthrough)
	return Record{Tag: t, Endpoint: append(json.RawMessage(nil), body...)}, nil
}
