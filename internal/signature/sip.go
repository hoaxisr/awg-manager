// Adapted from payloadGen (MIT) — https://github.com/Sketchystan1/payloadGen
package signature

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	mrand "math/rand"
	"strconv"
	"strings"
)

func init() { builders["sip"] = buildSIP }

// Таблицы §5.3 — дословно, только те, что нужны REGISTER.
var sipUserAgents = []string{
	"Linphone/5.2.5 (belle-sip/5.3.90)", "Zoiper rv2.10.15-mod", "MicroSIP/3.21.6",
	"baresip 3.8.0", "Blink 6.0.4 (Windows)", "Asterisk PBX 20.7.0",
}

var sipDisplayNames = []string{
	"Alice Carter", "Bob Smith", "Support Desk", "Sales Queue",
	"NOC Bridge", "Reception", "Operator", "Dispatch",
}

var sipSupportedHeaders = []string{
	"replaces, outbound, path, timer", "outbound, path, gruu, 100rel",
	"timer, replaces, resource-priority", "gruu, outbound, path, sec-agree",
}

var sipAllowHeaders = []string{
	"INVITE, ACK, CANCEL, OPTIONS, BYE, REFER, NOTIFY, INFO, MESSAGE, SUBSCRIBE",
	"INVITE, ACK, CANCEL, OPTIONS, BYE, UPDATE, MESSAGE",
	"INVITE, ACK, CANCEL, OPTIONS, BYE, PRACK, UPDATE",
}

var sipAllowEventsHeaders = []string{
	"presence, message-summary, refer", "dialog, presence, refer", "presence, kpml, talk",
}

var sipLocalPorts = []int{5060, 5062, 5070, 5080, 5160}

var sipExpires = []int{300, 600, 900, 1200, 1800, 3600}

var sipUserPrefixes = []string{
	"100", "101", "200", "300", "400", "500",
	"alice", "bob", "support", "sales", "noc", "ops",
}

// buildSIP — два пакета: REGISTER (I1) и повторный REGISTER с Digest (I2), как
// после ответа 401. Все поля, кроме Via branch, статичны внутри генерации —
// Call-ID, From tag и абонент обязаны совпадать в обоих пакетах, иначе это не
// один диалог. Рантайм-случаен только branch: <rc 18> вместо hex-хвоста
// z9hG4bK. Digest-строки в payloadGen нет (§5.5) — она собрана по RFC 2617.
func buildSIP(r *mrand.Rand) (GeneratedPackets, error) {
	host := pickHost(r)
	localIP := randomPrivateIPv4(r)
	localPort := strconv.Itoa(sipLocalPorts[r.Intn(len(sipLocalPorts))])
	fromUser := sipUserPart(r)
	display := sipDisplayNames[r.Intn(len(sipDisplayNames))]
	tag := randHex(r, 6)                  // From tag: 12 hex
	callID := randHex(r, 12) + "@" + host // Call-ID: 24 hex@host
	userAgent := sipUserAgents[r.Intn(len(sipUserAgents))]
	allow := sipAllowHeaders[r.Intn(len(sipAllowHeaders))]
	supported := sipSupportedHeaders[r.Intn(len(sipSupportedHeaders))]
	allowEvents := sipAllowEventsHeaders[r.Intn(len(sipAllowEventsHeaders))]
	expires := strconv.Itoa(sipExpires[r.Intn(len(sipExpires))])
	cseq := 1 + r.Intn(50)

	// REGISTER: To повторяет From без tag (§5.1).
	addr := `"` + display + `" <sip:` + fromUser + "@" + host + ">"
	contact := "<sip:" + fromUser + "@" + localIP + ":" + localPort + ";transport=udp>"

	head := "REGISTER sip:" + host + " SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP " + localIP + ":" + localPort + ";branch=z9hG4bK"

	// afterBranch — всё после branch (начинается с ;rport); порядок заголовков
	// §5.2, Authorization сразу за CSeq.
	afterBranch := func(cseqNum int, auth string) string {
		lines := []string{
			"Max-Forwards: 70",
			"From: " + addr + ";tag=" + tag,
			"To: " + addr,
			"Call-ID: " + callID,
			"CSeq: " + strconv.Itoa(cseqNum) + " REGISTER",
		}
		if auth != "" {
			lines = append(lines, auth)
		}
		lines = append(lines,
			"Contact: "+contact,
			"User-Agent: "+userAgent,
			"Allow: "+allow,
			"Supported: "+supported,
			"Allow-Events: "+allowEvents,
			"Expires: "+expires,
			"Content-Length: 0",
			"", "", // завершающая пустая строка
		)
		return ";rport\r\n" + strings.Join(lines, "\r\n")
	}

	packet := func(cseqNum int, auth string) string {
		return tokB([]byte(head)) + "<rc 18>" + tokB([]byte(afterBranch(cseqNum, auth)))
	}

	return GeneratedPackets{
		I1: packet(cseq, ""),
		I2: packet(cseq+1, sipAuthorization(r, fromUser, host)),
	}, nil
}

// sipAuthorization — Digest qop=auth по RFC 2617. Пароль случаен и никуда не
// уходит: он нужен только чтобы response выглядел как настоящий MD5.
func sipAuthorization(r *mrand.Rand, user, host string) string {
	password := randomLetters(r, 12)
	nonce := randHex(r, 16) // nonce: 32 hex
	cnonce := randHex(r, 8) // cnonce: 16 hex
	uri := "sip:" + host
	ha1 := md5Hex(user + ":" + host + ":" + password)
	ha2 := md5Hex("REGISTER:" + uri)
	response := md5Hex(ha1 + ":" + nonce + ":00000001:" + cnonce + ":auth:" + ha2)
	return `Authorization: Digest username="` + user + `", realm="` + host +
		`", nonce="` + nonce + `", uri="` + uri + `", response="` + response +
		`", algorithm=MD5, qop=auth, nc=00000001, cnonce="` + cnonce + `"`
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// sipUserPart — «alice734» и подобные (§5.1).
func sipUserPart(r *mrand.Rand) string {
	return sipUserPrefixes[r.Intn(len(sipUserPrefixes))] + strconv.Itoa(100+r.Intn(900))
}

// randomPrivateIPv4 — адрес из RFC 1918 (§5.1).
func randomPrivateIPv4(r *mrand.Rand) string {
	switch r.Intn(3) {
	case 0:
		return fmt.Sprintf("10.%d.%d.%d", r.Intn(256), r.Intn(256), 1+r.Intn(254))
	case 1:
		return fmt.Sprintf("172.%d.%d.%d", 16+r.Intn(16), r.Intn(256), 1+r.Intn(254))
	default:
		return fmt.Sprintf("192.168.%d.%d", r.Intn(256), 1+r.Intn(254))
	}
}

func randomLetters(r *mrand.Rand, n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[r.Intn(len(alphabet))]
	}
	return string(b)
}
