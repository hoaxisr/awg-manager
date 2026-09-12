package nwg

import (
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
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
				// 3.0: адресата в NDMS нет ни у одного.
				HeaderProtectionKey:    "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY=",
				ContentPaddingAddition: "16",
				RekeyAfterTime:         "100-120",
				RekeyTimeout:           "5-7",
				RejectAfterTime:        "180-200",
				KeepaliveTimeout:       "10-12",
				MaxHandshakeAttempts:   "5",
				// 3.1: канал такой же, то есть никакой.
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

// Параметры устройства AWG 3.0/3.1 в импортируемый в NDMS файл не попадают:
// адресата у них там нет, а строгий парсер прошивки пишет на каждый строку
// уровня W («skipping unrecognized parameter») — семь строк на импорт
// premium-туннеля, подтверждено на стенде 12.09.2026.
func TestNDMSImportConf_DropsAWG3DeviceParams(t *testing.T) {
	stored := awg3Tunnel()
	conf, _ := ndmsImportConf(stored)

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

// Сцепка «прошивка умеет 3.0 → мы её этим кормим». Сегодня ASC 3.0 не умеет ни
// одна выпущенная прошивка (ndmsinfo.SupportsWireguardASC3 — жёсткий false), и
// конфиг 3.x ОБЯЗАН уходить на проксирующий путь: параметры устройства
// применяет awg_proxy.ko. В день, когда флаг включат (Keenetic обещает в одной
// из 5.02.A), конфиг поедет нативным путём — и обязан приехать туда полным.
//
// Страж написан так, чтобы покраснеть ровно в этот день: buildASCJSON
// параметры 3.0 сейчас НЕ шлёт сознательно, и включённый флаг без правки
// payload'а поднял бы premium-туннель как 2.0 против сервера, который ждёт
// 3.0, — то есть молча не поднял бы вовсе.
//
// Значение ключа, а не имя поля: как прошивка назовёт его в JSON, пока не
// известно.
func TestASC3FlagAndPayloadMoveTogether(t *testing.T) {
	iface := awg3Tunnel().Interface

	if !ndmsinfo.SupportsWireguardASC3() {
		if ascCoversConfig(&iface, true, ndmsinfo.SupportsWireguardASC3()) {
			t.Fatal("ASC 3.0 нет, а конфиг 3.x не уходит на проксирующий путь: параметры устройства не применит никто")
		}
		return
	}

	raw, err := buildASCJSON(&iface)
	if err != nil {
		t.Fatalf("buildASCJSON: %v", err)
	}
	if !strings.Contains(string(raw), iface.HeaderProtectionKey) {
		t.Fatalf("ASC 3.0 включён, но payload не несёт параметров устройства 3.0 — premium-туннель встанет как 2.0:\n%s", raw)
	}
}
