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

// MaxSignatureBytes — потолок суммы I1–I5 (тот же в ASCEditor.svelte и schemas/tunnel.ts).
const MaxSignatureBytes = 4096

// DefaultProfile — профиль новых пиров встроенного сервера и дефолт выпадашки.
const DefaultProfile = "quic_initial"

// Profiles — каталог профилей имитации в порядке показа в UI.
var Profiles = []string{"quic_initial", "stun", "dns", "dtls", "sip"}

var ErrUnknownProtocol = errors.New("unknown protocol")
var ErrPacketsTooLarge = errors.New("signature packets exceed size limit")

type GeneratedPackets struct{ I1, I2, I3, I4, I5 string }

// Result — что отдаёт Generate: канонический профиль, пакеты, суммарный размер.
type Result struct {
	Profile  string
	Packets  GeneratedPackets
	ByteSize int
}

// builders заполняется профилями в своих файлах (init()).
var builders = map[string]func(r *mrand.Rand) (GeneratedPackets, error){}

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
	return generate(profile, newCryptoSeededRand())
}

func generate(profile string, r *mrand.Rand) (Result, error) {
	key := CanonicalProtocol(profile)
	if key == "" {
		return Result{}, ErrUnknownProtocol
	}
	build, ok := builders[key]
	if !ok {
		return Result{}, fmt.Errorf("profile %s not implemented", key)
	}
	packets, err := build(r)
	if err != nil {
		return Result{}, err
	}
	size := TotalByteSize(packets)
	if size > MaxSignatureBytes {
		return Result{}, fmt.Errorf("%w: %d > %d", ErrPacketsTooLarge, size, MaxSignatureBytes)
	}
	return Result{Profile: key, Packets: packets, ByteSize: size}, nil
}

// newCryptoSeededRand — math/rand с seed из crypto/rand: выбор хостов, длин и
// текстовых полей; ключевой материал QUIC берётся напрямую из crypto/rand.
func newCryptoSeededRand() *mrand.Rand {
	return mrand.New(mrand.NewSource(int64(binary.LittleEndian.Uint64(randBytes(8)))))
}

var cpsTagRe = regexp.MustCompile(`<(\w+)(?:\s+([^>]*))?>`)
var hexArgRe = regexp.MustCompile(`0x([0-9a-fA-F]*)`)

// ByteSize — размер полезной нагрузки одного паттерна (hex внутри <b>, N у
// r/rc/rd, 4 байта у t/c). Совпадает с calcByteSize во фронте.
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
