package ndmsinfo

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

// Третий канал к версии — файл, а не служба. `/etc/components.xml` описывает
// набор компонентов прошивки, и его корневой тег несёт версию с моделью:
//
//	<components platform="Keenetic Ltd." model="KN-1810" sandbox="stable"
//	            version="5.01.C.3.0-1" rtm="no" date="..." device="0x10188000">
//
// Формат строки версии тот же, что у RCI `release` (проверено на стенде 5.01).
// Канал не зависит ни от HTTP на :79, ни от unix-сокета ndm, поэтому отвечает
// даже когда ndm не поднялся вовсе.
//
// Состав компонентов в файле СОВПАДАЕТ с тем, что отдаёт RCI: на стенде 5.01
// оба множества — одни и те же 39 имён, включая wireguard, pingcheck и proxy
// (сверено поэлементно). Поэтому HasComponent() работает и по файлу.
//
// ЧЕГО В ФАЙЛЕ НЕТ: человекочитаемой модели («Ultra (KN-1810)» у RCI против
// «KN-1810» здесь), региона и аптайма — они идут только в показ карточки
// роутера (internal/sys/routerinfo). Ради них версия из файла помечается
// ЧАСТИЧНОЙ, и демон добирает полную у ndm фоном; на решения старта это уже
// не влияет.
//
// Всё, что решается на старте, здесь есть: поколение ОС (выбор оператора,
// режим файрвола, гейт OpkgTun), гейты ASC/H-ranges — они считаются по релизу,
// а не по компонентам, — hw_id для выбора .ko и сам состав компонентов.
// Поэтому старт больше не ждёт ndm ради версии (см. Init).
var componentsXMLPath = "/etc/components.xml"

// maxComponentsXML — потолок читаемого файла. На стенде он 31 КБ, так что
// 256 КБ — восьмикратный запас. Мегабайт, стоявший тут раньше, запасом не был:
// замер показал, что битый файл такого размера с глубокой вложенностью даёт
// ~22 МБ кучи (xml.Decoder.Skip рекурсивен), а это и есть «десятки мегабайт»,
// от которых потолок должен защищать.
const maxComponentsXML = 256 << 10

// componentsXMLWire — то, что нам нужно из файла. Компоненты лежат списком на
// верхнем уровне; <group> рядом описывает только названия разделов каталога.
type componentsXMLWire struct {
	// XMLName заставляет разбор отвергнуть чужой корневой тег: без него
	// Decode принимал любой XML, у которого в корне есть version=.
	XMLName    xml.Name `xml:"components"`
	Version    string   `xml:"version,attr"`
	Model      string   `xml:"model,attr"`
	Title      string   `xml:"title"`
	Components []string `xml:"component>name"`
}

// versionFromComponentsXML разбирает файл в версию.
func versionFromComponentsXML() (ndms.Version, error) {
	f, err := os.Open(componentsXMLPath)
	if err != nil {
		return ndms.Version{}, fmt.Errorf("components.xml: %w", err)
	}
	defer f.Close()

	var wire componentsXMLWire
	if err := xml.NewDecoder(io.LimitReader(f, maxComponentsXML)).Decode(&wire); err != nil {
		return ndms.Version{}, fmt.Errorf("components.xml: разбор: %w", err)
	}
	if !looksLikeRelease(wire.Version) {
		return ndms.Version{}, fmt.Errorf("components.xml: непригодный релиз %q", wire.Version)
	}

	components := make([]string, 0, len(wire.Components))
	for _, name := range wire.Components {
		if name = strings.TrimSpace(name); name != "" {
			components = append(components, name)
		}
	}
	// Пустой список не усыновляем — ровно по той же причине, что и у канала
	// ndmc (см. ndmc.go): усыновлённая пустота означает
	// HasWireguardComponent() == false до конца жизни процесса, то есть
	// «nativewg недоступен» там, где он есть. А пустым список окажется на
	// первой же прошивке, где компоненты вложены иначе: тег `component>name`
	// ловит только прямых детей корня. Лучше отказ: тогда сработает
	// waitForNDMSVersion, и демон дождётся правды у ndm.
	if len(components) == 0 {
		return ndms.Version{}, fmt.Errorf("components.xml: пустой список компонентов — не та раскладка файла")
	}

	// model= из файла — это ровно RCI-шный hw_id (сверено на стенде: RCI
	// отдаёт hw_id "KN-1810" и model "Ultra (KN-1810)", в файле — "KN-1810").
	// Кладём в HardwareID: по нему kmod выбирает SoC и .ko.
	return ndms.Version{
		Release:     wire.Version,
		HardwareID:  wire.Model,
		Title:       wire.Title,
		Components:  components,
		LastFetched: time.Now(),
	}, nil
}
