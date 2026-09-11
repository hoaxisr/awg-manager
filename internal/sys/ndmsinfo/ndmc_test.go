package ndmsinfo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Дословный ответ `ndmc -c "show version"` со стенда (KN-1810, 5.01.C.3.0-1,
// 2026-09-11), включая перенос значений по ширине посреди токена и ^[[K.
const standNdmcOutput = "\x1b[K\n" + `
          release: 5.01.C.3.0-1
          sandbox: stable
            title: 5.1.3
             arch: mips

              ndm:
                exact: 0-b73c650e10
                cdate: 31 Jul 2026

              bsp:
                exact: 0-597f798bc6
                cdate: 31 Jul 2026

              ndw:
             features: wifi_button,wifi5ghz,usb_3_conf,usb_3_first,
                       led_control,vht2ghz,bf2ghz,mimo5ghz,atf2ghz,atf5ghz,
                       dual_image,wifi_ft,wpa3,hwnat,sfp
           components: acl,base,cloudcontrol,corewireless,dhcpd,dns-
                       https,dns-tls,dot1x,exfat,ext,fat,hfsplus,igmp,ip6,lang-
                       en,lang-ru,mdns,miniupnpd,mws,ndmp,ndns,ntfs,opkg,opkg-
                       kmod-audio,opkg-kmod-fs,opkg-kmod-netfilter,opkg-kmod-
                       netfilter-addons,opkg-kmod-tc,opkg-kmod-usbip,opkg-kmod-
                       video,pingcheck,ppe,pppoe,proxy,storage,trafficcontrol,
                       tsmb,usb,wireguard

             ndw4:
              version: 5.1.C.3.0

     manufacturer: Keenetic Ltd.
           vendor: Keenetic
           series: KN
            model: Ultra (KN-1810)
       hw_version: 10188000
          hw_type: router
            hw_id: KN-1810
           device: Ultra
          consent: EA
           region: EA
      description: Keenetic Ultra (KN-1810)
` + "\x1b[K"

func TestParseNdmcVersion_Stand(t *testing.T) {
	v := parseNdmcVersion(standNdmcOutput)

	// Релиз — то, ради чего второй канал и заведён: из него osdetect делает
	// вывод 4.x/5.x. Вложенный ndw4.version (5.1.C.3.0) не должен его перебить.
	if v.Release != "5.01.C.3.0-1" {
		t.Errorf("Release = %q, хотели 5.01.C.3.0-1", v.Release)
	}
	if v.Title != "5.1.3" {
		t.Errorf("Title = %q, хотели 5.1.3", v.Title)
	}
	if v.HardwareID != "KN-1810" || v.Device != "Ultra" || v.Region != "EA" {
		t.Errorf("hw_id/device/region = %q/%q/%q", v.HardwareID, v.Device, v.Region)
	}
	// Двоеточий в значении нет, но скобки есть — ключом это быть не должно.
	if v.Model != "Ultra (KN-1810)" {
		t.Errorf("Model = %q", v.Model)
	}
	if v.Description != "Keenetic Ultra (KN-1810)" {
		t.Errorf("Description = %q", v.Description)
	}

	// Компоненты: значение перенесено по ширине ПОСРЕДИ токена (dns-https,
	// lang-en, opkg-kmod-*). Склейка встык обязана их восстановить — на этом
	// держатся HasWireguardComponent и родня.
	want := map[string]bool{
		"wireguard": false, "pingcheck": false, "proxy": false,
		"dns-https": false, "lang-en": false, "opkg-kmod-netfilter": false,
	}
	for _, c := range v.Components {
		if _, ok := want[c]; ok {
			want[c] = true
		}
	}
	for c, found := range want {
		if !found {
			t.Errorf("компонент %q не разобран; всего разобрано %d: %v", c, len(v.Components), v.Components)
		}
	}
	if len(v.Components) != 39 {
		t.Errorf("компонентов %d, хотели 39 (перенос склеен неверно?)", len(v.Components))
	}
}

func TestParseNdmcVersion_Empty(t *testing.T) {
	if v := parseNdmcVersion(""); v.Release != "" {
		t.Errorf("пустой ввод дал Release=%q", v.Release)
	}
}

// Вложенный ключ не должен перебивать верхнеуровневый: `release` под ndw4
// объявил бы четвёрку пятёркой, а с умолчанием osdetect на 5.x это уже ничем
// не страхуется.
func TestParseNdmcVersion_NestedKeyDoesNotOverride(t *testing.T) {
	out := `
          release: 4.03.C.1.0-1
            title: 4.3.1

             ndw4: 
              release: 5.9.9
              version: 5.1.C.3.0
`
	if got := parseNdmcVersion(out).Release; got != "4.03.C.1.0-1" {
		t.Errorf("Release = %q, хотели 4.03.C.1.0-1 (вложенный ключ перебил верхний)", got)
	}
}

// Незнакомый ключ (дефис, точка, заглавная — другая прошивка) обязан быть
// пропущен, а не приклеен к предыдущему значению: приклеенный портит release.
func TestParseNdmcVersion_UnknownKeyDoesNotCorruptNeighbour(t *testing.T) {
	for _, junk := range []string{"hw-version: 10188000", "Sandbox: stable", "ndw.x: 1"} {
		out := "          release: 4.03.C.1.0-1\n          " + junk + "\n"
		if got := parseNdmcVersion(out).Release; got != "4.03.C.1.0-1" {
			t.Errorf("после %q Release = %q, хотели 4.03.C.1.0-1", junk, got)
		}
	}
}

// Мусор вместо релиза не должен усыновляться: страж отбраковывает по форме.
func TestLooksLikeRelease(t *testing.T) {
	good := []string{"5.01.C.3.0-1", "4.03.C.8.0-0", "5.1.3"}
	bad := []string{"", "junk", "5", "5.", ".5", "x.1", strings.Repeat("9.9", 40)}
	for _, s := range good {
		if !looksLikeRelease(s) {
			t.Errorf("%q обязан быть принят", s)
		}
	}
	for _, s := range bad {
		if looksLikeRelease(s) {
			t.Errorf("%q обязан быть отвергнут", s)
		}
	}
}

// fakeNdmc подменяет бинарь скриптом, печатающим заданный текст.
func fakeNdmc(t *testing.T, stdout string, code int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ndmc")
	script := "#!/bin/sh\ncat <<'NDMCEOF'\n" + stdout + "\nNDMCEOF\nexit " + itoa(code) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := ndmcBinary
	ndmcBinary = path
	t.Cleanup(func() { ndmcBinary = old })
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	return string(rune('0' + i))
}

// Живой ответ второго канала доезжает до ndms.Version целиком.
func TestVersionFromNdmc_Success(t *testing.T) {
	fakeNdmc(t, standNdmcOutput, 0)

	v, err := versionFromNdmc(context.Background())
	if err != nil {
		t.Fatalf("versionFromNdmc: %v", err)
	}
	if v.Release != "5.01.C.3.0-1" || len(v.Components) != 39 {
		t.Errorf("release=%q компонентов=%d", v.Release, len(v.Components))
	}
	if v.LastFetched.IsZero() {
		t.Error("LastFetched обязан проставляться")
	}
}

// Мусор вместо версии НЕ усыновляется: иначе по нему заморозится выбор
// оператора, а это неисправимо без перезапуска демона.
func TestVersionFromNdmc_RejectsGarbage(t *testing.T) {
	for _, out := range []string{
		"", "совершенно не то", "release: мусор",
		"release: 99999999999999999999999999999999999999999999999999999999999999999999",
		// Мусорный релиз при ПРАВДОПОДОБНОМ списке компонентов: страж
		// пустых components такой вход пропускает, отбраковать его обязан
		// именно страж формы релиза.
		"release: мусор\ncomponents: base,dns-tls,wireguard",
	} {
		fakeNdmc(t, out, 0)
		if v, err := versionFromNdmc(context.Background()); err == nil {
			t.Errorf("мусор %q принят как версия %q", out, v.Release)
		}
	}
}

// Обрезанный вывод не усыновляется. exec при ErrWaitDelay обнуляет ошибку с
// пометкой «output may be truncated», и срез может прийтись между release и
// components. Пустой список компонентов означал бы HasWireguardComponent()
// == false до конца жизни процесса: nativewg объявлен недоступным.
func TestVersionFromNdmc_RejectsTruncatedOutput(t *testing.T) {
	cut := standNdmcOutput
	if i := strings.Index(cut, "components:"); i > 0 {
		cut = cut[:i]
	} else {
		t.Fatal("фикстура стенда больше не содержит components — тест потерял смысл")
	}
	fakeNdmc(t, cut, 0)

	if v, err := versionFromNdmc(context.Background()); err == nil {
		t.Errorf("обрезанный вывод усыновлён: release=%q компонентов=%d", v.Release, len(v.Components))
	}
}

// Потолок разбираемого вывода: склейка продолжений на мегабайтах даёт
// квадратичную работу, а на mipsel это жор CPU прямо на старте. За потолком
// ничего не разбирается.
func TestParseNdmcVersion_StopsAtOutputCap(t *testing.T) {
	var b strings.Builder
	b.WriteString("release: 5.01.C.3.0-1\ncomponents: base\n")
	for b.Len() < maxNdmcOutput+1024 {
		b.WriteString("мусорная строка без двоеточия\n")
	}
	b.WriteString("title: за потолком\n")

	v := parseNdmcVersion(b.String())

	if v.Release != "5.01.C.3.0-1" {
		t.Errorf("префикс обязан разбираться: release=%q", v.Release)
	}
	if v.Title != "" {
		t.Errorf("за потолком ничего не разбирается, а title=%q", v.Title)
	}
}

// Бинаря нет — отказ, а не пустая версия.
func TestVersionFromNdmc_MissingBinary(t *testing.T) {
	old := ndmcBinary
	ndmcBinary = filepath.Join(t.TempDir(), "нет-такого")
	t.Cleanup(func() { ndmcBinary = old })

	if _, err := versionFromNdmc(context.Background()); err == nil {
		t.Error("отсутствие бинаря обязано быть ошибкой")
	}
}
