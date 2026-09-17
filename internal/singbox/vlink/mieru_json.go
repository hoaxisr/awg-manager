// Package vlink: поддержка канонического JSON-конфига клиента mieru.
//
// Панели (RIXXX / Panel-Naive-Mieru и другие) экспортируют настройки mieru
// как protojson pb.ClientConfig — тот же формат, что принимает
// `mieru apply config <FILE>`: profiles[]{profileName, user, servers[]{
// ipAddress|domainName, portBindings[]{port|portRange, protocol}}, mtu,
// multiplexing}, activeProfile плюс клиентские настройки (rpcPort,
// socks5Port, loggingLevel, ...), которые для outbound не нужны.
//
// Entry points: IsMieruClientJSON детектирует формат; ParseMieruClientJSON
// возвращает BatchResult, по форме идентичный ParseBatch / ParseClashBody /
// ParseSingboxBody. Конверсия профиля в outbounds переиспользует
// selectMieruProfile + mieruProfileToOutbounds из mieru.go — JSON и
// mieru:// (base64 protobuf) дают идентичные outbounds.
package vlink

import (
	"bytes"
	"encoding/json"
	"fmt"

	pb "github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"google.golang.org/protobuf/encoding/protojson"
)

// utf8BOM — маркер порядка байт, который Windows-редакторы и некоторые
// панели дописывают в начало экспортированного файла. encoding/json и
// protojson его не терпят, поэтому срезаем сами.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// stripUTF8BOM возвращает body без ведущего UTF-8 BOM (если он есть).
func stripUTF8BOM(body []byte) []byte {
	return bytes.TrimPrefix(body, utf8BOM)
}

// IsMieruClientJSON reports whether body looks like a canonical mieru client
// config JSON (protojson pb.ClientConfig): JSON-объект с массивом "profiles"
// и БЕЗ "outbounds" — sing-box JSON сохраняет приоритет в каскадах
// детекции. Дешёвая структурная проверка в стиле IsSingboxJSON: probe-struct
// вместо полного protojson.Unmarshal, лишние ключи не материализуются.
//
// Терпимые false-positives: {"profiles":[]} проходит детекцию, а
// ParseMieruClientJSON возвращает точную ошибку «нет профилей» — это
// правильный UX (пользователь видит, что формат распознан, но конфиг пуст).
func IsMieruClientJSON(body []byte) bool {
	trimmed := trimLeadingSpace(stripUTF8BOM(body))
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var probe struct {
		Profiles  []json.RawMessage `json:"profiles"`
		Outbounds json.RawMessage   `json:"outbounds"`
	}
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return false
	}
	return probe.Profiles != nil && len(probe.Outbounds) == 0
}

// ParseMieruClientJSON парсит канонический mieru client config JSON и
// возвращает BatchResult в семантике ParseBatch: успешные outbounds в
// Outbounds, ошибки — в Errors (одна запись на весь документ, LineIdx 0).
// DiscardUnknown: панели дописывают свои поля поверх protojson-схемы,
// они не должны валить импорт.
func ParseMieruClientJSON(body []byte) BatchResult {
	out := BatchResult{}
	fail := func(msg string) BatchResult {
		out.Errors = append(out.Errors, ParseError{LineIdx: 0, Scheme: "mieru-json", Message: msg, Node: true})
		return out
	}
	cfg := &pb.ClientConfig{}
	opts := protojson.UnmarshalOptions{DiscardUnknown: true}
	if err := opts.Unmarshal(stripUTF8BOM(body), cfg); err != nil {
		return fail(fmt.Sprintf("не удалось разобрать mieru JSON: %s", err))
	}
	if len(cfg.GetProfiles()) == 0 {
		return fail("в mieru-конфиге нет профилей")
	}
	profile, err := selectMieruProfile(cfg)
	if err != nil {
		return fail(fmt.Sprintf("не удалось обработать mieru-конфиг: %s", err))
	}
	if len(profile.GetServers()) == 0 {
		return fail("в mieru-конфиге нет серверов")
	}
	parsed, err := mieruProfileToOutbounds(profile)
	if err != nil {
		return fail(fmt.Sprintf("не удалось обработать mieru-конфиг: %s", err))
	}
	out.Outbounds = parsed
	return out
}
