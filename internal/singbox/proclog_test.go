package singbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// tailFile: строки, дописанные ПОСЛЕ старта тейлера, доезжают до onLine;
// fromEnd=true не реиграет уже существующие строки.
func TestTailFile_FromEndSkipsExisting(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "err.log")
	if err := os.WriteFile(p, []byte("old-1\nold-2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tailFile(ctx, p, true, func(line string) {
		mu.Lock()
		got = append(got, line)
		mu.Unlock()
	})
	time.Sleep(700 * time.Millisecond) // тейлер занял позицию в конце
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0644)
	_, _ = f.WriteString("new-1\nnew-2\n")
	_ = f.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(got, ",") != "new-1,new-2" {
		t.Fatalf("tail lines = %v, want [new-1 new-2] (old lines must be skipped)", got)
	}
}

// fromEnd=false читает файл с нуля (свежий спавн).
func TestTailFile_FromStartReadsAll(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "out.log")
	if err := os.WriteFile(p, []byte("a\nb\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tailFile(ctx, p, false, func(line string) {
		mu.Lock()
		got = append(got, line)
		mu.Unlock()
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(got, ",") != "a,b" {
		t.Fatalf("tail lines = %v, want [a b]", got)
	}
}

// Незавершённая строка, доехавшая до диска двумя кусками через границу
// poll'ов ("par" без \n, затем "tial\n"), должна собраться в одну строку
// "partial" и не потеряться / не задвоиться из-за offset-инварианта.
func TestTailFile_PartialLineAcrossPolls(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "partial.log")
	if err := os.WriteFile(p, []byte("par"), 0644); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tailFile(ctx, p, false, func(line string) {
		mu.Lock()
		got = append(got, line)
		mu.Unlock()
	})
	time.Sleep(700 * time.Millisecond) // тейлер прочитал "par" без перевода строки
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("tial\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(got, ",") != "partial" {
		t.Fatalf("tail lines = %v, want [partial]", got)
	}
}

// I1: truncate (или исчезновение файла) ПОСЛЕ того как tail уже
// запозиционировался должно завершать горутину, а не сбрасывать offset на
// 0 и реиграть содержимое с нуля. Старое поведение реиграло строки НОВОГО
// поколения (которое усекло файл под нами), доставляя их в onLine дважды.
//
// Раунд 2 ревью: тест из раунда 1 был ВАКУОЗНЫМ — gen2 того же размера, что
// и gen1, давал size == offset, ветка size < offset вообще не срабатывала,
// и тест зеленел даже на старом коде с replay-с-нуля (verified ревьюером).
// Простое «сделать gen2 короче» тоже недостаточно: Seek за пределы файла в
// Go не ошибка, а последующий Read сразу отдаёт EOF — при size < offset
// ничего не доставится ДАЖЕ БЕЗ проверки `fi.Size() < offset`, потому что
// сам Seek-за-EOF уже не даёт байт. Значит "no delivery после shrink" сам
// по себе не различает наличие guard'а.
//
// Поэтому тест в два шага: (1) truncate на РАЗМЕР МЕНЬШЕ старого offset'а
// (без guard'а горутина НЕ вернулась бы, но и не доставила бы ничего — тот
// самый Seek-за-EOF псевдо-эффект); затем, дав один poll на то, чтобы
// guard сработал и горутина реально ЗАВЕРШИЛАСЬ, (2) файл ДОРАЩИВАЕТСЯ до
// размера БОЛЬШЕ старого offset'а. Без guard'а (горутина всё ещё жива)
// следующий poll увидел бы fi.Size() > offset, не удовлетворил бы ни
// size<offset, ни size==offset, и прочитал бы файл С СЕРЕДИНЫ (с байта
// offset=10) — доставив хвост нового поколения. С guard'ом горутина уже
// вернулась на шаге (1) и второй write не может её разбудить: ассерт на
// нуль новых строк реально различает оба случая.
//
// Catch-up-кейс (successor обгоняет старый offset ЗА ОДИН poll, без
// промежуточного shrink-poll'а) heuristic tailFile'а принципиально не
// видит — закрыт структурно на уровне Process, см.
// TestProcess_RapidRestartNoCrossGenerationDelivery в process_test.go
// (verified: 5/5 failed without the join fix).
func TestTailFile_TruncateEndsGeneration(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "err.log")
	if err := os.WriteFile(p, []byte("gen1-line\n"), 0644); err != nil { // 10 bytes
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tailFile(ctx, p, false, func(line string) {
		mu.Lock()
		got = append(got, line)
		mu.Unlock()
	})
	// Дать tail'у прочитать gen1-line и запозиционироваться за него (offset=10).
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	if len(got) != 1 || got[0] != "gen1-line" {
		mu.Unlock()
		t.Fatalf("precondition: got = %v, want [gen1-line]", got)
	}
	mu.Unlock()

	// Шаг 1: truncate короче старого offset'а (10 байт) — даём guard'у
	// (fi.Size() < offset) один poll на то, чтобы РЕАЛЬНО завершить
	// горутину (не просто "нечего читать из-за Seek-за-EOF").
	if err := os.WriteFile(p, []byte("g2\n"), 0644); err != nil { // 3 bytes < offset(10)
		t.Fatal(err)
	}
	time.Sleep(1200 * time.Millisecond) // > procLogTailPoll — даём guard'у сработать

	// Шаг 2: доращиваем файл ЗА старый offset (10 байт) новым содержимым.
	// Если горутина всё ещё жива (guard не сработал), следующий poll
	// прочитает файл С СЕРЕДИНЫ (offset=10) и доставит хвост нового
	// поколения — это и есть регрессия I1.
	if err := os.WriteFile(p, []byte("gen2-line-grown-past-old-offset\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Старый tail не должен доставить ни одной новой строки ни на шаге 1,
	// ни на шаге 2 — он завершился на truncate. Ждём достаточно долго,
	// чтобы поймать регрессию.
	time.Sleep(1500 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("tail lines after truncate = %v, want just [gen1-line] (tail must end on truncate, not replay the new generation)", got)
	}
}

// I2: pending-буфер капается на maxPendingLine — pathological поток без
// '\n' не растёт неограниченно, а сбрасывается в onLine одной строкой.
func TestTailFile_PendingCapFlushes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "nolinebreak.log")
	big := strings.Repeat("x", maxPendingLine+1000) // без '\n'
	if err := os.WriteFile(p, []byte(big), 0644); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tailFile(ctx, p, false, func(line string) {
		mu.Lock()
		got = append(got, line)
		mu.Unlock()
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("onLine called %d times, want exactly 1 (overflow flush)", len(got))
	}
	if len(got[0]) != len(big) {
		t.Fatalf("flushed line length = %d, want %d (content must not be lost)", len(got[0]), len(big))
	}
}

// Self-ротация: когда полностью потреблённый файл превышает procLogMaxBytes,
// tail усекает его САМ и продолжает то же поколение с offset 0 — писатель с
// O_APPEND-fd (как child sing-box) продолжает писать с нуля, и его новые
// строки доезжают до onLine тем же tail'ом (генерация НЕ завершается, в
// отличие от чужого truncate из TestTailFile_TruncateEndsGeneration).
func TestTailFile_SelfRotationKeepsGeneration(t *testing.T) {
	old := procLogMaxBytes.Swap(16)
	t.Cleanup(func() { procLogMaxBytes.Store(old) })

	dir := t.TempDir()
	p := filepath.Join(dir, "out.log")
	// 20 байт > потолка 16 — после потребления tail обязан усечь файл.
	if err := os.WriteFile(p, []byte("line-one\nline-two-\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// O_APPEND-fd писателя открыт ДО ротации — как у живого sing-box.
	w, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var mu sync.Mutex
	var got []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tailFile(ctx, p, false, func(line string) {
		mu.Lock()
		got = append(got, line)
		mu.Unlock()
	})

	// Ждём ротацию: файл усечён до нуля.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if fi, err := os.Stat(p); err == nil && fi.Size() == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("file was not self-truncated after exceeding procLogMaxBytes")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Писатель продолжает через старый O_APPEND-fd — строка должна доехать
	// тем же tail'ом (поколение живо).
	if _, err := w.WriteString("after-rotation\n"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	want := "line-one,line-two-,after-rotation"
	if strings.Join(got, ",") != want {
		t.Fatalf("tail lines = %v, want [%s]", got, want)
	}
}

// readLogTail: возвращает последние maxBytes (целыми строками не обязан).
func TestReadLogTail(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "err.log")
	if err := os.WriteFile(p, []byte(strings.Repeat("x", 100)+"TAIL"), 0644); err != nil {
		t.Fatal(err)
	}
	got := readLogTail(p, 8)
	if got != "xxxxTAIL" {
		t.Fatalf("readLogTail = %q, want %q", got, "xxxxTAIL")
	}
	if readLogTail(filepath.Join(dir, "absent.log"), 8) != "" {
		t.Fatal("absent file must give empty tail")
	}
}

// #562: openProcLog c truncate обязан сохранять O_APPEND — иначе у
// писателя (child sing-box) позиционный fd, и после self-ротации каждая
// его запись создаёт sparse-дыру из NUL-байтов с начала файла.
func TestOpenProcLog_TruncateKeepsAppend(t *testing.T) {
	dir := t.TempDir()
	f, err := openProcLog(filepath.Join(dir, "err.log"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	flags, err := unix.FcntlInt(f.Fd(), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if flags&unix.O_APPEND == 0 {
		t.Fatal("truncate-open must keep O_APPEND: positional fd + tail self-rotation = sparse NUL hole (#562)")
	}
}

// #562: адоптированный sing-box старой сборки держит ПОЗИЦИОННЫЙ fd (без
// O_APPEND). После self-ротации его следующая запись уходит на старое
// смещение — файл становится sparse, и tail с offset 0 вычитывает дыру из
// NUL-байтов. Эти байты не должны доезжать до onLine (до фикса они
// доставлялись 64КБ-«строками» и раздували ОЗУ через slog-экранирование).
func TestTailFile_PositionalWriterSparseHoleFiltered(t *testing.T) {
	old := procLogMaxBytes.Swap(16)
	t.Cleanup(func() { procLogMaxBytes.Store(old) })

	dir := t.TempDir()
	p := filepath.Join(dir, "err.log")
	// Позиционный fd, как у писателя, заспавненного до фикса.
	w, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.WriteString("line-one\nline-two-\n"); err != nil { // 19 байт > потолка 16
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tailFile(ctx, p, false, func(line string) {
		mu.Lock()
		got = append(got, line)
		mu.Unlock()
	})

	// Ждём self-ротацию (файл усечён до нуля).
	deadline := time.Now().Add(5 * time.Second)
	for {
		if fi, err := os.Stat(p); err == nil && fi.Size() == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("file was not self-truncated after exceeding procLogMaxBytes")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Позиционный писатель продолжает с offset 19 → sparse-дыра из 19 NUL.
	if _, err := w.WriteString("after-hole\n"); err != nil {
		t.Fatal(err)
	}

	deadline = time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	for i, l := range got {
		if strings.ContainsRune(l, '\x00') {
			t.Fatalf("line %d contains NUL bytes (len=%d): sparse hole leaked into onLine", i, len(l))
		}
	}
	want := "line-one,line-two-,after-hole"
	if strings.Join(got, ",") != want {
		t.Fatalf("tail lines = %v, want [%s]", got, want)
	}
}
