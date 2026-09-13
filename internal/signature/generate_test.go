package signature

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

var tagRe = regexp.MustCompile(`<(\w+)(?:\s+([^>]*))?>`)

// Токены только из пересечения kernel-модуля и amneziawg-go.
func assertAllowedTokens(t *testing.T, pattern string) {
	t.Helper()
	rest := tagRe.ReplaceAllString(pattern, "")
	if rest != "" {
		t.Fatalf("stray text outside tokens: %q", rest)
	}
	for _, m := range tagRe.FindAllStringSubmatch(pattern, -1) {
		switch m[1] {
		case "b":
			if !regexp.MustCompile(`^0x([0-9a-f]{2})+$`).MatchString(m[2]) {
				t.Fatalf("bad <b> payload %q", m[2])
			}
		case "t":
			if m[2] != "" {
				t.Fatalf("<t> takes no argument: %q", m[0])
			}
		case "r", "rc", "rd":
			if !regexp.MustCompile(`^[1-9]\d{0,3}$`).MatchString(m[2]) {
				t.Fatalf("bad %s size %q", m[1], m[2])
			}
		default:
			t.Fatalf("forbidden token %q", m[0])
		}
	}
}

func TestCanonicalProtocol(t *testing.T) {
	for _, p := range Profiles {
		if CanonicalProtocol(p) != p {
			t.Fatalf("%s not canonical", p)
		}
	}
	for _, bad := range []string{"tls", "quic_0rtt", "http3", "wireguard_noise", "dns_query", ""} {
		if CanonicalProtocol(bad) != "" {
			t.Fatalf("%q must be unknown", bad)
		}
	}
}

func TestGenerate_UnknownProfile(t *testing.T) {
	if _, err := Generate("tls"); !errors.Is(err, ErrUnknownProtocol) {
		t.Fatalf("err = %v, want ErrUnknownProtocol", err)
	}
}

func TestByteSize(t *testing.T) {
	cases := map[string]int{"": 0, "<b 0x0102>": 2, "<r 10><rc 3><rd 2><t>": 19, "<b 0xAB><t>": 5}
	for in, want := range cases {
		if got := ByteSize(in); got != want {
			t.Errorf("ByteSize(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestTokB(t *testing.T) {
	if got := tokB([]byte{0xc3, 0x00, 0xff}); got != "<b 0xc300ff>" {
		t.Fatalf("tokB = %q", got)
	}
}

func TestGenerate_AllProfilesWithinLimitAndGrammar(t *testing.T) {
	for _, p := range Profiles {
		res, err := Generate(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if res.Profile != p || res.ByteSize != TotalByteSize(res.Packets) || TotalChars(res.Packets) > MaxSignatureChars {
			t.Fatalf("%s: %+v", p, res)
		}
		for _, pk := range []string{res.Packets.I1, res.Packets.I2, res.Packets.I3, res.Packets.I4, res.Packets.I5} {
			if pk != "" {
				assertAllowedTokens(t, pk)
			}
		}
		if res.Packets.I1 == "" {
			t.Fatalf("%s: empty I1", p)
		}
		if p != "sip" && res.Packets.I2 != "" {
			t.Fatalf("%s: only SIP uses I2", p)
		}
	}
}

func TestCheckSize(t *testing.T) {
	// Форма из #888: 6060 байт полезной нагрузки при короткой строке. Стенд
	// такое принимает и читает обратно — гейт обязан пропускать.
	heavyPayload := GeneratedPackets{
		I1: "<b 0x" + strings.Repeat("ab", 1200) + ">",
		I2: "<r 1000>", I3: "<r 1000>", I4: "<r 1000>", I5: "<r 1860>",
	}
	if err := CheckSize(heavyPayload); err != nil {
		t.Fatalf("6060 байт нагрузки в 2438 символах должны проходить: %v", err)
	}
	if n := TotalByteSize(heavyPayload); n != 6060 {
		t.Fatalf("TotalByteSize = %d, want 6060", n)
	}

	// Обратный случай: 4096 байт одним <b> — это 8198 символов, стенд на таком
	// теряет возможность прочитать интерфейс (`awg show`: Message too large).
	fatString := GeneratedPackets{I1: "<b 0x" + strings.Repeat("ab", 4096) + ">"}
	if !errors.Is(CheckSize(fatString), ErrPacketsTooLarge) {
		t.Fatal("8198 символов в одном <b> обязаны отвергаться")
	}

	// Сырой текст без токенов — 0 байт по ByteSize, но строку занимает.
	// Предохранитель: размер входа считается от проверяемой константы, и её
	// мутация в большое значение превратила бы тест в пожирателя памяти —
	// прогон валит машину вместо того, чтобы покраснеть.
	if MaxSignatureChars > 1<<20 {
		t.Fatalf("MaxSignatureChars=%d неправдоподобен — тест не станет строить такой вход", MaxSignatureChars)
	}
	raw := GeneratedPackets{I1: strings.Repeat("x", MaxSignatureChars+1)}
	if !errors.Is(CheckSize(raw), ErrPacketsTooLarge) {
		t.Fatal("сырой текст сверх лимита обязан отвергаться")
	}

	edge := GeneratedPackets{I1: strings.Repeat("x", MaxSignatureChars)}
	if err := CheckSize(edge); err != nil {
		t.Fatalf("ровно лимит должен проходить: %v", err)
	}
}

// F166: модуль ядра (junk.c, parse_r_tag и близнецы) проверяет только код
// возврата kstrtoint, не значение. Одиночный отрицательный тег безвреден
// (kzalloc проваливается), но в смеси с <b> сумма выходит положительной,
// конфиг принимается, и в модуле остаётся модификатор с отрицательной длиной.
func TestCheckTags(t *testing.T) {
	bad := map[string]GeneratedPackets{
		"отрицательный r":   {I1: "<r -1>"},
		"отрицательный rc":  {I1: "<rc -5>"},
		"отрицательный rd":  {I1: "<rd -1000>"},
		"смесь с <b>":       {I1: "<b 0x0102><r -1>"},
		"не в I1":           {I3: "<r -7>"},
		"плюс перед числом": {I1: "<r +5>"},
		"сверх потолка":     {I1: "<r 2000000000>"},
		"не число":          {I1: "<r abc>"},
	}
	for name, p := range bad {
		if !errors.Is(CheckTags(p), ErrInvalidPacketTag) {
			t.Errorf("%s: должно отвергаться, CheckTags = %v", name, CheckTags(p))
		}
	}

	ok := []GeneratedPackets{
		{I1: "<r 1000>"},
		{I1: "<r 0>"},            // ноль ядро принимает, вреда нет
		{I1: "<b 0x0102><t><c>"}, // теги без числового аргумента
		{I1: "<b 0x01>", I2: "<r 10>", I5: "<rd 4>"}, // все поля
		{I1: fmt.Sprintf("<r %d>", MaxSplittableTagBytes)},
	}
	for _, p := range ok {
		if err := CheckTags(p); err != nil {
			t.Errorf("%+v: должно проходить, got %v", p, err)
		}
	}
}
