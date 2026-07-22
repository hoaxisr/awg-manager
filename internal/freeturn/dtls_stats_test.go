package freeturn

import "testing"

func TestCountActiveDTLSConnections(t *testing.T) {
	log := `
2026/07/22 18:16:07 [INFO] [STREAM 1] Established DTLS connection
2026/07/22 18:16:07 [INFO] [STREAM 6] Established DTLS connection
2026/07/22 18:16:08 [INFO] [STREAM 3] Established DTLS connection
2026/07/22 18:16:09 [INFO] [STREAM 1] DTLS connection closed
`
	if got := countActiveDTLSConnections(log); got != 2 {
		t.Fatalf("count = %d, want 2 (streams 6 and 3)", got)
	}
	if countActiveDTLSConnections("") != 0 {
		t.Fatal("empty log")
	}
}
