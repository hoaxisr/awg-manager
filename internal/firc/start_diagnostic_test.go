package firc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOperator_StartTimeoutAndDiagnosticSanitization(t *testing.T) {
	op := NewOperator(Options{ConfigDir: t.TempDir()})
	if op.startTimeout != 30*time.Second {
		t.Fatalf("expected default startTimeout=30s, got %s", op.startTimeout)
	}

	raw := "\x1b[32mStarting fircd...\x1b[0m\r\n\r\n\x1b[31mfailed to bind   socket\x1b[0m\n"
	got := sanitizeStartDiagnostic(raw)
	want := "Starting fircd...; failed to bind socket"
	if got != want {
		t.Fatalf("sanitizeStartDiagnostic() = %q, want %q", got, want)
	}

	lastPath := filepath.Join(t.TempDir(), "fircd.last")
	if err := os.WriteFile(lastPath, []byte("2026-11-21T20:11:58Z listen unix /tmp/firc/firc.sock: address already in use\n"), 0600); err != nil {
		t.Fatal(err)
	}
	op.lastOutPath = lastPath

	combined := op.fircStartDiagnostic([]byte("\x1b[33mStarting fircd...\x1b[0m\n"))
	if !strings.Contains(combined, "Starting fircd...") || !strings.Contains(combined, "; last: ") || !strings.Contains(combined, "address already in use") {
		t.Fatalf("unexpected combined fircStartDiagnostic: %q", combined)
	}

	deduped := op.fircStartDiagnostic([]byte("Starting fircd...\nfircd: the daemon's last line: 2026-11-21T20:11:58Z listen unix /tmp/firc/firc.sock: address already in use\n"))
	if strings.Contains(deduped, "; last: ") {
		t.Fatalf("expected fircStartDiagnostic not to duplicate fircd.last already in initOut, got: %q", deduped)
	}
}
