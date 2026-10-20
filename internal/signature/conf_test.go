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

	// Предохранитель: размер входа считается от проверяемой константы, и её
	// мутация в большое значение превратила бы тест в пожирателя памяти —
	// прогон валит машину вместо того, чтобы покраснеть.
	if MaxSignatureChars > 1<<20 {
		t.Fatalf("MaxSignatureChars=%d неправдоподобен — тест не станет строить такой вход", MaxSignatureChars)
	}
	if _, err := ValidateProfileAndSize("sip", GeneratedPackets{I1: strings.Repeat("a", MaxSignatureChars+1)}); !errors.Is(err, ErrPacketsTooLarge) {
		t.Fatalf("строка сверх лимита дала %v", err)
	}
	if _, err := ValidateProfileAndSize("sip", GeneratedPackets{I1: "<r " + strconv.Itoa(MaxSignatureChars*2) + ">"}); err != nil {
		t.Fatalf("большая нагрузка в короткой строке должна проходить: %v", err)
	}
}

// F167: пустые H1..H4 в клиентском .conf роняют весь `awg setconf`
// (`Line unrecognized: 'H1='`). Пусто = дефолт ядра: номера типов сообщений.
func TestWriteASCConf_EmptyHeadersFallBackToMessageTypes(t *testing.T) {
	var b strings.Builder
	WriteASCConf(&b, json.RawMessage(`{"jc":4,"jmin":50,"jmax":1000,"s1":56,"s2":78}`), GeneratedPackets{I1: "<b 0x01>"})

	out := b.String()
	if strings.Contains(out, "H1 = \n") {
		t.Fatalf("пустой H1 не должен попадать в .conf:\n%s", out)
	}
	for _, want := range []string{"H1 = 1", "H2 = 2", "H3 = 3", "H4 = 4"} {
		if !strings.Contains(out, want) {
			t.Fatalf("нет %q в:\n%s", want, out)
		}
	}
}

func TestHeaderOrDefault(t *testing.T) {
	if got := HeaderOrDefault("", 3); got != "3" {
		t.Fatalf("пусто → %q, want \"3\"", got)
	}
	if got := HeaderOrDefault("   ", 4); got != "4" {
		t.Fatalf("пробелы → %q, want \"4\"", got)
	}
	if got := HeaderOrDefault("1635672874-1803270462", 1); got != "1635672874-1803270462" {
		t.Fatalf("диапазон не должен подменяться, got %q", got)
	}
}

// Параметры 3.x (#688, F681): rc отдаёт числа, пара start/end — "N" или
// "N-M", нули не пишутся, флаги — "on" (грамматика config.writeAWG3Params).
func TestWriteASCConf_ASC3(t *testing.T) {
	for name, raw := range map[string]string{
		"числа":  `{"jc":3,"header-protection-key":"K=","rekey-after-time-start":120,"rekey-after-time-end":120,"content-padding-addition-start":0,"content-padding-addition-end":80,"keepalive-timeout-start":0,"keepalive-timeout-end":0,"random-trailers":1,"disable-cookies":0}`,
		"строки": `{"jc":3,"header-protection-key":"K=","rekey-after-time-start":"120","rekey-after-time-end":"120","content-padding-addition-start":"0","content-padding-addition-end":"80","keepalive-timeout-start":"0","keepalive-timeout-end":"0","random-trailers":"1","disable-cookies":"0"}`,
	} {
		var b strings.Builder
		WriteASCConf(&b, json.RawMessage(raw), GeneratedPackets{})
		got := b.String()
		for _, want := range []string{"HeaderProtectionKey = K=\n", "RekeyAfterTime = 120\n", "ContentPaddingAddition = 0-80\n", "RandomTrailers = on\n"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: нет %q:\n%s", name, want, got)
			}
		}
		if strings.Contains(got, "KeepaliveTimeout") || strings.Contains(got, "DisableCookies") {
			t.Errorf("%s: незаданное записано:\n%s", name, got)
		}
	}
}

// ASC 3.1 с Jc = 0 (сервер junk не шлёт) — не «обычный WireGuard»: без S/H/HP
// клиент не подключится (ревью #688).
func TestWriteASCConf_ASC3WithoutJunk(t *testing.T) {
	var b strings.Builder
	WriteASCConf(&b, json.RawMessage(`{"jc":0,"s1":16,"s2":17,"s3":18,"s4":19,"h1":"1","h2":"2","h3":"3","h4":"4","header-protection-key":"K=","random-trailers":1}`), GeneratedPackets{})
	got := b.String()
	for _, want := range []string{"S1 = 16\n", "S4 = 19\n", "HeaderProtectionKey = K=\n", "RandomTrailers = on\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("нет %q:\n%s", want, got)
		}
	}
}
