package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
)

// flakyGetter отдаёт ошибку первые failures раз, потом валидный /show/version.
type flakyGetter struct {
	failures int32
	calls    atomic.Int32
}

func (g *flakyGetter) Get(_ context.Context, path string, dst any) error {
	if g.calls.Add(1) <= g.failures {
		return errors.New("RCI недоступен")
	}
	return json.Unmarshal([]byte(`{"release":"5.01.C.3.0-1","title":"5.1.3"}`), dst)
}
func (g *flakyGetter) GetRaw(context.Context, string) ([]byte, error) { return nil, nil }
func (g *flakyGetter) Post(context.Context, any) (json.RawMessage, error) {
	return nil, nil
}

// Без версии демон обязан ЖДАТЬ, а не идти дальше на умолчании: половина
// проводки (оператор, файрвол, гейт DNS-маршрутов) замерзает снимком и не
// переигрывается никогда, поэтому неверная догадка здесь неисправима.
func TestWaitForNDMSVersion_WaitsUntilKnown(t *testing.T) {
	ndmsinfo.Reset()
	t.Cleanup(ndmsinfo.Reset)

	old := ndmsVersionRetryPeriod
	ndmsVersionRetryPeriod = 5 * time.Millisecond
	t.Cleanup(func() { ndmsVersionRetryPeriod = old })

	g := &flakyGetter{failures: 2}
	logSvc := logging.NewService(nil)
	t.Cleanup(logSvc.Stop) // иначе журнальные горутины ловит goleak
	a := &app{
		ndmsQueries: ndmsquery.NewQueries(ndmsquery.Deps{Getter: g}),
		bootLog:     logging.NewScopedLogger(logSvc, "system", "boot"),
	}

	done := make(chan struct{})
	go func() {
		a.waitForNDMSVersion(errors.New("первая попытка не удалась"))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ожидание версии не завершилось после успешной попытки")
	}

	if got := osdetect.ReleaseString(); got != "5.01.C.3.0-1" {
		t.Errorf("после ожидания релиз = %q, хотели 5.01.C.3.0-1", got)
	}
	if !osdetect.Is5() {
		t.Error("версия известна — Is5() обязан быть true по реальным данным")
	}
}

// Первый повтор идёт НЕМЕДЛЕННО. Сюда попадают после двух отказов RCI и
// одного ndmc, уложившихся в пару секунд, — такой отказ вполне может быть
// транзиентным и уже пройти, а пауза означает минуту недоступного демона
// (HTTP-морда поднимается позже этой фазы).
func TestWaitForNDMSVersion_FirstRetryIsImmediate(t *testing.T) {
	ndmsinfo.Reset()
	t.Cleanup(ndmsinfo.Reset)

	old := ndmsVersionRetryPeriod
	ndmsVersionRetryPeriod = 10 * time.Second // пауза, которую нельзя не заметить
	t.Cleanup(func() { ndmsVersionRetryPeriod = old })

	logSvc := logging.NewService(nil)
	t.Cleanup(logSvc.Stop)
	a := &app{
		ndmsQueries: ndmsquery.NewQueries(ndmsquery.Deps{Getter: &flakyGetter{failures: 0}}),
		bootLog:     logging.NewScopedLogger(logSvc, "system", "boot"),
	}

	start := time.Now()
	a.waitForNDMSVersion(errors.New("первая попытка не удалась"))
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("первый повтор ждал %v — пауза перед ним обязана быть нулевой", elapsed)
	}
}

// Ожидание не сдаётся после одной неудачной попытки: бюджет внутреннего Init
// может истечь целиком (RCI молчит дольше него), и тогда внешний цикл обязан
// зайти на второй круг, а не вернуться с неизвестной версией.
func TestWaitForNDMSVersion_DoesNotGiveUpAfterFirstAttempt(t *testing.T) {
	ndmsinfo.Reset()
	t.Cleanup(ndmsinfo.Reset)

	old := ndmsVersionRetryPeriod
	ndmsVersionRetryPeriod = 5 * time.Millisecond
	t.Cleanup(func() { ndmsVersionRetryPeriod = old })

	// Бюджет одной попытки Init — 5 с с тиком в секунду, то есть до 6
	// обращений. Семь отказов гарантированно переживают её целиком.
	g := &flakyGetter{failures: 7}
	logSvc := logging.NewService(nil)
	t.Cleanup(logSvc.Stop)
	a := &app{
		ndmsQueries: ndmsquery.NewQueries(ndmsquery.Deps{Getter: g}),
		bootLog:     logging.NewScopedLogger(logSvc, "system", "boot"),
	}

	done := make(chan struct{})
	go func() {
		a.waitForNDMSVersion(errors.New("первая попытка не удалась"))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("ожидание не завершилось")
	}

	if got := g.calls.Load(); got <= 6 {
		t.Errorf("обращений к RCI = %d: внешний цикл сдался после первой попытки", got)
	}
	if got := osdetect.ReleaseString(); got != "5.01.C.3.0-1" {
		t.Errorf("после ожидания релиз = %q", got)
	}
}

// Зависание старта обязано быть видно снаружи. Журнал приложения живёт в
// памяти и отдаётся только через HTTP, который в этой фазе ещё не поднят, а
// stderr демона, запущенного init-скриптом через busybox start-stop-daemon
// -S -b, уходит в /dev/null (проверено на стенде). Остаётся файл.
func TestReportStartupStall_WritesToFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "stderr.log")
	old := serviceStderrLog
	serviceStderrLog = path
	t.Cleanup(func() { serviceStderrLog = old })

	a := &app{}
	a.reportStartupStall("версия NDMS не определена")
	a.reportStartupStall("вторая попытка")

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("файл отчёта не создан: %v", err)
	}
	text := string(b)
	if !strings.Contains(text, "версия NDMS не определена") {
		t.Errorf("сообщение не записано: %q", text)
	}
	if !strings.Contains(text, "вторая попытка") {
		t.Error("вторая строка затёрла первую — файл обязан дописываться")
	}
}
