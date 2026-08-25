package captcha

import "testing"

func TestAnalyzeLog_multiStreamContention(t *testing.T) {
	log := `
2026/07/22 10:43:48 [INFO] [STREAM 1] [Captcha] Triggering manual captcha fallback
2026/07/22 10:43:50 [INFO] [Captcha] received success token from browser (1236 bytes)
2026/07/22 10:43:50 [INFO] [STREAM 1] [Captcha] Got token from browser
2026/07/22 10:43:51 [INFO] [STREAM 1] Established DTLS connection
2026/07/22 10:43:53 [INFO] [STREAM 10] [Captcha] Triggering manual captcha fallback
2026/07/22 10:43:53 [INFO] [STREAM 3] [Captcha] Triggering manual captcha fallback
2026/07/22 10:43:53 [WARN] [STREAM 3] [Captcha] Manual solver error: captcha listeners failed: 127.0.0.1:8765 (listen tcp 127.0.0.1:8765: bind: address already in use)
2026/07/22 10:43:53 [WARN] [STREAM 3] [Captcha] manual captcha failed (attempt 2): captcha listeners failed: 127.0.0.1:8765 (listen tcp 127.0.0.1:8765: bind: address already in use)
2026/07/22 10:43:53 [WARN] [STREAM 3] [VK Auth] Failed with client_id=6287487: provider backoff active
CAPTCHA_WAIT_REQUIRED
2026/07/22 10:43:53 [INFO] [STREAM 9] [Captcha] Triggering manual captcha fallback
2026/07/22 10:43:53 [WARN] [STREAM 9] [Captcha] manual captcha failed (attempt 2): captcha listeners failed: 127.0.0.1:8765 (listen tcp 127.0.0.1:8765: bind: address already in use)
2026/07/22 10:44:00 [INFO] [Captcha] received success token from browser (1240 bytes)
2026/07/22 10:44:00 [INFO] [STREAM 10] [Captcha] Got token from browser
2026/07/22 10:44:01 [INFO] [STREAM 40] Established DTLS connection
`
	summary := analyzeLog(log)
	if !summary.PortContention {
		t.Fatal("expected port contention")
	}
	if summary.PendingStreams != 2 {
		t.Fatalf("pending = %d, want 2 (streams 3 and 9)", summary.PendingStreams)
	}
	if !logIndicatesWaiting(log) {
		t.Fatal("log should still indicate captcha waiting")
	}
}
