package vlessserver

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// XHTTPTuning encapsulates low-latency tuning parameters for VLESS XHTTP / FiniteProto transport.
type XHTTPTuning struct {
	Preset             string `json:"preset"`                           // "live_low_latency" | "balanced" | "cdn_get" | "custom"
	UplinkMethod       string `json:"uplink_method"`                    // "GET" | "POST" | "DELETE"
	MinPostsIntervalMs string `json:"min_posts_interval_ms"`            // "5-10"
	MaxBufferedPosts   int    `json:"max_buffered_posts"`               // 1..1000, default 30
	StreamUpServerSecs string `json:"stream_up_server_secs"`            // "20-80"
	StreamPathSuffix   string `json:"stream_path_suffix"`               // "stream.ts"
	EnableXmux         bool   `json:"enable_xmux"`                      // false for low latency
	XPaddingBytes      string `json:"x_padding_bytes"`                  // "100-1000"
	XPaddingHeader     string `json:"x_padding_header"`                 // "Referer"
	XPaddingPlacement  string `json:"x_padding_placement"`              // "queryInHeader"
	XPaddingKey        string `json:"x_padding_key"`                    // "_dc" or "x_padding"
	XPaddingMethod     string `json:"x_padding_method"`                 // "tokenish" or "repeat-x"
	XPaddingObfsMode   bool   `json:"x_padding_obfs_mode,omitempty"`    // true when padding is obfuscated in header query
	ScMaxEachPostBytes int    `json:"sc_max_each_post_bytes,omitempty"` // max bytes per post (e.g. 1000000)
	SessionPlacement   string `json:"session_placement"`                // "query"
	SessionKey         string `json:"session_key"`                      // "sid"
	SeqPlacement       string `json:"seq_placement"`                    // "query"
	SeqKey             string `json:"seq_key"`                          // "page"
	FPChunkBytes       int    `json:"fp_chunk_bytes,omitempty"`         // FiniteProto v2 chunk_bytes (16384..262144; default 131072)
	FPMaxWays          int    `json:"fp_max_ways,omitempty"`            // FiniteProto v2 max_ways (1..8; default 1)
}

func DefaultLiveLowLatencyTuning() XHTTPTuning {
	return XHTTPTuning{
		Preset:             "live_low_latency",
		UplinkMethod:       "POST",
		MinPostsIntervalMs: "",
		MaxBufferedPosts:   1000,
		ScMaxEachPostBytes: 1000000,
		StreamUpServerSecs: "",
		StreamPathSuffix:   "",
		EnableXmux:         false,
		XPaddingBytes:      "100-500",
		XPaddingHeader:     "Referer",
		XPaddingPlacement:  "queryInHeader",
		XPaddingKey:        "_dc",
		XPaddingMethod:     "tokenish",
		XPaddingObfsMode:   true,
		SessionPlacement:   "query",
		SessionKey:         "sid",
		SeqPlacement:       "query",
		SeqKey:             "page",
		FPChunkBytes:       131072,
		FPMaxWays:          1,
	}
}

func DefaultBalancedTuning() XHTTPTuning {
	return XHTTPTuning{
		Preset:             "balanced",
		UplinkMethod:       "POST",
		MinPostsIntervalMs: "",
		MaxBufferedPosts:   1000,
		ScMaxEachPostBytes: 1000000,
		StreamUpServerSecs: "20-80",
		StreamPathSuffix:   "",
		EnableXmux:         true,
		XPaddingBytes:      "100-500",
		XPaddingHeader:     "Referer",
		XPaddingPlacement:  "queryInHeader",
		XPaddingKey:        "_dc",
		XPaddingMethod:     "tokenish",
		XPaddingObfsMode:   true,
		SessionPlacement:   "query",
		SessionKey:         "sid",
		SeqPlacement:       "query",
		SeqKey:             "page",
		FPChunkBytes:       131072,
		FPMaxWays:          1,
	}
}

func DefaultCDNGetTuning() XHTTPTuning {
	return XHTTPTuning{
		Preset:             "cdn_get",
		UplinkMethod:       "GET",
		MinPostsIntervalMs: "5-10",
		MaxBufferedPosts:   200,
		ScMaxEachPostBytes: 65536,
		StreamUpServerSecs: "20-80",
		StreamPathSuffix:   "",
		EnableXmux:         false,
		XPaddingBytes:      "100-500",
		XPaddingHeader:     "Referer",
		XPaddingPlacement:  "queryInHeader",
		XPaddingKey:        "_dc",
		XPaddingMethod:     "tokenish",
		XPaddingObfsMode:   true,
		SessionPlacement:   "query",
		SessionKey:         "sid",
		SeqPlacement:       "query",
		SeqKey:             "page",
		FPChunkBytes:       131072,
		FPMaxWays:          1,
	}
}

var rangeRegex = regexp.MustCompile(`^\d+(-\d+)?$`)

func validateRangeString(val, fieldName string) error {
	val = strings.TrimSpace(val)
	if val == "" {
		return nil
	}
	if !rangeRegex.MatchString(val) {
		return fmt.Errorf("%s must match format 'N' or 'min-max', got %q", fieldName, val)
	}
	if strings.Contains(val, "-") {
		parts := strings.Split(val, "-")
		minVal, _ := strconv.Atoi(parts[0])
		maxVal, _ := strconv.Atoi(parts[1])
		if minVal > maxVal {
			return fmt.Errorf("%s: min (%d) cannot be greater than max (%d)", fieldName, minVal, maxVal)
		}
	}
	return nil
}

func sanitizeStreamPathSuffix(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimLeft(raw, "/")
	raw = strings.ReplaceAll(raw, "..", "")
	if idx := strings.IndexAny(raw, "?#"); idx != -1 {
		raw = raw[:idx]
	}
	return strings.TrimSpace(raw)
}

func normalizeFPTuning(t *XHTTPTuning, chunkBytes, maxWays int) {
	if chunkBytes <= 0 {
		chunkBytes = 131072
	} else if chunkBytes < 16384 {
		chunkBytes = 16384
	} else if chunkBytes > 262144 {
		chunkBytes = 262144
	}
	if maxWays <= 0 {
		maxWays = 1
	} else if maxWays > 8 {
		maxWays = 8
	}
	t.FPChunkBytes = chunkBytes
	t.FPMaxWays = maxWays
}

func ValidateAndNormalizeXHTTPTuning(t *XHTTPTuning) error {
	if t == nil {
		return nil
	}
	fpChunkBytes := t.FPChunkBytes
	fpMaxWays := t.FPMaxWays
	preset := strings.ToLower(strings.TrimSpace(t.Preset))
	switch preset {
	case "live_low_latency", "cdn_keendns":
		*t = DefaultLiveLowLatencyTuning()
		normalizeFPTuning(t, fpChunkBytes, fpMaxWays)
		return nil
	case "balanced", "keendns_direct":
		*t = DefaultBalancedTuning()
		normalizeFPTuning(t, fpChunkBytes, fpMaxWays)
		return nil
	case "cdn_get", "strict_cdn":
		*t = DefaultCDNGetTuning()
		normalizeFPTuning(t, fpChunkBytes, fpMaxWays)
		return nil
	case "custom":
		t.Preset = "custom"
		method := strings.ToUpper(strings.TrimSpace(t.UplinkMethod))
		if method != "GET" && method != "POST" && method != "DELETE" {
			if method == "" {
				method = "GET"
			} else {
				return fmt.Errorf("invalid uplink_method %q: must be GET, POST, or DELETE", t.UplinkMethod)
			}
		}
		t.UplinkMethod = method

		if t.MinPostsIntervalMs != "" {
			if err := validateRangeString(t.MinPostsIntervalMs, "min_posts_interval_ms"); err != nil {
				return err
			}
		}

		if t.MaxBufferedPosts <= 0 {
			t.MaxBufferedPosts = 30
		} else if t.MaxBufferedPosts < 1 || t.MaxBufferedPosts > 1000 {
			return fmt.Errorf("max_buffered_posts must be between 1 and 1000, got %d", t.MaxBufferedPosts)
		}

		if t.ScMaxEachPostBytes <= 0 {
			t.ScMaxEachPostBytes = 1000000
		}

		if t.StreamUpServerSecs != "" {
			if err := validateRangeString(t.StreamUpServerSecs, "stream_up_server_secs"); err != nil {
				return err
			}
		}

		t.StreamPathSuffix = sanitizeStreamPathSuffix(t.StreamPathSuffix)

		if t.XPaddingBytes == "" {
			t.XPaddingBytes = "100-1000"
		}
		if err := validateRangeString(t.XPaddingBytes, "x_padding_bytes"); err != nil {
			return err
		}

		if t.XPaddingHeader == "" {
			t.XPaddingHeader = "Referer"
		}
		if t.XPaddingPlacement == "" {
			t.XPaddingPlacement = "queryInHeader"
		}
		if t.XPaddingPlacement == "queryInHeader" || t.XPaddingKey != "" {
			t.XPaddingObfsMode = true
		}
		normalizeFPTuning(t, fpChunkBytes, fpMaxWays)
		return nil
	default:
		*t = DefaultLiveLowLatencyTuning()
		normalizeFPTuning(t, fpChunkBytes, fpMaxWays)
		return nil
	}
}
