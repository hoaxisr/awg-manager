package routerclock

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParsePOSIXTZ(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		in         string
		wantName   string
		wantOffset int
		wantOK     bool
	}{
		{name: "MSK-3", in: "MSK-3", wantName: "MSK", wantOffset: 180, wantOK: true},
		{name: "UTC0", in: "UTC0", wantName: "UTC", wantOffset: 0, wantOK: true},
		{name: "GMT0", in: "GMT0", wantName: "GMT", wantOffset: 0, wantOK: true},
		{name: "EST5", in: "EST5", wantName: "EST", wantOffset: -300, wantOK: true},
		{name: "EST5EDT", in: "EST5EDT,M3.2.0/2,M11.1.0/2", wantName: "EST", wantOffset: -300, wantOK: true},
		{name: "IST-5:30", in: "IST-5:30", wantName: "IST", wantOffset: 330, wantOK: true},
		{name: "invalid empty", in: "", wantOK: false},
		{name: "invalid missing offset", in: "MSK", wantOK: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotName, gotOffset, ok := parsePOSIXTZ(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("ok=%v want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if gotName != tt.wantName {
				t.Fatalf("name=%q want %q", gotName, tt.wantName)
			}
			if gotOffset != tt.wantOffset {
				t.Fatalf("offset=%d want %d", gotOffset, tt.wantOffset)
			}
		})
	}
}

func TestConvertUsesLocation(t *testing.T) {
	t.Parallel()
	src := time.Date(2026, 5, 20, 18, 57, 54, 0, time.UTC)
	loc := time.FixedZone("MSK", 3*60*60)
	got := src.In(loc)
	if got.Hour() != 21 || got.Location().String() != "MSK" {
		t.Fatalf("convert semantics mismatch: got=%s", got.Format(time.RFC3339))
	}
}

func TestGet_UsesEtcTZSourceAndRaw(t *testing.T) {
	dir := t.TempDir()
	etc := filepath.Join(dir, "TZ.etc")
	varPath := filepath.Join(dir, "TZ.var")
	if err := os.WriteFile(etc, []byte("MSK-3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(varPath, []byte("UTC0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	restore := setTZCandidatesForTest([]string{etc, varPath})
	defer restore()

	info := Get()
	if info.Source != etc {
		t.Fatalf("source=%q want %q", info.Source, etc)
	}
	if info.RawTZ != "MSK-3" {
		t.Fatalf("rawTZ=%q", info.RawTZ)
	}
	if info.ZoneName != "MSK" || info.OffsetMinutes != 180 {
		t.Fatalf("zone=%q offset=%d", info.ZoneName, info.OffsetMinutes)
	}
}

func TestGet_FallsBackToVarTZSource(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	varPath := filepath.Join(dir, "TZ.var")
	if err := os.WriteFile(varPath, []byte("UTC0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	restore := setTZCandidatesForTest([]string{missing, varPath})
	defer restore()

	info := Get()
	if info.Source != varPath {
		t.Fatalf("source=%q want %q", info.Source, varPath)
	}
	if info.RawTZ != "UTC0" || info.ZoneName != "UTC" || info.OffsetMinutes != 0 {
		t.Fatalf("unexpected info: %#v", info)
	}
}

func TestInstallAsLocal_RouterTZ(t *testing.T) {
	dir := t.TempDir()
	tzFile := dir + "/TZ"
	if err := os.WriteFile(tzFile, []byte("MSK-3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	restore := setTZCandidatesForTest([]string{tzFile})
	defer restore()

	prev := time.Local
	defer func() { time.Local = prev }()

	if !InstallAsLocal() {
		t.Fatal("InstallAsLocal returned false for a valid router TZ")
	}
	_, offset := time.Now().Zone()
	if offset != 3*3600 {
		t.Fatalf("time.Local offset = %d, want %d (MSK+3)", offset, 3*3600)
	}
}

func TestAppendTZFromRouter(t *testing.T) {
	dir := t.TempDir()
	tzFile := filepath.Join(dir, "TZ")
	if err := os.WriteFile(tzFile, []byte("MSK-3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	restore := setTZCandidatesForTest([]string{tzFile})
	defer restore()

	got := AppendTZFromRouter([]string{"PATH=/bin", "HOME=/root"})
	if len(got) != 3 || got[2] != "TZ=MSK-3" {
		t.Fatalf("AppendTZFromRouter() = %#v, want TZ=MSK-3 appended", got)
	}

	unchanged := AppendTZFromRouter([]string{"TZ=UTC0", "PATH=/bin"})
	if len(unchanged) != 2 {
		t.Fatalf("existing TZ must win: %#v", unchanged)
	}
}
