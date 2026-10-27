package subscription

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/vlink"
)

func TestApplyDiff_DistinctUsernamesWithSharedPassword(t *testing.T) {
	for _, scheme := range []string{"naive+http", "naive+https"} {
		t.Run(scheme, func(t *testing.T) {
			var parsed []vlink.ParsedOutbound
			var links []string
			for _, username := range []string{"alice", "bob", "alice"} {
				link := fmt.Sprintf("%s://%s:shared-test-password@proxy.example:1080", scheme, username)
				p, err := vlink.ParseLink(link)
				if err != nil {
					t.Fatalf("parse test link: %v", err)
				}
				parsed = append(parsed, *p)
				links = append(links, link)
			}

			// An account that was already distinguishable keeps its stored tag.
			legacyTag := StableTag("accounts", parsed[0])
			single := ApplyDiff("accounts", []string{legacyTag}, []vlink.ParsedOutbound{parsed[0], parsed[2]})
			if len(single.Existing) != 1 || len(single.New) != 0 || len(single.Orphan) != 0 || single.SkippedDuplicate != 1 {
				t.Fatalf("a single account changed its legacy tag: %+v", single)
			}

			first := ApplyDiff("accounts", nil, parsed)
			if len(first.New) != 2 || first.SkippedDuplicate != 1 {
				t.Fatalf("distinct accounts were discarded: New=%d SkippedDuplicate=%d, want 2 and 1", len(first.New), first.SkippedDuplicate)
			}
			tags := make(map[string]string)
			var current []string
			for _, member := range first.New {
				var outbound struct {
					Username string `json:"username"`
				}
				if err := json.Unmarshal(member.Out.Outbound, &outbound); err != nil {
					t.Fatal(err)
				}
				tags[outbound.Username] = member.Tag
				current = append(current, member.Tag)
			}
			if tags["alice"] == "" || tags["bob"] == "" || tags["alice"] == tags["bob"] {
				t.Fatalf("both accounts must survive with distinct tags: %v", tags)
			}

			preview, err := (&Service{}).previewBody([]byte(strings.Join(links, "\n")), "text/plain")
			if err != nil {
				t.Fatal(err)
			}
			if len(preview) != 2 {
				t.Fatalf("preview discarded a distinct account: got %d members, want 2", len(preview))
			}
			previewKeys := make(map[string]bool)
			for _, member := range preview {
				previewKeys[member.Key] = true
			}
			for _, tag := range current {
				if !previewKeys[tag[len(tag)-8:]] {
					t.Errorf("preview exclusion key does not match refresh tag %q", tag)
				}
			}

			// Reordering a feed must not replace either account or change its tag.
			next := ApplyDiff("accounts", current, []vlink.ParsedOutbound{parsed[1], parsed[0], parsed[2]})
			if len(next.New) != 0 || len(next.Orphan) != 0 || len(next.Existing) != 2 || next.SkippedDuplicate != 1 {
				t.Fatalf("unchanged accounts did not survive refresh: New=%d Existing=%d Orphan=%d SkippedDuplicate=%d", len(next.New), len(next.Existing), len(next.Orphan), next.SkippedDuplicate)
			}
			for _, member := range next.Existing {
				var outbound struct {
					Username string `json:"username"`
				}
				if err := json.Unmarshal(member.Out.Outbound, &outbound); err != nil {
					t.Fatal(err)
				}
				if member.Tag != tags[outbound.Username] {
					t.Errorf("tag changed for account %q", outbound.Username)
				}
			}
		})
	}
}

// Один server:port:пароль с другим шифром или обфускацией — другой сервер, а
// не дубль (F737).
func TestApplyDiff_DistinctCipherOnSharedPassword(t *testing.T) {
	ss := func(method string) string {
		return "ss://" + base64.RawURLEncoding.EncodeToString([]byte(method+":shared")) + "@h.example:8388#" + method
	}
	for name, links := range map[string][]string{
		"shadowsocks method": {ss("aes-128-gcm"), ss("chacha20-ietf-poly1305"), ss("aes-128-gcm")},
		"shadowsocks obfs host": {
			"ss://aes-256-gcm:shared@h.example:8388?plugin=obfs-local%3Bobfs%3Dtls%3Bobfs-host%3Da.example#A",
			"ss://aes-256-gcm:shared@h.example:8388?plugin=obfs-local%3Bobfs%3Dtls%3Bobfs-host%3Db.example#B",
			"ss://aes-256-gcm:shared@h.example:8388?plugin=obfs-local%3Bobfs%3Dtls%3Bobfs-host%3Da.example#A",
		},
		"shadowsocks plugin": {
			"ss://aes-256-gcm:shared@h.example:8388?plugin=obfs-local%3Bmode%3Dwebsocket#A",
			"ss://aes-256-gcm:shared@h.example:8388?plugin=v2ray-plugin%3Bmode%3Dwebsocket#B",
			"ss://aes-256-gcm:shared@h.example:8388?plugin=obfs-local%3Bmode%3Dwebsocket#A",
		},
		"hysteria2 obfs type": {
			"hysteria2://shared@h.example:443?sni=h&obfs=salamander&obfs-password=one#A",
			"hysteria2://shared@h.example:443?sni=h&obfs=gecko&obfs-password=one#B",
			"hysteria2://shared@h.example:443?sni=h&obfs=salamander&obfs-password=one#A",
		},
		"hysteria2 obfs": {
			"hysteria2://shared@h.example:443?sni=h&obfs=salamander&obfs-password=one#A",
			"hysteria2://shared@h.example:443?sni=h&obfs=salamander&obfs-password=two#B",
			"hysteria2://shared@h.example:443?sni=h&obfs=salamander&obfs-password=one#A",
		},
	} {
		t.Run(name, func(t *testing.T) {
			var parsed []vlink.ParsedOutbound
			for _, link := range links {
				p, err := vlink.ParseLink(link)
				if err != nil {
					t.Fatalf("parse %q: %v", link, err)
				}
				parsed = append(parsed, *p)
			}
			first := ApplyDiff("ciphers", nil, parsed)
			if len(first.New) != 2 || first.SkippedDuplicate != 1 || first.New[0].Tag == first.New[1].Tag {
				t.Fatalf("want 2 distinct servers and 1 duplicate: %+v", first)
			}
			var current []string
			for _, m := range first.New {
				current = append(current, m.Tag)
			}

			preview, err := (&Service{}).previewBody([]byte(strings.Join(links, "\n")), "text/plain")
			if err != nil {
				t.Fatal(err)
			}
			previewKeys := map[string]bool{}
			for _, m := range preview {
				previewKeys[m.Key] = true
			}
			for _, tag := range current {
				if !previewKeys[tag[len(tag)-8:]] {
					t.Errorf("preview key does not match refresh tag %q", tag)
				}
			}

			next := ApplyDiff("ciphers", current, []vlink.ParsedOutbound{parsed[1], parsed[2], parsed[0]})
			if len(next.Existing) != 2 || len(next.New) != 0 || len(next.Orphan) != 0 {
				t.Fatalf("reordered feed changed tags: %+v", next)
			}
		})
	}
}

// Группа, которую различает уже уровень cred (#1092, вышел в 2.19.21), не
// должна переехать на новый уровень wire.
func TestChooseKeys_CredTierGroupKeepsTags(t *testing.T) {
	var parsed []vlink.ParsedOutbound
	for _, user := range []string{"alice", "bob"} {
		p, err := vlink.ParseLink("naive+https://" + user + ":shared@h.example:443")
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, *p)
	}
	for i, k := range chooseKeys(parsed) {
		if k != keysOf(parsed[i]).cred {
			t.Errorf("member %d moved off the cred tier", i)
		}
	}
}
