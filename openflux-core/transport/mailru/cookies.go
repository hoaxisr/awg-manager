package mailru

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxMailruCookiesInputBytes = 256 << 10
	errNoValidMailruCookies    = "mailru cookies: no valid mail.ru cookies found"
)

// ParseCookiesInput parses Mail.ru cookies from a raw string or file path.
// Supported formats are Cookie-Editor JSON arrays, flat or CookieStore JSON
// objects, Netscape cookies.txt files, and raw HTTP Cookie headers.
// Returned errors never quote raw cookie values.
func ParseCookiesInput(input string) (map[string]string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, nil
	}

	raw := trimmed
	if !strings.ContainsAny(trimmed, "\r\n;") {
		if info, err := os.Stat(trimmed); err == nil && info.Mode().IsRegular() {
			data, err := readLimitedCookieFile(trimmed)
			if err != nil {
				return nil, err
			}
			raw = string(data)
		}
	}

	raw = strings.TrimPrefix(raw, "\xef\xbb\xbf")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	var jar map[string]string
	var err error
	now := time.Now().Unix()

	switch {
	case strings.HasPrefix(raw, "[") || strings.HasPrefix(raw, "{"):
		jar, err = parseJSONCookies(raw, now)
	case looksLikeNetscapeCookies(raw):
		jar, err = parseNetscapeCookies(raw, now)
	default:
		jar = parseHeaderCookies(raw)
	}
	if err != nil {
		return nil, err
	}
	if len(jar) == 0 {
		return nil, errors.New(errNoValidMailruCookies)
	}
	return jar, nil
}

func readLimitedCookieFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("mailru cookies: open file: %w", err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxMailruCookiesInputBytes+1))
	if err != nil {
		return nil, fmt.Errorf("mailru cookies: read file: %w", err)
	}
	if len(data) > maxMailruCookiesInputBytes {
		return nil, errors.New("mailru cookies: file too large")
	}
	return bytes.TrimSpace(data), nil
}

func isMailruDomain(domain string) bool {
	d := strings.ToLower(strings.TrimSpace(domain))
	d = strings.TrimPrefix(d, ".")
	if d == "" {
		return true
	}
	return d == "mail.ru" || strings.HasSuffix(d, ".mail.ru")
}

func isExpiredUnix(exp float64, now int64) bool {
	if exp <= 0 {
		return false
	}
	if exp > 1e12 {
		exp /= 1000
	}
	return int64(exp) <= now
}

func parseJSONCookies(raw string, now int64) (map[string]string, error) {
	if strings.HasPrefix(raw, "[") {
		var entries []struct {
			Name           string  `json:"name"`
			Value          string  `json:"value"`
			Domain         string  `json:"domain"`
			ExpirationDate float64 `json:"expirationDate"`
		}
		if err := json.Unmarshal([]byte(raw), &entries); err != nil {
			return nil, errors.New("mailru cookies: invalid JSON cookie array")
		}
		jar := make(map[string]string)
		for _, e := range entries {
			name := strings.TrimSpace(e.Name)
			val := strings.TrimSpace(e.Value)
			if name == "" || val == "" || strings.ContainsAny(name+val, "\r\n") {
				continue
			}
			if !isMailruDomain(e.Domain) {
				continue
			}
			if isExpiredUnix(e.ExpirationDate, now) {
				continue
			}
			jar[name] = val
		}
		return jar, nil
	}

	var nested map[string]map[string]string
	if err := json.Unmarshal([]byte(raw), &nested); err == nil && len(nested) > 0 {
		keys := make([]string, 0, len(nested))
		for k := range nested {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var firstNonEmpty map[string]string
		for _, k := range keys {
			candidate := sanitizeStringJar(nested[k])
			if len(candidate) == 0 {
				continue
			}
			if candidate["Mpop"] != "" {
				return candidate, nil
			}
			if firstNonEmpty == nil {
				firstNonEmpty = candidate
			}
		}
		if firstNonEmpty != nil {
			return firstNonEmpty, nil
		}
	}

	var flat map[string]string
	if err := json.Unmarshal([]byte(raw), &flat); err != nil {
		return nil, errors.New("mailru cookies: invalid JSON cookie object")
	}
	return sanitizeStringJar(flat), nil
}

func sanitizeStringJar(in map[string]string) map[string]string {
	out := make(map[string]string)
	for k, v := range in {
		name := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		if name == "" || val == "" || strings.ContainsAny(name+val, "\r\n") {
			continue
		}
		out[name] = val
	}
	return out
}

func looksLikeNetscapeCookies(raw string) bool {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || (strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#HttpOnly_")) {
			continue
		}
		if len(strings.Split(line, "\t")) == 7 {
			return true
		}
	}
	return false
}

func parseNetscapeCookies(raw string, now int64) (map[string]string, error) {
	jar := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || (strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#HttpOnly_")) {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 7 {
			continue
		}
		domain := strings.TrimPrefix(fields[0], "#HttpOnly_")
		if !isMailruDomain(domain) {
			continue
		}
		if fields[4] != "" {
			exp, err := strconv.ParseInt(strings.TrimSpace(fields[4]), 10, 64)
			if err != nil {
				continue
			}
			if exp > 0 && exp <= now {
				continue
			}
		}
		name := strings.TrimSpace(fields[5])
		val := strings.TrimSpace(fields[6])
		if name == "" || val == "" || strings.ContainsAny(name+val, "\r\n") {
			continue
		}
		jar[name] = val
	}
	return jar, nil
}

func parseHeaderCookies(raw string) map[string]string {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) >= len("cookie:") && strings.EqualFold(trimmed[:len("cookie:")], "cookie:") {
		trimmed = strings.TrimSpace(trimmed[len("cookie:"):])
	}
	jar := make(map[string]string)
	parts := strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == ';' || r == '\n' || r == '\r'
	})
	for _, part := range parts {
		name, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		val = strings.TrimSpace(val)
		if name == "" || val == "" || strings.ContainsAny(name+val, "\r\n") {
			continue
		}
		jar[name] = val
	}
	return jar
}
