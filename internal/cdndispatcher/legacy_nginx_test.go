//go:build linux

package cdndispatcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const firmwareListen = "    listen 91.144.142.72:443 ssl;\n    listen unix:/var/nginx/ndns.ssl.socket ssl proxy_protocol;\n"
const hookedListen = "    listen 91.144.142.72:443 ssl http2;\n    listen unix:/var/nginx/ndns.ssl.socket ssl http2 proxy_protocol;\n"

func withLegacyPaths(t *testing.T) (dir string, cmds *[]string) {
	t.Helper()
	dir = t.TempDir()
	old := []string{legacyNginxHookPath, legacyNginxOptConf, legacyNginxCompOff, legacyNginxConf}
	oldRun := runNginxCmd
	legacyNginxHookPath = filepath.Join(dir, "65-awgm-nginx-unbuffer.sh")
	legacyNginxOptConf = filepath.Join(dir, "opt_comp_off.conf")
	legacyNginxCompOff = filepath.Join(dir, "comp_off.conf")
	legacyNginxConf = filepath.Join(dir, "nginx.conf")
	var got []string
	runNginxCmd = func(args ...string) error { got = append(got, strings.Join(args, " ")); return nil }
	t.Cleanup(func() {
		legacyNginxHookPath, legacyNginxOptConf, legacyNginxCompOff, legacyNginxConf = old[0], old[1], old[2], old[3]
		runNginxCmd = oldRun
	})
	return dir, &got
}

// F868: следы нашего хука — http2 снят со всех слушателей, подмена
// comp_off.conf снята, nginx перечитан reload'ом (не остановлен).
func TestRemoveLegacyNginxTuning(t *testing.T) {
	_, cmds := withLegacyPaths(t)
	for p, c := range map[string]string{legacyNginxHookPath: "#!/bin/sh\n", legacyNginxOptConf: "gzip off;\n", legacyNginxConf: hookedListen} {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	RemoveLegacyNginxTuning()
	if got, _ := os.ReadFile(legacyNginxConf); string(got) != firmwareListen {
		t.Fatalf("nginx.conf not restored:\n%s", got)
	}
	for _, p := range []string{legacyNginxHookPath, legacyNginxOptConf} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s left behind", p)
		}
	}
	joined := strings.Join(*cmds, "|")
	if !strings.Contains(joined, "umount") || !strings.Contains(joined, "nginx -s reload") || strings.Contains(joined, "-s stop") {
		t.Fatalf("unexpected commands: %v", *cmds)
	}
}

// Без следов нашего хука http2 прошивки (если она его когда-нибудь включит)
// не трогаем и nginx не дёргаем.
func TestRemoveLegacyNginxTuningLeavesFirmwareHTTP2(t *testing.T) {
	_, cmds := withLegacyPaths(t)
	if err := os.WriteFile(legacyNginxConf, []byte(hookedListen), 0o644); err != nil {
		t.Fatal(err)
	}
	RemoveLegacyNginxTuning()
	if got, _ := os.ReadFile(legacyNginxConf); string(got) != hookedListen || len(*cmds) != 0 {
		t.Fatalf("touched nginx without our hook: conf=%q cmds=%v", got, *cmds)
	}
}
