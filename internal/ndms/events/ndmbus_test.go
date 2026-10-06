package events

import (
	"bytes"
	"encoding/binary"
	"io"
	"runtime"
	"strings"
	"testing"
)

// Кадры шины в форме libndm (К48): байт ctrl<<6|type, у NODE/ATTR/SIBL —
// две строки u32 BE + байты, у END данных нет.
func busNode(b *bytes.Buffer, ctrl, typ byte, name, value string) {
	b.WriteByte(ctrl<<6 | typ)
	for _, s := range []string{name, value} {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(s)))
		b.Write(l[:])
		b.WriteString(s)
	}
}

func busEnd(b *bytes.Buffer) { b.WriteByte(busCtrlEnd << 6) }

// busSavedDoc — документ ConfigurationSaved дословно как в ev-save.bin
// (стенд 06.10, save-signal-research.md §6): один END.
func busSavedDoc(b *bytes.Buffer, raise string) {
	busNode(b, busCtrlNode, 0, "", "")
	busNode(b, busCtrlNode, 1, "event", "")
	busNode(b, busCtrlAttr, 0, "class", BusClassConfigurationSaved)
	busNode(b, busCtrlAttr, 0, "raise_time", raise)
	busEnd(b)
}

// busNeighbourDoc — документ с детьми, как Neighbour в ev-save.bin: ребёнок,
// SIBL-соседи, END последнего ребёнка и END корня.
func busNeighbourDoc(b *bytes.Buffer) {
	busNode(b, busCtrlNode, 0, "", "")
	busNode(b, busCtrlNode, 1, "event", "")
	busNode(b, busCtrlAttr, 0, "class", "Event::Type::Neighbour")
	busNode(b, busCtrlAttr, 0, "raise_time", "536455.934069")
	busNode(b, busCtrlNode, 1, "action", "update")
	busNode(b, busCtrlSibl, 1, "update", "unknown")
	busNode(b, busCtrlSibl, 1, "entry-id", "26")
	busEnd(b)
	busEnd(b)
}

// Первый документ ev-save.bin и следующий байт 00 — ровно одно событие.
// Мутация: документ кончается вторым END → событие не разобрано, красный.
func TestNDMBus_ParseConfigurationSaved(t *testing.T) {
	raw := []byte("\x00\x00\x00\x00\x00\x00\x00\x00\x00" +
		"\x01\x00\x00\x00\x05event\x00\x00\x00\x00" +
		"\x40\x00\x00\x00\x05class\x00\x00\x00\x1fEvent::Type::ConfigurationSaved" +
		"\x40\x00\x00\x00\x0araise_time\x00\x00\x00\x0d536449.859563" +
		"\xc0" + "\x00")
	p := newBusParser(bytes.NewReader(raw))
	ev, err := p.next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if ev != (BusEvent{Class: BusClassConfigurationSaved, RaiseTime: 536449.859563}) {
		t.Fatalf("событие %+v", ev)
	}
	if ev, err := p.next(); err == nil {
		t.Fatalf("второе событие из одного документа: %+v", ev)
	}
}

// Документ с детьми — одно событие, следующий документ разобран.
// Мутация: первый END — конец документа → второй документ не разобран, красный.
func TestNDMBus_ParseNeighbourWithChildren(t *testing.T) {
	var b bytes.Buffer
	busNeighbourDoc(&b)
	busSavedDoc(&b, "536460.5")
	p := newBusParser(&b)
	ev, err := p.next()
	if err != nil || ev.Class != "Event::Type::Neighbour" || ev.RaiseTime != 536455.934069 {
		t.Fatalf("первый: %+v, %v", ev, err)
	}
	ev, err = p.next()
	if err != nil || ev != (BusEvent{Class: BusClassConfigurationSaved, RaiseTime: 536460.5}) {
		t.Fatalf("второй: %+v, %v", ev, err)
	}
}

// Узел неизвестного type несёт те же две строки — пропущен без рассинхрона.
// Мутация: читать строки только у известных типов → рассинхрон, красный.
func TestNDMBus_UnknownTypeKeepsSync(t *testing.T) {
	var b bytes.Buffer
	busNode(&b, busCtrlNode, 0, "", "")
	busNode(&b, busCtrlNode, 1, "event", "")
	busNode(&b, busCtrlAttr, 0, "class", "Event::Type::AuditLog")
	busNode(&b, busCtrlNode, 0x3f, "weird", "v")
	busEnd(&b)
	busEnd(&b)
	busSavedDoc(&b, "7.25")
	p := newBusParser(&b)
	if ev, err := p.next(); err != nil || ev.Class != "Event::Type::AuditLog" {
		t.Fatalf("первый: %+v, %v", ev, err)
	}
	if ev, err := p.next(); err != nil || ev != (BusEvent{Class: BusClassConfigurationSaved, RaiseTime: 7.25}) {
		t.Fatalf("второй: %+v, %v", ev, err)
	}
}

// Длина строки сверх maxBusString — ошибка протокола без выделения по чужой
// длине. Мутация: снять границу → make на 2 ГиБ и «unexpected EOF», красный.
func TestNDMBus_StringBound(t *testing.T) {
	raw := []byte{0x00, 0x80, 0x00, 0x00, 0x00}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := newBusParser(bytes.NewReader(raw)).next()
	runtime.ReadMemStats(&after)
	if err == nil || err == io.EOF || !strings.Contains(err.Error(), "больше") {
		t.Fatalf("ошибка %v, ждали отказ по границе длины", err)
	}
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 1<<20 {
		t.Fatalf("выделено %d байт под чужую длину", grown)
	}
}
