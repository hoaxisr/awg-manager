package vkauth

import (
	"testing"
)

func TestCaptchaSolveModeForAttempt(t *testing.T) {
	t.Parallel()

	t.Run("auto only flow", func(t *testing.T) {
		t.Parallel()

		for i := 0; i < captchaAutoRounds; i++ {
			mode, ok := CaptchaSolveModeForAttempt(i, false, false)
			if !ok || mode != CaptchaSolveModeAuto {
				t.Fatalf("attempt %d: expected auto, got mode=%v ok=%v", i, mode, ok)
			}
		}
		if _, ok := CaptchaSolveModeForAttempt(captchaAutoRounds, false, false); ok {
			t.Fatal("expected auto-only flow to stop after auto rounds")
		}
	})

	t.Run("auto then manual fallback", func(t *testing.T) {
		t.Parallel()

		for i := 0; i < captchaAutoRounds; i++ {
			mode, ok := CaptchaSolveModeForAttempt(i, false, true)
			if !ok || mode != CaptchaSolveModeAuto {
				t.Fatalf("attempt %d: expected auto, got mode=%v ok=%v", i, mode, ok)
			}
		}
		mode, ok := CaptchaSolveModeForAttempt(captchaAutoRounds, false, true)
		if !ok || mode != CaptchaSolveModeManual {
			t.Fatalf("expected manual fallback after auto rounds, got mode=%v ok=%v", mode, ok)
		}
		if _, ok = CaptchaSolveModeForAttempt(captchaAutoRounds+1, false, true); ok {
			t.Fatal("expected only one manual attempt in fallback flow")
		}
	})

	t.Run("manual only flow", func(t *testing.T) {
		t.Parallel()

		mode, ok := CaptchaSolveModeForAttempt(0, true, false)
		if !ok || mode != CaptchaSolveModeManual {
			t.Fatalf("expected manual mode on first attempt, got mode=%v ok=%v", mode, ok)
		}

		if _, ok = CaptchaSolveModeForAttempt(1, true, false); ok {
			t.Fatal("expected only one manual captcha attempt when manual mode is forced")
		}
	})
}
