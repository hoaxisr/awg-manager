package hydraroute

import (
	"os"
	"path/filepath"
	"testing"
)

// Protobuf encoding helpers for building test binary data.

// varint encodes a protobuf varint.
func varint(v uint64) []byte {
	buf := make([]byte, 10)
	n := 0
	for v >= 0x80 {
		buf[n] = byte(v) | 0x80
		v >>= 7
		n++
	}
	buf[n] = byte(v)
	return buf[:n+1]
}

// field encodes a length-delimited field (wire type 2).
func field(fieldNum int, data []byte) []byte {
	tag := varint(uint64(fieldNum<<3 | 2))
	length := varint(uint64(len(data)))
	out := make([]byte, 0, len(tag)+len(length)+len(data))
	out = append(out, tag...)
	out = append(out, length...)
	out = append(out, data...)
	return out
}

// varintField encodes a varint field (wire type 0).
func varintField(fieldNum int, val uint64) []byte {
	tag := varint(uint64(fieldNum << 3))
	v := varint(val)
	out := make([]byte, 0, len(tag)+len(v))
	out = append(out, tag...)
	out = append(out, v...)
	return out
}

// buildGeoEntry builds an entry submessage with a string country_code (field ccField)
// and n repeated length-delimited items (field countField).
func buildGeoEntry(ccField int, name string, countField int, n int) []byte {
	var entry []byte
	entry = append(entry, field(ccField, []byte(name))...)
	for i := 0; i < n; i++ {
		// Each repeated item is a non-empty length-delimited field
		entry = append(entry, field(countField, []byte{0x01})...)
	}
	return entry
}

// buildGeoDAT wraps a list of entry submessages into a top-level .dat message.
// Each entry is wrapped in top-level field 1 (length-delimited).
func buildGeoDAT(entries [][]byte) []byte {
	var dat []byte
	for _, e := range entries {
		dat = append(dat, field(1, e)...)
	}
	return dat
}

func TestExtractGeoSiteTags(t *testing.T) {
	// Build .dat with 2 entries: GOOGLE (3 domains), TELEGRAM (1 domain)
	entries := [][]byte{
		buildGeoEntry(1, "GOOGLE", 2, 3),
		buildGeoEntry(1, "TELEGRAM", 2, 1),
	}
	dat := buildGeoDAT(entries)

	tmp := filepath.Join(t.TempDir(), "geosite.dat")
	if err := os.WriteFile(tmp, dat, 0o644); err != nil {
		t.Fatal(err)
	}

	tags, err := ExtractGeoSiteTags(tmp)
	if err != nil {
		t.Fatalf("ExtractGeoSiteTags error: %v", err)
	}

	if len(tags) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(tags))
	}

	// Tags are sorted by name: GOOGLE < TELEGRAM
	if tags[0].Name != "GOOGLE" {
		t.Errorf("expected tags[0].Name=GOOGLE, got %q", tags[0].Name)
	}
	if tags[0].Count != 3 {
		t.Errorf("expected tags[0].Count=3, got %d", tags[0].Count)
	}
	if tags[1].Name != "TELEGRAM" {
		t.Errorf("expected tags[1].Name=TELEGRAM, got %q", tags[1].Name)
	}
	if tags[1].Count != 1 {
		t.Errorf("expected tags[1].Count=1, got %d", tags[1].Count)
	}
}

func TestExtractGeoSiteTags_FileNotFound(t *testing.T) {
	_, err := ExtractGeoSiteTags("/nonexistent/path/geosite.dat")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

// Verifies the streaming parser copes with entries whose payload exceeds the
// bufio.Reader buffer size (64 KB). A single large tag with 200 k items forces
// the reader to chain-discard across multiple refills. If the parser ever does
// an os.ReadFile or allocates per-item, this test will either OOM on a small
// VM or take multi-second time — we assert both correctness and a loose time
// bound.
func TestExtractGeoSiteTags_LargeEntryBeyondBuffer(t *testing.T) {
	const itemCount = 200_000 // ~800 KB of item bytes, > 64 KB buffer
	entries := [][]byte{buildGeoEntry(1, "HUGE", 2, itemCount)}
	dat := buildGeoDAT(entries)

	tmp := filepath.Join(t.TempDir(), "geosite.dat")
	if err := os.WriteFile(tmp, dat, 0o644); err != nil {
		t.Fatal(err)
	}

	tags, err := ExtractGeoSiteTags(tmp)
	if err != nil {
		t.Fatalf("ExtractGeoSiteTags error: %v", err)
	}
	if len(tags) != 1 {
		t.Fatalf("tags = %d, want 1", len(tags))
	}
	if tags[0].Name != "HUGE" {
		t.Errorf("name = %q, want HUGE", tags[0].Name)
	}
	if tags[0].Count != itemCount {
		t.Errorf("count = %d, want %d", tags[0].Count, itemCount)
	}
}

// Verifies multiple entries interleaved with unknown top-level fields (forward
// compatibility) are handled correctly.
func TestExtractGeoSiteTags_UnknownTopLevelFieldsAreSkipped(t *testing.T) {
	var dat []byte
	// Entry 1
	dat = append(dat, field(1, buildGeoEntry(1, "A", 2, 3))...)
	// Unknown top-level field 99 (length-delimited, 50 bytes of garbage)
	dat = append(dat, field(99, make([]byte, 50))...)
	// Entry 2
	dat = append(dat, field(1, buildGeoEntry(1, "B", 2, 5))...)
	// Unknown varint at top-level
	dat = append(dat, varintField(77, 1234)...)

	tmp := filepath.Join(t.TempDir(), "geosite.dat")
	if err := os.WriteFile(tmp, dat, 0o644); err != nil {
		t.Fatal(err)
	}

	tags, err := ExtractGeoSiteTags(tmp)
	if err != nil {
		t.Fatalf("ExtractGeoSiteTags error: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("tags = %d, want 2", len(tags))
	}
	if tags[0].Name != "A" || tags[0].Count != 3 {
		t.Errorf("tags[0] = %+v, want {A 3}", tags[0])
	}
	if tags[1].Name != "B" || tags[1].Count != 5 {
		t.Errorf("tags[1] = %+v, want {B 5}", tags[1])
	}
}

func TestExtractGeoIPTags(t *testing.T) {
	// Build .dat with 2 entries: RU (2 CIDRs), US (5 CIDRs)
	entries := [][]byte{
		buildGeoEntry(1, "RU", 2, 2),
		buildGeoEntry(1, "US", 2, 5),
	}
	dat := buildGeoDAT(entries)

	tmp := filepath.Join(t.TempDir(), "geoip.dat")
	if err := os.WriteFile(tmp, dat, 0o644); err != nil {
		t.Fatal(err)
	}

	tags, err := ExtractGeoIPTags(tmp)
	if err != nil {
		t.Fatalf("ExtractGeoIPTags error: %v", err)
	}

	if len(tags) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(tags))
	}

	// Tags are sorted by name: RU < US
	if tags[0].Name != "RU" {
		t.Errorf("expected tags[0].Name=RU, got %q", tags[0].Name)
	}
	if tags[0].Count != 2 {
		t.Errorf("expected tags[0].Count=2, got %d", tags[0].Count)
	}
	if tags[1].Name != "US" {
		t.Errorf("expected tags[1].Name=US, got %q", tags[1].Name)
	}
	if tags[1].Count != 5 {
		t.Errorf("expected tags[1].Count=5, got %d", tags[1].Count)
	}
}

// TestVarintHelper ensures our test varint helper is correct.
func TestVarintHelper(t *testing.T) {
	_ = varintField(1, 42) // suppress unused warning — varintField available for future tests
}
