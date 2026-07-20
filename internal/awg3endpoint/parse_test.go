package awg3endpoint

import (
	_ "embed"
	"encoding/json"
	"errors"
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
}
