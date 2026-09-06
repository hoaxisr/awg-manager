package obfuscator

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Ключи [instance], которые несёт клиентский .conf Phobos (buildClientObfConf)
// и которые мы понимаем. source-if/source-lport/verbose — наши, из файла не берутся.
var instanceOwnKeys = map[string]bool{"source-if": true, "source-lport": true, "verbose": true}

// ParseInstance читает секцию [instance]. present=false — секции нет.
// unknown — ключи, которых мы не знаем (логируются, импорт не роняют).
// mode=socks5 — отказ: это не WG-режим.
func ParseInstance(conf string) (o *storage.Obfuscator, unknown []string, present bool, err error) {
	in := false
	for _, raw := range strings.Split(conf, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			in = strings.EqualFold(line, "[instance]")
			if in {
				present = true
				o = &storage.Obfuscator{Flavor: storage.ObfuscatorFlavorPhobos, Masking: "AUTO"}
			}
			continue
		}
		if !in {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		switch k {
		case "mode":
			if !strings.EqualFold(v, "wireguard") && !strings.EqualFold(v, "wg") {
				return nil, nil, true, fmt.Errorf("[instance] mode=%s не поддерживается (только wireguard; socks5-режим Phobos — не туннель)", v)
			}
		case "target":
			o.Target = v
		case "key":
			o.Key = v
		case "masking":
			o.Masking = strings.ToUpper(v)
		case "max-dummy", "idle-timeout", "obfuscate-bytes":
			num, convErr := strconv.Atoi(v)
			if convErr != nil {
				return nil, nil, true, fmt.Errorf("[instance] %s = %q: ожидается число", k, v)
			}
			switch k {
			case "max-dummy":
				o.MaxDummy = num
			case "idle-timeout":
				o.IdleTimeout = num
			default:
				o.ObfuscateBytes = num
			}
		default:
			if !instanceOwnKeys[k] {
				unknown = append(unknown, k)
			}
		}
	}
	return o, unknown, present, nil
}

// StripInstance убирает секцию [instance] (до следующего заголовка или конца).
func StripInstance(conf string) string {
	var out []string
	skip := false
	for _, raw := range strings.Split(conf, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") {
			skip = strings.EqualFold(line, "[instance]")
		}
		if !skip {
			out = append(out, raw)
		}
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
}

// RenderInstance — секция [instance] для экспорта (round-trip, Q-допущение 7).
// source-lport не пишем: порт на другой машине выберут заново.
func RenderInstance(o *storage.Obfuscator) string {
	var b strings.Builder
	b.WriteString("[instance]\n")
	fmt.Fprintf(&b, "target = %s\nkey = %s\nmasking = %s\nmax-dummy = %d\n", o.Target, o.Key, o.Masking, o.MaxDummy)
	if o.IdleTimeout > 0 {
		fmt.Fprintf(&b, "idle-timeout = %d\n", o.IdleTimeout)
	}
	if o.Flavor == storage.ObfuscatorFlavorPhobos && o.ObfuscateBytes > 0 {
		fmt.Fprintf(&b, "obfuscate-bytes = %d\n", o.ObfuscateBytes)
	}
	return b.String()
}

// NormalizeNoneValues убирает строки `ключ = none` во всех секциях: так
// phobos:// дописывает отсутствующие «обязательные» поля (docs/phobos-link-format.md §2.3).
func NormalizeNoneValues(conf string) string {
	var out []string
	for _, raw := range strings.Split(conf, "\n") {
		if _, v, ok := strings.Cut(raw, "="); ok && strings.EqualFold(strings.TrimSpace(v), "none") {
			continue
		}
		out = append(out, raw)
	}
	return strings.Join(out, "\n")
}

const phobosScheme = "phobos://"

var versionedLink = regexp.MustCompile(`^v\d+\.`)

func IsPhobosLink(s string) bool { return strings.HasPrefix(strings.TrimSpace(s), phobosScheme) }

// DecodePhobosLink: phobos://<base64url(conf)>#<urlencoded name>. Версии нет
// (v1); префикс `v2.` — чужой формат, отказ.
func DecodePhobosLink(link string) (conf, name string, err error) {
	rest := strings.TrimPrefix(strings.TrimSpace(link), phobosScheme)
	payload, frag, _ := strings.Cut(rest, "#")
	// v1 без префикса; `v2.<payload>` и далее — чужой формат (docs/phobos-link-format.md §6).
	if versionedLink.MatchString(payload) {
		return "", "", errors.New("phobos://-ссылка неизвестной версии")
	}
	payload = strings.TrimRight(payload, "=")
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", "", fmt.Errorf("phobos://: %w", err)
	}
	if name, err = url.QueryUnescape(frag); err != nil {
		name = frag
	}
	return string(raw), name, nil
}
