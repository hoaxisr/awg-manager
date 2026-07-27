package awg3endpoint

import (
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

//go:embed testdata/routebox-client.json
var routeboxGolden []byte

func TestParse_Envelope(t *testing.T) {
	rec, err := Parse(routeboxGolden, "VPS Amsterdam", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if rec.Tag != "VPS Amsterdam" {
		t.Fatalf("tag = %q", rec.Tag)
	}
	var ep map[string]any
	if err := json.Unmarshal(rec.Endpoint, &ep); err != nil {
		t.Fatal(err)
	}
	if ep["type"] != "awg" {
		t.Fatalf("type = %v", ep["type"])
	}
	// passthrough: AWG3-поля сохранены нетронутыми
	if ep["header_protection_key"] == nil || ep["content_padding_addition"] != "64" {
		t.Fatalf("awg3 fields not passthrough: %v", ep)
	}
}

// AWG3 device-timers are opaque to Parse — they must survive untouched in
// Record.Endpoint (edited only in RouteBox; awg-manager is passthrough).
func TestParse_TimerFieldsPassthrough(t *testing.T) {
	raw := []byte(`{"type":"awg","private_key":"k",` +
		`"header_protection_key":"h","s1":12,"s2":12,"s3":12,"s4":12,` +
		`"rekey_timeout":"5","reject_after_time":"180",` +
		`"keepalive_timeout":"25","max_handshake_attempts":"5",` +
		`"peers":[{"public_key":"p","address":"1.2.3.4:51820"}]}`)
	rec, err := Parse(raw, "t1", map[string]bool{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, k := range []string{"rekey_timeout", "reject_after_time", "keepalive_timeout", "max_handshake_attempts"} {
		if !strings.Contains(string(rec.Endpoint), k) {
			t.Fatalf("таймер-поле %q должно пройти passthrough в Endpoint: %s", k, rec.Endpoint)
		}
	}
}

func TestParse_BareObject(t *testing.T) {
	var env map[string]json.RawMessage
	_ = json.Unmarshal(routeboxGolden, &env)
	bare := env["data"]
	if _, err := Parse(bare, "x", nil); err != nil {
		t.Fatalf("bare: %v", err)
	}
}

func TestParse_Rejects(t *testing.T) {
	cases := []struct {
		name string
		body string
		want error
	}{
		{"not-awg", `{"type":"wireguard","private_key":"k","peers":[{"public_key":"p","address":"h"}]}`, ErrNotAwg},
		{"no-key", `{"type":"awg","peers":[{"public_key":"p","address":"h"}]}`, ErrMissingKey},
		{"whitespace-key", `{"type":"awg","private_key":"   ","peers":[{"public_key":"p","address":"h"}]}`, ErrMissingKey},
		{"no-peer", `{"type":"awg","private_key":"k","peers":[]}`, ErrMissingPeer},
		{"hp-s-low", `{"type":"awg","private_key":"k","header_protection_key":"h","s1":4,"peers":[{"public_key":"p","address":"h"}]}`, ErrHeaderProtectionS},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Parse([]byte(c.body), "t", nil); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// An error envelope (success=false, or success present with empty data) is a
// RouteBox error/empty response, not an endpoint — reject with a clear message
// instead of a misleading ErrNotAwg from unmarshalling the whole envelope.
func TestParse_ErrorEnvelope(t *testing.T) {
	for _, body := range []string{`{"success":false}`, `{"success":true}`} {
		_, err := Parse([]byte(body), "t", nil)
		if err == nil || errors.Is(err, ErrNotAwg) {
			t.Fatalf("body=%s err=%v, want a clear non-ErrNotAwg error", body, err)
		}
		if !strings.Contains(err.Error(), "success") {
			t.Fatalf("body=%s err=%q, want mention of success", body, err)
		}
	}
}

func TestParse_TagValidation(t *testing.T) {
	body := `{"type":"awg","private_key":"k","peers":[{"public_key":"p","address":"h"}]}`
	if _, err := Parse([]byte(body), "", nil); !errors.Is(err, ErrTag) {
		t.Fatalf("empty tag must reject")
	}
	if _, err := Parse([]byte(body), `bad"quote`, nil); !errors.Is(err, ErrTag) {
		t.Fatalf("bad chars must reject")
	}
	if _, err := Parse([]byte(body), "taken", map[string]bool{"taken": true}); !errors.Is(err, ErrTag) {
		t.Fatalf("duplicate tag must reject")
	}
	// Тег обрезается по краям перед валидацией и сохранением.
	if rec, err := Parse([]byte(body), "  x  ", nil); err != nil || rec.Tag != "x" {
		t.Fatalf("trim: rec.Tag=%q err=%v", rec.Tag, err)
	}
}
