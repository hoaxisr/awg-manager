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
	// всем импортом, поэтому диапазона в файле не остаётся ни в каком виде: с
	// читаемой нижней границей уходит она, с нулевой ("0-80" — валидатор
	// формата его принимает, и приезжает он импортом) поле стирается, и
	// генератор подставляет DefaultPersistentKeepalive. Подмена видимая и
	// НАМЕРЕННАЯ: выключенным keepalive не записывается вовсе — Keepalive.IsZero
	// считает нулём и пустую строку, и "0", — потому что здесь это страховка для
	// пользователей за NAT, а не настройка вкуса (решение владельца 12.09.2026).
	// Отказать здесь нельзя, иначе туннель не создастся вовсе.
	//
	// Одиночные значения не трогаем: всё, что доезжает до записи, прошивка
	// принимает как есть.
	if k := stored.Peer.PersistentKeepalive; k.IsRange() {
		safe.Peer.PersistentKeepalive = ""
		if n, ok := k.Effective(); ok {
			safe.Peer.PersistentKeepalive = storage.Keepalive(strconv.Itoa(n))
		}
	}
	stripAWG3Params(&safe.Interface)
	return config.GenerateForExport(&safe), note
}

// stripAWG3Params убирает из ИМПОРТИРУЕМОГО в NDMS файла параметры устройства
// AWG 3.0/3.1. Пользовательской выгрузки это не касается: она идёт мимо этой
// проекции, прямо через config.GenerateForExport.
//
// У этих параметров НЕТ адресата в NDMS ни на одной прошивке. Канал у прошивки
// ровно один — метод ASC, и он их не несёт: ndms.ASCParamsExtended
// заканчивается на S3/S4 и I1-I5, а buildASCJSON не шлёт их сознательно
// («firmware ASC does not model the awg3-specific device params»). Импорт
// каналом не является тем более: файл разбирает строгий парсер AmneziaWG, и
// каждый такой ключ он выбрасывает со строкой уровня W в системном журнале
// («skipping unrecognized parameter»). На стенде 12.09.2026 (5.01.C.3.0-1) это
// семь строк W на КАЖДЫЙ импорт premium-туннеля; kernel-бэкенд на том же
// конфиге не дал ни одной.
//
// Потерять нечего: применяют эти параметры awg_proxy.ko (buildKmodConfig берёт
// их из ЗАПИСИ туннеля, operator.go) и kernel-бэкенд через `awg setconf` —
// оба читают хранилище, а не этот файл. Конфиг с любым из них на сегодняшних
// прошивках вообще уходит на проксирующий путь: ASC 3.0 не умеет ни одна
// (ndmsinfo.SupportsWireguardASC3), а на проксирующем пути startProxy ещё и
// снимает принятые прошивкой параметры ASC, иначе обфускация ляжет дважды.
//
// Флаги 3.1 (RandomTrailers, DisableCookies) снимаются вместе с остальными:
// канал у них ровно такой же, то есть никакой.
//
// Когда ASC научится 3.0 (Keenetic обещает в одной из 5.02.A), нести их будет
// метод ASC — правка ляжет в ASCParamsExtended и buildASCJSON, а импорт
// останется чистым. Сцепку сторожит TestASC3FlagAndPayloadMoveTogether.
func stripAWG3Params(iface *storage.AWGInterface) {
	iface.HeaderProtectionKey = ""
	iface.ContentPaddingAddition = ""
	iface.RekeyAfterTime = ""
	iface.RekeyTimeout = ""
	iface.RejectAfterTime = ""
	iface.KeepaliveTimeout = ""
	iface.MaxHandshakeAttempts = ""
	iface.RandomTrailers = false
	iface.DisableCookies = false
}
