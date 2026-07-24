package awg3endpoint

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// eqJSON структурно сравнивает два JSON-байта (порядок ключей не важен).
func eqJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got невалидный JSON: %v\n%s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want невалидный JSON: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("не совпало\n got: %s\nwant: %s", got, want)
	}
}

func TestParseConf_Minimal(t *testing.T) {
	conf := `[Interface]
PrivateKey = CLIENTPRIV==
Address = 10.10.0.2/32

[Peer]
PublicKey = SERVERPUB==
Endpoint = vpn.example.com:51820
AllowedIPs = 0.0.0.0/0
PersistentKeepalive = 25`
	got, err := ParseConf(conf)
	if err != nil {
		t.Fatalf("ParseConf: %v", err)
	}
	eqJSON(t, got, `{
		"type":"awg","useIntegratedTun":false,
		"private_key":"CLIENTPRIV==",
		"address":["10.10.0.2/32"],
		"peers":[{
			"public_key":"SERVERPUB==",
			"address":"vpn.example.com","port":51820,
			"allowed_ips":["0.0.0.0/0"],
			"persistent_keepalive_interval":25
		}]
	}`)
}

func TestParseConf_FullAwg3(t *testing.T) {
	conf := `[Interface]
PrivateKey = CLIENTPRIV==
Address = 10.10.0.2/32
DNS = 1.1.1.1
MTU = 1420
Jc = 4
Jmin = 40
Jmax = 70
S1 = 50
S2 = 50
S3 = 12
S4 = 12
H1 = 1
H2 = 2
H3 = 3
H4 = 4
ContentPaddingAddition = 200-400
RekeyAfterTime = 120-150
RekeyTimeout = 5
RejectAfterTime = 180
KeepaliveTimeout = 25
MaxHandshakeAttempts = 18
I1 =  <b 0xde>
HeaderProtectionKey = HPKBASE64==

[Peer]
PublicKey = SERVERPUB==
PresharedKey = PSK==
Endpoint = vpn.example.com:51820
AllowedIPs = 0.0.0.0/0
PersistentKeepalive = 25`
	got, err := ParseConf(conf)
	if err != nil {
		t.Fatalf("ParseConf: %v", err)
	}
	eqJSON(t, got, `{
		"type":"awg","useIntegratedTun":false,
		"private_key":"CLIENTPRIV==","address":["10.10.0.2/32"],"mtu":1420,
		"jc":4,"jmin":40,"jmax":70,"s1":50,"s2":50,"s3":12,"s4":12,
		"h1":"1","h2":"2","h3":"3","h4":"4",
		"content_padding_addition":"200-400",
		"rekey_after_time":"120-150","rekey_timeout":"5","reject_after_time":"180",
		"keepalive_timeout":"25","max_handshake_attempts":"18",
		"i1":"<b 0xde>","header_protection_key":"HPKBASE64==",
		"peers":[{
			"public_key":"SERVERPUB==","preshared_key":"PSK==",
			"address":"vpn.example.com","port":51820,
			"allowed_ips":["0.0.0.0/0"],"persistent_keepalive_interval":25
		}]
	}`)
	// DNS выброшен
	if strings.Contains(string(got), "1.1.1.1") {
		t.Fatalf("DNS не должен попадать в endpoint: %s", got)
	}
}

func TestParseConf_Edge(t *testing.T) {
	t.Run("bare-ip нормализуется", func(t *testing.T) {
		got, err := ParseConf("[Interface]\nPrivateKey = K==\nAddress = 10.8.1.2\n[Peer]\nPublicKey = P==\nEndpoint = h:1\nAllowedIPs = 10.0.0.0/24, 1.2.3.4")
		if err != nil {
			t.Fatal(err)
		}
		s := string(got)
		if !strings.Contains(s, `"10.8.1.2/32"`) || !strings.Contains(s, `"1.2.3.4/32"`) || !strings.Contains(s, `"10.0.0.0/24"`) {
			t.Fatalf("bare-ip не нормализован: %s", s)
		}
	})
	t.Run("BOM срезается", func(t *testing.T) {
		_, err := ParseConf("\ufeff[Interface]\nPrivateKey = K==\n[Peer]\nPublicKey = P==\nEndpoint = h:1")
		if err != nil {
			t.Fatalf("BOM сломал парс: %v", err)
		}
	})
	t.Run("IPv6 endpoint в скобках", func(t *testing.T) {
		got, err := ParseConf("[Interface]\nPrivateKey = K==\n[Peer]\nPublicKey = P==\nEndpoint = [2001:db8::1]:51820")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(got), `"address":"2001:db8::1"`) || !strings.Contains(string(got), `"port":51820`) {
			t.Fatalf("IPv6 split неверный: %s", got)
		}
	})
	t.Run("несколько peer", func(t *testing.T) {
		got, err := ParseConf("[Interface]\nPrivateKey = K==\n[Peer]\nPublicKey = A==\nEndpoint = h:1\n[Peer]\nPublicKey = B==\nEndpoint = h:2")
		if err != nil {
			t.Fatal(err)
		}
		var obj struct {
			Peers []any `json:"peers"`
		}
		_ = json.Unmarshal(got, &obj)
		if len(obj.Peers) != 2 {
			t.Fatalf("ожидалось 2 peer, получено %d", len(obj.Peers))
		}
	})
	t.Run("ошибки", func(t *testing.T) {
		cases := map[string]string{
			"нет Interface":      "[Peer]\nPublicKey = P==\nEndpoint = h:1",
			"нет Peer":           "[Interface]\nPrivateKey = K==",
			"Endpoint без порта": "[Interface]\nPrivateKey = K==\n[Peer]\nPublicKey = P==\nEndpoint = host",
			"не-число в MTU":     "[Interface]\nPrivateKey = K==\nMTU = abc\n[Peer]\nPublicKey = P==\nEndpoint = h:1",
		}
		for name, conf := range cases {
			if _, err := ParseConf(conf); err == nil {
				t.Errorf("%s: ожидалась ошибка", name)
			}
		}
	})
}
