package warpscout

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseAccountImport_Formats(t *testing.T) {
	priv, wantPub, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("vernette warpscout json", func(t *testing.T) {
		raw := `{
			"id": "dev-json-1",
			"token": "tok-json-1",
			"private_key": "` + priv + `",
			"peer_public_key": "` + WarpPeerPublicKey + `",
			"address_v4": "172.16.0.5",
			"address_v6": "2606:4700:110::5",
			"client_id": "AQID",
			"account_type": "free",
			"warp_plus": true
		}`
		acct, err := ParseAccountImport(raw)
		if err != nil {
			t.Fatalf("ParseAccountImport: %v", err)
		}
		if acct.ID != "dev-json-1" || acct.Token != "tok-json-1" || !acct.HasToken || !acct.Imported {
			t.Fatalf("метаданные аккаунта: %+v", acct)
		}
		if acct.PublicKey != wantPub || acct.AddressV4 != "172.16.0.5" || acct.ClientID != "AQID" {
			t.Fatalf("ключи/адрес: %+v", acct)
		}
		if acct.WarpPlus {
			t.Fatalf("account_type=free без quota не должен стать WARP+: %+v", acct)
		}
	})

	t.Run("raw cloudflare reg json with quota", func(t *testing.T) {
		raw := `{
			"id": "cf-reg-1",
			"token": "cf-tok-1",
			"private_key": "` + priv + `",
			"account": {"account_type": "limited", "warp_plus": true, "quota": 1000000, "license": "lic-123"},
			"config": {
				"client_id": "BAUG",
				"peers": [{"public_key": "` + WarpPeerPublicKey + `"}],
				"interface": {"addresses": {"v4": "172.16.0.9/32", "v6": "2606:4700:110::9/128"}}
			}
		}`
		acct, err := ParseAccountImport(raw)
		if err != nil {
			t.Fatalf("ParseAccountImport: %v", err)
		}
		if !acct.HasToken || !acct.WarpPlus || acct.License != "lic-123" || acct.AddressV4 != "172.16.0.9" || acct.ClientID != "BAUG" {
			t.Fatalf("неверно разобран ответ /reg: %+v", acct)
		}
	})

	t.Run("camelCase json with reserved array and no token", func(t *testing.T) {
		raw := `{
			"privateKey": "` + priv + `",
			"addressV4": "172.16.0.8",
			"reserved": [1, 2, 3],
			"warpPlus": true
		}`
		acct, err := ParseAccountImport(raw)
		if err != nil {
			t.Fatalf("ParseAccountImport: %v", err)
		}
		if acct.HasToken || acct.Token != "" || !strings.HasPrefix(acct.ID, "imported-") {
			t.Fatalf("без device id должен быть токен-фри импорт: %+v", acct)
		}
		if acct.Reserved() != [3]int{1, 2, 3} || !acct.WarpPlus {
			t.Fatalf("reserved/warpPlus: %+v reserved=%v", acct, acct.Reserved())
		}
	})

	t.Run("wireguard amneziawg conf", func(t *testing.T) {
		conf := `
[Interface]
PrivateKey = ` + priv + ` # user key
Address = 172.16.0.12/32, 2606:4700:110::12/128
# Reserved = 10, 20, 30
MTU = 1280

[Peer]
PublicKey = ` + WarpPeerPublicKey + `
Endpoint = 162.159.192.1:2408
`
		acct, err := ParseAccountImport(conf)
		if err != nil {
			t.Fatalf("ParseAccountImport(.conf): %v", err)
		}
		if acct.HasToken || acct.Token != "" || acct.AddressV4 != "172.16.0.12" || acct.AddressV6 != "2606:4700:110::12/128" {
			t.Fatalf("ошибка разбора .conf: %+v", acct)
		}
		if acct.Reserved() != [3]int{10, 20, 30} {
			t.Fatalf("ожидалось reserved [10 20 30], получили %v", acct.Reserved())
		}
	})

	t.Run("wgcf-account.toml", func(t *testing.T) {
		toml := `
device_id = 'wgcf-dev-1'
access_token = 'wgcf-tok-1'
private_key = '` + priv + `'
license_key = 'abcd-1234'
`
		acct, err := ParseAccountImport(toml)
		if err != nil {
			t.Fatalf("ParseAccountImport(.toml): %v", err)
		}
		if !acct.HasToken || acct.ID != "wgcf-dev-1" || acct.Token != "wgcf-tok-1" || acct.License != "abcd-1234" {
			t.Fatalf("ошибка разбора .toml: %+v", acct)
		}
		if acct.AddressV4 != DefaultTunnelIPv4 || acct.PeerPublicKey != WarpPeerPublicKey {
			t.Fatalf("значения по умолчанию .toml: %+v", acct)
		}
	})
}

func TestParseAccountImport_RejectsInvalidInputs(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"empty", "   \n "},
		{"oversized", strings.Repeat("a", maxImportBytes+1)},
		{"unknown format", "just some random text without keys"},
		{"bad private key in conf", "[Interface]\nPrivateKey = not-base64!\n"},
		{"bad private key in json", `{"private_key":"AQID"}`},
		{"bad reserved out of range", `{"private_key":"gZwP6wviWSsSUhp4AbX0iLuzKKVVTwlB5asMAQ+BWBE=","reserved":[1,2,300]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseAccountImport(tc.raw); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("ожидалась ErrInvalidInput, получили %v", err)
			}
		})
	}
}

// Декодер base64 пропускает переводы строк, а ключи ложатся строкой в .conf.
func TestParseAccountImport_CanonicalizesKeys(t *testing.T) {
	priv, pub, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	split := func(v string) string { return v[:2] + `\r\n` + v[2:] }
	acct, err := ParseAccountImport(`{"private_key":"` + split(priv) + `","peer_public_key":"` + split(WarpPeerPublicKey) +
		`","client_id":"` + split("AQID") + `","id":"t.0a1b2c3d-0000-4000-8000-0123456789ab","token":"tok","address_v6":"2606:4700:110:0::9/128"}`)
	if err != nil {
		t.Fatal(err)
	}
	if acct.PrivateKey != priv || acct.PublicKey != pub || acct.PeerPublicKey != WarpPeerPublicKey || acct.ClientID != "AQID" {
		t.Fatalf("значения не приведены к канонической записи: %+v", acct)
	}
	if acct.ID != "t.0a1b2c3d-0000-4000-8000-0123456789ab" || acct.AddressV6 != "2606:4700:110::9/128" {
		t.Fatalf("id/address: %+v", acct)
	}
	if conf := BuildConf(acct, AWGParams{}, ProtocolWG, "162.159.192.1:2408"); strings.Contains(conf, "\r") {
		t.Fatalf("перевод строки попал в .conf: %q", conf)
	}
}

func TestParseAccountImport_RejectsHostileFields(t *testing.T) {
	priv, _, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	for name, extra := range map[string]string{
		"id с путём":            `"id":"../reg/x"`,
		"id с точками":          `"id":".."`,
		"id с запросом":         `"id":"a?b=c"`,
		"id с переводом строки": `"id":"a\nb"`,
		"id длинный":            `"id":"` + strings.Repeat("a", 65) + `"`,
		"ipv6 не адрес":         `"address_v6":"::1\nPostUp = x"`,
		"ipv6 с зоной":          `"address_v6":"fe80::1%eth0"`,
		"ipv4 вместо ipv6":      `"address_v6":"1.2.3.4"`,
		"client_id не 3 байта":  `"client_id":"AQIDBA=="`,
		"ключ пира не 32 байта": `"peer_public_key":"AQID"`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseAccountImport(`{"private_key":"` + priv + `",` + extra + `}`); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("ожидалась ErrInvalidInput, получили %v", err)
			}
		})
	}
}

// Отвечать на регистрацию может сторонний relay: поля уходят в .conf и в путь запроса.
func TestRegisterAccount_RejectsMalformedResponse(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	const tmpl = `{"id":%q,"token":"t","warp_enabled":true,"config":{"client_id":%q,"peers":[{"public_key":%q}],"interface":{"addresses":{"v4":%q,"v6":%q}}}}`
	for name, tc := range map[string]struct {
		id, clientID, peer, v4, v6 string
		ok                         bool
	}{
		"корректный":      {"abc-1", "AQID", key, "172.16.0.2", "2606:4700::1", true},
		"путь в id":       {"a/../x", "AQID", key, "172.16.0.2", "2606:4700::1", false},
		"битый пир":       {"abc-1", "AQID", "AAAA\nEndpoint = 1", "172.16.0.2", "2606:4700::1", false},
		"битый client_id": {"abc-1", "AQ", key, "172.16.0.2", "2606:4700::1", false},
		"битый v4":        {"abc-1", "AQID", key, "172.16.0.2\nDNS = 1", "2606:4700::1", false},
		"битый v6":        {"abc-1", "AQID", key, "172.16.0.2", "x", false},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, tmpl, tc.id, tc.clientID, tc.peer, tc.v4, tc.v6)
			}))
			defer srv.Close()
			_, err := registerAccountWithBase(context.Background(), srv.Client(), srv.URL)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, ok = %v", err, tc.ok)
			}
		})
	}

	// Перевод строки внутри base64 декодер пропускает — сохраняем каноническое значение.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, tmpl, "abc-1", "AQID", key[:10]+"\n"+key[10:], "", "")
	}))
	defer srv.Close()
	a, err := registerAccountWithBase(context.Background(), srv.Client(), srv.URL)
	if err != nil || a.PeerPublicKey != key {
		t.Fatalf("peer = %q, err = %v", a.PeerPublicKey, err)
	}
}
