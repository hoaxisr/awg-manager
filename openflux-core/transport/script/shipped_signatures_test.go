package script

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The scripts in js/ are what the apps install as first-party: each must carry
// a signature that verifies under OfficialKeyHex. PENDING_SIGNATURE marks the
// state between changing a script and the key holder signing it; the test
// skips then (loudly), and fails if the marker is left behind after signing.
func TestShippedScriptSignaturesVerifyUnderTheOfficialKey(t *testing.T) {
	pub, err := DecodePublicKeyHex(OfficialKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob("js/*.js")
	var stale []string
	for _, f := range files {
		if strings.HasSuffix(f, "template.js") {
			continue
		}
		if _, err := LoadSigned(f, pub); err != nil {
			stale = append(stale, filepath.Base(f))
		}
	}
	_, err = os.Stat("js/PENDING_SIGNATURE")
	pending := err == nil // the marker exists
	switch {
	case len(stale) == 0 && pending:
		t.Fatal("js/PENDING_SIGNATURE is still there but every signature verifies: delete the marker")
	case len(stale) > 0 && !pending:
		t.Fatalf("signatures do not verify under the official key: %v (run js/build.sh with the key, or add js/PENDING_SIGNATURE if signing is waiting on its holder)", stale)
	case len(stale) > 0 && pending:
		t.Skipf("signatures pending (js/PENDING_SIGNATURE): %v are not signed for their current content", stale)
	}
}
