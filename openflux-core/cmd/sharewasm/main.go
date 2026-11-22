//go:build js && wasm

package main

import "syscall/js"

func wrapOneStringArg(fn func(string) string) js.Func {
	return js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 1 {
			return marshalWasmError("missing argument")
		}
		return fn(args[0].String())
	})
}

func main() {
	js.Global().Set("openfluxShare", map[string]any{
		"read": wrapOneStringArg(readLinkJSON),
		"make": wrapOneStringArg(makeLinkJSON),
		"qr":   wrapOneStringArg(qrLinkJSON),
	})
	select {}
}
