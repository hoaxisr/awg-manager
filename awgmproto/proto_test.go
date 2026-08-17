package awgmproto

import (
	"errors"
	"strings"
	"testing"
)

func TestEncodeLineIsSingleLine(t *testing.T) {
	// Кадрирование — строки: перевод строки внутри сообщения разорвал бы кадр.
	line, err := EncodeLine(Response{V: Version, ID: 1, OK: false, Code: CodeInternal,
		Error: "ошибка\nс переводом строки"})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(line), "\n"); n != 1 {
		t.Fatalf("переводов строки %d, ожидали ровно один — завершающий", n)
	}
	if !strings.HasSuffix(string(line), "\n") {
		t.Fatal("строка обязана заканчиваться переводом строки")
	}
}

func TestDecodeLineDistinguishesKinds(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Kind
	}{
		{"запрос — есть cmd", `{"v":1,"id":3,"cmd":"state"}`, KindRequest},
		{"ответ — есть ok", `{"v":1,"id":3,"ok":true}`, KindResponse},
		// Отказной кадр §5.1/§5.3 — самая частая форма ответа, и опознаваться он
		// обязан по НАЛИЧИЮ поля ok, а не по его истинности. Наивная проба с
		// `bool` вместо `*bool` разбирает этот кадр как «ни то, ни другое, ни
		// третье», и менеджер теряет код ошибки.
		{"ответ об отказе — ok:false", `{"v":1,"id":3,"ok":false,"code":"busy"}`, KindResponse},
		{"push — есть event и нет id", `{"v":1,"event":"hello"}`, KindEvent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, _, err := DecodeLine([]byte(c.in))
			if err != nil {
				t.Fatal(err)
			}
			if kind != c.want {
				t.Fatalf("вид %v, ожидали %v", kind, c.want)
			}
		})
	}
}

func TestDecodeLineRejectsWrongMajor(t *testing.T) {
	// Несовпадение мажора — отказ, а не попытка разобрать как есть. Проверяются
	// обе стороны от текущей версии и её отсутствие: гейт, написанный как
	// `probe.V > Version`, отвергал бы только версию сверху, а кадр вообще без
	// поля `v` принимал бы за первую — тогда как Global Constraints требуют
	// поле `v` в КАЖДОМ сообщении, а несовпадение мажора считают отказом.
	cases := []struct {
		name string
		in   string
	}{
		{"версия выше нашей", `{"v":2,"id":1,"cmd":"state"}`},
		{"версия ниже нашей", `{"v":0,"id":1,"cmd":"state"}`},
		{"поля v нет вовсе", `{"id":1,"cmd":"state"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := DecodeLine([]byte(c.in)); err == nil {
				t.Fatal("ожидали отказ по версии протокола")
			}
		})
	}
}

// TestDecodeLineTypesWrongMajor — чужой мажор отличим от мусора ПО КЛАССУ
// ошибки, а не по тексту.
//
// Реакции на них противоположны: чужой мажор — приговор инстансу без ретраев,
// мусор в кадре временен. Пока обе стороны получали одну безымянную ошибку,
// принимающая сторона разбирала поле v сама, вторым разбором того же кадра
// рядом с первым, — и именно эта копия обязана исчезнуть.
//
// Кадры литеральные: round-trip через EncodeLine проверял бы согласие пакета с
// самим собой, а тут проверяется договор с проводом.
func TestDecodeLineTypesWrongMajor(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		major bool // ожидаем ErrProtocolMajor, а не общий отказ разбора
		got   int
	}{
		// Границы берутся вплотную к нашей версии с обеих сторон: гейт,
		// написанный как `> Version`, пропустил бы соседа снизу.
		{name: "версия выше нашей", in: `{"v":2,"event":"hello"}`, major: true, got: 2},
		{name: "версия ниже нашей", in: `{"v":0,"id":1,"cmd":"state"}`, major: true, got: 0},
		// Дальше — мусор, и он обязан остаться обычной ошибкой: кадр без v
		// НЕ «версия ноль», иначе оборванный кадр приговорит живой инстанс.
		{name: "поля v нет вовсе", in: `{"id":1,"cmd":"state"}`},
		{name: "v не число", in: `{"v":"1","id":1,"cmd":"state"}`},
		{name: "не JSON вовсе", in: `это не кадр`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := DecodeLine([]byte(c.in))
			if err == nil {
				t.Fatal("кадр принят")
			}
			if got := errors.Is(err, ErrProtocolMajor); got != c.major {
				t.Fatalf("errors.Is(ErrProtocolMajor) = %v, ожидали %v: %v", got, c.major, err)
			}
			if !c.major {
				return
			}
			var pme *ProtocolMajorError
			if !errors.As(err, &pme) {
				t.Fatalf("версия из кадра недоступна через errors.As: %v", err)
			}
			if pme.Got != c.got {
				t.Fatalf("в отказе версия %d, в кадре %d", pme.Got, c.got)
			}
		})
	}
}

func TestDecodeLineKeepsResponseFields(t *testing.T) {
	// Вида ответа мало: решение менеджер принимает по code и error (§5.1), а
	// шаг реконсиляции связывает с командой по id. Кадр здесь ЛИТЕРАЛЬНЫЙ, а не
	// собранный EncodeLine: round-trip переживает переименование тега, потому
	// что обе стороны меняются вместе, а провод — нет.
	kind, msg, err := DecodeLine([]byte(
		`{"v":1,"id":7,"ok":false,"code":"busy","error":"дескриптор уже прикреплён"}`))
	if err != nil {
		t.Fatal(err)
	}
	if kind != KindResponse {
		t.Fatalf("вид %v, ожидали %v", kind, KindResponse)
	}
	resp, ok := msg.(Response)
	if !ok {
		t.Fatalf("разобрано в %T, ожидали Response", msg)
	}
	if resp.ID != 7 {
		t.Fatalf("id %d, ожидали 7 — ответ не свяжется со своей командой", resp.ID)
	}
	if resp.OK {
		t.Fatal("отказ разобран как успех")
	}
	if resp.Code != CodeBusy {
		t.Fatalf("код %q, ожидали %q — менеджер потерял причину отказа", resp.Code, CodeBusy)
	}
	if resp.Error != "дескриптор уже прикреплён" {
		t.Fatalf("пояснение %q — в журнал уедет не то", resp.Error)
	}
}

func TestDecodeLineKeepsStateFields(t *testing.T) {
	// Тот же довод, что и в TestDecodeLineKeepsResponseFields, но для вложенного
	// снимка: он приезжает с провода, и его теги обязаны совпадать с §5.2
	// побуквенно.
	_, msg, err := DecodeLine([]byte(`{"v":1,"id":3,"ok":true,"state":{"role":"server",` +
		`"instance":"default","pid":42,"config_hash":"aa","binary_sha256":"bb",` +
		`"uptime_s":11,"last_error":"","listen":{"dtls":56000,"direct":0,"raw":0},` +
		`"clients":0}}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, ok := msg.(Response)
	if !ok {
		t.Fatalf("разобрано в %T, ожидали Response", msg)
	}
	if resp.State == nil {
		t.Fatal("снимок состояния потерян при разборе")
	}
	st := resp.State
	if st.Role != "server" || st.Instance != "default" || st.PID != 42 {
		t.Fatalf("опознание процесса разобрано неверно: %+v", st)
	}
	if st.ConfigHash != "aa" || st.BinarySHA256 != "bb" {
		t.Fatalf("отпечатки разобраны неверно: %+v", st)
	}
	if st.UptimeS != 11 {
		t.Fatalf("uptime_s %d, ожидали 11", st.UptimeS)
	}
	if st.Listen == nil || st.Listen.DTLS != 56000 || st.Listen.Direct != 0 || st.Listen.Raw != 0 {
		t.Fatalf("listen разобран неверно: %+v", st.Listen)
	}
	if st.Clients == nil {
		t.Fatal("ноль клиентов разобран как «неизвестно»")
	}
	if *st.Clients != 0 {
		t.Fatalf("clients %d, ожидали 0", *st.Clients)
	}
}

func TestEncodeLineCapIsExact(t *testing.T) {
	// Потолок обязан проверяться, и проверяться строго по «больше»: кадр ровно
	// в потолок законен и обязан уехать, кадр на байт длиннее — нет. Проверка
	// на точной границе ловит и снятый потолок, и подмену `>` на `>=`.
	probe, err := EncodeLine(Event{V: Version, Event: EventError, Message: "x"})
	if err != nil {
		t.Fatal(err)
	}
	overhead := len(probe) - 1 // всё, кроме единственного байта сообщения

	line, err := EncodeLine(Event{V: Version, Event: EventError,
		Message: strings.Repeat("a", maxLine-overhead)})
	if err != nil {
		t.Fatalf("кадр ровно в потолок отвергнут: %v", err)
	}
	if len(line) != maxLine {
		t.Fatalf("длина кадра %d, ожидали ровно %d", len(line), maxLine)
	}

	if _, err := EncodeLine(Event{V: Version, Event: EventError,
		Message: strings.Repeat("a", maxLine-overhead+1)}); err == nil {
		t.Fatal("кадр на байт длиннее потолка закодирован молча")
	}
}

func TestDecodeLineCapIsExact(t *testing.T) {
	// Вторая половина того же инварианта, что и в TestEncodeLineCapIsExact:
	// кадр ровно в потолок законен и обязан РАЗОБРАТЬСЯ, на байт длиннее —
	// отвергнуться. Без проверки на точной границе `>` подменяется на `>=`
	// незаметно, и разбор отвергал бы кадр, который наш же EncodeLine считает
	// легальным. Кадр здесь без завершающего перевода строки: ReadFrame отдаёт
	// DecodeLine именно такой.
	const head = `{"v":1,"event":"error","message":"`
	const tail = `"}`

	exact := head + strings.Repeat("a", maxLine-len(head)-len(tail)) + tail
	if len(exact) != maxLine {
		t.Fatalf("набивка посчитана неверно: кадр %d байт, ожидали %d", len(exact), maxLine)
	}
	kind, _, err := DecodeLine([]byte(exact))
	if err != nil {
		t.Fatalf("кадр ровно в потолок отвергнут при разборе: %v", err)
	}
	if kind != KindEvent {
		t.Fatalf("вид %v, ожидали %v", kind, KindEvent)
	}

	over := head + strings.Repeat("a", maxLine-len(head)-len(tail)+1) + tail
	if _, _, err := DecodeLine([]byte(over)); err == nil {
		t.Fatal("кадр на байт длиннее потолка разобран молча")
	}
}

func TestDecodeLineRejectsOversize(t *testing.T) {
	huge := `{"v":1,"event":"error","message":"` + strings.Repeat("a", 70*1024) + `"}`
	if _, _, err := DecodeLine([]byte(huge)); err == nil {
		t.Fatal("ожидали отказ по длине строки")
	}
}

func TestStateOmitsUnknownFields(t *testing.T) {
	// Отсутствие поля означает «неизвестно» и обязано отсутствовать в JSON,
	// а не приезжать нулевым значением: на этом различии стоит правило
	// «unknown не порождает шагов» в движке.
	line, err := EncodeLine(Response{V: Version, ID: 1, OK: true,
		State: &State{Role: "server", Instance: "default", PID: 42,
			ConfigHash: "aa", BinarySHA256: "bb"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"address"`, `"mtu"`, `"tun"`, `"wg"`, `"mode"`,
		`"listen"`, `"clients"`} {
		if strings.Contains(string(line), forbidden) {
			t.Fatalf("незаполненное поле %s просочилось в JSON: %s", forbidden, line)
		}
	}
}

func TestStateKeepsZeroClients(t *testing.T) {
	// Ноль клиентов — законное значение, а не «неизвестно»: поле обязательно
	// для роли wdtt-server по матрице §6, и по правилу «нет поля = unknown»
	// менеджер иначе не отличит пустой сервер от неотвечающего. Ровно поэтому
	// Clients — указатель, а не int с omitempty.
	zero := 0
	line, err := EncodeLine(Response{V: Version, ID: 1, OK: true,
		State: &State{Role: "server", Instance: "default", PID: 42,
			ConfigHash: "aa", BinarySHA256: "bb",
			Listen:  &ListenState{DTLS: 56000},
			Clients: &zero}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(line), `"clients":0`) {
		t.Fatalf("ноль клиентов стёрт при сериализации: %s", line)
	}
	// Выключенный транспорт — тоже ноль, и он тоже обязан доехать: Listen
	// присутствует целиком или отсутствует целиком.
	for _, want := range []string{`"dtls":56000`, `"direct":0`, `"raw":0`} {
		if !strings.Contains(string(line), want) {
			t.Fatalf("в listen нет %s: %s", want, line)
		}
	}
}

func TestEventTunCarriesAttachedBothWays(t *testing.T) {
	// Push tun при откреплении несёт attached:false. Если бы поле было `bool`
	// с omitempty, оно бы исчезло, и событие «tun» приезжало бы вообще без
	// признака — а пример §5.4 спеки показывает его в обоих случаях.
	yes, no := true, false
	for _, c := range []struct {
		name string
		in   *bool
		want string
	}{
		{"прикрепление", &yes, `"attached":true`},
		{"открепление", &no, `"attached":false`},
	} {
		t.Run(c.name, func(t *testing.T) {
			line, err := EncodeLine(Event{V: Version, Event: EventTun,
				Iface: "opkgtun18", Attached: c.in})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(line), c.want) {
				t.Fatalf("в событии нет %s: %s", c.want, line)
			}
		})
	}
	// А событию не про tun поле не нужно вовсе — иначе им обросли бы все push.
	line, err := EncodeLine(Event{V: Version, Event: EventHello, Impl: "wt-client"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(line), `"attached"`) {
		t.Fatalf("привязка к tun просочилась в hello: %s", line)
	}
}
