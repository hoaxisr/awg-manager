package logging

import (
	"strings"
	"testing"
)

// TestRedactURLs — адрес подписки несёт токен в пути, query или userinfo,
// share-link — uuid сервера в userinfo. Ошибки net/http и url.Parse
// цитируют адрес целиком, в том числе адрес редиректа, которого нет в
// настройках и который точной заменой не поймать.
func TestRedactURLs(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"path token": {
			`Get "https://sub.example.com/sub/AbC123secret": dial tcp: i/o timeout`,
			`Get "https://sub.example.com/…": dial tcp: i/o timeout`,
		},
		"query and userinfo": {
			`fetch https://user:pw@sub.example.com:8443/x?token=s3cret#frag failed`,
			`fetch https://sub.example.com:8443/… failed`,
		},
		"redirect hop": {
			`Get "https://cdn.provider.net/r/Zz9-token": x509: certificate signed by unknown authority`,
			`Get "https://cdn.provider.net/…": x509: certificate signed by unknown authority`,
		},
		"share link uuid": {
			`line 3 (vless): parse "vless://3a3b1c2e-9999-4321-aaaa-1234567890a1@h1.example:443?security=tls#A": invalid port`,
			`line 3 (vless): parse "vless://h1.example:443/…": invalid port`,
		},
		// Legacy ss:// packs method, password and address into one base64
		// blob that url.Parse reads as the host. No dot, so it is no host.
		"base64 blob as host": {
			`bad link ss://YWVzLTI1Ni1nY206cGFzc0AxLjIuMy40OjgzODg#name`,
			`bad link ss://<redacted>`,
		},
		"bare host kept": {
			`Get "https://sub.example.com": EOF`,
			`Get "https://sub.example.com": EOF`,
		},
		"ipv6 host": {
			`Get "http://[2001:db8::1]:8080/list?k=v": refused`,
			`Get "http://[2001:db8::1]:8080/…": refused`,
		},
		// The daemon's own hint lists schemes with nothing after them.
		"scheme list is not a URL": {
			`plain text со ссылками vless://, trojan://, ss://, tt:// (TrustTunnel)`,
			`plain text со ссылками vless://, trojan://, ss://, tt:// (TrustTunnel)`,
		},
		"no url": {`HTTP 404`, `HTTP 404`},
		// Userinfo may start with anything a URL allows: a percent-encoded
		// trojan password, base64 ss userinfo starting with + or /.
		"percent-encoded password": {
			`bad trojan://%21secret@h.example:443 link`,
			`bad trojan://h.example:443/… link`,
		},
		"underscore password": {
			`trojan://_secretpw@h.example:443`,
			`trojan://h.example:443/…`,
		},
		"base64 userinfo with plus": {
			`ss://+YWVzLTI1@h.example:1`,
			`ss://h.example:1/…`,
		},
		// Parentheses and quotes inside a path are part of it.
		"parenthesis in path": {
			`fetch https://sub.example.com/sub/tok(en)abc failed`,
			`fetch https://sub.example.com/… failed`,
		},
		// A scheme glued to a word character is still a URL.
		"no word boundary": {
			`x_https://sub.example.com/tok`,
			`x_https://sub.example.com/…`,
		},
		// Sentence punctuation after a URL is not part of the host.
		"trailing period": {
			`could not reach https://sub.example.com.`,
			`could not reach https://sub.example.com.`,
		},
		"trailing period after path": {
			`see https://sub.example.com/tok.`,
			`see https://sub.example.com/….`,
		},
		"two urls": {
			`https://a.example/t1 then https://b.example/t2`,
			`https://a.example/… then https://b.example/…`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := RedactURLs(tc.in); got != tc.want {
				t.Fatalf("RedactURLs(%q)\n got  %q\n want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRedactURLsLeavesNoSecret(t *testing.T) {
	in := `Get "https://u:hunter2@h.example/sub/tok3n?key=k3y#f": EOF; vless://3a3b1c2e-9999-4321-aaaa-1234567890a1@h.example:1`
	got := RedactURLs(in)
	for _, secret := range []string{"hunter2", "tok3n", "k3y", "3a3b1c2e"} {
		if strings.Contains(got, secret) {
			t.Fatalf("%q survived: %q", secret, got)
		}
	}
}
