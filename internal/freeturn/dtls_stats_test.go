package freeturn

import "testing"

func TestCountActiveDTLSConnections(t *testing.T) {
	log := `
2026/07/22 18:16:07 [INFO] [STREAM 1] Established DTLS connection
2026/07/22 18:16:07 [INFO] [STREAM 6] Established DTLS connection
2026/07/22 18:16:08 [INFO] [STREAM 3] Established DTLS connection
2026/07/22 18:16:09 [INFO] [STREAM 1] Closed DTLS connection
`
	if got := countActiveDTLSConnections(log); got != 2 {
		t.Fatalf("count = %d, want 2 (streams 6 and 3)", got)
	}
	if countActiveDTLSConnections("") != 0 {
		t.Fatal("empty log")
	}
}

func TestCountActiveDTLSConnectionsMassClose(t *testing.T) {
	// Сценарий после рестарта FT-сервера: все потоки закрылись, reconnect ещё нет.
	log := `2026/08/10 13:47:00 [INFO] [STREAM 1] Established DTLS connection
2026/08/10 13:47:01 [INFO] [STREAM 2] Established DTLS connection
2026/08/10 13:49:32 [INFO] [STREAM 1] Closed DTLS connection
2026/08/10 13:49:32 [INFO] [STREAM 2] Closed DTLS connection
`
	if got := countActiveDTLSConnections(log); got != 0 {
		t.Fatalf("count = %d, want 0 after mass close", got)
	}
}
