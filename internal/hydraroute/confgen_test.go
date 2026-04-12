package hydraroute

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- GenerateDomainConf ---

func TestGenerateDomainConf_Basic(t *testing.T) {
	lists := []ManagedEntry{
		{
			ListID:   "abc123",
			ListName: "Telegram",
			Domains:  []string{"t.me", "telegram.org"},
			Iface:    "Wireguard0",
		},
	}
	got := GenerateDomainConf(lists)

	mustContain(t, got, markerStart)
	mustContain(t, got, markerEnd)
	mustContain(t, got, "## list:abc123:Telegram")
	mustContain(t, got, "t.me,telegram.org/Wireguard0")

	assertMarkerOrder(t, got)
}

func TestGenerateDomainConf_GeoSiteTags(t *testing.T) {
	lists := []ManagedEntry{
		{
			ListID:   "g1",
			ListName: "Google",
			Domains:  []string{"google.com", "geosite:GOOGLE"},
			Iface:    "Wireguard1",
		},
	}
	got := GenerateDomainConf(lists)

	mustContain(t, got, "## list:g1:Google")
	mustContain(t, got, "google.com,geosite:GOOGLE/Wireguard1")
}

func TestGenerateDomainConf_Empty(t *testing.T) {
	got := GenerateDomainConf(nil)

	mustContain(t, got, markerStart)
	mustContain(t, got, markerEnd)

	// No extra content between markers
	inner := extractInner(got)
	if strings.TrimSpace(inner) != "" {
		t.Errorf("expected empty inner section, got: %q", inner)
	}
}

// --- GenerateIPList ---

func TestGenerateIPList_Basic(t *testing.T) {
	lists := []ManagedEntry{
		{
			ListID:   "abc123",
			ListName: "Telegram",
			Subnets:  []string{"91.108.4.0/22", "149.154.160.0/20"},
			Iface:    "Wireguard0",
		},
	}
	got := GenerateIPList(lists)

	mustContain(t, got, markerStart)
	mustContain(t, got, markerEnd)
	mustContain(t, got, "##Telegram")
	mustContain(t, got, "/Wireguard0")
	mustContain(t, got, "91.108.4.0/22")
	mustContain(t, got, "149.154.160.0/20")

	assertMarkerOrder(t, got)
}

func TestGenerateIPList_GeoIPTag(t *testing.T) {
	lists := []ManagedEntry{
		{
			ListID:   "ru1",
			ListName: "Russia",
			Subnets:  []string{"5.8.0.0/21", "geoip:RU"},
			Iface:    "Wireguard2",
		},
	}
	got := GenerateIPList(lists)

	mustContain(t, got, "##Russia")
	mustContain(t, got, "/Wireguard2")
	mustContain(t, got, "5.8.0.0/21")
	mustContain(t, got, "geoip:RU")
}

// --- WriteManagedSection ---

func TestWriteManagedSection_NewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "domain.conf")

	content := markerStart + "\nhello\n" + markerEnd + "\n"
	if err := WriteManagedSection(path, content); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := readFile(t, path)
	if got != content {
		t.Errorf("file content mismatch\ngot:  %q\nwant: %q", got, content)
	}
}

func TestWriteManagedSection_ReplaceExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "domain.conf")

	// Write a file that already has markers plus user content before/after
	initial := "# User content before\n" +
		markerStart + "\n" +
		"## list:old:Old\n" +
		"old.example.com/eth0\n" +
		markerEnd + "\n" +
		"# User content after\n"
	writeFile(t, path, initial)

	newSection := markerStart + "\n## list:new:New\nnew.example.com/Wireguard0\n" + markerEnd + "\n"
	if err := WriteManagedSection(path, newSection); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := readFile(t, path)

	mustContain(t, got, "# User content before")
	mustContain(t, got, "# User content after")
	mustContain(t, got, "## list:new:New")
	mustContain(t, got, "new.example.com/Wireguard0")

	if strings.Contains(got, "old.example.com") {
		t.Error("old section content should have been replaced")
	}
	if strings.Contains(got, "## list:old:Old") {
		t.Error("old section comment should have been replaced")
	}
}

func TestWriteManagedSection_AppendIfNoMarkers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "domain.conf")

	existing := "# Existing user content\nexample.com/eth0\n"
	writeFile(t, path, existing)

	newSection := markerStart + "\nhello\n" + markerEnd + "\n"
	if err := WriteManagedSection(path, newSection); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := readFile(t, path)

	mustContain(t, got, "# Existing user content")
	mustContain(t, got, "example.com/eth0")
	mustContain(t, got, markerStart)
	mustContain(t, got, "hello")
	mustContain(t, got, markerEnd)

	// Existing content must appear before the managed section
	existingIdx := strings.Index(got, "# Existing user content")
	markerIdx := strings.Index(got, markerStart)
	if existingIdx > markerIdx {
		t.Error("existing content should appear before the appended managed section")
	}
}

func TestWriteManagedSection_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deep", "nested", "domain.conf")

	content := markerStart + "\nhello\n" + markerEnd + "\n"
	if err := WriteManagedSection(path, content); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := readFile(t, path)
	if got != content {
		t.Errorf("file content mismatch\ngot:  %q\nwant: %q", got, content)
	}
}

// --- helpers ---

func mustContain(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected output to contain %q\nfull output:\n%s", substr, s)
	}
}

func assertMarkerOrder(t *testing.T, s string) {
	t.Helper()
	startIdx := strings.Index(s, markerStart)
	endIdx := strings.Index(s, markerEnd)
	if startIdx < 0 || endIdx < 0 {
		t.Error("missing markers")
		return
	}
	if startIdx >= endIdx {
		t.Errorf("markerStart (%d) must appear before markerEnd (%d)", startIdx, endIdx)
	}
}

// extractInner returns the text strictly between markerStart and markerEnd lines.
func extractInner(s string) string {
	lines := strings.Split(s, "\n")
	var inside bool
	var inner []string
	for _, l := range lines {
		if strings.TrimSpace(l) == markerStart {
			inside = true
			continue
		}
		if strings.TrimSpace(l) == markerEnd {
			break
		}
		if inside {
			inner = append(inner, l)
		}
	}
	return strings.Join(inner, "\n")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readFile(%q): %v", path, err)
	}
	return string(data)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile(%q): %v", path, err)
	}
}
