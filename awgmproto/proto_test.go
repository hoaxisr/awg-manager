package awgmproto

import (
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
	// Несовпадение мажора — отказ, а не попытка разобрать как есть.
	if _, _, err := DecodeLine([]byte(`{"v":2,"id":1,"cmd":"state"}`)); err == nil {
		t.Fatal("ожидали отказ по версии протокола")
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
