package script

import (
	"os"
	"path/filepath"
	"regexp"

	"github.com/dop251/goja"
)

// ---- shared.read / shared.write ----
//
// A few bytes a transport shares with its own copies in the other processes
// of this host: a service that limits an address (Avito: 10 handshakes a
// minute) is limited for every node and client behind it, so the budget and
// the pause after a refusal must be one for all of them, as the native
// transports keep them in a file. One file per transport name and key in the
// temp directory; a write replaces it whole. OPENFLUX_SCRIPT_SHARED_DIR moves
// the directory, "off" keeps everything to this process (reads see nothing).
//
//	shared.read(key)        the text last written, "" if none
//	shared.write(key, text) at most 4 KB

const sharedMaxBytes = 4 << 10

var sharedKeyRe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func sharedPath(name, key string) string {
	dir := os.Getenv("OPENFLUX_SCRIPT_SHARED_DIR")
	switch dir {
	case "off":
		return ""
	case "":
		dir = os.TempDir()
	}
	return filepath.Join(dir, "openflux-"+sharedKeyRe.ReplaceAllString(name, "_")+"-"+sharedKeyRe.ReplaceAllString(key, "_"))
}

func registerShared(vm *goja.Runtime, t *ScriptTransport) {
	obj := vm.NewObject()
	obj.Set("read", func(call goja.FunctionCall) goja.Value {
		p := sharedPath(t.name, call.Argument(0).String())
		if p == "" {
			return vm.ToValue("")
		}
		b, err := os.ReadFile(p)
		if err != nil || len(b) > sharedMaxBytes {
			return vm.ToValue("")
		}
		return vm.ToValue(string(b))
	})
	obj.Set("write", func(call goja.FunctionCall) goja.Value {
		p := sharedPath(t.name, call.Argument(0).String())
		v := call.Argument(1).String()
		if p == "" || len(v) > sharedMaxBytes {
			return vm.ToValue(false)
		}
		tmp := p + ".tmp"
		if os.WriteFile(tmp, []byte(v), 0o666) != nil || os.Rename(tmp, p) != nil {
			return vm.ToValue(false)
		}
		return vm.ToValue(true)
	})
	vm.Set("shared", obj)
}
