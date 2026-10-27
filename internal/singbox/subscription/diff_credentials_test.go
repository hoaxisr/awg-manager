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
			checkTwoServersOneDuplicate(t, strings.Join(links, "\n"))
		})
	}
}

// checkTwoServersOneDuplicate: выдача из трёх записей — два разных сервера и
// точный повтор первого. Разбор и отбраковка — рабочим путём, как у refresh и
// превью. Оба сервера выживают с разными тегами, ключи превью совпадают с
// тегами refresh, перестановка строк теги не меняет.
func checkTwoServersOneDuplicate(t *testing.T, body string) {
	t.Helper()
	parsed := partitionParsedOutbounds("feed", parseSubscriptionBody([]byte(body), "text/plain").Outbounds).Valid
	if len(parsed) != 3 {
		t.Fatalf("feed must hold 3 members, got %d", len(parsed))
	}
	first := ApplyDiff("feed", nil, parsed)
	if len(first.New) != 2 || first.SkippedDuplicate != 1 || first.New[0].Tag == first.New[1].Tag {
		t.Fatalf("want 2 distinct servers and 1 duplicate: %+v", first)
	}
	var current []string
	for _, m := range first.New {
		current = append(current, m.Tag)
	}

	preview, err := (&Service{}).previewBody([]byte(body), "text/plain")
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

	next := ApplyDiff("feed", current, []vlink.ParsedOutbound{parsed[1], parsed[2], parsed[0]})
	if len(next.Existing) != 2 || len(next.New) != 0 || len(next.Orphan) != 0 {
		t.Fatalf("reordered feed changed tags: %+v", next)
	}
}

// Один адрес и учётка, но другой транспорт-строкой (mieru TCP/UDP) или другие
// диапазоны портов (mieru, hysteria2 mport) — другой сервер (F738, F739).
func TestApplyDiff_DistinctPortsOrPlainTransport(t *testing.T) {
	mieruClash := func(name, transport string) string {
		return "  - {name: " + name + ", type: mieru, server: h.example, port: 2999, transport: " + transport + ", username: u, password: p}\n"
	}
	// Диапазон без основного порта отбраковывает partitionParsedOutbounds,
	// поэтому у сервера есть и порт, и диапазон. Разбирается только активный
	// профиль, так что варианты — серверы одного профиля.
	mieruServer := func(portRange string) string {
		return `{"ipAddress":"h.example","portBindings":[{"port":2999,"protocol":"TCP"},{"portRange":"` + portRange + `","protocol":"TCP"}]}`
	}
	for name, body := range map[string]string{
		"mieru transport": "proxies:\n" + mieruClash("A", "TCP") + mieruClash("B", "UDP") + mieruClash("A", "TCP"),
		"mieru port range": `{"profiles":[{"profileName":"default","user":{"name":"u","password":"p"},"servers":[` +
			mieruServer("3000-3010") + "," + mieruServer("4000-4010") + "," + mieruServer("3000-3010") + `]}]}`,
		"hysteria2 mport": strings.Join([]string{
			"hysteria2://shared@h.example:443?sni=h&mport=20000-21000#A",
			"hysteria2://shared@h.example:443?sni=h&mport=30000-31000#B",
			"hysteria2://shared@h.example:443?sni=h&mport=20000-21000#A",
		}, "\n"),
	} {
		t.Run(name, func(t *testing.T) {
			checkTwoServersOneDuplicate(t, body)
		})
	}
}

// Группа, которую различает уже уровень wire (F737), не должна переехать на
// уровень ports.
func TestChooseKeys_WireTierGroupKeepsTags(t *testing.T) {
	var parsed []vlink.ParsedOutbound
	for _, pass := range []string{"one", "two"} {
		p, err := vlink.ParseLink("hysteria2://shared@h.example:443?sni=h&obfs=salamander&obfs-password=" + pass)
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, *p)
	}
	for i, k := range chooseKeys(parsed) {
		if k != keysOf(parsed[i]).wire {
			t.Errorf("member %d moved off the wire tier", i)
		}
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
