package systemtunnel

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/signature"
)

// ascSignatureSlots are the ASC fields carrying CPS signature packets.
var ascSignatureSlots = [...]string{"i1", "i2", "i3", "i4", "i5"}

// splitASCSignatures rewrites oversized <r>/<rc>/<rd> tokens in the i1-i5
// fields of an ASC params object and describes what changed for the log
// ("" if nothing).
//
// The /system-tunnels/asc form posts these straight through to the same NDMS
// ASC endpoint the managed tunnels use, so a signature the router refuses
// fails here for the same reason and with the same unhelpful message —
// `"WireguardN": invalid I1 value.` Pasting the docker-amneziawg default I1
// into the form is enough to hit it.
//
// Anything that is not a JSON object, or has no signature fields to fix, comes
// back byte-identical: this is a fixup, not a reformatter, and the caller's
// payload is otherwise none of our business.
func splitASCSignatures(params json.RawMessage) (json.RawMessage, string) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(params, &obj); err != nil {
		return params, ""
	}

	var notes []string
	for _, slot := range ascSignatureSlots {
		raw, ok := obj[slot]
		if !ok {
			continue
		}
		var val string
		if err := json.Unmarshal(raw, &val); err != nil {
			continue
		}
		split, note := signature.SplitOversizedTags(val)
		if note == "" {
			continue
		}
		encoded, err := marshalNoEscape(split)
		if err != nil {
			continue
		}
		obj[slot] = encoded
		notes = append(notes, strings.ToUpper(slot)+": "+note)
	}
	if len(notes) == 0 {
		return params, ""
	}

	out, err := marshalNoEscape(obj)
	if err != nil {
		return params, ""
	}
	return out, strings.Join(notes, "; ")
}

// stripASCSignatures removes the i1-i5 keys from an ASC params object.
//
// Сигнатура интерфейса-сервера принадлежит его пирам (CONTEXT.md «Сигнатура
// AWG»), и форма ASC сервера её не задаёт. Ключи именно ВЫРЕЗАЮТСЯ, а не
// обнуляются: базовая форма ASC на 4.x про i1..i5 не знает, и пустая строка
// в них — такой же отказ NDMS, как и заполненная.
//
// Как и splitASCSignatures: не объект или нечего резать — возвращаем ввод
// байт в байт.
func stripASCSignatures(params json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(params, &obj); err != nil {
		return params
	}
	found := false
	for _, slot := range ascSignatureSlots {
		if _, ok := obj[slot]; ok {
			delete(obj, slot)
			found = true
		}
	}
	if !found {
		return params
	}
	out, err := marshalNoEscape(obj)
	if err != nil {
		return params
	}
	return out
}

// marshalNoEscape marshals v without HTML escaping, so the <> in signature
// packets survive instead of becoming </>.
func marshalNoEscape(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n")), nil
}
