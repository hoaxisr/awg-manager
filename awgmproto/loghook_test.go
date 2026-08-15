package awgmproto

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestOpenLogTruncatesPreviousRun(t *testing.T) {
	// Границу запуска задаёт процесс усечением при открытии: на этом стоят
	// предикаты здоровья, считающие содержимое файла журналом текущего запуска.
	path := filepath.Join(t.TempDir(), "a.log")
	if err := os.WriteFile(path, []byte("хвост прошлого запуска\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	lg, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("файл не усечён при открытии: %q", data)
	}
}

func TestLogTruncatesOnCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.log")
	lg, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()

	line := []byte(strings.Repeat("x", 4096) + "\n")
	for written := 0; written < LogCapBytes+8*len(line); written += len(line) {
		if _, err := lg.Write(line); err != nil {
			t.Fatal(err)
		}
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() > LogCapBytes {
		t.Fatalf("журнал %d байт при потолке %d — усечение по потолку не работает", st.Size(), LogCapBytes)
	}
	if st.Size() == 0 {
		t.Fatal("после усечения запись должна продолжаться")
	}
}

func TestLogCapIsExact(t *testing.T) {
	// Потолок — «превысил», а не «достиг»: проверка на точном значении, иначе
	// сдвиг предиката на единицу (> вместо >=) переживает набор целиком —
	// прогон с крупными строками просто усекает на строку раньше.
	path := filepath.Join(t.TempDir(), "a.log")
	lg, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()

	if _, err := lg.Write(make([]byte, LogCapBytes-1)); err != nil {
		t.Fatal(err)
	}
	if _, err := lg.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != LogCapBytes {
		t.Fatalf("ровно потолок (%d) усечён до %d — усечение сработало раньше времени", LogCapBytes, st.Size())
	}

	if _, err := lg.Write([]byte("y")); err != nil {
		t.Fatal(err)
	}
	st, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != 1 {
		t.Fatalf("байт сверх потолка дал %d байт — ожидалось усечение и запись с нуля", st.Size())
	}
}

func TestLogWritesFromZeroAfterCap(t *testing.T) {
	// Без O_APPEND смещение переживает Truncate(0): следующая запись уходит на
	// старое смещение, а голова файла становится дырой из нулевых байтов.
	// Размер при этом снова перевалит за потолок, но диагноз будет ложный —
	// «усечение не работает», — а настоящий вред в другом: читатели журнала
	// (детект капчи, разбор рукопожатий) получают мегабайт NUL перед строками.
	path := filepath.Join(t.TempDir(), "a.log")
	lg, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()

	if _, err := lg.Write(make([]byte, LogCapBytes)); err != nil {
		t.Fatal(err)
	}
	marker := []byte("строка после усечения\n")
	if _, err := lg.Write(marker); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, marker) {
		t.Fatalf("после усечения в журнале %d байт, начало %q — запись ушла не с нуля", len(data), data[:min(len(data), 32)])
	}
}

func TestLogKeepsWritesAfterCap(t *testing.T) {
	// Счётчик обязан обнуляться вместе с файлом. Оставшись на потолке, он
	// заставит усекать файл ПЕРЕД КАЖДОЙ записью: в журнале всегда ровно одна
	// последняя строка. Симптома нет — файл есть, пишется, туннель жив, — а
	// trafficStalled (десять последних выборок), детект капчи и разбор
	// рукопожатий читают однострочный файл. Проверка ОДНОЙ записи после
	// усечения (TestLogWritesFromZeroAfterCap) этого не видит.
	path := filepath.Join(t.TempDir(), "a.log")
	lg, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()

	if _, err := lg.Write(make([]byte, LogCapBytes)); err != nil {
		t.Fatal(err)
	}
	want := ""
	for _, line := range []string{"первая\n", "вторая\n", "третья\n"} {
		if _, err := lg.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
		want += line
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("после усечения журнал = %q, ожидались все строки %q", data, want)
	}
}

func TestOpenLogIsPrivate(t *testing.T) {
	// Журнал лежит в общем /tmp, и в него попадает всё, что печатает процесс.
	// Режим задан кодом; закрепляем, чтобы расширение доступа не проехало
	// молча вместе с правкой флагов открытия.
	path := filepath.Join(t.TempDir(), "a.log")
	lg, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()

	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Fatalf("журнал создан с режимом %04o, ожидалось 0600", perm)
	}
}

func TestLogWriteIsSerialized(t *testing.T) {
	// Пишущих в журнал несколько: вывод самого процесса и обвязка. Без замка
	// учёт размера теряет обновления и потолок уползает вверх.
	//
	// Гонять ТОЛЬКО под -race (он в «Воротах плана» постоянно). Без -race тест
	// декоративен: под мутантом со снятым замком его утверждение остаётся
	// истинным, и мутант проходит. Вся ценность — детерминированная
	// конкуренция для детектора, поэтому убирать -race из прогона нельзя.
	path := filepath.Join(t.TempDir(), "a.log")
	lg, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()

	line := []byte(strings.Repeat("z", 4095) + "\n")
	const writers, each = 8, 64
	done := make(chan error, writers)
	for range writers {
		go func() {
			for range each {
				if _, err := lg.Write(line); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}()
	}
	for range writers {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() > LogCapBytes {
		t.Fatalf("журнал %d байт при потолке %d — учёт размера потерял записи", st.Size(), LogCapBytes)
	}
}

func TestLogFdIsTheJournal(t *testing.T) {
	// Этот дескриптор наследуют хелперы, которых процесс порождает сам. Ошибись
	// он — их вывод уедет в /dev/null (менеджер отдаёт процессу именно его)
	// вместе с маркерами, которые читают предикаты здоровья. Симптома нет:
	// туннель работает, журнал пишется, исчезают только чужие строки.
	path := filepath.Join(t.TempDir(), "a.log")
	lg, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()

	marker := []byte("вывод хелпера\n")
	if _, err := syscall.Write(int(lg.Fd()), marker); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, marker) {
		t.Fatalf("через Fd() в журнал попало %q, ожидалось %q", data, marker)
	}
}
