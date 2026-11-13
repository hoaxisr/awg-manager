package warpscout

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/signature"
)

var cpsTokenRe = regexp.MustCompile(`<(\w+)(?:\s+([^>]*))?>`)

// CPSToRaw разворачивает CPS-строку сигнатуры (<b 0x…><r N><rc N><rd N><t>)
// в сырые байты пакета для C-сканера. Случайные части заполняются заново
// при каждом вызове — так же поступает модуль ядра на каждом рукопожатии.
func CPSToRaw(spec string) ([]byte, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	var out []byte
	for _, m := range cpsTokenRe.FindAllStringSubmatch(spec, -1) {
		tag, arg := strings.ToLower(m[1]), strings.TrimSpace(m[2])
		switch tag {
		case "b":
			h := strings.TrimPrefix(strings.TrimPrefix(arg, "0x"), "0X")
			b, err := hex.DecodeString(h)
			if err != nil {
				return nil, fmt.Errorf("CPS <b>: %w", err)
			}
			out = append(out, b...)
		case "r", "rc", "rd":
			n, err := strconv.Atoi(arg)
			if err != nil || n < 0 || n > 2048 {
				return nil, fmt.Errorf("CPS <%s %s>: неверная длина", tag, arg)
			}
			out = append(out, randomBytes(tag, n)...)
		case "t":
			var ts [4]byte
			binary.BigEndian.PutUint32(ts[:], uint32(time.Now().Unix()))
			out = append(out, ts[:]...)
		case "c":
			out = append(out, 0, 0, 0, 0)
		default:
			return nil, fmt.Errorf("CPS: неизвестный тег <%s>", tag)
		}
	}
	if len(out) > 1500 {
		return nil, fmt.Errorf("CPS: пакет %d байт больше 1500", len(out))
	}
	return out, nil
}

func randomBytes(tag string, n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	switch tag {
	case "rc":
		const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
		for i := range b {
			b[i] = letters[int(b[i])%len(letters)]
		}
	case "rd":
		for i := range b {
			b[i] = '0' + b[i]%10
		}
	}
	return b
}

// EnsureSignature заполняет I1..I5 генератором, если профиль задан, а
// пакеты пусты. Возвращает true, если параметры изменились.
func EnsureSignature(p *AWGParams) (bool, error) {
	prof := signature.CanonicalProtocol(p.SignatureProfile)
	if prof == "" {
		return false, nil
	}
	if p.I1 != "" {
		return false, nil
	}
	res, err := signature.Generate(prof)
	if err != nil {
		return false, err
	}
	p.I1, p.I2, p.I3, p.I4, p.I5 = res.Packets.I1, res.Packets.I2, res.Packets.I3, res.Packets.I4, res.Packets.I5
	return true, nil
}

// signatureHex — I1..I5 в hex сырых байтов для stdin сканера.
func signatureHex(p AWGParams) ([5]string, error) {
	var out [5]string
	for i, spec := range []string{p.I1, p.I2, p.I3, p.I4, p.I5} {
		raw, err := CPSToRaw(spec)
		if err != nil {
			return out, fmt.Errorf("I%d: %w", i+1, err)
		}
		out[i] = hex.EncodeToString(raw)
	}
	return out, nil
}

// nestedSignatureMax — потолок пакета I1..I5 у каскада: он идёт внутри
// внешнего туннеля с MTU 1280 (минус IP и UDP — 1252, берём с запасом).
const nestedSignatureMax = 1200

// fitSignature подгоняет I1..I5 под maxRaw байт. Пакет, который не
// помещается, заменяется своим же усечённым началом в виде `<b 0x…>`:
// усечь запись с тегами <r>/<t> иначе нельзя. Пакеты в пределах остаются
// как есть, со своими случайными частями.
func fitSignature(p AWGParams, maxRaw int) (AWGParams, error) {
	for i, spec := range []*string{&p.I1, &p.I2, &p.I3, &p.I4, &p.I5} {
		raw, err := CPSToRaw(*spec)
		if err != nil {
			return p, fmt.Errorf("%w: сигнатура I%d: %v", ErrInvalidInput, i+1, err)
		}
		if len(raw) > maxRaw {
			*spec = "<b 0x" + hex.EncodeToString(raw[:maxRaw]) + ">"
		}
	}
	return p, nil
}

// DefaultTunnelIPv4 — локальный адрес туннеля WARP, пока пользователь не задал свой.
const DefaultTunnelIPv4 = "172.16.0.2"

// NormalizeTunnelIPv4 проверяет и нормализует пользовательский локальный IPv4
// туннеля (вида `172.16.0.3` или `172.16.0.3/32`). Адреса без маски /32
// сохраняют суффикс /N, а /32 нормализуются в голый хост для совместимости с AddressV4.
func NormalizeTunnelIPv4(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("%w: укажите IPv4-адрес туннеля", ErrInvalidInput)
	}
	var addr netip.Addr
	bits := 32
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return "", fmt.Errorf("%w: неверный IPv4-адрес туннеля", ErrInvalidInput)
		}
		addr = p.Addr()
		bits = p.Bits()
	} else {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return "", fmt.Errorf("%w: неверный IPv4-адрес туннеля", ErrInvalidInput)
		}
		addr = a
	}
	if !addr.Is4() || addr.Zone() != "" || bits < 1 || bits > 32 {
		return "", fmt.Errorf("%w: допустим только IPv4-адрес", ErrInvalidInput)
	}
	if addr.IsUnspecified() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr == netip.MustParseAddr("255.255.255.255") {
		return "", fmt.Errorf("%w: этот IPv4-адрес не может использоваться как адрес туннеля", ErrInvalidInput)
	}
	if bits == 32 {
		return addr.String(), nil
	}
	return fmt.Sprintf("%s/%d", addr.String(), bits), nil
}

// BuildConf собирает .conf для туннеля WARP со стандартным MTU 1280.
func BuildConf(a *Account, awg AWGParams, protocol, endpoint string) string {
	return BuildConfWithMTU(a, awg, protocol, endpoint, DefaultMTU)
}

// BuildConfWithMTU собирает .conf для туннеля WARP с заданным MTU (для WARP-in-WARP — 1220).
func BuildConfWithMTU(a *Account, awg AWGParams, protocol, endpoint string, mtu int) string {
	if mtu <= 0 {
		mtu = DefaultMTU
	}
	var b strings.Builder
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", a.PrivateKey)
	addr := strings.TrimSpace(a.AddressV4)
	if addr == "" {
		addr = DefaultTunnelIPv4
	}
	if !strings.Contains(addr, "/") {
		addr += "/32"
	}
	fmt.Fprintf(&b, "Address = %s\n", addr)
	b.WriteString("DNS = 1.1.1.1, 1.0.0.1\n")
	fmt.Fprintf(&b, "MTU = %d\n", mtu)
	if protocol != ProtocolWG {
		if awg.Jc > 0 {
			fmt.Fprintf(&b, "Jc = %d\nJmin = %d\nJmax = %d\n", awg.Jc, awg.Jmin, awg.Jmax)
		}
		// Серверы WARP — штатный WireGuard: префиксы и типы сообщений
		// обязаны оставаться стандартными.
		b.WriteString("S1 = 0\nS2 = 0\nH1 = 1\nH2 = 2\nH3 = 3\nH4 = 4\n")
		for i, v := range []string{awg.I1, awg.I2, awg.I3, awg.I4, awg.I5} {
			if strings.TrimSpace(v) != "" {
				fmt.Fprintf(&b, "I%d = %s\n", i+1, v)
			}
		}
	}
	b.WriteString("\n[Peer]\n")
	peer := a.PeerPublicKey
	if peer == "" {
		peer = WarpPeerPublicKey
	}
	fmt.Fprintf(&b, "PublicKey = %s\n", peer)
	b.WriteString("AllowedIPs = 0.0.0.0/0\n")
	fmt.Fprintf(&b, "Endpoint = %s\n", endpoint)
	b.WriteString("PersistentKeepalive = 25\n")
	return b.String()
}

var (
	// Имя целиком в том виде, в каком его выдаёт панель: «WARP», «WARP HEL»,
	// «WARP-in-WARP HEL», с необязательным номером в конце. Имя, к которому
	// приложил руку пользователь, под шаблон не попадает и не трогается.
	warpColoNameRe   = regexp.MustCompile(`^(WARP(?:-in-WARP)?)(?: ([A-Z]{3}))?( \d+)?$`)
	nonColoWarpWords = map[string]bool{
		"AWG": true, "VPN": true, "PRO": true, "MAX": true, "DNS": true,
		"WAN": true, "LAN": true, "NAT": true, "RAW": true, "TCP": true,
		"UDP": true, "ONE": true, "APP": true, "CLI": true, "BOT": true,
		"NET": true, "NEW": true, "OLD": true, "TOP": true,
	}
)

func isValidColoCode(s string) bool {
	if len(s) != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		if s[i] < 'A' || s[i] > 'Z' {
			return false
		}
	}
	return true
}

// DeriveWarpTunnelName обновляет код колокейшна (HEL, TLL, FRA, …) в имени
// WARP-туннеля при подмене endpoint'а. Меняется только имя, которое панель
// выдала сама (см. warpColoNameRe): трёхбуквенное слово в пользовательском
// имени («WARP Mom», «Дом WARP Bob», «WARP via ISP») кодом узла не считается.
func DeriveWarpTunnelName(currentName, newColo string) (string, bool) {
	colo := strings.ToUpper(strings.TrimSpace(newColo))
	if !isValidColoCode(colo) || nonColoWarpWords[colo] {
		return "", false
	}
	trimmed := strings.TrimSpace(currentName)
	m := warpColoNameRe.FindStringSubmatch(trimmed)
	if m == nil || nonColoWarpWords[m[2]] {
		return "", false
	}
	next := m[1] + " " + colo + m[3]
	if next == trimmed || len(next) > 64 {
		return trimmed, false
	}
	return next, true
}
