package signature

import (
	"encoding/binary"
	"errors"
	"fmt"
	mrand "math/rand"
	"regexp"
	"strconv"
	"strings"
)

// Генератор сигнатур I1–I5 (см. CONTEXT.md «Сигнатура AWG»). Один на проект:
// UI получает результат через POST /api/signature/generate. Профили — по
// одному файлу (quic.go, stun.go, dns.go, dtls.go, sip.go); байтовые раскладки
// портированы из payloadGen (MIT) — https://github.com/Sketchystan1/payloadGen.
//
// Токены только b/t/r/rc/rd: пересечение kernel-модуля (нет d/ds/dz) и нашего
// amneziawg-go (нет <c> — отвергает весь I1).

// MaxSignatureChars — потолок суммарной ДЛИНЫ СТРОК I1–I5 (тот же в
// protocols.ts). Меряется в символах, а не в полезной нагрузке: `awg setconf`
// кладёт device-атрибуты в netlink-буфер `malloc(pagesize≤8192)` = 4096 байт
// (amneziawg-tools netlink.h, mnlg_socket_open) НЕпроверяемым
// mnl_attr_put_strz — переполнение молча портит кучу, а не даёт ошибку.
// `<r 1000>` при этом занимает 8 символов: тысячу байт генерирует модуль ядра
// уже при отправке, в сообщение они не попадают.
//
// Стенд 5.01.C.3.0-1 / awg-tools v3.1.20260812 / mipsel, полный набор
// параметров AWG 3.1 + пир: сумма 3710 символов применяется и читается
// обратно, 3720 — `awg setconf` молча отдаёт rc=0, после чего `awg show` на
// интерфейсе отвечает `Message too large`. 3500 — этот порог с запасом.
const MaxSignatureChars = 3500

// DefaultProfile — профиль новых пиров серверов (системного и managed) и
// дефолт выпадашки. «Встроенный сервер» в коде — только сервер, созданный
// самим NDMS (internal/ndms/types.go), не наш managed (F180).
const DefaultProfile = "quic_initial"

// Profiles — каталог профилей имитации в порядке показа в UI.
var Profiles = []string{"quic_initial", "stun", "dns", "dtls", "sip"}

var ErrUnknownProtocol = errors.New("unknown protocol")
var ErrPacketsTooLarge = errors.New("signature packets exceed size limit")
var ErrInvalidPacketTag = errors.New("invalid signature packet tag")

type GeneratedPackets struct{ I1, I2, I3, I4, I5 string }

// Result — что отдаёт Generate: канонический профиль, пакеты, суммарный размер
// и домен (SNI/QNAME/realm), зашитый в сигнатуру.
type Result struct {
	Profile  string
	Domain   string
	Packets  GeneratedPackets
	ByteSize int
}

// GenerateOptions позволяет переопределить выбор домена при генерации I1–I5:
// конкретный домен (или список), пул доменов пресета (ru_whitelist, cdn) или
// явный срез доменов.
type GenerateOptions struct {
	Domain     string
	HostPreset string
	Domains    []string
}

// builders и hostBuilders заполняются профилями в своих файлах (init()).
var builders = map[string]func(r *mrand.Rand) (GeneratedPackets, error){}
var hostBuilders = map[string]func(r *mrand.Rand, customHost string) (GeneratedPackets, string, error){}

// CanonicalProtocol возвращает ключ профиля или "" для неизвестного.
func CanonicalProtocol(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	for _, known := range Profiles {
		if p == known {
			return p
		}
	}
	return ""
}

// Generate собирает сигнатуру профиля. Случайность — при каждом вызове, без seed.
func Generate(profile string) (Result, error) {
	return generateWithOptions(profile, newCryptoSeededRand(), GenerateOptions{})
}

// GenerateWithOptions собирает сигнатуру профиля с заданным доменом или пулом доменов.
func GenerateWithOptions(profile string, opts GenerateOptions) (Result, error) {
	return generateWithOptions(profile, newCryptoSeededRand(), opts)
}

func generate(profile string, r *mrand.Rand) (Result, error) {
	return generateWithOptions(profile, r, GenerateOptions{})
}

func resolveCustomHost(r *mrand.Rand, opts GenerateOptions) (string, error) {
	preset := CanonicalHostPreset(opts.HostPreset)
	if opts.HostPreset != "" && preset == "" {
		return "", fmt.Errorf("%w: %s", ErrInvalidDomainPreset, opts.HostPreset)
	}
	if strings.TrimSpace(opts.Domain) != "" {
		list, err := ParseDomainList(opts.Domain)
		if err != nil {
			return "", err
		}
		if len(list) == 1 {
			return list[0], nil
		}
		if len(list) > 1 {
			return list[r.Intn(len(list))], nil
		}
	}
	if len(opts.Domains) > 0 {
		valid := make([]string, 0, len(opts.Domains))
		for _, raw := range opts.Domains {
			d, err := ValidateDomain(raw)
			if err != nil {
				return "", err
			}
			valid = append(valid, d)
		}
		return valid[r.Intn(len(valid))], nil
	}
	switch preset {
	case HostPresetRU:
		return ruWhitelistPool[r.Intn(len(ruWhitelistPool))], nil
	case HostPresetCDN:
		return cdnHostPool[r.Intn(len(cdnHostPool))], nil
	case HostPresetCustom:
		return "", fmt.Errorf("%w: empty custom domain", ErrInvalidDomain)
	default:
		return "", nil
	}
}

func generateWithOptions(profile string, r *mrand.Rand, opts GenerateOptions) (Result, error) {
	key := CanonicalProtocol(profile)
	if key == "" {
		return Result{}, ErrUnknownProtocol
	}
	customHost, err := resolveCustomHost(r, opts)
	if err != nil {
		return Result{}, err
	}
	var packets GeneratedPackets
	var usedHost string
	if hBuild, ok := hostBuilders[key]; ok {
		packets, usedHost, err = hBuild(r, customHost)
	} else if build, ok := builders[key]; ok {
		packets, err = build(r)
		usedHost = customHost
	} else {
		return Result{}, fmt.Errorf("profile %s not implemented", key)
	}
	if err != nil {
		return Result{}, err
	}
	if err := CheckSize(packets); err != nil {
		return Result{}, err
	}
	return Result{Profile: key, Domain: usedHost, Packets: packets, ByteSize: TotalByteSize(packets)}, nil
}

// newCryptoSeededRand — math/rand с seed из crypto/rand: выбор хостов, длин и
// текстовых полей; ключевой материал QUIC берётся напрямую из crypto/rand.
func newCryptoSeededRand() *mrand.Rand {
	return mrand.New(mrand.NewSource(int64(binary.LittleEndian.Uint64(randBytes(8)))))
}

var cpsTagRe = regexp.MustCompile(`<(\w+)(?:\s+([^>]*))?>`)
var hexArgRe = regexp.MustCompile(`0x([0-9a-fA-F]*)`)

// ByteSize — размер полезной нагрузки одного паттерна (hex внутри <b>, N у
// r/rc/rd, 4 байта у t/c), то есть размер имитируемого пакета на проводе.
// Справочная величина для API и тестов: лимит считается НЕ по ней (см.
// MaxSignatureChars).
func ByteSize(pattern string) int {
	if pattern == "" {
		return 0
	}
	total := 0
	for _, m := range cpsTagRe.FindAllStringSubmatch(pattern, -1) {
		tag, arg := strings.ToLower(m[1]), strings.TrimSpace(m[2])
		switch tag {
		case "b":
			if hm := hexArgRe.FindStringSubmatch(arg); hm != nil {
				total += len(hm[1]) / 2
			}
		case "r", "rc", "rd":
			if n, err := strconv.Atoi(arg); err == nil && n > 0 {
				total += n
			}
		case "c", "t":
			total += 4
		}
	}
	return total
}

func TotalByteSize(p GeneratedPackets) int {
	return ByteSize(p.I1) + ByteSize(p.I2) + ByteSize(p.I3) + ByteSize(p.I4) + ByteSize(p.I5)
}

// ValidateProfileAndSize — единый гейт правки сигнатуры пира: профиль обязан
// быть из Profiles (пустой допустим — сигнатура набрана руками), суммарная
// длина строк I1–I5 обязана влезать в лимит. Возвращает КАНОНИЧЕСКИЙ ключ
// профиля: хранить
// присланное (" SIP ") нельзя. Ошибки — сентинелы ErrUnknownProtocol и
// ErrPacketsTooLarge, вызывающий переводит их в свои коды.
func ValidateProfileAndSize(profile string, p GeneratedPackets) (string, error) {
	canonical := CanonicalProtocol(profile)
	if profile != "" && canonical == "" {
		return "", fmt.Errorf("%w: %s", ErrUnknownProtocol, profile)
	}
	if err := CheckSize(p); err != nil {
		return "", err
	}
	if err := CheckTags(p); err != nil {
		return "", err
	}
	return canonical, nil
}

// CheckTags — проверка аргументов у <r>/<rc>/<rd>. Модуль ядра
// (amneziawg-kernel src/junk.c, parse_r_tag:112 и два близнеца) смотрит только
// код возврата kstrtoint, но НЕ само значение: отрицательный размер попадает в
// tag->pkt_size, оттуда в mod->buf_len, а из него — в get_random_bytes(buf,
// len) с приведением к size_t.
//
// Стенд 08.09: одиночный <r -1>/<rc -5>/<r 2000000000> безвреден — сумма
// отрицательная или гигантская, kzalloc проваливается и setconf отвергается с
// «Out of memory». Но в смеси сумма выходит маленькой положительной:
// `<b 0x0102><r -1>` принимается с rc=0 и читается обратно, а в модуле
// остаётся модификатор с отрицательной длиной.
//
// Строку I1–I5 мы берём из пользовательского .conf и отдаём ядру дословно,
// поэтому режем здесь. Верх — MaxSplittableTagBytes: тот же потолок, за
// которым тег и так считается неспасаемым (normalize.go).
func CheckTags(p GeneratedPackets) error {
	for _, pattern := range []string{p.I1, p.I2, p.I3, p.I4, p.I5} {
		for _, m := range cpsTagRe.FindAllStringSubmatch(pattern, -1) {
			tag := strings.ToLower(m[1])
			if tag != "r" && tag != "rc" && tag != "rd" {
				continue
			}
			arg := strings.TrimSpace(m[2])
			n, err := strconv.Atoi(arg)
			if err != nil || !isDecimal(arg) || n < 0 || n > MaxSplittableTagBytes {
				return fmt.Errorf("%w: <%s %s>", ErrInvalidPacketTag, tag, arg)
			}
		}
	}
	return nil
}

// isDecimal — только цифры: strconv.Atoi берёт "+5" и "-5", а нам нужен отказ
// на любом знаке.
func isDecimal(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// CheckSize — единая проверка размера сигнатуры для туннелей и пиров.
func CheckSize(p GeneratedPackets) error {
	if n := TotalChars(p); n > MaxSignatureChars {
		return fmt.Errorf("%w: %d > %d chars", ErrPacketsTooLarge, n, MaxSignatureChars)
	}
	return nil
}

// TotalChars — суммарная длина строк I1–I5, та величина, которую ограничивает
// netlink-буфер awg-tools.
func TotalChars(p GeneratedPackets) int {
	return len(p.I1) + len(p.I2) + len(p.I3) + len(p.I4) + len(p.I5)
}
