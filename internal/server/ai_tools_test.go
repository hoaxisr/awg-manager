package server

import (
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

func TestSafeDiagnosticErrorRedactsURLAndLimitsLength(t *testing.T) {
	value := safeDiagnosticError("fetch https://user:secret@example.com/path?token=secret Authorization: Bearer abc123 api_key=xyz " + strings.Repeat("x", 700))
	if strings.Contains(value, "secret") || strings.Contains(value, "example.com") || len(value) > 503 {
		t.Fatalf("unsafe diagnostic error: %q", value)
	}
	if strings.Contains(value, "abc123") || strings.Contains(value, "xyz") {
		t.Fatalf("credentials leaked: %q", value)
	}
}

func TestAILogAllowlist(t *testing.T) {
	if bucket, err := allowedLogBucket("app"); err != nil || bucket != logging.BucketApp {
		t.Fatalf("app bucket=%q err=%v", bucket, err)
	}
	if _, err := allowedLogBucket("/var/log/messages"); err == nil {
		t.Fatal("arbitrary log source must be rejected")
	}
	if !allowedLogGroup(logging.BucketApp, "routing") || allowedLogGroup(logging.BucketApp, "singbox") {
		t.Fatal("app group allowlist is incorrect")
	}
	if !allowedLogGroup(logging.BucketSingbox, "singbox") || allowedLogGroup(logging.BucketSingbox, "system") {
		t.Fatal("singbox group allowlist is incorrect")
	}
	if allowedLogLevel("trace") || !allowedLogLevel("warn") {
		t.Fatal("log level allowlist is incorrect")
	}
}

func TestSafeInspectDestination(t *testing.T) {
	for _, value := range []string{"youtube.com", "1.1.1.1", "2001:db8::1"} {
		if !safeInspectDestination(value) {
			t.Fatalf("valid destination rejected: %q", value)
		}
	}
	for _, value := range []string{"https://youtube.com", "a b", "../secret", "host?x=1"} {
		if safeInspectDestination(value) {
			t.Fatalf("unsafe destination accepted: %q", value)
		}
	}
}

func TestSelectedAIEngineRejectsUnknownValue(t *testing.T) {
	s := &Server{}
	if engine, err := s.selectedAIEngine("sing-box"); err != nil || engine != "singbox" {
		t.Fatalf("sing-box engine=%q err=%v", engine, err)
	}
	if _, err := s.selectedAIEngine("arbitrary"); err == nil {
		t.Fatal("unknown engine must be rejected")
	}
}
