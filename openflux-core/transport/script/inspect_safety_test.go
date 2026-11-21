package script

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Inspect runs on scripts nobody has verified yet.
func TestInspectDoesNotHangOnAnEndlessScript(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		_, err := Inspect([]byte(`while(true){}; var Transport={info:function(){return {name:"x"}}}`))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "ran too long") {
			t.Fatalf("want a timeout error, got %v", err)
		}
	case <-time.After(inspectBudget + 3*time.Second):
		t.Fatal("Inspect of an unverified script never returned")
	}
}

func TestInspectCannotReachTheWorld(t *testing.T) {
	for name, call := range map[string]string{
		"http":       `http.fetch({url:"http://127.0.0.1:1/"})`,
		"ws":         `ws.open("wss://example.invalid/", {})`,
		"udp":        `udp.open("127.0.0.1:9")`,
		"httpserver": `httpserver.listen(function(){})`,
		"cookieJar":  `cookieJar.get()`,
		"raise":      `raise("needsSetup", {})`,
	} {
		src := `try { ` + call + `; var r = "ALLOWED"; } catch (e) { var r = "denied"; }
var Transport = { info: function () { return { name: r }; } };`
		info, err := Inspect([]byte(src))
		if err != nil || info.Name != "denied" {
			t.Errorf("%s at the top level of an unverified script: name=%q err=%v (want denied)", name, info.Name, err)
		}
	}
}

// The pure helpers a script may legitimately use while it builds its manifest.
func TestInspectKeepsThePureHelpers(t *testing.T) {
	info, err := Inspect([]byte(`var Transport = { info: function () { return { name: "n-" + text.decode(base64.decode("aGk=")) }; } };`))
	if err != nil || info.Name != "n-hi" {
		t.Fatalf("name=%q err=%v", info.Name, err)
	}
}

// Every script we ship must still read under the restricted API.
func TestInspectReadsEveryShippedScript(t *testing.T) {
	files, _ := filepath.Glob("js/*.js")
	if len(files) < 5 {
		t.Fatalf("expected the shipped scripts under js/, found %d", len(files))
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if info, err := Inspect(src); err != nil || info.Name == "" {
			t.Errorf("%s: name=%q err=%v", f, info.Name, err)
		}
	}
}

func TestReadPackageRefusesADecompressionBomb(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"manifest.json", "main.js"} {
		w, _ := zw.Create(name)
		w.Write(bytes.Repeat([]byte("A"), maxPackageEntryByte+1024)) // deflates to a few KB
	}
	zw.Close()
	if buf.Len() > 1<<20 {
		t.Fatalf("the bomb should be small on the wire, got %d bytes", buf.Len())
	}
	if _, err := ReadPackage(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("want a size refusal, got %v", err)
	}
}

func TestReadPackageRefusesTooManyEntries(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < maxPackageEntries+1; i++ {
		w, _ := zw.Create(strings.Repeat("a", i+1))
		w.Write([]byte("x"))
	}
	zw.Close()
	if _, err := ReadPackage(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "entries") {
		t.Fatalf("want an entry-count refusal, got %v", err)
	}
}
