package nwg

import (
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/signature"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
)

// NDMS parses I1-I5 with a strict AmneziaWG parser that enforces a per-tag
// ceiling on <r>/<rc>/<rd> which none of our own datapaths do (see
// signature.MaxTagBytes for the provenance). A config generated elsewhere —
// notably the docker-amneziawg default I1 — therefore works everywhere until
// it reaches the router, which refuses the whole interface with
// `"WireguardN": invalid I1 value.` and creates nothing.
//
// Only what goes to NDMS is rewritten. The stored tunnel keeps the signature
// exactly as imported, so a .conf downloaded from the UI is still the file the
// user gave us.

// splitSignatureTags returns iface with every I1-I5 slot passed through
// signature.SplitOversizedTags, plus a description of what changed for the log
// ("" if nothing). The input is not modified.
func splitSignatureTags(iface *storage.AWGInterface) (storage.AWGInterface, string) {
	out := *iface
	slots := []struct {
		name string
		val  *string
	}{
		{"I1", &out.I1}, {"I2", &out.I2}, {"I3", &out.I3},
		{"I4", &out.I4}, {"I5", &out.I5},
	}

	var notes []string
	for _, s := range slots {
		v, note := signature.SplitOversizedTags(*s.val)
		if note == "" {
			continue
		}
		*s.val = v
		notes = append(notes, s.name+": "+note)
	}
	return out, strings.Join(notes, "; ")
}

// logSignatureSplit reports a signature rewrite for the given tunnel. The ASC
// paths run on every start and every param sync, so without this the config
// NDMS actually runs would differ from the stored one with nothing said —
// precisely the diagnostic this fixup exists to provide.
func (o *OperatorNativeWG) logSignatureSplit(stage, name string, iface *storage.AWGInterface) {
	_, note := splitSignatureTags(iface)
	o.logSplitNote(stage, name, note)
}

// logSplitNote writes an already computed rewrite description, if any.
func (o *OperatorNativeWG) logSplitNote(stage, name, note string) {
	if note != "" {
		o.appLog.Info(stage, name, signature.RewriteLogMessage(note))
	}
}

// ndmsImportConf renders the .conf uploaded to NDMS with oversized signature
// tokens split, and a description of what was split for the log ("" if
// nothing). Byte-identical to config.GenerateForExport for any config the
// router would have accepted anyway.
func ndmsImportConf(stored *storage.AWGTunnel) (string, string) {
	safe := *stored
	iface, note := splitSignatureTags(&stored.Interface)
	safe.Interface = iface
	// Тот же строгий парсер отвергает диапазон keepalive (AWG 3.0) вместе со
	// всем импортом, поэтому в файл уходит его нижняя граница. Остальные
	// значения не трогаем: пустое и "0" генератор разберёт сам, а нечитаемое
	// дойдёт до NDMS и будет отвергнуто громко — это лучше, чем подменить его
	// выдуманным дефолтом генератора.
	if n, ok := stored.Peer.PersistentKeepalive.Effective(); ok && stored.Peer.PersistentKeepalive.IsRange() {
		safe.Peer.PersistentKeepalive = storage.Keepalive(strconv.Itoa(n))
	}
	return config.GenerateForExport(&safe), note
}
