package nwg

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
)

// awg3Tunnel — запись premium-формы: обфускация 2.0 плюс полный набор
// параметров устройства AWG 3.0 и оба флага 3.1.
func awg3Tunnel() *storage.AWGTunnel {
	return &storage.AWGTunnel{
		Name: "t1",
		Interface: storage.AWGInterface{
			PrivateKey: "wMvstfyVWYn6WGn5CjVlSsGj/9tzCvdNoIjPB/Vsc1w=",
			Address:    "10.13.14.5",
			AWGObfuscation: storage.AWGObfuscation{
				// 2.0: остаётся в файле — это прошивка понимает.
				Jc: 4, Jmin: 40, Jmax: 70,
				S1: 80, S2: 90,
				H1: "1000000-1100000", H2: "1200000-1300000",
				H3: "1400000-1500000", H4: "1600000-1700000",
				// 3.0: до 5.02.A.11 адресата в NDMS нет ни у одного.
				HeaderProtectionKey:    "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY=",
				ContentPaddingAddition: "16",
				RekeyAfterTime:         "100-120",
				RekeyTimeout:           "5-7",
				RejectAfterTime:        "180-200",
				KeepaliveTimeout:       "10-12",
				MaxHandshakeAttempts:   "5",
				// 3.1: канал такой же.
				RandomTrailers: true,
				DisableCookies: true,
			},
		},
		Peer: storage.AWGPeer{
			PublicKey:           "GLi3Az9hKTEm2GT4Jlktzy3t0nB6Abb4Svf9JlxS4Q4=",
			Endpoint:            "vpn.example.test:32949",
			PersistentKeepalive: "25",
		},
	}
}

// Без ASC3 параметры устройства AWG 3.0/3.1 в импортируемый в NDMS файл не
// попадают: адресата у них там нет, а строгий парсер прошивки пишет на каждый
// строку уровня W («skipping unrecognized parameter») — семь строк на импорт
// premium-туннеля, подтверждено на стенде 12.09.2026.
func TestNDMSImportConf_DropsAWG3DeviceParams(t *testing.T) {
	stored := awg3Tunnel()
	conf, _ := ndmsImportConf(stored, false)

	for _, key := range []string{
		"HeaderProtectionKey", "ContentPaddingAddition", "RekeyAfterTime",
		"RekeyTimeout", "RejectAfterTime", "KeepaliveTimeout",
		"MaxHandshakeAttempts", "RandomTrailers", "DisableCookies",
	} {
		if strings.Contains(conf, key) {
			t.Errorf("параметр %s доехал до импортируемого .conf — прошивка ответит строкой W:\n%s", key, conf)
		}
	}

	// Обфускация 2.0 остаётся: её прошивка понимает, и на нативном пути она
	// приезжает именно импортом.
	for _, line := range []string{"Jc = 4", "S1 = 80", "H1 = 1000000-1100000"} {
		if !strings.Contains(conf, line) {
			t.Errorf("вместе с 3.0 потерян параметр 2.0 %q:\n%s", line, conf)
		}
	}

	// Запись туннеля не трогаем: пользовательская выгрузка обязана вернуть то,
	// что он импортировал, а kmod берёт параметры 3.0 именно из неё.
	if stored.Interface.HeaderProtectionKey == "" || !stored.Interface.RandomTrailers {
		t.Error("запись туннеля изменена — kmod и выгрузка потеряют параметры 3.0")
	}
	if !strings.Contains(config.GenerateForExport(stored), "HeaderProtectionKey = ") {
		t.Error("параметры 3.0 пропали из пользовательской выгрузки")
	}
}

// С ASC3 (5.02.A.11+) импорт несёт все девять: прошивка сохраняет их в
// `wireguard asc` без единой строки W (стенд 25.09.2026).
func TestNDMSImportConf_KeepsAWG3OnASC3(t *testing.T) {
	conf, _ := ndmsImportConf(awg3Tunnel(), true)
	for _, line := range []string{
		"HeaderProtectionKey = YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY=",
		"ContentPaddingAddition = 16", "RekeyAfterTime = 100-120", "RekeyTimeout = 5-7",
		"RejectAfterTime = 180-200", "KeepaliveTimeout = 10-12", "MaxHandshakeAttempts = 5",
		"RandomTrailers = on", "DisableCookies = on",
	} {
		if !strings.Contains(conf, line) {
			t.Errorf("нет %q в импорте:\n%s", line, conf)
		}
	}
}

// Страж дрейфа: новый параметр устройства AWG 3.x, добавленный в типы и
// генератор, обязан сниматься здесь же. Проверка идёт не по списку имён (он бы
// разъехался молча), а по КЛАССИФИКАТОРУ: пока в интерфейсе остаётся хоть один
// параметр 3.x, ClassifyAWGVersion возвращает awg3/awg3.1.
func TestStripAWG3Params_LeavesNothingOfVersion3(t *testing.T) {
	iface := awg3Tunnel().Interface
	if got := config.ClassifyAWGVersion(&iface); got != "awg3.1" {
		t.Fatalf("фикстура перестала быть конфигом 3.1 (%s) — тест проверяет не то", got)
	}

	stripAWG3Params(&iface)

	switch got := config.ClassifyAWGVersion(&iface); got {
	case "awg3", "awg3.1":
		t.Fatalf("после снятия конфиг всё ещё %s — параметр 3.x остался в файле", got)
	case "awg2.0":
	default:
		t.Fatalf("снято лишнее: обфускация 2.0 обязана пережить снятие, получили %s", got)
	}
}

// Сцепка «прошивка умеет 3.x → мы её этим кормим». Без ASC3 конфиг 3.x обязан
// уходить на awg_proxy.ko (параметры устройства применяет он); с ASC3 — идти
// нативно и приехать ПОЛНЫМ: набор 3.x прошивка принимает только целиком,
// неполный молча игнорирует (пустой ответ, стенд 5.02.A.11). Поэтому сверка
// идёт по всему набору ключей и значениям — они сняты со стенда
// (`show/rc/interface/<имя>/wireguard/asc` после импорта того же конфига).
func TestASC3PathAndPayload(t *testing.T) {
	iface := awg3Tunnel().Interface
	if ascCoversConfig(&iface, true, false) {
		t.Fatal("ASC 3.x нет, а конфиг 3.x не уходит на проксирующий путь: параметры устройства не применит никто")
	}
	if !ascCoversConfig(&iface, true, true) {
		t.Fatal("ASC 3.x есть, а конфиг 3.x всё ещё уходит на awg_proxy")
	}

	// Без ASC3 полей 3.x нет даже при конфиге 3.x: прошивка их не знает.
	if raw, _ := buildASCJSON(&iface, false); strings.Contains(string(raw), "header-protection-key") {
		t.Fatalf("без ASC3 ушли поля 3.x: %s", raw)
	}

	raw, err := buildASCJSON(&iface, true)
	if err != nil {
		t.Fatalf("buildASCJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"jc": 4.0, "jmin": 40.0, "jmax": 70.0, "s1": 80.0, "s2": 90.0,
		"h1": "1000000-1100000", "h2": "1200000-1300000", "h3": "1400000-1500000", "h4": "1600000-1700000",
		"s3": 0.0, "s4": 0.0, "i1": "", "i2": "", "i3": "", "i4": "", "i5": "",
		"header-protection-key":          "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY=",
		"content-padding-addition-start": 16.0, "content-padding-addition-end": 16.0,
		"rekey-after-time-start": 100.0, "rekey-after-time-end": 120.0,
		"rekey-timeout-start": 5.0, "rekey-timeout-end": 7.0,
		"reject-after-time-start": 180.0, "reject-after-time-end": 200.0,
		"keepalive-timeout-start": 10.0, "keepalive-timeout-end": 12.0,
		"max-handshake-attempts-start": 5.0, "max-handshake-attempts-end": 5.0,
		"random-trailers": 1.0, "disable-cookies": 1.0,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("payload ASC 3.x:\n got %v\nwant %v", got, want)
	}

	// Конфиг 3.0 без флагов 3.1: нулевые флаги обязаны уйти явными нулями —
	// без поля прошивка проигнорирует весь запрос.
	iface.RandomTrailers, iface.DisableCookies = false, false
	raw, err = buildASCJSON(&iface, true)
	var got30 map[string]any
	if err != nil || json.Unmarshal(raw, &got30) != nil {
		t.Fatalf("ASC 3.0: %v %s", err, raw)
	}
	want["random-trailers"], want["disable-cookies"] = 0.0, 0.0
	if !reflect.DeepEqual(got30, want) {
		t.Fatalf("payload ASC 3.0:\n got %v\nwant %v", got30, want)
	}

	// Конфиг 2.0 на той же прошивке: полей 3.x нет вовсе — запись без них
	// снимает прежние 3.x с интерфейса (стенд), что и нужно при понижении.
	iface.HeaderProtectionKey, iface.ContentPaddingAddition, iface.RekeyAfterTime = "", "", ""
	iface.RekeyTimeout, iface.RejectAfterTime, iface.KeepaliveTimeout, iface.MaxHandshakeAttempts = "", "", "", ""
	iface.RandomTrailers, iface.DisableCookies = false, false
	raw, _ = buildASCJSON(&iface, true)
	if strings.Contains(string(raw), "header-protection-key") {
		t.Fatalf("конфиг 2.0 несёт поля 3.x: %s", raw)
	}
}

func TestASCAWG3JSON_BadValue(t *testing.T) {
	iface := awg3Tunnel().Interface
	iface.RekeyAfterTime = "abc"
	if _, err := buildASCJSON(&iface, true); err == nil || !strings.Contains(err.Error(), "RekeyAfterTime") {
		t.Fatalf("err = %v", err)
	}
}
