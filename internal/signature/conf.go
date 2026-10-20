package signature

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

// HeaderOrDefault — значение H-параметра для .conf. Пустым его писать НЕЛЬЗЯ:
// `awg setconf` на строке `H1 = ` отвергает ВЕСЬ файл (`Line unrecognized:
// 'H1='` + `Configuration parsing error`, rc=1 — стенд 08.09, F167), то есть
// туннель не стартует вовсе. Пустое поле означает «как в ванильном
// WireGuard», а там H1..H4 — это номера типов сообщений 1..4: ровно то, что
// показывает `awg showconf` на интерфейсе без header-параметров.
func HeaderOrDefault(v string, msgType int) string {
	if strings.TrimSpace(v) == "" {
		return strconv.Itoa(msgType)
	}
	return v
}

// WriteASCConf пишет хвост [Interface] клиентского .conf: числовые и
// header-параметры — из снимка ASC интерфейса (raw), сигнатуру I1–I5 — из
// записи ПИРА (CONTEXT.md «Сигнатура AWG»: у сервера своей нет).
//
// Сервер без ASC (Jc == 0 и без защиты заголовков) — обычный WireGuard: ни
// числовых параметров, ни сигнатуры в конфиг не пишем, иначе клиент получит
// обфускацию, которой на сервере нет. ASC 3.1 с Jc = 0 легален (сервер junk не
// шлёт), и без S/H/HP клиент к нему не подключится. Один writer на оба пути —
// managed-сервер (internal/managed) и системный (internal/api).
func WriteASCConf(b *strings.Builder, raw json.RawMessage, packets GeneratedPackets) {
	var ext struct {
		ndms.ASCParamsExtended
		HeaderProtectionKey string `json:"header-protection-key"`
	}
	if err := json.Unmarshal(raw, &ext); err != nil || (ext.Jc == 0 && ext.HeaderProtectionKey == "") {
		return
	}

	fmt.Fprintf(b, "Jc = %d\n", ext.Jc)
	fmt.Fprintf(b, "Jmin = %d\n", ext.Jmin)
	fmt.Fprintf(b, "Jmax = %d\n", ext.Jmax)
	fmt.Fprintf(b, "S1 = %d\n", ext.S1)
	fmt.Fprintf(b, "S2 = %d\n", ext.S2)
	fmt.Fprintf(b, "H1 = %s\n", HeaderOrDefault(ext.H1, 1))
	fmt.Fprintf(b, "H2 = %s\n", HeaderOrDefault(ext.H2, 2))
	fmt.Fprintf(b, "H3 = %s\n", HeaderOrDefault(ext.H3, 3))
	fmt.Fprintf(b, "H4 = %s\n", HeaderOrDefault(ext.H4, 4))

	if ext.S3 > 0 || ext.S4 > 0 {
		fmt.Fprintf(b, "S3 = %d\n", ext.S3)
		fmt.Fprintf(b, "S4 = %d\n", ext.S4)
	}
	for i, sig := range []string{packets.I1, packets.I2, packets.I3, packets.I4, packets.I5} {
		if sig != "" {
			fmt.Fprintf(b, "I%d = %s\n", i+1, sig)
		}
	}
	writeASC3Conf(b, raw)
}

// writeASC3Conf — параметры AWG 3.x из ASC интерфейса (5.02.A.11+, #688):
// без них клиент сервера 3.1 не подключится — 0 ответов (F681). Ключи и
// грамматика — как у туннелей (config.writeAWG3Params): пишется только
// заданное, пара start/end — "N" или "N-M", флаги — "on". rc отдаёт числа,
// поэтому принимаются и числа, и строки.
func writeASC3Conf(b *strings.Builder, raw json.RawMessage) {
	var asc map[string]json.RawMessage
	if json.Unmarshal(raw, &asc) != nil {
		return
	}
	num := func(k string) int {
		n, _ := strconv.Atoi(strings.Trim(string(asc[k]), `"`))
		return n
	}
	var hp string
	if json.Unmarshal(asc["header-protection-key"], &hp) == nil && hp != "" {
		fmt.Fprintf(b, "HeaderProtectionKey = %s\n", hp)
	}
	for _, r := range []struct{ conf, key string }{
		{"ContentPaddingAddition", "content-padding-addition"},
		{"RekeyAfterTime", "rekey-after-time"},
		{"RekeyTimeout", "rekey-timeout"},
		{"RejectAfterTime", "reject-after-time"},
		{"KeepaliveTimeout", "keepalive-timeout"},
		{"MaxHandshakeAttempts", "max-handshake-attempts"},
	} {
		lo, hi := num(r.key+"-start"), num(r.key+"-end")
		switch {
		case lo == 0 && hi == 0:
		case lo == hi:
			fmt.Fprintf(b, "%s = %d\n", r.conf, lo)
		default:
			fmt.Fprintf(b, "%s = %d-%d\n", r.conf, lo, hi)
		}
	}
	if num("random-trailers") != 0 {
		b.WriteString("RandomTrailers = on\n")
	}
	if num("disable-cookies") != 0 {
		b.WriteString("DisableCookies = on\n")
	}
}
