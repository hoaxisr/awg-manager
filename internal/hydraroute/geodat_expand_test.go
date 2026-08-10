package hydraroute

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func buildDomainItem(domainType int, value string) []byte {
	var domain []byte
	domain = append(domain, varintField(1, uint64(domainType))...)
	domain = append(domain, field(2, []byte(value))...)
	return field(2, domain)
}

func buildCidrItem(ip []byte, prefix uint32) []byte {
	var cidr []byte
	cidr = append(cidr, field(1, ip)...)
	if prefix > 0 {
		cidr = append(cidr, varintField(2, uint64(prefix))...)
	}
	return field(2, cidr)
}

func buildGeoEntryWithItems(ccField int, name string, items [][]byte) []byte {
	var entry []byte
	entry = append(entry, field(ccField, []byte(name))...)
	for _, item := range items {
		entry = append(entry, item...)
	}
	return entry
}

func TestExtractGeoSiteTagLines(t *testing.T) {
	entries := [][]byte{
		buildGeoEntryWithItems(1, "GOOGLE", [][]byte{
			buildDomainItem(0, "google.com"),
			buildDomainItem(2, "googlevideo.com"),
			buildDomainItem(1, `^ads\.google\.`),
		}),
		buildGeoEntryWithItems(1, "TELEGRAM", [][]byte{
			buildDomainItem(3, "t.me"),
		}),
	}
	dat := buildGeoDAT(entries)
	tmp := filepath.Join(t.TempDir(), "geosite.dat")
	if err := os.WriteFile(tmp, dat, 0o644); err != nil {
		t.Fatal(err)
	}

	lines, err := ExtractGeoSiteTagLines(tmp, "google")
	if err != nil {
		t.Fatalf("ExtractGeoSiteTagLines: %v", err)
	}
	want := []string{
		"google.com",
		".googlevideo.com",
		`domain_regex:^ads\.google\.`,
	}
	if len(lines) != len(want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("lines[%d] = %q, want %q", i, lines[i], want[i])
		}
	}
}

// TestExtractGeoSiteTagLinesTyped verifies the lossless typed line format for
// all four v2ray domain types: Plain → keyword:, Regex → domain_regex:,
// RootDomain → leading dot, Full → full:. The legacy extractor above must stay
// byte-stable (Plain/Full bare) — TestExtractGeoSiteTagLines pins that.
func TestExtractGeoSiteTagLinesTyped(t *testing.T) {
	entries := [][]byte{
		buildGeoEntryWithItems(1, "GOOGLE", [][]byte{
			buildDomainItem(0, "google.com"),
			buildDomainItem(2, "googlevideo.com"),
			buildDomainItem(1, `^ads\.google\.`),
			buildDomainItem(3, "chatgpt.com"),
		}),
	}
	dat := buildGeoDAT(entries)
	tmp := filepath.Join(t.TempDir(), "geosite.dat")
	if err := os.WriteFile(tmp, dat, 0o644); err != nil {
		t.Fatal(err)
	}

	lines, err := ExtractGeoSiteTagLinesTyped(tmp, "google")
	if err != nil {
		t.Fatalf("ExtractGeoSiteTagLinesTyped: %v", err)
	}
	want := []string{
		"keyword:google.com",
		".googlevideo.com",
		`domain_regex:^ads\.google\.`,
		"full:chatgpt.com",
	}
	if len(lines) != len(want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("lines[%d] = %q, want %q", i, lines[i], want[i])
		}
	}
}

// TestExpandGeoTagTyped_GeoIPMatchesLegacy pins that the typed expansion only
// changes geosite output; geoip CIDR lines are identical to the legacy path.
func TestExpandGeoTagTyped_GeoIPMatchesLegacy(t *testing.T) {
	entries := [][]byte{
		buildGeoEntryWithItems(1, "RU", [][]byte{
			buildCidrItem([]byte{5, 8, 0, 0}, 21),
		}),
	}
	dat := buildGeoDAT(entries)
	dir := t.TempDir()
	tmp := filepath.Join(dir, "geoip.dat")
	if err := os.WriteFile(tmp, dat, 0o644); err != nil {
		t.Fatal(err)
	}

	gds := NewGeoDataStore(dir)
	gds.entries = []GeoFileEntry{{Type: "geoip", Path: tmp}}

	legacy, _, err := gds.ExpandGeoTag("geoip", "RU")
	if err != nil {
		t.Fatalf("ExpandGeoTag: %v", err)
	}
	typed, _, err := gds.ExpandGeoTagTyped("geoip", "RU")
	if err != nil {
		t.Fatalf("ExpandGeoTagTyped: %v", err)
	}
	if len(legacy) != 1 || len(typed) != 1 || legacy[0] != typed[0] || typed[0] != "5.8.0.0/21" {
		t.Fatalf("legacy = %v, typed = %v, want identical [5.8.0.0/21]", legacy, typed)
	}
}

func TestExtractGeoIPTagLines(t *testing.T) {
	entries := [][]byte{
		buildGeoEntryWithItems(1, "RU", [][]byte{
			buildCidrItem([]byte{5, 8, 0, 0}, 21),
			buildCidrItem([]byte{1, 1, 1, 1}, 32),
		}),
	}
	dat := buildGeoDAT(entries)
	tmp := filepath.Join(t.TempDir(), "geoip.dat")
	if err := os.WriteFile(tmp, dat, 0o644); err != nil {
		t.Fatal(err)
	}

	lines, err := ExtractGeoIPTagLines(tmp, "ru")
	if err != nil {
		t.Fatalf("ExtractGeoIPTagLines: %v", err)
	}
	want := []string{"5.8.0.0/21", "1.1.1.1/32"}
	if len(lines) != len(want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("lines[%d] = %q, want %q", i, lines[i], want[i])
		}
	}
}

// TestExpandGeoTag_NotFoundSentinel pins that a missing tag is reported with
// ErrGeoTagNotFound through every expansion entry point, so consumers can tell
// it apart from a .dat parse failure (which is fatal for them).
func TestExpandGeoTag_NotFoundSentinel(t *testing.T) {
	entries := [][]byte{
		buildGeoEntryWithItems(1, "RU", [][]byte{
			buildCidrItem([]byte{5, 8, 0, 0}, 21),
		}),
	}
	dir := t.TempDir()
	tmp := filepath.Join(dir, "geoip.dat")
	if err := os.WriteFile(tmp, buildGeoDAT(entries), 0o644); err != nil {
		t.Fatal(err)
	}

	gds := NewGeoDataStore(dir)
	gds.entries = []GeoFileEntry{{Type: "geoip", Path: tmp}}

	if _, _, err := gds.ExpandGeoTag("geoip", "nosuchtag"); !errors.Is(err, ErrGeoTagNotFound) {
		t.Fatalf("ExpandGeoTag: want ErrGeoTagNotFound, got %v", err)
	}
	if _, _, err := gds.ExpandGeoTagTyped("geoip", "nosuchtag"); !errors.Is(err, ErrGeoTagNotFound) {
		t.Fatalf("ExpandGeoTagTyped: want ErrGeoTagNotFound, got %v", err)
	}
	if err := StreamGeoIPTagLines(tmp, "nosuchtag", func(string) error { return nil }); !errors.Is(err, ErrGeoTagNotFound) {
		t.Fatalf("StreamGeoIPTagLines: want ErrGeoTagNotFound, got %v", err)
	}
	if _, err := ExtractGeoIPTagLines(tmp, "nosuchtag"); !errors.Is(err, ErrGeoTagNotFound) {
		t.Fatalf("ExtractGeoIPTagLines: want ErrGeoTagNotFound, got %v", err)
	}

	// No tracked file of that kind at all — still "not found", not a parse error.
	empty := NewGeoDataStore(t.TempDir())
	if _, _, err := empty.ExpandGeoTag("geoip", "ru"); !errors.Is(err, ErrGeoTagNotFound) {
		t.Fatalf("ExpandGeoTag (no files): want ErrGeoTagNotFound, got %v", err)
	}
}

func TestExtractGeoSiteTagLines_NotFound(t *testing.T) {
	entries := [][]byte{buildGeoEntryWithItems(1, "A", nil)}
	dat := buildGeoDAT(entries)
	tmp := filepath.Join(t.TempDir(), "geosite.dat")
	if err := os.WriteFile(tmp, dat, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ExtractGeoSiteTagLines(tmp, "MISSING")
	if err == nil {
		t.Fatal("expected error for missing tag")
	}
}
