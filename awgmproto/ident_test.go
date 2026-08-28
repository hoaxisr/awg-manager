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

func TestConfigHashKeepsNameCase(t *testing.T) {
	// §5.5 п.2: срезаются дефисы, регистр СОХРАНЯЕТСЯ. Нормализация регистра
	// на одной стороне и не на другой — разные отпечатки навсегда.
	a := ConfigHash([]string{"-Peer", "x"})
	b := ConfigHash([]string{"-peer", "x"})
	if a == b {
		t.Fatalf("имена разного регистра схлопнулись в один отпечаток %s", a)
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

func TestConfigHashValueChangesFingerprint(t *testing.T) {
	// Значение обязано попадать в хеш. Без этого смена peer'а, пароля или порта
	// перестаёт менять config_hash — возвращается ровно тот класс «изменения не
	// применяются до выкл/вкл», против которого написана §5.5. Симптомов у
	// дефекта нет: отпечаток есть, он стабилен, он просто ничего не значит.
	a := ConfigHash([]string{"-peer", "1.1.1.1"})
	b := ConfigHash([]string{"-peer", "9.9.9.9"})
	if a == b {
		t.Fatalf("разные значения одного флага дали один отпечаток %s — значение не попадает в хеш", a)
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

func TestConfigHashIgnoresAwgmFlagsInEqualsForm(t *testing.T) {
	// Форму `--awgm-имя=значение` фильтр обязан отбрасывать так же, как
	// пробельную. Последствие пропуска асимметрично: процесс считает отпечаток
	// по argv ПОСЛЕ SplitArgs (флагов обвязки там уже нет), менеджер — по
	// полному argv, и согласие сторон держится именно на этом фильтре.
	// Пропустил одну форму — вечный цикл перезапусков.
	a := ConfigHash([]string{"-peer", "x"})
	b := ConfigHash([]string{"-peer", "x",
		"--awgm-control-socket=/tmp/a.sock", "--awgm-protocol=1"})
	if a != b {
		t.Fatal("флаги вида --awgm-имя=значение попали в отпечаток")
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

func TestConfigHashGoldenVector(t *testing.T) {
	// Эталон. Сравнительные тесты фиксируют СВОЙСТВА и переживают подмену
	// разделителя на \x01, hex в верхнем регистре, обратную сортировку и
	// перестановку «значение, имя»: под любой из этих правок все сравнения
	// остаются верными, а форма отпечатка — другая.
	//
	// Довод «код на обеих сторонах один, значит расхождение симметрично»
	// работает только ВНУТРИ одной версии модуля. awgmproto версионируется
	// тегами, стороны бампают его независимо, а гейт --awgm-protocol сверяет
	// мажор протокола, а не форму отпечатка: между версией менеджера и версией
	// бинаря форма обязана быть зафиксирована текстом, а не поведением.
	//
	// Значение посчитано ВНЕ пакета по буквальному тексту §5.5 п.6, а не
	// вызовом ConfigHash: иначе тест зафиксировал бы поведение реализации, в
	// том числе ошибочное. Каноническая строка для набора ниже —
	//   "no-nat\x00true\x00peer\x001.2.3.4:56000\x00workers\x0018\x00"
	// её SHA256 в нижнем hex и стоит эталоном.
	const want = "f7dcbfc8258cf3f8a8c69f4fde3ef7635e7289fc12e71e4cc8debf00a25212e4"

	got := ConfigHash([]string{"-peer", "1.2.3.4:56000", "--no-nat", "-workers", "18"})
	if got != want {
		t.Fatalf("отпечаток разошёлся с эталоном §5.5:\nполучено %s\nэталон   %s", got, want)
	}
}

func TestBinarySHA256MatchesExecutable(t *testing.T) {
	// Страж от вырождения: без него подмена тела на возврат пустой строки
	// переживает весь набор, а поле binary_sha256 молча становится
	// «неизвестно» у всех четырёх ролей сразу.
	//
	// Источник он НЕ сторожит, и это надо знать: os.Executable() на Linux
	// читает тот же /proc/self/exe, поэтому подмену чтения на os.Args[0] —
	// ровно ту ошибку, от которой предостерегает §5.2, — тест переживёт.
	// Разделить эти две дороги можно только запуском бинаря из переименованного
	// или подменённого файла, то есть отдельным сценарием, а не unit-тестом.
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
