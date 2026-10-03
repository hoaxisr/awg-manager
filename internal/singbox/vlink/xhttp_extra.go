package vlink

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// xmuxFields maps the camelCase keys Xray puts inside ?extra=.xmux to the
// snake_case fields our sing-box fork accepts (option.V2RayXHTTPXmuxOptions).
// The bool says whether the value is a Range (number, "N" or "N-M"); the rest
// are plain integers.
var xmuxFields = map[string]struct {
	key     string
	isRange bool
}{
	"maxConcurrency":   {"max_concurrency", true},
	"maxConnections":   {"max_connections", true},
	"cMaxReuseTimes":   {"c_max_reuse_times", true},
	"hMaxRequestTimes": {"h_max_request_times", true},
	"hMaxReusableSecs": {"h_max_reusable_secs", true},
	"hKeepAlivePeriod": {"h_keep_alive_period", false},
}

type extraKind int

const (
	kindRange         extraKind = iota // number, "N" or "N-M"
	kindPositiveRange                  // a Range that must not start at zero
	kindBool
	kindInt
	kindHeaders
	kindString
)

// extraFields maps the camelCase keys of Xray's xhttpSettings to the
// snake_case fields of option.V2RayXHTTPBaseOptions in our fork. Keys carried
// by dedicated query parameters (host, path, mode) and downloadSettings — a
// whole nested outbound — are deliberately absent.
var extraFields = map[string]struct {
	key  string
	kind extraKind
}{
	"headers":       {"headers", kindHeaders},
	"xPaddingBytes": {"x_padding_bytes", kindPositiveRange},
	"noGRPCHeader":  {"no_grpc_header", kindBool},
	"noSSEHeader":   {"no_sse_header", kindBool},
	// sc*-диапазоны — positive: ноль у них означает РАЗНОЕ у Xray и у нашего
	// sing-box, и расхождение стоит процесса. Xray считает нулевую верхнюю
	// границу признаком «не задано» и подставляет умолчание
	// (splithttp/config.go: `c.ScMaxEachPostBytes == nil || …To == 0`), а порт
	// той же функции в sing-box проверяет только nil
	// (option/v2ray_transport.go:376) и затем ПАНИКУЕТ на `From <= 0`
	// (transport/v2rayxhttp/client.go:185) — падает весь процесс, а с ним и
	// прокси. Поэтому диапазон, начинающийся с нуля, не переносим вовсе: у
	// sing-box включится ровно то умолчание, которое подставил бы Xray (#908).
	"scMaxEachPostBytes":   {"sc_max_each_post_bytes", kindPositiveRange},
	"scMinPostsIntervalMs": {"sc_min_posts_interval_ms", kindPositiveRange},
	"scMaxBufferedPosts":   {"sc_max_buffered_posts", kindInt},
	"scStreamUpServerSecs": {"sc_stream_up_server_secs", kindPositiveRange},
	"uplinkHTTPMethod":     {"uplink_http_method", kindString},
	"seqKey":               {"seq_key", kindString},
	"seqPlacement":         {"seq_placement", kindString},
	"sessionIDKey":         {"session_key", kindString},
	"sessionIdKey":         {"session_key", kindString},
	"sessionKey":           {"session_key", kindString},
	"sessionIDPlacement":   {"session_placement", kindString},
	"sessionIdPlacement":   {"session_placement", kindString},
	"sessionPlacement":     {"session_placement", kindString},
	"xPaddingHeader":       {"x_padding_header", kindString},
	"xPaddingKey":          {"x_padding_key", kindString},
	"xPaddingMethod":       {"x_padding_method", kindString},
	"xPaddingObfsMode":     {"x_padding_obfs_mode", kindBool},
	"xPaddingPlacement":    {"x_padding_placement", kindString},
	"uplinkDataKey":        {"uplink_data_key", kindString},
	"uplinkDataPlacement":  {"uplink_data_placement", kindString},
	"uplinkChunkSize":      {"uplink_chunk_size", kindInt},
}

// canonicalExtraKeys maps sing-box snake_case transport fields back to the
// canonical camelCase keys Xray uses inside ?extra=.
var canonicalExtraKeys = []struct {
	snake string
	camel string
}{
	{"headers", "headers"},
	{"x_padding_bytes", "xPaddingBytes"},
	{"no_grpc_header", "noGRPCHeader"},
	{"no_sse_header", "noSSEHeader"},
	{"sc_max_each_post_bytes", "scMaxEachPostBytes"},
	{"sc_min_posts_interval_ms", "scMinPostsIntervalMs"},
	{"sc_max_buffered_posts", "scMaxBufferedPosts"},
	{"sc_stream_up_server_secs", "scStreamUpServerSecs"},
	{"uplink_http_method", "uplinkHTTPMethod"},
	{"seq_key", "seqKey"},
	{"seq_placement", "seqPlacement"},
	{"session_key", "sessionIDKey"},
	{"session_placement", "sessionIDPlacement"},
	{"x_padding_header", "xPaddingHeader"},
	{"x_padding_key", "xPaddingKey"},
	{"x_padding_method", "xPaddingMethod"},
	{"x_padding_obfs_mode", "xPaddingObfsMode"},
	{"x_padding_placement", "xPaddingPlacement"},
	{"uplink_data_key", "uplinkDataKey"},
	{"uplink_data_placement", "uplinkDataPlacement"},
	{"uplink_chunk_size", "uplinkChunkSize"},
}

// clashXHTTPFields maps mihomo's kebab-case xhttp-opts keys to the camelCase
// keys Xray uses inside extra=, so a Clash import goes through exactly the same
// mapping and validation as a share link.
// Reference: mihomo adapter/outbound/vless.go (XHTTPOptions).
var clashXHTTPFields = map[string]string{
	"headers":                  "headers",
	"x-padding-bytes":          "xPaddingBytes",
	"no-grpc-header":           "noGRPCHeader",
	"no-sse-header":            "noSSEHeader",
	"sc-max-each-post-bytes":   "scMaxEachPostBytes",
	"sc-min-posts-interval-ms": "scMinPostsIntervalMs",
	"uplink-http-method":       "uplinkHTTPMethod",
	"seq-key":                  "seqKey",
	"seq-placement":            "seqPlacement",
	"session-key":              "sessionIDKey",
	"session-id-key":           "sessionIDKey",
	"session-placement":        "sessionIDPlacement",
	"session-id-placement":     "sessionIDPlacement",
	"x-padding-header":         "xPaddingHeader",
	"x-padding-key":            "xPaddingKey",
	"x-padding-method":         "xPaddingMethod",
	"x-padding-obfs-mode":      "xPaddingObfsMode",
	"x-padding-placement":      "xPaddingPlacement",
	"uplink-data-key":          "uplinkDataKey",
	"uplink-data-placement":    "uplinkDataPlacement",
	"uplink-chunk-size":        "uplinkChunkSize",
}

// clashXmuxFields maps mihomo's reuse-settings — its name for xmux — to the
// camelCase keys of Xray's extra.xmux.
var clashXmuxFields = map[string]string{
	"max-concurrency":     "maxConcurrency",
	"max-connections":     "maxConnections",
	"c-max-reuse-times":   "cMaxReuseTimes",
	"h-max-request-times": "hMaxRequestTimes",
	"h-max-reusable-secs": "hMaxReusableSecs",
	"h-keep-alive-period": "hKeepAlivePeriod",
}

// xhttpExtraFromClash renders mihomo's xhttp-opts as an Xray extra= object.
// Values travel untouched: parseXHTTPExtra validates them downstream.
func xhttpExtraFromClash(opts map[string]any) string {
	extra := map[string]any{}
	for kebab, camel := range clashXHTTPFields {
		if v, ok := opts[kebab]; ok {
			extra[camel] = v
		}
	}
	if reuse, ok := opts["reuse-settings"].(map[string]any); ok {
		xmux := map[string]any{}
		for kebab, camel := range clashXmuxFields {
			if v, ok := reuse[kebab]; ok {
				xmux[camel] = v
			}
		}
		if len(xmux) > 0 {
			extra["xmux"] = xmux
		}
	}
	if len(extra) == 0 {
		return ""
	}
	encoded, err := json.Marshal(extra)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// parseXHTTPExtra decodes the Xray ?extra= object into sing-box xhttp
// transport fields. sing-box decodes strictly, so anything unknown or of the
// wrong type is dropped rather than passed on: one bad share link must not
// make the whole config unloadable.
func parseXHTTPExtra(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var extra map[string]any
	if json.Unmarshal([]byte(raw), &extra) != nil {
		return nil
	}

	out := map[string]any{}
	for k, v := range extra {
		f, known := extraFields[k]
		if !known {
			continue
		}
		switch f.kind {
		case kindRange:
			if isRangeValue(v) {
				out[f.key] = v
			}
		case kindPositiveRange:
			// Ноль здесь ломает по-разному: x_padding_bytes «cannot be
			// disabled» и делает конфигурацию незагружаемой, а sc*-диапазоны
			// роняют процесс уже в рантайме (#908). В обоих случаях поле
			// просто не переносим — включается умолчание.
			if lo, _, ok := rangeBounds(v); ok && lo > 0 {
				out[f.key] = v
			}
		case kindBool:
			if b, ok := v.(bool); ok {
				out[f.key] = b
			}
		case kindInt:
			if isWholeNumber(v) {
				out[f.key] = v
			}
		case kindHeaders:
			if h := stringMap(v); len(h) > 0 {
				out[f.key] = h
			}
		case kindString:
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				out[f.key] = strings.TrimSpace(s)
			}
		}
	}
	if xmuxRaw, ok := extra["xmux"].(map[string]any); ok {
		xmux := map[string]any{}
		for k, v := range xmuxRaw {
			f, known := xmuxFields[k]
			if !known {
				continue
			}
			if f.isRange {
				if isRangeValue(v) {
					xmux[f.key] = v
				}
			} else if isWholeNumber(v) {
				xmux[f.key] = v
			}
		}
		// The option layer refuses both at once and that rejection takes the
		// whole config down; Xray's semantics are that concurrency wins.
		if rangeUpper(xmux["max_concurrency"]) > 0 && rangeUpper(xmux["max_connections"]) > 0 {
			delete(xmux, "max_connections")
		}
		if len(xmux) > 0 {
			out["xmux"] = xmux
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// xhttpExtraFromTransport rebuilds the Xray ?extra= object from an xhttp
// transport block, so a link imported with xmux carries it again when shared
// back out. Returns "" when there is nothing worth carrying.
func xhttpExtraFromTransport(transport map[string]any) string {
	extra := map[string]any{}
	for _, mapping := range canonicalExtraKeys {
		if v, ok := transport[mapping.snake]; ok {
			extra[mapping.camel] = v
		}
	}
	if xmuxRaw, ok := transport["xmux"].(map[string]any); ok {
		xmux := map[string]any{}
		for camel, f := range xmuxFields {
			if v, ok := xmuxRaw[f.key]; ok {
				xmux[camel] = v
			}
		}
		if len(xmux) > 0 {
			extra["xmux"] = xmux
		}
	}
	if len(extra) == 0 {
		return ""
	}
	encoded, err := json.Marshal(extra)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// stringMap keeps only string-valued headers and drops "host": the option
// layer rejects a headers entry named host (xhttp carries it top-level).
func stringMap(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for k, val := range m {
		s, ok := val.(string)
		if !ok || strings.EqualFold(k, "host") {
			continue
		}
		out[k] = s
	}
	return out
}

// rangeUpper returns the upper bound of an already-validated Range value, the
// same field the option layer compares against zero.
func rangeUpper(v any) int64 {
	_, hi, _ := rangeBounds(v)
	return hi
}

// isRangeValue reports whether v survives xbadoption.Range: a number, "N" or
// "N-M", decoded into int32 and refused when from > to.
func isRangeValue(v any) bool {
	_, _, ok := rangeBounds(v)
	return ok
}

// rangeBounds parses the Range forms Xray emits. Values out of int32 are
// refused because sing-box would fail the whole config on them; negatives are
// refused as a house rule — Xray never emits one, and for x_padding_bytes the
// option layer does reject it.
func rangeBounds(v any) (lo, hi int64, ok bool) {
	switch t := v.(type) {
	case float64:
		if !isWholeNumber(t) {
			return 0, 0, false
		}
		return int64(t), int64(t), true
	case string:
		loStr, hiStr, found := strings.Cut(t, "-")
		lo, err := strconv.ParseInt(loStr, 10, 32)
		if err != nil || lo < 0 {
			return 0, 0, false
		}
		if !found {
			return lo, lo, true
		}
		hi, err := strconv.ParseInt(hiStr, 10, 32)
		if err != nil || hi < lo {
			return 0, 0, false
		}
		return lo, hi, true
	}
	return 0, 0, false
}

// isWholeNumber reports whether v is a non-negative integer that fits int32 —
// the width every numeric xhttp option decodes into.
func isWholeNumber(v any) bool {
	n, ok := v.(float64)
	return ok && n == math.Trunc(n) && n >= 0 && n <= math.MaxInt32
}
