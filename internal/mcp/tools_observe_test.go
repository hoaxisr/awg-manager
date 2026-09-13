package mcp_test

import (
	"strings"
	"testing"
)

// TestTools_ListConnections — «что сейчас делает телевизор и через какой
// туннель» отвечается только по таблице соединений; до этого агенту было
// нечем.
func TestTools_ListConnections(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "list_connections", nil)
	if res.IsError {
		t.Fatal(toolText(res))
	}
	conns := out["connections"].([]any)
	if len(conns) != 2 {
		t.Fatalf("connections = %v", conns)
	}
	first := conns[0].(map[string]any)
	if first["protocol"] != "tcp" || first["dst"] != "142.250.1.1" {
		t.Fatalf("connection = %v", first)
	}
	if first["tunnelId"] != "tn-1" || first["clientName"] != "laptop" {
		t.Fatalf("a connection must say whose it is and where it goes: %v", first)
	}
	// The total is separate from the page: an agent must not read "2 shown"
	// as "2 open".
	if out["total"] != float64(7) {
		t.Fatalf("total = %v, want the count before paging", out["total"])
	}

	_, out = callTool(t, s, "list_connections", map[string]any{"tunnelId": "tn-2"})
	if n := len(out["connections"].([]any)); n != 1 {
		t.Fatalf("filtered by tunnel = %d", n)
	}
	if res, _ := callTool(t, s, "list_connections", map[string]any{"tunnelId": "../etc"}); !res.IsError {
		t.Error("a malformed tunnelId must be rejected before Deps")
	}
	// clientIp used to be forwarded as free text; it is an address.
	if res, _ := callTool(t, s, "list_connections", map[string]any{"clientIp": "laptop"}); !res.IsError {
		t.Error("a non-IP clientIp must be rejected before Deps")
	}
	res, out = callTool(t, s, "list_connections", map[string]any{"clientIp": " 192.168.1.20"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if n := len(out["connections"].([]any)); n != 1 {
		t.Fatalf("filtered by device = %d, want the canonical spelling to match", n)
	}
}

// TestTools_GetDiagnosticsWhileRunning — пока прогон идёт, ответ обязан
// быть «идёт», а не «отчёта нет, запустите»: иначе агент запускает снова
// и получает «уже идёт».
func TestTools_GetDiagnosticsWhileRunning(t *testing.T) {
	s, fake := newTestSession(t)
	fake.DiagnosticsRunning = true

	res, out := callTool(t, s, "get_diagnostics", nil)
	if res.IsError {
		t.Fatalf("a running sweep is not an error: %s", toolText(res))
	}
	if out["status"] != "running" {
		t.Fatalf("status = %v", out["status"])
	}
	if txt := strings.ToLower(toolText(res)); !strings.Contains(txt, "running") || strings.Contains(txt, "run_diagnostics") {
		t.Fatalf("the text must say the sweep is still running and not send the model back to run_diagnostics: %q", txt)
	}
}

// TestTools_GetPingcheckLogs — матрица мониторинга показывает только
// «сейчас». На вопрос «когда туннель начал падать» отвечает журнал
// проверок.
func TestTools_GetPingcheckLogs(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "get_pingcheck_logs", nil)
	if res.IsError {
		t.Fatal(toolText(res))
	}
	entries := out["entries"].([]any)
	if len(entries) != 3 {
		t.Fatalf("entries = %v", entries)
	}
	first := entries[0].(map[string]any)
	if first["tunnelId"] != "tn-2" || first["success"] != false {
		t.Fatalf("entry = %v", first)
	}
	if first["timestamp"] == "" {
		t.Fatal("without a timestamp the log answers nothing about when")
	}

	_, out = callTool(t, s, "get_pingcheck_logs", map[string]any{"tunnelId": "tn-1"})
	entries = out["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("filtered = %v", entries)
	}
	if res, _ := callTool(t, s, "get_pingcheck_logs", map[string]any{"tunnelId": "../etc"}); !res.IsError {
		t.Error("a malformed tunnelId must be rejected before Deps")
	}
}

// TestTools_RunDiagnosticsIsAsynchronous — полный прогон занимает
// десятки секунд. Инструмент запускает его и говорит об этом, а не
// держит вызов открытым до конца.
func TestTools_RunDiagnostics(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "run_diagnostics", nil)
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["status"] != "running" {
		t.Fatalf("status = %v", out["status"])
	}
	if out["started"] != true {
		t.Fatalf("started = %v", out["started"])
	}
}

// TestTools_GetDiagnosticsReportsFailuresFirst — отчёт целиком в контекст
// модели не влезает, а нужен из него в первую очередь список
// непройденных проверок.
func TestTools_GetDiagnostics(t *testing.T) {
	s, _ := newTestSession(t)

	// Nothing has run yet: that is a clear answer, not an empty report.
	res, _ := callTool(t, s, "get_diagnostics", nil)
	if !res.IsError {
		t.Fatal("with no completed run the tool must say so")
	}
	if txt := toolText(res); !strings.Contains(strings.ToLower(txt), "run_diagnostics") {
		t.Errorf("the error must point at the tool that starts a run: %q", txt)
	}

	if res, _ := callTool(t, s, "run_diagnostics", nil); res.IsError {
		t.Fatal("setup")
	}
	res, out := callTool(t, s, "get_diagnostics", nil)
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["status"] != "done" {
		t.Fatalf("status = %v", out["status"])
	}
	// Counts come from the whole report; the listed checks are the
	// failures only, so the two must not be confused.
	if out["passed"] != float64(2) || out["failed"] != float64(1) || out["warnings"] != float64(1) {
		t.Fatalf("counts = %v", out)
	}
	problems := out["problems"].([]any)
	if len(problems) != 2 {
		t.Fatalf("problems = %v, want the failure and the warning", problems)
	}
	first := problems[0].(map[string]any)
	if first["status"] != "fail" {
		t.Fatalf("failures must come before warnings: %v", problems)
	}
	if first["name"] == "" || first["detail"] == "" {
		t.Fatalf("a problem without a name or detail is not actionable: %v", first)
	}
}
