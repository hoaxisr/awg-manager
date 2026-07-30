package nwg

import (
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// An AWG 3.0 tunnel on the NativeWG (NDMS ASC) path still carries the AWG 1.5/2.0
// obfuscation NDMS understands (S3/S4, I1-I5). The awg3-specific device params are
// not modelled by firmware ASC, but classifying as "awg3" must NOT drop the
// extended params — buildASCJSON must still emit the extended block.
func TestBuildASCJSON_AWG3_KeepsExtendedParams(t *testing.T) {
	iface := &storage.AWGInterface{
		AWGObfuscation: storage.AWGObfuscation{
			Jc: 4, Jmin: 40, Jmax: 70,
			S1: 12, S2: 12, S3: 20, S4: 20,
			H1: "1", H2: "2", H3: "3", H4: "4",
			I1: "AABB", I5: "CCDD",
			RekeyAfterTime: "120-150", // makes it classify as awg3
		},
	}

	raw, err := buildASCJSON(iface)
	if err != nil {
		t.Fatalf("buildASCJSON error = %v", err)
	}
	got := string(raw)
	for _, want := range []string{`"i1":"AABB"`, `"i5":"CCDD"`, `"s3":20`, `"s4":20`} {
		if !strings.Contains(got, want) {
			t.Errorf("buildASCJSON output missing %s:\n%s", want, got)
		}
	}
}
