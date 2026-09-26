package storage

import "testing"

// TestSettingsStore_ObfuscatorKmodTrip покрывает сторож ядро/процесс (§4.9):
// по умолчанию — ядро, TripObfuscatorKmod переключает на процесс и
// запоминает причину, ClearObfuscatorKmodTripped снимает причину (флаг
// process при этом не трогает — переключатель ручной).
func TestSettingsStore_ObfuscatorKmodTrip(t *testing.T) {
	s := NewSettingsStore(t.TempDir())
	if _, err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if s.IsObfuscatorRelayProcess() {
		t.Fatal("по умолчанию — ядро")
	}
	if err := s.TripObfuscatorKmod("oops в awgm_relay"); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Load()
	if !s.IsObfuscatorRelayProcess() || st.ObfuscatorKmodTripped != "oops в awgm_relay" {
		t.Fatalf("после срабатывания: process=%v tripped=%q", s.IsObfuscatorRelayProcess(), st.ObfuscatorKmodTripped)
	}
	if err := s.ClearObfuscatorKmodTripped(); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Load()
	if st.ObfuscatorKmodTripped != "" {
		t.Fatal("причина не снята")
	}
}

// TestSettingsStore_ObfuscatorKmodOopsHash покрывает хранение hash
// последней обработанной записи /proc/mtdoops/oops — дедуп повторов между
// перезапусками демона.
func TestSettingsStore_ObfuscatorKmodOopsHash(t *testing.T) {
	s := NewSettingsStore(t.TempDir())
	if _, err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if s.ObfuscatorKmodOopsHash() != "" {
		t.Fatal("по умолчанию — пусто")
	}
	if err := s.SetObfuscatorKmodOopsHash("abc123"); err != nil {
		t.Fatal(err)
	}
	if got := s.ObfuscatorKmodOopsHash(); got != "abc123" {
		t.Fatalf("ObfuscatorKmodOopsHash() = %q, want abc123", got)
	}
}
