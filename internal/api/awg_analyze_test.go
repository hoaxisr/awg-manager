package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// 32 байта в base64 — валидный HeaderProtectionKey для ValidateAWG3.
const testHPKey = "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE="

const confPlainWG = `[Interface]
PrivateKey = cGVlclByaXZhdGVLZXlCYXNlNjRFeGFtcGxlMDAwMDAwMD0=
Address = 10.8.0.2/32
MTU = 1420

[Peer]
PublicKey = c2VydmVyUHVibGljS2V5QmFzZTY0RXhhbXBsZTAwMD0=
Endpoint = vpn.example.com:51820
AllowedIPs = 0.0.0.0/0
PersistentKeepalive = 25
`

func confWith(extraIface string, extraPeer string) string {
	c := strings.Replace(confPlainWG, "MTU = 1420\n", "MTU = 1420\n"+extraIface, 1)
	return strings.Replace(c, "PersistentKeepalive = 25\n", "PersistentKeepalive = 25\n"+extraPeer, 1)
}

func TestAnalyzeAwgConf_Versions(t *testing.T) {
	cases := []struct {
		name  string
		iface string
		want  string
	}{
		{"wg", "", "wg"},
		{"awg1.0", "Jc = 4\nJmin = 50\nJmax = 1000\nS1 = 15\nS2 = 30\nH1 = 10\nH2 = 20\nH3 = 30\nH4 = 40\n", "awg1.0"},
		{"awg1.5", "H1 = 10\nH2 = 20\nH3 = 30\nH4 = 40\nI1 = <b 0xc0000001>\n", "awg1.5"},
		{"awg2.0", "H1 = 10-2000\nH2 = 3000-4000\nH3 = 5000-6000\nH4 = 7000-8000\n", "awg2.0"},
		{"awg3 timers only", "H1 = 10\nH2 = 20\nH3 = 30\nH4 = 40\nRekeyAfterTime = 120-150\n", "awg3"},
		{"awg3.1 flags", "H1 = 1\nH2 = 2\nH3 = 3\nH4 = 4\nS1 = 12\nS2 = 12\nS3 = 12\nS4 = 12\nHeaderProtectionKey = " + testHPKey + "\nRandomTrailers = on\n", "awg3.1"},
		{"showconf с off — не 3.1", "H1 = 10\nH2 = 20\nH3 = 30\nH4 = 40\nRandomTrailers = off\nDisableCookies = off\n", "awg1.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := analyzeAwgConf(confWith(tc.iface, ""), nil, "")
			if err != nil {
				t.Fatalf("analyze: %v", err)
			}
			if d.Version != tc.want {
				t.Fatalf("version: want %s, got %s", tc.want, d.Version)
			}
		})
	}
}

func TestAnalyzeAwgConf_NeverLeaksKeys(t *testing.T) {
	const psk = "cHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHM9"
	const privKey = "cGVlclByaXZhdGVLZXlCYXNlNjRFeGFtcGxlMDAwMDAwMD0="
	d, err := analyzeAwgConf(confWith("HeaderProtectionKey = "+testHPKey+"\nS1 = 12\nS2 = 12\nS3 = 12\nS4 = 12\nH1 = 10\nH2 = 20\nH3 = 30\nH4 = 40\n", "PresharedKey = "+psk+"\n"), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Interface.HeaderProtection || !d.HasPrivateKey || !d.Peer.HasPresharedKey {
		t.Fatalf("flags: hp=%v pk=%v psk=%v", d.Interface.HeaderProtection, d.HasPrivateKey, d.Peer.HasPresharedKey)
	}
	// Ответ сериализуется целиком в handler'е — проверяем итоговый JSON, а
	// не отдельные поля: так утечка через любое строковое поле DTO ловится.
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{testHPKey, privKey, psk} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("key leaked into JSON: %q\n%s", secret, b)
		}
	}
}

func TestAnalyzeAwgConf_HPPaddingError(t *testing.T) {
	d, err := analyzeAwgConf(confWith("HeaderProtectionKey = "+testHPKey+"\nS1 = 12\nS2 = 5\nS3 = 12\nS4 = 12\nH1 = 10\nH2 = 20\nH3 = 30\nH4 = 40\n", ""), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Errors) != 1 || d.Errors[0].Code != "hp_padding_min" {
		t.Fatalf("want hp_padding_min, got %+v", d.Errors)
	}
}

func TestAnalyzeAwgConf_HPKeyInvalid(t *testing.T) {
	d, err := analyzeAwgConf(confWith("HeaderProtectionKey = bm90LWEta2V5\nS1 = 12\nS2 = 12\nS3 = 12\nS4 = 12\nH1 = 10\nH2 = 20\nH3 = 30\nH4 = 40\n", ""), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Errors) != 1 || d.Errors[0].Code != "hp_key_invalid" {
		t.Fatalf("want hp_key_invalid, got %+v", d.Errors)
	}
}

func TestAnalyzeAwgConf_HeaderOverlapError(t *testing.T) {
	d, err := analyzeAwgConf(confWith("H1 = 10-100\nH2 = 50\nH3 = 300\nH4 = 400\n", ""), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Errors) != 1 || d.Errors[0].Code != "h_overlap" {
		t.Fatalf("want h_overlap, got %+v", d.Errors)
	}
}

func TestAnalyzeAwgConf_ModuleWarnings(t *testing.T) {
	awg3 := "H1 = 10\nH2 = 20\nH3 = 30\nH4 = 40\nS1 = 12\nS2 = 12\nS3 = 12\nS4 = 12\nHeaderProtectionKey = " + testHPKey + "\n"
	awg31 := awg3 + "RandomTrailers = on\n"
	cases := []struct {
		name, iface, kmod string
		wantCode          string
	}{
		{"3.0 на модуле 1.x", awg3, "1.0.20250706", "module_below_awg3"},
		{"3.0 на модуле 3.0", awg3, "3.0.20260801", ""},
		{"3.1 на модуле 3.0", awg31, "3.0.20260801", "module_below_awg31"},
		{"3.1 на модуле 3.1", awg31, "3.1.20260906", ""},
		{"версия неизвестна — без предупреждений", awg31, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := analyzeAwgConf(confWith(tc.iface, ""), nil, tc.kmod)
			if err != nil {
				t.Fatal(err)
			}
			got := ""
			if len(d.Warnings) > 0 {
				got = d.Warnings[0].Code
			}
			if got != tc.wantCode {
				t.Fatalf("want %q, got %q (%+v)", tc.wantCode, got, d.Warnings)
			}
		})
	}
}

func TestAnalyzeAwgConf_NativeWGSkipsModuleWarnings(t *testing.T) {
	awg31 := "H1 = 10\nH2 = 20\nH3 = 30\nH4 = 40\nS1 = 12\nS2 = 12\nS3 = 12\nS4 = 12\nHeaderProtectionKey = " + testHPKey + "\nRandomTrailers = on\n"
	stored := &storage.AWGTunnel{Backend: "nativewg"}
	d, err := analyzeAwgConf(confWith(awg31, ""), stored, "1.0.20250706")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Warnings) != 0 {
		t.Fatalf("nativewg must not warn about kernel module: %+v", d.Warnings)
	}
}

// #865: GET туннеля не отдаёт PSK и PrivateKey, текст из него приходит без
// них — ключи берутся из хранилища по tunnelId.
func TestAnalyzeAwgConf_MergesStoredKeys(t *testing.T) {
	noKeys := strings.Replace(confPlainWG, "PrivateKey = cGVlclByaXZhdGVLZXlCYXNlNjRFeGFtcGxlMDAwMDAwMD0=\n", "", 1)
	stored := &storage.AWGTunnel{
		Interface: storage.AWGInterface{PrivateKey: "c3RvcmVkUHJpdmF0ZUtleUJhc2U2NEV4YW1wbGUwMDAwMD0="},
		Peer:      storage.AWGPeer{PresharedKey: "c3RvcmVkUFNLcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHM9"},
	}
	d, err := analyzeAwgConf(noKeys, stored, "")
	if err != nil {
		t.Fatalf("analyze with stored keys: %v", err)
	}
	if !d.HasPrivateKey || !d.Peer.HasPresharedKey || !d.Peer.PresharedKeyFromStore {
		t.Fatalf("stored keys not merged: pk=%v psk=%v fromStore=%v", d.HasPrivateKey, d.Peer.HasPresharedKey, d.Peer.PresharedKeyFromStore)
	}
	// PSK в тексте — не «из туннеля».
	inText, err := analyzeAwgConf(confWith("", "PresharedKey = cHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHM9\n"), stored, "")
	if err != nil {
		t.Fatalf("analyze psk in text: %v", err)
	}
	if !inText.Peer.HasPresharedKey || inText.Peer.PresharedKeyFromStore {
		t.Fatalf("psk from text must not be flagged as from store: %+v", inText.Peer)
	}
	// Контроль: без хранилища тот же текст не парсится вовсе.
	if _, err := analyzeAwgConf(noKeys, nil, ""); err == nil {
		t.Fatal("conf without PrivateKey must fail to parse when no stored tunnel")
	}
}

// Parse подставляет MTU 1280, keepalive 25 и AllowedIPs по умолчанию —
// фронт должен отличать их от заданных пользователем.
func TestAnalyzeAwgConf_DefaultsFlagged(t *testing.T) {
	bare := "[Interface]\nPrivateKey = cGVlclByaXZhdGVLZXlCYXNlNjRFeGFtcGxlMDAwMDAwMD0=\nAddress = 10.8.0.2/32\n\n[Peer]\nPublicKey = c2VydmVyUHVibGljS2V5QmFzZTY0RXhhbXBsZTAwMD0=\nEndpoint = vpn.example.com:4443\n"
	d, err := analyzeAwgConf(bare, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Interface.MTUSet || d.Peer.KeepaliveSet || d.Peer.AllowedIPsSet {
		t.Fatalf("defaults flagged as set: mtu=%v ka=%v allowed=%v", d.Interface.MTUSet, d.Peer.KeepaliveSet, d.Peer.AllowedIPsSet)
	}
	if d.Interface.MTU != 1280 || d.Peer.PersistentKeepalive != "25" || len(d.Peer.AllowedIPs) != 2 {
		t.Fatalf("parser defaults changed: %+v %+v", d.Interface.MTU, d.Peer)
	}
	full, err := analyzeAwgConf(confPlainWG, nil, "")
	if err != nil {
		t.Fatalf("analyze full conf: %v", err)
	}
	if !full.Interface.MTUSet || !full.Peer.KeepaliveSet || !full.Peer.AllowedIPsSet {
		t.Fatalf("explicit values not flagged: %+v %+v", full.Interface.MTUSet, full.Peer)
	}
}

func TestMergeStoredKeys_KeepsExplicitText(t *testing.T) {
	stored := &storage.AWGTunnel{
		Interface: storage.AWGInterface{PrivateKey: "STORED"},
		Peer:      storage.AWGPeer{PresharedKey: "STOREDPSK"},
	}
	out := mergeStoredKeys(confWith("", "PresharedKey = TEXTPSK\n"), stored)
	if strings.Contains(out, "STORED\n") || strings.Contains(out, "STOREDPSK") {
		t.Fatalf("explicit keys in text must win:\n%s", out)
	}
}

// Пустая строка ключа в тексте (config.Generate пишет `PrivateKey = %s`
// безусловно, tunnels_view.go затирает значение пустой строкой) не должна
// побеждать ключ из хранилища: она должна быть заменена, а не оставлена
// висеть ПОСЛЕ вставленной строки — иначе config.Parse берёт последнее
// встреченное значение, то есть пустое.
func TestMergeStoredKeys_EmptyKeyLineIsReplaced(t *testing.T) {
	conf := strings.Replace(confPlainWG, "PrivateKey = cGVlclByaXZhdGVLZXlCYXNlNjRFeGFtcGxlMDAwMDAwMD0=\n", "PrivateKey =\n", 1)
	conf = strings.Replace(conf, "PersistentKeepalive = 25\n", "PersistentKeepalive = 25\nPresharedKey =\n", 1)
	stored := &storage.AWGTunnel{
		Interface: storage.AWGInterface{PrivateKey: "c3RvcmVkUHJpdmF0ZUtleUJhc2U2NEV4YW1wbGUwMDAwMD0="},
		Peer:      storage.AWGPeer{PresharedKey: "c3RvcmVkUFNLcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHM9"},
	}
	d, err := analyzeAwgConf(conf, stored, "")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if !d.HasPrivateKey || !d.Peer.HasPresharedKey || !d.Peer.PresharedKeyFromStore {
		t.Fatalf("empty key lines not replaced: pk=%v psk=%v fromStore=%v", d.HasPrivateKey, d.Peer.HasPresharedKey, d.Peer.PresharedKeyFromStore)
	}
	out := mergeStoredKeys(conf, stored)
	if strings.Count(out, "PrivateKey") != 1 {
		t.Fatalf("want exactly one PrivateKey line, got:\n%s", out)
	}
}

func TestModuleSupports(t *testing.T) {
	cases := []struct {
		v            string
		major, minor int
		want         bool
	}{
		{"", 3, 1, true},
		{"garbage", 3, 1, true},
		{"1.0.20250706", 3, 0, false},
		{"3.0.20260801", 3, 0, true},
		{"3.0.20260801", 3, 1, false},
		{"3.1.20260906", 3, 1, true},
		{"4.0.1", 3, 1, true},
	}
	for _, tc := range cases {
		if got := moduleSupports(tc.v, tc.major, tc.minor); got != tc.want {
			t.Errorf("moduleSupports(%q, %d.%d) = %v, want %v", tc.v, tc.major, tc.minor, got, tc.want)
		}
	}
}

func TestAnalyzeAwgConf_NormalizedFields(t *testing.T) {
	d, err := analyzeAwgConf(confWith("Jc = 4\nJmin = 50\nJmax = 1000\nS1 = 15\nS2 = 30\nH1 = 10\nH2 = 20\nH3 = 30\nH4 = 40\nI1 = <b 0xc0>\nI2 = <b 0x01>\nContentPaddingAddition = 0-64\nDNS = 10.8.0.1\n", "PersistentKeepalive = 25-35\n"), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	i := d.Interface
	if i.Jc != 4 || i.Jmin != 50 || i.Jmax != 1000 || i.S1 != 15 || i.S2 != 30 || i.H1 != "10" || i.I2 != "<b 0x01>" || i.ContentPaddingAddition != "0-64" || i.MTU != 1420 || i.DNS != "10.8.0.1" {
		t.Fatalf("interface fields: %+v", i)
	}
	if d.Peer.Endpoint != "vpn.example.com:51820" || len(d.Peer.AllowedIPs) != 1 || d.Peer.PersistentKeepalive != "25-35" {
		t.Fatalf("peer fields: %+v", d.Peer)
	}
}
