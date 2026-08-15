package awgmproto

import "testing"

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

func TestConfigHashStableAcrossRuns(t *testing.T) {
	args := []string{"-peer", "x", "-workers", "18"}
	first := ConfigHash(args)
	for i := 0; i < 20; i++ {
		if ConfigHash(args) != first {
			t.Fatal("отпечаток нестабилен между вызовами — вероятно, обход карты")
		}
	}
}
