package awgmproto

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"testing"
)

func TestConfigHashNormalizesDashes(t *testing.T) {
	// Процесс берёт имена из пакета flag (без дефисов), менеджер — из своего
	// argv (с дефисами). Форма записи не должна менять отпечаток.
	a := ConfigHash([]string{"-peer", "1.2.3.4:56000", "--no-nat"})
	b := ConfigHash([]string{"peer", "1.2.3.4:56000", "no-nat"})
	if a != b {
		t.Fatalf("форма записи флага изменила отпечаток:\n%s\n%s", a, b)
	}
}

func TestConfigHashIgnoresOrder(t *testing.T) {
	a := ConfigHash([]string{"-peer", "x", "-workers", "18"})
	b := ConfigHash([]string{"-workers", "18", "-peer", "x"})
	if a != b {
		t.Fatal("порядок аргументов изменил отпечаток")
	}
}

func TestConfigHashLastWins(t *testing.T) {
	// Пакет flag оставляет последнее значение; менеджер обязан вести себя так же.
	a := ConfigHash([]string{"-peer", "первый", "-peer", "второй"})
	b := ConfigHash([]string{"-peer", "второй"})
	if a != b {
		t.Fatal("повторяющийся флаг обработан не как «побеждает последний»")
	}
}

func TestConfigHashIgnoresAwgmFlags(t *testing.T) {
	// Флаги обвязки принадлежат менеджеру, а не конфигурации: их смена не
	// должна выглядеть изменением настроек и вызывать перезапуск.
	a := ConfigHash([]string{"-peer", "x"})
	b := ConfigHash([]string{"-peer", "x",
		"--awgm-control-socket", "/tmp/a.sock", "--awgm-log-file", "/tmp/a.log"})
	if a != b {
		t.Fatal("флаги --awgm-* попали в отпечаток")
	}
}

func TestConfigHashBooleanFlagIsTrue(t *testing.T) {
	a := ConfigHash([]string{"-debug"})
	b := ConfigHash([]string{"-debug", "true"})
	if a != b {
		t.Fatal("переключатель без значения должен давать значение true")
	}
}

func TestConfigHashUnderstandsEqualsForm(t *testing.T) {
	// Пакет flag принимает обе записи, и менеджер вправе выбрать любую.
	// Разбор "--name=value" как пары ("name=value","true") дал бы отпечаток,
	// которого вторая сторона не повторит никогда.
	a := ConfigHash([]string{"--peer=1.2.3.4:56000", "--no-nat"})
	b := ConfigHash([]string{"-peer", "1.2.3.4:56000", "-no-nat"})
	if a != b {
		t.Fatalf("форма --имя=значение изменила отпечаток:\n%s\n%s", a, b)
	}
}

func TestConfigHashDashValueNeedsEqualsForm(t *testing.T) {
	// Значение, начинающееся с дефиса, неотличимо от следующего флага без
	// знания типов флагов — ни у нас, ни у пакета flag на той стороне.
	// Поэтому такое значение обязано ехать формой --имя=значение (Global
	// Constraints), и именно эта форма разбирается правильно.
	withEquals := ConfigHash([]string{"--offset=-1"})
	spaced := ConfigHash([]string{"-offset", "-1"})
	if withEquals == spaced {
		t.Fatal("пробельная форма со значением-дефисом разобралась как пара — так не бывает")
	}
}

func TestConfigHashSeparatesNameFromValue(t *testing.T) {
	// Разделитель \x00 после имени И после значения. Без него разные наборы
	// склеиваются в одну строку и дают один отпечаток — дефект тихий: не
	// вечный перезапуск (код на обеих сторонах один), а ПРОПУЩЕННОЕ изменение
	// конфигурации, то есть перезапуск не случится там, где должен.
	cases := []struct {
		name string
		a, b []string
	}{
		{
			// Без разделителя после имени обе пары дают "abc".
			name: "разделитель после имени",
			a:    []string{"-ab", "c"},
			b:    []string{"-a", "bc"},
		},
		{
			// Без разделителя после значения обе пары дают "a\x00bcd\x00e".
			name: "разделитель после значения",
			a:    []string{"-a", "b", "-cd", "e"},
			b:    []string{"-a", "bc", "-d", "e"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, other := ConfigHash(tc.a), ConfigHash(tc.b); got == other {
				t.Fatalf("разные наборы дали один отпечаток %s — пары склеены без разделителя", got)
			}
		})
	}
}

func TestBinarySHA256MatchesExecutable(t *testing.T) {
	// Страж от вырождения: без него подмена тела на возврат пустой строки
	// переживает весь набор, а поле binary_sha256 молча становится
	// «неизвестно» у всех четырёх ролей сразу.
	got, err := BinarySHA256()
	if err != nil {
		t.Fatal(err)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	want := hex.EncodeToString(h.Sum(nil))

	if got != want {
		t.Fatalf("сумма не совпала с суммой самого бинаря:\n%s\n%s", got, want)
	}
}

func TestConfigHashStableAcrossRuns(t *testing.T) {
	args := []string{"-peer", "x", "-workers", "18"}
	first := ConfigHash(args)
	for i := 0; i < 20; i++ {
		if ConfigHash(args) != first {
			t.Fatal("отпечаток нестабилен между вызовами — вероятно, обход карты")
		}
	}
}
