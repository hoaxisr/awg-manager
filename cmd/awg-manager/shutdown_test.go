package main

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
)

// H1: остановка ждёт конца записи конфигурации (Flush + событие), и внешний
// ждущий (init-скрипт, `--service stop`) обязан ждать дольше, иначе SIGKILL
// посреди записи. Граница скрипта и граница Go — одно число.
// Мутация: STOP_WAIT=5 (или shutdownFlushTimeout 60 с) → красный.
func TestShutdownBudget_FitsInitWait(t *testing.T) {
	src, err := os.ReadFile("../../entware/files/etc/init.d/S99awg-manager")
	if err != nil {
		t.Fatalf("чтение init-скрипта: %v", err)
	}
	body := string(src)
	m := regexp.MustCompile(`(?m)^STOP_WAIT=(\d+)$`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("в init-скрипте нет STOP_WAIT=<секунды>")
	}
	n, _ := strconv.Atoi(m[1])
	if time.Duration(n)*time.Second != serviceStopWait {
		t.Fatalf("STOP_WAIT=%d, а serviceStopWait=%s — границы разъехались", n, serviceStopWait)
	}
	// Запас на хуки остановки вне Flush (sing-box, nwg, сервис туннелей).
	const otherHooks = 15 * time.Second
	if need := serveShutdownTimeout + shutdownFlushTimeout + ndmscommand.SaveEventCap + otherHooks; need > serviceStopWait {
		t.Fatalf("бюджет остановки %s больше ожидания init-скрипта %s", need, serviceStopWait)
	}
	stop := body[strings.Index(body, "stop() {"):strings.Index(body, "restart() {")]
	if c := strings.Count(stop, "[ $count -lt $STOP_WAIT ]"); c != 2 {
		t.Fatalf("ожиданий STOP_WAIT в stop(): %d, ждали 2 (по pid-файлу и по pidof)", c)
	}
}

// Сохранение на остановке — на обоих путях: shutdown-хуки исполняются только
// перед syscall.Exec, SIGTERM выходит через onExit. Регистрация onExit — до
// shutdownCancel (исполнение после него, LIFO).
// Мутация: убрать deferOnExit(a.flushSaveOnShutdown) → красный.
func TestWiring_FlushOnBothShutdownPaths(t *testing.T) {
	src, err := os.ReadFile("wiring_server.go")
	if err != nil {
		t.Fatalf("чтение проводки: %v", err)
	}
	body := string(src)
	flush := strings.Index(body, "a.deferOnExit(a.flushSaveOnShutdown)")
	cancel := strings.Index(body, "a.deferOnExit(a.shutdownCancel)")
	if flush < 0 || cancel < 0 || flush > cancel {
		t.Fatalf("сохранение на SIGTERM не зарегистрировано до shutdownCancel: flush=%d cancel=%d", flush, cancel)
	}
	if !strings.Contains(body, "a.srv.AddShutdownHook(a.flushSaveOnShutdown)") {
		t.Fatal("нет сохранения перед самоперезапуском")
	}
}
