package wdttdiag

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseDiagOutput_DeduplicatesStepsAndKeepsVerdict(t *testing.T) {
	stdout := strings.Join([]string{
		"[КЛИЕНТ] шумовая строка лога",
		`DIAG|{"ev":"start","run":1,"trigger":"manual","mode":"raw"}`,
		`DIAG|{malformed json`,
		`DIAG|{"ev":"step","run":1,"step":"turn","title":"TURN VK","state":"run"}`,
		`DIAG|{"ev":"step","run":1,"step":"server","title":"Ответ сервера","state":"run"}`,
		`DIAG|{"ev":"step","run":1,"step":"turn","title":"TURN VK","state":"ok","text":"udp:193.0.0.1:3478"}`,
		`DIAG|{"ev":"step","run":1,"step":"server","title":"","state":"fail","text":"таймаут пробы"}`,
		`DIAG|{"ev":"verdict","run":1,"code":"password_unknown","sev":"error","title":"Сервер не принял пароль","text":"Пароль не найден на сервере","actions":["Перевыпустите ссылку","Обновите подписку"],"facts":{"turn":"ok","password":"unknown"}}`,
		`DIAG|{"ev":"end","run":1}`,
	}, "\n")

	mode, steps, verdict := parseDiagOutput(stdout)
	if mode != "raw" {
		t.Fatalf("mode = %q, want raw", mode)
	}
	wantSteps := []Step{
		{Step: "turn", Title: "TURN VK", State: "ok", Text: "udp:193.0.0.1:3478"},
		{Step: "server", Title: "Ответ сервера", State: "fail", Text: "таймаут пробы"},
	}
	if !reflect.DeepEqual(steps, wantSteps) {
		t.Fatalf("steps = %+v, want %+v", steps, wantSteps)
	}
	if verdict == nil {
		t.Fatal("verdict == nil")
	}
	if verdict.Code != "password_unknown" || verdict.Sev != "error" {
		t.Fatalf("verdict = %+v", verdict)
	}
	if len(verdict.Actions) != 2 || verdict.Facts["turn"] != "ok" {
		t.Fatalf("actions/facts = %+v", verdict)
	}
}

func TestParseDiagOutput_EmptyReturnsNonNilSteps(t *testing.T) {
	mode, steps, verdict := parseDiagOutput("")
	if mode != "" || verdict != nil {
		t.Fatalf("unexpected mode=%q verdict=%v", mode, verdict)
	}
	if steps == nil || len(steps) != 0 {
		t.Fatalf("steps must be non-nil empty slice, got %#v", steps)
	}
}

func TestRedactSecrets(t *testing.T) {
	in := "failed auth password=supersecret1&pass=a%2Bb%20c dk=diagTok999 short=ab"
	out := redactSecrets(in, "supersecret1", "a+b c", "diagTok999", "ab", "")
	if strings.Contains(out, "supersecret1") || strings.Contains(out, "diagTok999") || strings.Contains(out, "a%2Bb+c") {
		t.Fatalf("secret leaked in %q", out)
	}
	if !strings.Contains(out, "short=ab") {
		t.Fatalf("short secret (<3 chars) should not corrupt normal tokens: %q", out)
	}
	if strings.Count(out, "[REDACTED]") < 2 {
		t.Fatalf("expected redaction markers: %q", out)
	}
}

func TestIsUnsupportedFlagErr(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want bool
	}{
		{"diag flag", "flag provided but not defined: -diag\nUsage of wt-client:", true},
		{"diag-key flag", "flag provided but not defined: -diag-key", true},
		{"diag-ports flag", "flag provided but not defined: -diag-ports", true},
		{"turn-transport flag", "flag provided but not defined: -turn-transport", true},
		{"unrelated error", "dial udp: i/o timeout", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUnsupportedFlagErr(tc.out); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
