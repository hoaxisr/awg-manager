package signature

import (
	"encoding/hex"
	mrand "math/rand"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestSIP_RegisterThenDigestRegister(t *testing.T) {
	pk, err := buildSIP(mrand.New(mrand.NewSource(11)))
	if err != nil {
		t.Fatal(err)
	}
	assertAllowedTokens(t, pk.I1)
	assertAllowedTokens(t, pk.I2)
	if pk.I3 != "" {
		t.Fatal("SIP fills I1 and I2 only")
	}
	render := func(p string) string { // <b> → текст, <rc N> → N букв 'x'
		var sb strings.Builder
		for _, m := range tagRe.FindAllStringSubmatch(p, -1) {
			switch m[1] {
			case "b":
				b, _ := hex.DecodeString(m[2][2:])
				sb.Write(b)
			case "rc":
				n, _ := strconv.Atoi(m[2])
				sb.WriteString(strings.Repeat("x", n))
			default:
				t.Fatalf("unexpected %s in SIP", m[1])
			}
		}
		return sb.String()
	}
	r1, r2 := render(pk.I1), render(pk.I2)
	for _, s := range []string{r1, r2} {
		if !strings.HasPrefix(s, "REGISTER sip:") || !strings.HasSuffix(s, "\r\n\r\n") {
			t.Fatalf("frame: %q", s)
		}
		if !strings.Contains(s, "Via: SIP/2.0/UDP ") || !strings.Contains(s, ";branch=z9hG4bKxxxxxxxxxxxxxxxxxx;rport") {
			t.Fatal("Via/branch must carry <rc 18>")
		}
		if !strings.Contains(s, "Content-Length: 0\r\n") {
			t.Fatal("Content-Length")
		}
	}
	line := func(s, h string) string {
		for _, l := range strings.Split(s, "\r\n") {
			if strings.HasPrefix(l, h) {
				return l
			}
		}
		return ""
	}
	if line(r1, "Call-ID:") == "" || line(r1, "Call-ID:") != line(r2, "Call-ID:") {
		t.Fatal("Call-ID must be static and equal in both")
	}
	if line(r1, "From:") != line(r2, "From:") {
		t.Fatal("From tag must match")
	}
	if !strings.HasPrefix(line(r1, "CSeq:"), "CSeq: ") || line(r1, "CSeq:") == line(r2, "CSeq:") {
		t.Fatal("CSeq must increment")
	}
	auth := line(r2, "Authorization:")
	for _, f := range []string{"Digest ", "username=\"", "realm=\"", "nonce=\"", "uri=\"sip:", "response=\"", "algorithm=MD5", "qop=auth", "nc=00000001", "cnonce=\""} {
		if !strings.Contains(auth, f) {
			t.Fatalf("Authorization lacks %q: %s", f, auth)
		}
	}
	if strings.Contains(r1, "Authorization:") {
		t.Fatal("first REGISTER has no Authorization")
	}
	if m := regexp.MustCompile(`response="([0-9a-f]{32})"`).FindStringSubmatch(auth); m == nil {
		t.Fatal("response must be 32 hex")
	}
}

func TestSIP_HeaderOrder(t *testing.T) {
	pk, err := buildSIP(mrand.New(mrand.NewSource(11)))
	if err != nil {
		t.Fatal(err)
	}
	render := func(p string) string {
		var sb strings.Builder
		for _, m := range tagRe.FindAllStringSubmatch(p, -1) {
			switch m[1] {
			case "b":
				b, _ := hex.DecodeString(m[2][2:])
				sb.Write(b)
			case "rc":
				n, _ := strconv.Atoi(m[2])
				sb.WriteString(strings.Repeat("x", n))
			}
		}
		return sb.String()
	}
	r1, r2 := render(pk.I1), render(pk.I2)

	headerName := func(l string) string {
		if i := strings.Index(l, ":"); i >= 0 {
			return l[:i]
		}
		return l
	}
	names := func(s string) []string {
		lines := strings.Split(s, "\r\n")
		var out []string
		// первая строка — request line, вторая — Via (обе не в списке заголовков ниже).
		for _, l := range lines[2:] {
			if l == "" {
				continue
			}
			out = append(out, headerName(l))
		}
		return out
	}

	want := []string{"Via", "Max-Forwards", "From", "To", "Call-ID", "CSeq", "Contact",
		"User-Agent", "Allow", "Supported", "Allow-Events", "Expires", "Content-Length"}
	got1 := append([]string{"Via"}, names(r1)...)
	if strings.Join(got1, ",") != strings.Join(want, ",") {
		t.Fatalf("I1 header order = %v, want %v", got1, want)
	}

	got2 := append([]string{"Via"}, names(r2)...)
	authIdx := -1
	for i, n := range got2 {
		if n == "Authorization" {
			authIdx = i
		}
	}
	cseqIdx := -1
	for i, n := range got2 {
		if n == "CSeq" {
			cseqIdx = i
		}
	}
	if authIdx == -1 || cseqIdx == -1 || authIdx != cseqIdx+1 {
		t.Fatalf("I2 Authorization must follow CSeq immediately: %v", got2)
	}

	line := func(s, h string) string {
		for _, l := range strings.Split(s, "\r\n") {
			if strings.HasPrefix(l, h) {
				return l
			}
		}
		return ""
	}
	from := line(r1, "From:")
	to := line(r1, "To:")
	if i := strings.Index(from, ";tag="); i < 0 || from[:i] != "From: "+to[len("To: "):] {
		t.Fatalf("To must equal From without ;tag=…: From=%q To=%q", from, to)
	}
}
