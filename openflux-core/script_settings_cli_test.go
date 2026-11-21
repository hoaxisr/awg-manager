package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/p1neappleXpress/OpenFlux/transport/script"
)

func TestScriptSettingsCLI(t *testing.T) {
	const src = `
var Transport = {
  info: function () { return { name: "cli-demo", version: "2.0.0", params: [
    { key: "url", label: "U", type: "url" },
    { key: "token", label: "Token", type: "secret", required: true },
    { key: "n", label: "N", type: "number", default: 4 } ] }; },
  open: function () {}
};`
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	js := filepath.Join(dir, "cli-demo.js")
	if err := os.WriteFile(js, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(js+".sig", ed25519.Sign(priv, []byte(src)), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) script.SettingsReport {
		t.Helper()
		var out bytes.Buffer
		if code := runScriptSettings(args, &out); code != 0 {
			t.Fatalf("exit %d: %s", code, out.String())
		}
		var rep script.SettingsReport
		if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
			t.Fatalf("not one JSON report: %v\n%s", err, out.String())
		}
		return rep
	}
	key := hex.EncodeToString(pub)

	rep := run("--data="+js, "--sig="+js+".sig", "--pubkey="+key, `--values={"token":"abc"}`, "--lang=en")
	if !rep.OK || rep.Name != "cli-demo" || rep.Version != "2.0.0" || rep.Signature != "valid" {
		t.Fatalf("report = %+v", rep)
	}
	if len(rep.Params) != 3 || rep.Values["token"] != "abc" || rep.Values["n"] != "4" {
		t.Errorf("params %v values %v", rep.Params, rep.Values)
	}
	if !strings.Contains(rep.HTML, `lang="en"`) || !strings.Contains(rep.HTML, `value="abc"`) {
		t.Error("the page must be English and prefilled")
	}

	if r := run("--data="+js, "--sig="+js+".sig", "--pubkey="+strings.Repeat("0", 64)); r.OK || r.Code != script.CodeSettingsBadSig {
		t.Errorf("another key: %+v", r)
	}
	if r := run("--data="+js, "--pubkey="+key, "--values=oops"); r.OK || r.Error == "" {
		t.Errorf("bad --values: %+v", r)
	}
	if r := run("--data="+filepath.Join(dir, "missing.js"), "--pubkey="+key); r.OK || r.Error == "" {
		t.Errorf("missing file: %+v", r)
	}
	var out bytes.Buffer
	if code := runScriptSettings(nil, &out); code != 1 || !strings.Contains(out.String(), "usage") {
		t.Errorf("no args: exit %d %s", code, out.String())
	}
}

// A saved setting goes to the core as one line of the .conf, whatever is in it.
func TestConfParamsLine(t *testing.T) {
	line := script.EncodeSettings(map[string]string{"token": "a#b;c", "notes": "x\ny"})
	conf := filepath.Join(t.TempDir(), "p.conf")
	body := "[Interface]\nRole = client\n\n[Transport script-demo]\nType = script\nPriority = 10\nPath = /p.flux\nPubkey = aa\nName = demo\nParams = " + line + "\n"
	if err := os.WriteFile(conf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	parsed, err := parseConf(conf)
	if err != nil {
		t.Fatal(err)
	}
	got, err := script.DecodeSettings(parsed.Transports[0].Values["Params"])
	if err != nil {
		t.Fatal(err)
	}
	if got["token"] != "a#b;c" || got["notes"] != "x\ny" {
		t.Errorf("settings through the .conf: %v", got)
	}
}
