package ndmsinfo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// standComponentsXML — дословное начало файла со стенда (5.01.C.3.0-1,
// KN-1810), включая хвост с <version> у компонентов: парсер обязан брать
// атрибут корневого тега, а не первый попавшийся тег версии.
const standComponentsXML = `<?xml version="1.0" encoding="utf-8"?>
<components platform="Keenetic Ltd." model="KN-1810" sandbox="stable" version="5.01.C.3.0-1" rtm="no" date="Tue, 18 Aug 2026 15:41:01 +0000" device="0x10188000">
<title>5.1.3</title>
<group>
	<name>Applications</name>
	<description>Utilities and services</description>
</group>
<component>
	<name>acl</name>
	<version>1.2.3</version>
	<size>12345</size>
</component>
<component>
	<name>wireguard</name>
	<version>4.5.6</version>
</component>
<component>
	<name>pingcheck</name>
	<version>7.8.9</version>
</component>
</components>
`

func writeComponentsXML(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "components.xml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	old := componentsXMLPath
	componentsXMLPath = path
	t.Cleanup(func() { componentsXMLPath = old })
}

// Третий канал: файл отвечает и тогда, когда ndm не поднялся вовсе.
func TestVersionFromComponentsXML_Stand(t *testing.T) {
	writeComponentsXML(t, standComponentsXML)

	v, err := versionFromComponentsXML()
	if err != nil {
		t.Fatalf("versionFromComponentsXML: %v", err)
	}
	if v.Release != "5.01.C.3.0-1" {
		t.Errorf("release = %q, хотели 5.01.C.3.0-1 (версия компонента не должна побеждать)", v.Release)
	}
	// model= из файла кладётся в HardwareID: именно его читает kmod, выбирая
	// SoC и .ko (RCI-шный Model выглядит иначе — "Ultra (KN-1810)").
	if v.HardwareID != "KN-1810" {
		t.Errorf("hardwareID = %q", v.HardwareID)
	}
	if v.LastFetched.IsZero() {
		t.Error("LastFetched обязан проставляться")
	}
	if v.Title != "5.1.3" {
		t.Errorf("title = %q", v.Title)
	}
	// Состав компонентов в файле совпадает с ответом RCI (сверено поэлементно
	// на стенде), поэтому HasComponent() работает и без ndm. Имена групп из
	// <group> при этом компонентами НЕ считаются.
	want := []string{"acl", "wireguard", "pingcheck"}
	if len(v.Components) != len(want) {
		t.Fatalf("компонентов %d (%v), ожидали %d — в список могли попасть имена групп",
			len(v.Components), v.Components, len(want))
	}
	for i, name := range want {
		if v.Components[i] != name {
			t.Errorf("компонент %d = %q, ожидали %q", i, v.Components[i], name)
		}
	}
}

// Мусор не усыновляется: по релизу замораживается выбор оператора, и ошибка
// здесь неисправима без перезапуска.
func TestVersionFromComponentsXML_Rejects(t *testing.T) {
	// ВАЖНО: кейсы должны быть синтаксически ВАЛИДНЫМ xml, иначе их отвергает
	// парсер, а наши стражи остаются непроверенными — ровно так и было, пока
	// мутация «снять looksLikeRelease» не выжила.
	cases := map[string]string{
		"чужой корневой тег": `<?xml version="1.0"?><other version="5.01.C.3.0-1" model="KN-1810"><component><name>base</name></component></other>`,
		"релиз не похож":     `<components model="KN-1810" version="совсем не версия"><component><name>base</name></component></components>`,
		"релиза нет вовсе":   `<components model="KN-1810" sandbox="stable"><component><name>base</name></component></components>`,
		"нет компонентов":    `<components model="KN-1810" version="5.01.C.3.0-1"></components>`,
		"компоненты вложены иначе": `<components model="KN-1810" version="5.01.C.3.0-1">` +
			`<group><component><name>base</name></component></group></components>`,
		"тег не закрыт": `<components version="5.01.C.3.0-1"`,
		"пустой файл":   ``,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			writeComponentsXML(t, body)
			if v, err := versionFromComponentsXML(); err == nil {
				t.Errorf("принято как версия: %q", v.Release)
			}
		})
	}
}

// Файла нет — отказ, а не пустая версия.
func TestVersionFromComponentsXML_Missing(t *testing.T) {
	old := componentsXMLPath
	componentsXMLPath = filepath.Join(t.TempDir(), "нет-такого.xml")
	t.Cleanup(func() { componentsXMLPath = old })

	if _, err := versionFromComponentsXML(); err == nil {
		t.Fatal("отсутствующий файл обязан давать ошибку")
	}
}

// Потолок чтения закреплён: без него битый файл с глубокой вложенностью
// уводил разбор в память роутера (замер: мегабайт такого файла давал ~22 МБ
// кучи, потому что xml.Decoder.Skip рекурсивен).
//
// Размер фикстуры считается от потолка — иначе тест перестанет проверять
// границу, если потолок поднимут. Но ПЕРЕД этим стоит предохранитель: мутация
// константы в большое значение иначе превращает тест в пожирателя памяти
// (проверено на себе — уронило рабочую машину). Тест обязан краснеть, а не
// валить систему.
func TestVersionFromComponentsXML_StopsAtSizeCap(t *testing.T) {
	if maxComponentsXML > 4<<20 {
		t.Fatalf("потолок %d неправдоподобен — тест не станет строить файл такого размера", maxComponentsXML)
	}
	fixtureSize := maxComponentsXML + 4<<10

	var b strings.Builder
	b.Grow(fixtureSize + 128)
	b.WriteString(`<components model="KN-1810" version="5.01.C.3.0-1">`)
	filler := `<component><name>` + strings.Repeat("x", 1000) + `</name></component>`
	for b.Len() < fixtureSize {
		b.WriteString(filler)
	}
	b.WriteString(`</components>`)
	writeComponentsXML(t, b.String())

	// Потолок обрезает файл на середине — обрезанный XML не разбирается,
	// и версия не усыновляется.
	if v, err := versionFromComponentsXML(); err == nil {
		t.Errorf("файл за потолком разобран целиком: компонентов %d", len(v.Components))
	}
}
