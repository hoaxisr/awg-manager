package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/p1neappleXpress/OpenFlux/transport/script"
)

// The desktop's way in: check, apply and roll back an installed script
// transport through the CLI subcommands, against a real local HTTP server.
func TestScriptUpdateCLI(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pubHex := hex.EncodeToString(pub)
	tmp := t.TempDir()
	pack := func(version string) []byte {
		p := filepath.Join(tmp, version+".flux")
		src := []byte(`var Transport = { info: function() { return { name: "demo", version: "` + version + `" }; } };`)
		if err := script.WritePackage(p, script.PackageManifest{ID: "demo", Name: "demo", Version: version}, src, nil, priv, true); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(p)
		return b
	}
	v1, v2 := pack("1.0.0"), pack("1.1.0")
	sum := sha256.Sum256(v2)

	mux := http.NewServeMux()
	mux.HandleFunc("/update.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(script.UpdateIndex{Format: 1, ID: "demo", Channels: map[string]script.IndexChannel{
			"stable": {Version: "1.1.0", URL: "demo-1.1.0.flux", SHA256: hex.EncodeToString(sum[:]), Notes: "n"},
		}})
	})
	mux.HandleFunc("/demo-1.1.0.flux", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(v2) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := filepath.Join(tmp, "scripts")
	if err := script.InstallPackage(dir, "demo.flux", v1); err != nil {
		t.Fatal(err)
	}
	common := []string{"--id=demo", "--version=1.0.0", "--pubkey=" + pubHex, "--update=" + srv.URL + "/update.json"}
	run := func(f func([]string, *bytes.Buffer) int, args ...string) script.UpdateReport {
		var out bytes.Buffer
		f(args, &out)
		var rep script.UpdateReport
		if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
			t.Fatalf("not a report: %q", out.String())
		}
		return rep
	}
	check := func(a []string, o *bytes.Buffer) int { return runCheckScriptUpdate(a, o) }
	apply := func(a []string, o *bytes.Buffer) int { return runApplyScriptUpdate(a, o) }
	rollback := func(a []string, o *bytes.Buffer) int { return runRollbackScript(a, o) }

	if rep := run(check, common...); rep.Status != script.UpdateAvailable || rep.Latest != "1.1.0" || rep.Notes != "n" {
		t.Fatalf("check: %+v", rep)
	}
	if rep := run(apply, append(common, "--dir="+dir)...); rep.Status != script.UpdateInstalled {
		t.Fatalf("apply: %+v", rep)
	}
	cur, err := script.LoadSignedPackage(filepath.Join(dir, "demo.flux"), pub)
	if err != nil || cur.Manifest.Version != "1.1.0" {
		t.Fatalf("installed: %v", err)
	}
	if rep := run(rollback, "--id=demo", "--pubkey="+pubHex, "--dir="+dir); rep.Status != script.UpdateInstalled || rep.Latest != "1.0.0" {
		t.Fatalf("rollback: %+v", rep)
	}
	// Bad usage is a report too.
	if rep := run(check, "--id=demo"); rep.Code != "usage" {
		t.Fatalf("usage: %+v", rep)
	}
}
