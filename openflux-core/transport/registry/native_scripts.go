package registry

import (
	"os"
	"path/filepath"

	"github.com/p1neappleXpress/OpenFlux/transport"
	"github.com/p1neappleXpress/OpenFlux/transport/script"
	"github.com/p1neappleXpress/OpenFlux/utils"
)

// nativeScriptIDs maps a native type to the id of the JS package that
// carries the same wire.
var nativeScriptIDs = map[string]string{
	"yandex":     "yandex",
	"vyandex":    "vyandex",
	"boards":     "boards",
	"mailru":     "mailru",
	"cupsonline": "cupsonline",
	"avito":      "avito",
	"telemost":   "telemost",
	"oneme":      "oneme-iceinject",
}

// fromScript builds typ from its JS package in o.ScriptDir (<id>.flux, signed
// by one of the project's own keys) instead of the native code. ok is false
// when there is no such package or it does not load: the native one is used
// then, as before.
func fromScript(typ, url string, params map[string]interface{}, o Options) (t transport.Transport, ok bool) {
	id, known := nativeScriptIDs[typ]
	if !known || o.ScriptDir == "" {
		return nil, false
	}
	path := filepath.Join(o.ScriptDir, id+".flux")
	if _, err := os.Stat(path); err != nil {
		return nil, false
	}
	base := o.Base
	if o.LowMemory {
		base.MaxQueueSize = 512
	}
	var lastErr error
	for _, k := range script.OfficialKeys() {
		pub, err := script.DecodePublicKeyHex(k)
		if err != nil {
			continue
		}
		st, err := script.New(id, path, pub, url, withRole(params, o.IsExit), base)
		if err == nil {
			utils.Infof("[REGISTRY] %s runs on its JS package %s", typ, path)
			return st, true
		}
		lastErr = err
	}
	utils.Infof("[REGISTRY] %s: JS package %s not used (%v); the native transport runs", typ, path, lastErr)
	return nil, false
}
