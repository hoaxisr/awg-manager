package service

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
)

// .conf с HeaderProtectionKey проходил мимо ValidateAWG3: гейт стоял только на
// create/update. Битый ключ доезжал до ядра, туннель вставал с выключенной
// header protection, и ни одна строка об этом не говорила.
func awg31Conf(hpKey string, s1 int) string {
	return `[Interface]
PrivateKey = CA9lE1yLCcziI8Oq0dXDYr3QtdzFCEsKYw8sxAQ132o=
Address = 10.8.0.2/32
MTU = 1280
HeaderProtectionKey = ` + hpKey + `
S1 = ` + strconv.Itoa(s1) + `
S2 = 12
S3 = 12
S4 = 12

[Peer]
PublicKey = hOPHc7ZBk0dGrLLDFrCG7WHYzZ8SS5xBWMzOJ9CkNFo=
Endpoint = 192.0.2.1:51820
AllowedIPs = 0.0.0.0/0
`
}

func TestImportRejectsBadHeaderProtectionKey(t *testing.T) {
	s, tunnels, _ := serviceForCreate(t, &createOp{})

	// 31 байт вместо 32 — pubKeyToHex ниже по течению молча вернёт "".
	_, err := s.Import(context.Background(), awg31Conf("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZQ==", 12), "t", "kernel")
	if err == nil {
		t.Fatal("импорт с битым HeaderProtectionKey обязан провалиться")
	}
	if !strings.Contains(err.Error(), "HeaderProtectionKey") {
		t.Errorf("ошибка должна называть ключ; got: %v", err)
	}
	if entries, _ := os.ReadDir(tunnels); len(entries) != 0 {
		t.Errorf("запись не должна появиться при провале валидации, got %d", len(entries))
	}
}

func TestImportRejectsShortPaddingWithHeaderProtection(t *testing.T) {
	s, _, _ := serviceForCreate(t, &createOp{})

	_, err := s.Import(context.Background(), awg31Conf("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=", 11), "t", "kernel")
	if err == nil {
		t.Fatal("импорт с S1 < 12 при заданном HeaderProtectionKey обязан провалиться")
	}
}

func TestImportAcceptsValidAWG31Conf(t *testing.T) {
	s, _, _ := serviceForCreate(t, &createOp{})

	if _, err := s.Import(context.Background(), awg31Conf("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=", 12), "t", "kernel"); err != nil {
		t.Fatalf("валидный AWG 3.1 .conf обязан импортироваться: %v", err)
	}
}
