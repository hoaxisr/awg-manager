package vlink

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	pb "github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// mieruClientJSONSample — канонический экспорт панели (формат
// `mieru apply config`): profiles + activeProfile + клиентские настройки
// (rpcPort/socks5Port/loggingLevel), которые парсер должен игнорировать.
const mieruClientJSONSample = `{
  "profiles": [
    {
      "profileName": "default",
      "user": { "name": "baozi", "password": "manlianpenfen" },
      "servers": [
        {
          "ipAddress": "12.34.56.78",
          "portBindings": [
            { "port": 6666, "protocol": "TCP" },
            { "portRange": "9998-9999", "protocol": "TCP" },
            { "port": 6489, "protocol": "UDP" }
          ]
        }
      ],
      "mtu": 1400,
      "multiplexing": { "level": "MULTIPLEXING_HIGH" }
    }
  ],
  "activeProfile": "default",
  "rpcPort": 8964,
  "socks5Port": 1080,
  "loggingLevel": "INFO"
}`

func TestParseMieruClientJSON_CanonicalSample(t *testing.T) {
	if !IsMieruClientJSON([]byte(mieruClientJSONSample)) {
		t.Fatal("canonical sample must be detected as mieru client JSON")
	}
	res := ParseMieruClientJSON([]byte(mieruClientJSONSample))
	if len(res.Errors) != 0 {
		t.Fatalf("errors: %+v", res.Errors)
	}
	if len(res.Outbounds) != 2 {
		t.Fatalf("got %d outbounds, want 2 (TCP+UDP)", len(res.Outbounds))
	}

	tcp := decodeOutbound(t, res.Outbounds[0])
	if tcp["type"] != "mieru" || tcp["transport"] != "TCP" {
		t.Fatalf("unexpected tcp outbound: %+v", tcp)
	}
	if tcp["server"] != "12.34.56.78" || tcp["username"] != "baozi" || tcp["password"] != "manlianpenfen" {
		t.Fatalf("bad identity fields: %+v", tcp)
	}
	if tcp["server_port"] != float64(6666) {
		t.Fatalf("server_port=%v want 6666", tcp["server_port"])
	}
	assertStringSlice(t, tcp["server_ports"], []string{"9998-9999"})
	if tcp["multiplexing"] != "MULTIPLEXING_HIGH" {
		t.Fatalf("multiplexing=%v", tcp["multiplexing"])
	}

	udp := decodeOutbound(t, res.Outbounds[1])
	if udp["transport"] != "UDP" || udp["server_port"] != float64(6489) {
		t.Fatalf("unexpected udp outbound: %+v", udp)
	}
	if res.Outbounds[0].Label != "default" || res.Outbounds[1].Label != "default" {
		t.Fatalf("labels: %q / %q, want profile name", res.Outbounds[0].Label, res.Outbounds[1].Label)
	}
}

// TestParseMieruClientJSON_EquivalentToMieruLink: один и тот же
// pb.ClientConfig, поданный как mieru:// (base64 бинарного protobuf) и как
// protojson-документ, обязан дать побайтно идентичные outbounds — обе
// дороги сходятся в selectMieruProfile + mieruProfileToOutbounds.
func TestParseMieruClientJSON_EquivalentToMieruLink(t *testing.T) {
	cfg := &pb.ClientConfig{
		ActiveProfile: proto.String("default"),
		Profiles: []*pb.ClientProfile{
			{
				ProfileName: proto.String("default"),
				User: &pb.User{
					Name:     proto.String("baozi"),
					Password: proto.String("manlianpenfen"),
				},
				// Два сервера в профиле: эквивалентность обязана покрывать
				// multi-server конфиги (панели экспортируют и такие).
				Servers: []*pb.ServerEndpoint{
					{
						IpAddress: proto.String("12.34.56.78"),
						PortBindings: []*pb.PortBinding{
							{Port: proto.Int32(6666), Protocol: pb.TransportProtocol_TCP.Enum()},
							{PortRange: proto.String("9998-9999"), Protocol: pb.TransportProtocol_TCP.Enum()},
							{Port: proto.Int32(6489), Protocol: pb.TransportProtocol_UDP.Enum()},
						},
					},
					{
						DomainName: proto.String("mirror.example.com"),
						PortBindings: []*pb.PortBinding{
							{Port: proto.Int32(443), Protocol: pb.TransportProtocol_TCP.Enum()},
						},
					},
				},
				Mtu:          proto.Int32(1400),
				Multiplexing: &pb.MultiplexingConfig{Level: pb.MultiplexingLevel_MULTIPLEXING_HIGH.Enum()},
			},
		},
	}

	binary, err := proto.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	link := "mieru://" + base64.StdEncoding.EncodeToString(binary)
	viaLink := ParseBatch([]string{link})
	if len(viaLink.Errors) != 0 {
		t.Fatalf("mieru:// errors: %+v", viaLink.Errors)
	}

	jsonBody, err := protojson.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !IsMieruClientJSON(jsonBody) {
		t.Fatal("protojson body must be detected as mieru client JSON")
	}
	viaJSON := ParseMieruClientJSON(jsonBody)
	if len(viaJSON.Errors) != 0 {
		t.Fatalf("json errors: %+v", viaJSON.Errors)
	}

	if !reflect.DeepEqual(viaLink.Outbounds, viaJSON.Outbounds) {
		t.Fatalf("mieru:// and JSON must yield identical outbounds:\nlink: %+v\njson: %+v",
			viaLink.Outbounds, viaJSON.Outbounds)
	}
}

func TestParseMieruClientJSON_ActiveProfileHonored(t *testing.T) {
	body := `{
		"profiles": [
			{
				"profileName": "first",
				"user": { "name": "u1", "password": "p1" },
				"servers": [ { "ipAddress": "1.1.1.1", "portBindings": [ { "port": 443, "protocol": "TCP" } ] } ]
			},
			{
				"profileName": "second",
				"user": { "name": "u2", "password": "p2" },
				"servers": [ { "domainName": "srv.example", "portBindings": [ { "port": 8443, "protocol": "TCP" } ] } ]
			}
		],
		"activeProfile": "second"
	}`
	res := ParseMieruClientJSON([]byte(body))
	if len(res.Errors) != 0 {
		t.Fatalf("errors: %+v", res.Errors)
	}
	if len(res.Outbounds) != 1 {
		t.Fatalf("got %d outbounds, want 1", len(res.Outbounds))
	}
	ob := decodeOutbound(t, res.Outbounds[0])
	if ob["server"] != "srv.example" || ob["username"] != "u2" {
		t.Fatalf("activeProfile ignored: %+v", ob)
	}
}

func TestParseMieruClientJSON_DiscardsUnknownFields(t *testing.T) {
	body := `{
		"profiles": [
			{
				"profileName": "default",
				"user": { "name": "u", "password": "p" },
				"servers": [ { "ipAddress": "1.1.1.1", "portBindings": [ { "port": 443, "protocol": "TCP" } ] } ],
				"panelExtra": "whatever"
			}
		],
		"panelVersion": 42
	}`
	res := ParseMieruClientJSON([]byte(body))
	if len(res.Errors) != 0 {
		t.Fatalf("unknown fields must be discarded, got errors: %+v", res.Errors)
	}
	if len(res.Outbounds) != 1 {
		t.Fatalf("got %d outbounds, want 1", len(res.Outbounds))
	}
}

func TestParseMieruClientJSON_BOMAndWhitespaceTolerated(t *testing.T) {
	body := "\xef\xbb\xbf \r\n\t" + mieruClientJSONSample
	if !IsMieruClientJSON([]byte(body)) {
		t.Fatal("BOM + leading whitespace must be tolerated by detection")
	}
	res := ParseMieruClientJSON([]byte(body))
	if len(res.Errors) != 0 {
		t.Fatalf("errors: %+v", res.Errors)
	}
	if len(res.Outbounds) != 2 {
		t.Fatalf("got %d outbounds, want 2", len(res.Outbounds))
	}
}

func TestIsMieruClientJSON_Negatives(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"sing-box json with outbounds", `{"outbounds":[{"type":"vless","tag":"x","server":"h","server_port":443,"uuid":"u"}]}`},
		{"profiles AND outbounds → sing-box priority", `{"profiles":[],"outbounds":[]}`},
		{"clash yaml", "proxies:\n  - name: a\n    type: vless\n"},
		{"share links", "vless://uuid@host:443#x"},
		{"json array", `[{"profiles":[]}]`},
		{"profiles not an array", `{"profiles":{"a":1}}`},
		{"empty", ""},
		{"null profiles", `{"profiles":null}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if IsMieruClientJSON([]byte(tc.body)) {
				t.Errorf("IsMieruClientJSON(%q) = true, want false", tc.body)
			}
		})
	}
}

func TestParseMieruClientJSON_EmptyProfiles(t *testing.T) {
	body := `{"profiles":[]}`
	if !IsMieruClientJSON([]byte(body)) {
		t.Fatal("empty profiles must still be detected (parse reports the precise error)")
	}
	res := ParseMieruClientJSON([]byte(body))
	if len(res.Outbounds) != 0 {
		t.Fatalf("got outbounds: %+v", res.Outbounds)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "нет профилей") {
		t.Fatalf("errors=%+v", res.Errors)
	}
}

func TestParseMieruClientJSON_BadJSON(t *testing.T) {
	res := ParseMieruClientJSON([]byte(`{"profiles":[{"user":42}]}`))
	if len(res.Outbounds) != 0 {
		t.Fatalf("got outbounds: %+v", res.Outbounds)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "не удалось разобрать mieru JSON") {
		t.Fatalf("errors=%+v", res.Errors)
	}
}

func TestParseMieruClientJSON_NoServers(t *testing.T) {
	body := `{"profiles":[{"profileName":"default","user":{"name":"u","password":"p"}}]}`
	res := ParseMieruClientJSON([]byte(body))
	if len(res.Outbounds) != 0 {
		t.Fatalf("got outbounds: %+v", res.Outbounds)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "нет серверов") {
		t.Fatalf("errors=%+v", res.Errors)
	}
}

// Разбор клиентского JSON идёт через protojson с DiscardUnknown, поэтому поля,
// которых нет в схеме вендоренного mieru, молча выпадают. lowEntropy появился в
// 3.35.0: если пин откатится ниже, настройка исчезнет из outbound'а без ошибки,
// и туннель к серверу с включённым режимом просто замолчит.
func TestParseMieruClientJSON_KeepsLowEntropy(t *testing.T) {
	body := `{"profiles":[{"profileName":"default","user":{"name":"u","password":"p"},
      "servers":[{"ipAddress":"12.34.56.78","portBindings":[{"port":6666,"protocol":"TCP"}]}],
      "trafficPattern":{"lowEntropy":{"mode":"LOW_ENTROPY_MODE_32"}}}]}`

	res := ParseMieruClientJSON([]byte(body))
	if len(res.Errors) != 0 || len(res.Outbounds) != 1 {
		t.Fatalf("outbounds=%d errors=%+v", len(res.Outbounds), res.Errors)
	}
	encoded, _ := decodeOutbound(t, res.Outbounds[0])["traffic_pattern"].(string)
	if encoded == "" {
		t.Fatal("traffic_pattern пуст: lowEntropy потерян при разборе")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	var tp pb.TrafficPattern
	if err := proto.Unmarshal(raw, &tp); err != nil {
		t.Fatalf("protobuf: %v", err)
	}
	if got := tp.GetLowEntropy().GetMode(); got != pb.LowEntropyMode_LOW_ENTROPY_MODE_32 {
		t.Fatalf("lowEntropy.mode = %v, want LOW_ENTROPY_MODE_32", got)
	}
}
