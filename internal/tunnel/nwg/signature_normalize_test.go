package nwg

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
)

// The default I1 emitted by the docker-amneziawg container: a QUIC Initial
// padded to the RFC 9000 §14.1 minimum of 1200 bytes, whose payload lands in a
// single <r 1178> — over the AmneziaWG per-tag limit. NDMS rejects the whole
// interface with `"WireguardN": invalid I1 value.`
const containerI1 = "<b 0xc3><b 0x00000001><b 0x08><r 8><b 0x00><b 0x00><b 0x449e><r 4><r 1178>"
const containerI1Split = "<b 0xc3><b 0x00000001><b 0x08><r 8><b 0x00><b 0x00><b 0x449e><r 4><r 1000><r 178>"

func oversizedIface() *storage.AWGInterface {
	return &storage.AWGInterface{
		AWGObfuscation: storage.AWGObfuscation{
			Jc: 6, Jmin: 76, Jmax: 236,
			S1: 12, S2: 12, S3: 12, S4: 12,
			H1: "205127846-255127846", H2: "592243917-642243917",
			H3: "1526611121-1576611121", H4: "1669322812-1719322812",
			I1: containerI1,
		},
	}
}

// buildASCJSON feeds the NDMS ASC RCI payload on the batch path. An oversized
// token must be split before it gets there.
func TestBuildASCJSON_SplitsOversizedSignatureTag(t *testing.T) {
	raw, err := buildASCJSON(oversizedIface(), true)
	if err != nil {
		t.Fatalf("buildASCJSON error = %v", err)
	}
	// Decode rather than substring-match: encoding/json escapes < and > as
	// \u003c/\u003e, so a raw-string assertion would fail on correct output.
	if got := ascI1(t, raw); got != containerI1Split {
		t.Errorf("I1 in NDMS payload\n got %q\nwant %q", got, containerI1Split)
	}
}

// ascI1 pulls the i1 field out of a marshalled ASC payload.
func ascI1(t *testing.T, raw []byte) string {
	t.Helper()
	var p struct {
		I1 string `json:"i1"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("unmarshal ASC payload: %v", err)
	}
	return p.I1
}

// A signature already within the limit must reach NDMS untouched.
func TestBuildASCJSON_LeavesCompliantSignatureAlone(t *testing.T) {
	iface := oversizedIface()
	iface.I1 = "<b 0x170303><r 32><t>"
	raw, err := buildASCJSON(iface, true)
	if err != nil {
		t.Fatalf("buildASCJSON error = %v", err)
	}
	if got := ascI1(t, raw); got != "<b 0x170303><r 32><t>" {
		t.Errorf("compliant signature was altered: %q", got)
	}
}

// The import path uploads a generated .conf that NDMS parses itself, so the
// split has to happen in the file, not just in the RCI payload.
func TestNDMSImportConf_SplitsOversizedSignatureTag(t *testing.T) {
	stored := &storage.AWGTunnel{
		Name:      "t1",
		Interface: *oversizedIface(),
		Peer: storage.AWGPeer{
			PublicKey: "GLi3Az9hKTEm2GT4Jlktzy3t0nB6Abb4Svf9JlxS4Q4=",
			Endpoint:  "45.153.48.240:32949",
		},
	}
	stored.Interface.PrivateKey = "wMvstfyVWYn6WGn5CjVlSsGj/9tzCvdNoIjPB/Vsc1w="
	stored.Interface.Address = "10.13.14.5"

	conf, note := ndmsImportConf(stored, false)
	if note == "" {
		t.Error("split of an oversized tag must be reported for the log")
	}
	if strings.Contains(conf, "<r 1178>") {
		t.Errorf("oversized tag reached the imported .conf:\n%s", conf)
	}
	if !strings.Contains(conf, "I1 = "+containerI1Split) {
		t.Errorf("expected split I1 line in .conf:\n%s", conf)
	}

	// Everything else must match the untouched generator byte for byte —
	// this fixup is only about the oversized token.
	want := strings.Replace(config.GenerateForExport(stored), containerI1, containerI1Split, 1)
	if conf != want {
		t.Errorf("conf differs beyond the I1 split:\ngot:\n%s\nwant:\n%s", conf, want)
	}

	// The stored tunnel must not be mutated: the user's config is what it is,
	// and a download must return what they imported.
	if stored.Interface.I1 != containerI1 {
		t.Errorf("stored interface was mutated: %q", stored.Interface.I1)
	}
}

// Canonicalisation must not depend on an oversized token sharing the slot:
// "<r500>" alone is rejected by upstream's parser just the same, and the
// systemtunnel path already rewrites it unconditionally.
func TestSplitSignatureTags_CanonicalisesWithoutOversizedTag(t *testing.T) {
	iface := oversizedIface()
	iface.I1 = "<b 0xdead><r500>"
	iface.I2 = "<r 32>"
	out, note := splitSignatureTags(iface)
	if out.I1 != "<b 0xdead><r 500>" || out.I2 != "<r 32>" {
		t.Errorf("I1=%q I2=%q", out.I1, out.I2)
	}
	if note != "I1: <r500> → <r 500>" {
		t.Errorf("note = %q", note)
	}
	if iface.I1 != "<b 0xdead><r500>" {
		t.Errorf("input was mutated: %q", iface.I1)
	}
}
