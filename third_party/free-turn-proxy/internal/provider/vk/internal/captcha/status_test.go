package captcha

import "testing"

func TestIsCheckboxRetryableStatus(t *testing.T) {
	t.Parallel()

	for _, st := range []string{"bot", "BOT", "error", "ERROR"} {
		if !isCheckboxRetryableStatus(st) {
			t.Fatalf("expected retryable status %q", st)
		}
	}
	for _, st := range []string{"ok", "error_limit", "fail"} {
		if isCheckboxRetryableStatus(st) {
			t.Fatalf("expected non-retryable status %q", st)
		}
	}
}
