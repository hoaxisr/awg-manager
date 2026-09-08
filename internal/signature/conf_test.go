package signature

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestWriteASCConf_SignatureOnlyWithASC(t *testing.T) {
	packets := GeneratedPackets{I1: "<b 0x01>", I3: "<r 8>"}
	asc := json.RawMessage(`{"jc":3,"jmin":8,"jmax":80,"s1":18,"s2":22,"h1":"1","h2":"2","h3":"3","h4":"4"}`)

	var b strings.Builder
	WriteASCConf(&b, asc, packets)
	got := b.String()
	for _, want := range []string{"Jc = 3\n", "S1 = 18\n", "H4 = 4\n", "I1 = <b 0x01>\n", "I3 = <r 8>\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("нет %q:\n%s", want, got)
		}
	}
	// Пустые слоты сигнатуры и не заданные S3/S4 в конфиг не попадают.
	for _, unwanted := range []string{"I2 =", "S3 =", "S4 ="} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("лишняя строка %q:\n%s", unwanted, got)
		}
	}

	var noASC strings.Builder
	WriteASCConf(&noASC, json.RawMessage(`{"jc":0}`), packets)
	if noASC.String() != "" {
		t.Fatalf("сервер без ASC получил хвост:\n%s", noASC.String())
	}

	var broken strings.Builder
	WriteASCConf(&broken, json.RawMessage(`не json`), packets)
	if broken.String() != "" {
		t.Fatalf("битый снимок ASC дал вывод: %q", broken.String())
	}
}

func TestWriteASCConf_EmitsS3S4WhenSet(t *testing.T) {
	var b strings.Builder
	WriteASCConf(&b, json.RawMessage(`{"jc":3,"s3":12,"s4":14}`), GeneratedPackets{})
	got := b.String()
	if !strings.Contains(got, "S3 = 12\n") || !strings.Contains(got, "S4 = 14\n") {
		t.Fatalf("S3/S4 потеряны:\n%s", got)
	}
}

func TestValidateProfileAndSize(t *testing.T) {
	canon, err := ValidateProfileAndSize(" SIP ", GeneratedPackets{I1: "<b 0x01>"})
	if err != nil || canon != "sip" {
		t.Fatalf("канонизация: %q, %v", canon, err)
	}

	// Пустой профиль допустим: сигнатура набрана руками, профиля у неё нет.
	if canon, err := ValidateProfileAndSize("", GeneratedPackets{I1: "<b 0x01>"}); err != nil || canon != "" {
		t.Fatalf("пустой профиль: %q, %v", canon, err)
	}

	if _, err := ValidateProfileAndSize("tls", GeneratedPackets{}); !errors.Is(err, ErrUnknownProtocol) {
		t.Fatalf("неизвестный профиль дал %v", err)
	}

	if _, err := ValidateProfileAndSize("sip", GeneratedPackets{I1: strings.Repeat("a", MaxSignatureRawChars+1)}); !errors.Is(err, ErrPacketsTooLarge) {
		t.Fatalf("сырой текст сверх лимита дал %v", err)
	}
	if _, err := ValidateProfileAndSize("sip", GeneratedPackets{I1: "<r " + strconv.Itoa(MaxSignatureBytes+1) + ">"}); !errors.Is(err, ErrPacketsTooLarge) {
		t.Fatalf("байты сверх лимита дали %v", err)
	}
}
