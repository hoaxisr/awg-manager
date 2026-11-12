package ndms

import "strings"

// kernelNamePrefixes — классы NDMS, у которых имя ядра задано номером записи.
// Снято на стенде (5.01.C.6, 30.09.2026): Wireguard0/1/3→nwg0/1/3,
// OpkgTun10/11→opkgtun10/11, Proxy0→t2s0, Bridge0→br0.
// Расширять только по факту стенда (TestKernelName_ExactlyFourClasses).
//
// PPPoE в таблице нет намеренно: на стенде PPPoE0 — это ppp0, но в поле
// встретился PPPoE0 с default-маршрутом через ppp1 (#1166). С зашитым ppp0
// хуки состояния (system_name=ppp1) не находили запись в модели WAN, и
// основной канал выпадал из выбора. Имя PPP-интерфейса знает только NDMS.
//
// zeroOnly: у Bridge на стенде видели только номер 0 (Bridge0→br0); что
// Bridge1 — это br1, не доказано, поэтому прочие номера — ok=false, их имя
// спросит резолвер вслед за списком. Wireguard/OpkgTun/Proxy — по любому
// номеру: на стенде несколько номеров (nwg0/1/3, opkgtun10/11), и наш же
// код строит nwgN/opkgtunN/t2sN по номеру.
var kernelNamePrefixes = [...]struct {
	ndms, kernel string
	zeroOnly     bool
}{
	{"Wireguard", "nwg", false},
	{"OpkgTun", "opkgtun", false},
	{"Proxy", "t2s", false},
	{"Bridge", "br", true},
}

// KernelName — имя ядра по NDMS-id для классов, где оно задано номером
// записи и снято на стенде (5.01.C.6, 30.09.2026): Wireguard→nwg,
// OpkgTun→opkgtun, Proxy→t2s, Bridge0→br0. Прочие классы
// (Ethernet, Wifi*, Usb*, PPPoE, L2TP, PPTP, SSTP, OpenVPN, IPsec…) — ok=false:
// у них имя зависит от модели и порядка, его знает только NDMS.
//
// Номер — строго десятичный, без знака и без ведущих нулей: иначе из одного
// числа получились бы два разных id с одним именем ядра.
func KernelName(ndmsID string) (name string, ok bool) {
	for _, p := range kernelNamePrefixes {
		num, found := strings.CutPrefix(ndmsID, p.ndms)
		if !found {
			continue
		}
		if !canonicalDecimal(num) || (p.zeroOnly && num != "0") {
			return "", false
		}
		return p.kernel + num, true
	}
	return "", false
}

// canonicalDecimal — непустая строка ASCII-цифр без ведущего нуля ("0" — да).
func canonicalDecimal(s string) bool {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
