package hydraroute

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cleanIssue144Conf = `CIDR=true
clearIPSet=true
IpsetEnableTimeout=true
log=off
logfile=/opt/var/log/LOGhrneo.log
DirectRouteEnabled=true
GlobalRouting=false
ConntrackFlush=true
GeoIPFile=
GeoSiteFile=
PolicyOrder=
`

// setupTestConf writes content to a temp hrneo.conf and overrides hrConfPath/hrDir
// for the duration of the test.
func setupTestConf(t *testing.T, content string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "hrneo.conf")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write test conf: %v", err)
	}
	origConf := hrConfPath
	origDir := hrDir
	hrConfPath = path
	hrDir = dir
	t.Cleanup(func() {
		hrConfPath = origConf
		hrDir = origDir
	})
}

// setupEmptyConf points hrConfPath at a non-existent file in a temp dir.
func setupEmptyConf(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	origConf := hrConfPath
	origDir := hrDir
	hrConfPath = filepath.Join(dir, "hrneo.conf")
	hrDir = dir
	t.Cleanup(func() {
		hrConfPath = origConf
		hrDir = origDir
	})
}

func TestReadConfig_Basic(t *testing.T) {
	content := `# hrneo.conf example
AutoStart=true
ClearIPSet=false
CIDR=true
IpsetEnableTimeout=true
IpsetTimeout=300
IpsetMaxElem=131072
DirectRouteEnabled=true
GlobalRouting=false
ConntrackFlush=true
Log=info
LogFile=/opt/var/log/hrneo.log
GeoIPFile=/opt/etc/HydraRoute/geo/geoip.dat
GeoSiteFile=/opt/etc/HydraRoute/geo/geosite.dat
`
	setupTestConf(t, content)

	cfg, err := ReadConfig()
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	if !cfg.AutoStart {
		t.Error("AutoStart: want true")
	}
	if cfg.ClearIPSet {
		t.Error("ClearIPSet: want false")
	}
	if !cfg.CIDR {
		t.Error("CIDR: want true")
	}
	if !cfg.IpsetEnableTimeout {
		t.Error("IpsetEnableTimeout: want true")
	}
	if cfg.IpsetTimeout != 300 {
		t.Errorf("IpsetTimeout: want 300, got %d", cfg.IpsetTimeout)
	}
	if cfg.IpsetMaxElem != 131072 {
		t.Errorf("IpsetMaxElem: want 131072, got %d", cfg.IpsetMaxElem)
	}
	if !cfg.DirectRouteEnabled {
		t.Error("DirectRouteEnabled: want true")
	}
	if cfg.GlobalRouting {
		t.Error("GlobalRouting: want false")
	}
	if !cfg.ConntrackFlush {
		t.Error("ConntrackFlush: want true")
	}
	if cfg.Log != "info" {
		t.Errorf("Log: want info, got %q", cfg.Log)
	}
	if cfg.LogFile != "/opt/var/log/hrneo.log" {
		t.Errorf("LogFile: want /opt/var/log/hrneo.log, got %q", cfg.LogFile)
	}
	if len(cfg.GeoIPFiles) != 1 || cfg.GeoIPFiles[0] != "/opt/etc/HydraRoute/geo/geoip.dat" {
		t.Errorf("GeoIPFiles: got %v", cfg.GeoIPFiles)
	}
	if len(cfg.GeoSiteFiles) != 1 || cfg.GeoSiteFiles[0] != "/opt/etc/HydraRoute/geo/geosite.dat" {
		t.Errorf("GeoSiteFiles: got %v", cfg.GeoSiteFiles)
	}
}

func TestReadConfig_MultiGeoFiles(t *testing.T) {
	content := `AutoStart=false
GeoIPFile=/path/to/geoip1.dat
GeoIPFile=/path/to/geoip2.dat
GeoSiteFile=/path/to/geosite1.dat
GeoSiteFile=/path/to/geosite2.dat
GeoSiteFile=/path/to/geosite3.dat
`
	setupTestConf(t, content)

	cfg, err := ReadConfig()
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	if len(cfg.GeoIPFiles) != 2 {
		t.Errorf("GeoIPFiles: want 2, got %d: %v", len(cfg.GeoIPFiles), cfg.GeoIPFiles)
	}
	if len(cfg.GeoSiteFiles) != 3 {
		t.Errorf("GeoSiteFiles: want 3, got %d: %v", len(cfg.GeoSiteFiles), cfg.GeoSiteFiles)
	}
	if cfg.GeoIPFiles[0] != "/path/to/geoip1.dat" {
		t.Errorf("GeoIPFiles[0]: got %q", cfg.GeoIPFiles[0])
	}
	if cfg.GeoSiteFiles[2] != "/path/to/geosite3.dat" {
		t.Errorf("GeoSiteFiles[2]: got %q", cfg.GeoSiteFiles[2])
	}
}

func TestReadConfig_EmptyGeoFiles(t *testing.T) {
	content := `GeoIPFile=
GeoSiteFile=
`
	setupTestConf(t, content)

	cfg, err := ReadConfig()
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	if len(cfg.GeoIPFiles) != 0 {
		t.Errorf("GeoIPFiles: want empty slice, got %v", cfg.GeoIPFiles)
	}
	if len(cfg.GeoSiteFiles) != 0 {
		t.Errorf("GeoSiteFiles: want empty slice, got %v", cfg.GeoSiteFiles)
	}
}

func TestWriteConfig_PreservesUnknownKeys(t *testing.T) {
	content := `# HydraRoute Neo config
watchlistPath=/opt/etc/HydraRoute/watchlist
AutoStart=false
InterfaceFwMarkStart=100
DirectRouteEnabled=false
PolicyOrder=main,default
ConntrackFlush=false
`
	setupTestConf(t, content)

	cfg := &Config{
		AutoStart:          true,
		DirectRouteEnabled: true,
		ConntrackFlush:     true,
		Log:                "debug",
	}

	if err := WriteConfig(cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	result, err := os.ReadFile(hrConfPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	text := string(result)

	// Unknown keys must be preserved.
	for _, must := range []string{
		"watchlistPath=/opt/etc/HydraRoute/watchlist",
		"InterfaceFwMarkStart=100",
	} {
		if !strContains(text, must) {
			t.Errorf("missing preserved key: %q\nfull output:\n%s", must, text)
		}
	}

	// Known keys must be updated, preserving the case the original
	// file used (AutoStart, DirectRouteEnabled, ConntrackFlush were
	// all PascalCase on disk).
	if !strContains(text, "AutoStart=true") {
		t.Errorf("AutoStart not updated\nfull output:\n%s", text)
	}
	if !strContains(text, "DirectRouteEnabled=true") {
		t.Errorf("DirectRouteEnabled not updated\nfull output:\n%s", text)
	}
	if !strContains(text, "ConntrackFlush=true") {
		t.Errorf("ConntrackFlush not updated\nfull output:\n%s", text)
	}
	// Log was missing from the original — appended using hr-neo's
	// own lowercase convention so the daemon recognises it.
	if !strContains(text, "log=debug") {
		t.Errorf("Log not written\nfull output:\n%s", text)
	}
}

func TestWriteConfig_GeoFilesMultiValue(t *testing.T) {
	content := `AutoStart=false
GeoIPFile=/old/path1.dat
GeoIPFile=/old/path2.dat
GeoSiteFile=/old/site.dat
`
	setupTestConf(t, content)

	cfg := &Config{
		GeoIPFiles:   []string{"/new/geoip1.dat", "/new/geoip2.dat", "/new/geoip3.dat"},
		GeoSiteFiles: []string{"/new/geosite1.dat"},
	}

	if err := WriteConfig(cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	result, err := os.ReadFile(hrConfPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	text := string(result)

	// Old paths must be gone.
	if strContains(text, "/old/path1.dat") || strContains(text, "/old/path2.dat") || strContains(text, "/old/site.dat") {
		t.Errorf("old geo file paths not replaced\nfull output:\n%s", text)
	}

	// New paths must appear.
	for _, want := range []string{
		"GeoIPFile=/new/geoip1.dat",
		"GeoIPFile=/new/geoip2.dat",
		"GeoIPFile=/new/geoip3.dat",
		"GeoSiteFile=/new/geosite1.dat",
	} {
		if !strContains(text, want) {
			t.Errorf("missing %q\nfull output:\n%s", want, text)
		}
	}
}

func TestReadConfig_PolicyOrder(t *testing.T) {
	content := `AutoStart=true
PolicyOrder=AWG_YouTube,awgm0,AWG_Google
`
	setupTestConf(t, content)

	cfg, err := ReadConfig()
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	if len(cfg.PolicyOrder) != 3 {
		t.Fatalf("PolicyOrder: want 3 elements, got %d: %v", len(cfg.PolicyOrder), cfg.PolicyOrder)
	}
	want := []string{"AWG_YouTube", "awgm0", "AWG_Google"}
	for i, w := range want {
		if cfg.PolicyOrder[i] != w {
			t.Errorf("PolicyOrder[%d]: want %q, got %q", i, w, cfg.PolicyOrder[i])
		}
	}
}

func TestReadConfig_PolicyOrderEmpty(t *testing.T) {
	content := `AutoStart=true
PolicyOrder=
`
	setupTestConf(t, content)

	cfg, err := ReadConfig()
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	if len(cfg.PolicyOrder) != 0 {
		t.Errorf("PolicyOrder: want empty slice, got %v", cfg.PolicyOrder)
	}
}

func TestWriteConfig_PolicyOrder(t *testing.T) {
	content := `AutoStart=true
PolicyOrder=old_policy,old_iface
`
	setupTestConf(t, content)

	cfg := &Config{
		AutoStart:   true,
		PolicyOrder: []string{"AWG_YouTube", "awgm0", "AWG_Google"},
	}

	if err := WriteConfig(cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	result, err := os.ReadFile(hrConfPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	text := string(result)

	if !strContains(text, "PolicyOrder=AWG_YouTube,awgm0,AWG_Google") {
		t.Errorf("PolicyOrder not written correctly\nfull output:\n%s", text)
	}
	if strContains(text, "old_policy") {
		t.Errorf("old PolicyOrder value not replaced\nfull output:\n%s", text)
	}
}

func TestWriteConfig_PolicyOrderEmpty(t *testing.T) {
	content := `AutoStart=true
PolicyOrder=old_policy,old_iface
`
	setupTestConf(t, content)

	cfg := &Config{
		AutoStart:   true,
		PolicyOrder: nil,
	}

	if err := WriteConfig(cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	result, err := os.ReadFile(hrConfPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	text := string(result)

	if !strContains(text, "PolicyOrder=\n") && !strContains(text, "PolicyOrder=") {
		t.Errorf("PolicyOrder key not preserved\nfull output:\n%s", text)
	}
	if strContains(text, "old_policy") {
		t.Errorf("old PolicyOrder value not cleared\nfull output:\n%s", text)
	}
}

// strContains is strings.Contains without importing strings in test file.
func strContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// strCount returns the number of non-overlapping occurrences of sub in s.
func strCount(s, sub string) int {
	if sub == "" {
		return 0
	}
	n := 0
	for i := 0; i <= len(s)-len(sub); {
		if s[i:i+len(sub)] == sub {
			n++
			i += len(sub)
		} else {
			i++
		}
	}
	return n
}

// TestReadConfig_AcceptsDaemonCasing covers the production regression:
// hr-neo writes hrneo.conf with a mix of camelCase, lowercase, and
// PascalCase keys. The old case-sensitive switch missed the camelCase
// and lowercase variants, so UI showed default-zero values for keys
// the daemon had actively set. Reader now lowercases the key before
// dispatching, so every casing the daemon emits is accepted.
func TestReadConfig_AcceptsDaemonCasing(t *testing.T) {
	// Exact snippet from a production hrneo.conf, minus the appended
	// PascalCase duplicates the old writer added.
	content := `autoStart=true
clearIPSet=true
IpsetEnableTimeout=true
IpsetTimeout=21600
CIDR=true
log=off
logfile=/opt/var/log/LOGhrneo.log
DirectRouteEnabled=true
GlobalRouting=false
ConntrackFlush=true
`
	setupTestConf(t, content)

	cfg, err := ReadConfig()
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if !cfg.AutoStart {
		t.Errorf("AutoStart: want true (from camelCase autoStart=true)")
	}
	if !cfg.ClearIPSet {
		t.Errorf("ClearIPSet: want true (from mixed-case clearIPSet=true)")
	}
	if cfg.Log != "off" {
		t.Errorf("Log: want %q (from lowercase log=off), got %q", "off", cfg.Log)
	}
	if cfg.LogFile != "/opt/var/log/LOGhrneo.log" {
		t.Errorf("LogFile: want path (from lowercase logfile=...), got %q", cfg.LogFile)
	}
}

// TestWriteConfig_PreservesOriginalCase verifies the writer rewrites
// each managed key in the exact casing the existing line used —
// daemon-cased lines stay daemon-cased so subsequent daemon reads
// still see the value. Without this, our writer was producing PascalCase
// lines for daemon-cased keys, causing the duplicate-key disaster.
func TestWriteConfig_PreservesOriginalCase(t *testing.T) {
	content := `autoStart=true
clearIPSet=false
log=off
logfile=/some/path
ConntrackFlush=true
`
	setupTestConf(t, content)

	cfg := &Config{
		AutoStart:      false, // flip
		ClearIPSet:     true,  // flip
		Log:            "debug",
		LogFile:        "/new/path",
		ConntrackFlush: false, // flip
	}
	if err := WriteConfig(cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	raw, _ := os.ReadFile(hrConfPath)
	text := string(raw)

	// Original casing must be preserved.
	for _, want := range []string{
		"autoStart=false",
		"clearIPSet=true",
		"log=debug",
		"logfile=/new/path",
		"ConntrackFlush=false",
	} {
		if !strContains(text, want) {
			t.Errorf("missing %q\nfull output:\n%s", want, text)
		}
	}
	// And the WRONG casing must not appear as a new line.
	for _, mustNot := range []string{
		"AutoStart=", "ClearIPSet=", "Log=", "LogFile=",
	} {
		if strContains(text, mustNot) {
			t.Errorf("unexpected PascalCase duplicate %q\nfull output:\n%s", mustNot, text)
		}
	}
}

// TestWriteConfig_DedupsCaseInsensitiveDuplicates feeds the writer a
// file in the exact broken shape we saw in production — daemon-cased
// keys followed by PascalCase duplicates appended by the old writer.
// After one WriteConfig pass the file must have ONE line per managed
// key, in daemon casing, with the new value.
func TestWriteConfig_DedupsCaseInsensitiveDuplicates(t *testing.T) {
	content := `autoStart=true
clearIPSet=true
IpsetEnableTimeout=true
IpsetTimeout=21600
CIDR=true
CIDRfile=/opt/etc/HydraRoute/ip.list
log=off
logfile=/opt/var/log/LOGhrneo.log
DirectRouteEnabled=true
InterfaceFwMarkStart=12289
InterfaceTableStart=301
GlobalRouting=false
ConntrackFlush=true
GeoIPFile=/opt/etc/HydraRoute/geoip_GA.dat
GeoSiteFile=/opt/etc/HydraRoute/geosite_GA.dat
PolicyOrder=HydraRoute
AutoStart=false
ClearIPSet=false
IpsetMaxElem=0
Log=
LogFile=
`
	setupTestConf(t, content)

	cfg := &Config{
		AutoStart:          true, // user toggles ON in UI
		ClearIPSet:         true,
		CIDR:               true,
		IpsetEnableTimeout: true,
		IpsetTimeout:       21600,
		IpsetMaxElem:       0,
		DirectRouteEnabled: true,
		ConntrackFlush:     true,
		Log:                "off",
		LogFile:            "/opt/var/log/LOGhrneo.log",
		GeoIPFiles:         []string{"/opt/etc/HydraRoute/geoip_GA.dat"},
		GeoSiteFiles:       []string{"/opt/etc/HydraRoute/geosite_GA.dat"},
		PolicyOrder:        []string{"HydraRoute"},
	}
	if err := WriteConfig(cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	raw, _ := os.ReadFile(hrConfPath)
	text := string(raw)

	// Each managed key must appear exactly once.
	for key, wantCount := range map[string]int{
		// Daemon-cased lines (originals from line 1-16 of input). The
		// strings match a non-substring of another key.
		"autoStart=": 1, "clearIPSet=": 1, "log=": 1, "logfile=": 1,
		"IpsetEnableTimeout=": 1, "IpsetTimeout=": 1,
		"DirectRouteEnabled=": 1, "GlobalRouting=": 1, "ConntrackFlush=": 1,
		"GeoIPFile=": 1, "GeoSiteFile=": 1, "PolicyOrder=": 1,
		// PascalCase duplicates from the bottom of the input must be gone.
		"AutoStart=":  0,
		"ClearIPSet=": 0,
		"Log=":        0,
		"LogFile=":    0,
		// CIDR-only line (not the CIDRfile unknown key).
		"CIDR=true": 1,
	} {
		if got := strCount(text, key); got != wantCount {
			t.Errorf("key %q appears %d times, want %d\nfull output:\n%s", key, got, wantCount, text)
		}
	}

	// Unknown keys preserved.
	for _, want := range []string{
		"CIDRfile=/opt/etc/HydraRoute/ip.list",
		"InterfaceFwMarkStart=12289",
		"InterfaceTableStart=301",
	} {
		if !strContains(text, want) {
			t.Errorf("preserved-unknown %q missing\nfull output:\n%s", want, text)
		}
	}

	// New value applied: autoStart now true.
	if !strContains(text, "autoStart=true") {
		t.Errorf("autoStart not toggled to true\nfull output:\n%s", text)
	}

	// Sanity: re-read should round-trip cleanly.
	got, err := ReadConfig()
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if !got.AutoStart || !got.ClearIPSet || got.Log != "off" {
		t.Errorf("round-trip mismatch: %#v", got)
	}
}

// TestWriteConfig_AppendsNewKeysWithCanonicalCase covers the
// fresh-install path (or a file that never had a given managed key):
// when WriteConfig appends a key not previously present, it must use
// the casing hr-neo itself would have used, NOT our former hard-coded
// PascalCase. Otherwise the next daemon write would create a new
// duplicate.
func TestWriteConfig_AppendsNewKeysWithCanonicalCase(t *testing.T) {
	setupEmptyConf(t)

	cfg := &Config{
		AutoStart:      true,
		ClearIPSet:     true,
		ConntrackFlush: true,
		Log:            "info",
		LogFile:        "/var/log/hr.log",
	}
	if err := WriteConfig(cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	raw, _ := os.ReadFile(hrConfPath)
	text := string(raw)

	// camel / lower for these — daemon's convention.
	for _, want := range []string{
		"autoStart=true",
		"clearIPSet=true",
		"log=info",
		"logfile=/var/log/hr.log",
	} {
		if !strContains(text, want) {
			t.Errorf("missing %q\nfull output:\n%s", want, text)
		}
	}
	// Pascal for these.
	for _, want := range []string{
		"ConntrackFlush=true",
	} {
		if !strContains(text, want) {
			t.Errorf("missing %q\nfull output:\n%s", want, text)
		}
	}
	// No PascalCase variants of the daemon-cased keys.
	for _, mustNot := range []string{
		"AutoStart=", "ClearIPSet=", "Log=", "LogFile=",
	} {
		if strContains(text, mustNot) {
			t.Errorf("unexpected PascalCase append %q\nfull output:\n%s", mustNot, text)
		}
	}
}

func TestWriteGeoFilesOnly_DoesNotMaterializeZeroValueKeys(t *testing.T) {
	setupTestConf(t, cleanIssue144Conf)

	if err := WriteGeoFilesOnly(nil, []string{"/opt/etc/HydraRoute/geosite_GA.dat"}); err != nil {
		t.Fatalf("WriteGeoFilesOnly: %v", err)
	}
	raw, _ := os.ReadFile(hrConfPath)
	text := string(raw)

	if !strContains(text, "GeoSiteFile=/opt/etc/HydraRoute/geosite_GA.dat") {
		t.Fatalf("GeoSiteFile not updated\nfull output:\n%s", text)
	}
	for _, forbidden := range []string{
		"IpsetMaxElem=0",
		"IpsetTimeout=0",
		"autoStart=false",
	} {
		if strContains(text, forbidden) {
			t.Errorf("unexpected materialized key %q\nfull output:\n%s", forbidden, text)
		}
	}
	expected := `CIDR=true
clearIPSet=true
IpsetEnableTimeout=true
log=off
logfile=/opt/var/log/LOGhrneo.log
DirectRouteEnabled=true
GlobalRouting=false
ConntrackFlush=true
GeoIPFile=
GeoSiteFile=/opt/etc/HydraRoute/geosite_GA.dat
PolicyOrder=
`
	if text != expected {
		t.Fatalf("unexpected output after geo patch\nwant:\n%s\ngot:\n%s", expected, text)
	}
}

func TestWritePolicyOrderOnly_DoesNotMaterializeZeroValueKeys(t *testing.T) {
	setupTestConf(t, cleanIssue144Conf)

	if err := WritePolicyOrderOnly([]string{"PolicyA", "PolicyB"}); err != nil {
		t.Fatalf("WritePolicyOrderOnly: %v", err)
	}
	raw, _ := os.ReadFile(hrConfPath)
	text := string(raw)

	if !strContains(text, "PolicyOrder=PolicyA,PolicyB") {
		t.Fatalf("PolicyOrder not updated\nfull output:\n%s", text)
	}
	for _, forbidden := range []string{
		"IpsetMaxElem=0",
		"IpsetTimeout=0",
		"autoStart=false",
	} {
		if strContains(text, forbidden) {
			t.Errorf("unexpected materialized key %q\nfull output:\n%s", forbidden, text)
		}
	}
	expected := `CIDR=true
clearIPSet=true
IpsetEnableTimeout=true
log=off
logfile=/opt/var/log/LOGhrneo.log
DirectRouteEnabled=true
GlobalRouting=false
ConntrackFlush=true
GeoIPFile=
GeoSiteFile=
PolicyOrder=PolicyA,PolicyB
`
	if text != expected {
		t.Fatalf("unexpected output after policy patch\nwant:\n%s\ngot:\n%s", expected, text)
	}
}

func TestWriteConfig_DoesNotWriteIpsetMaxElemZero(t *testing.T) {
	setupTestConf(t, cleanIssue144Conf)

	cfg, err := ReadConfig()
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	cfg.IpsetMaxElem = 0
	if err := WriteConfig(cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	raw, _ := os.ReadFile(hrConfPath)
	text := string(raw)

	if strContains(text, "IpsetMaxElem=0") {
		t.Fatalf("invalid zero value was written\nfull output:\n%s", text)
	}
	if !strContains(text, fmt.Sprintf("IpsetMaxElem=%d", defaultMaxElem)) {
		t.Fatalf("expected normalized default value\nfull output:\n%s", text)
	}
}

func TestPatchSingleScalarKey_UnknownKey_NoWrite(t *testing.T) {
	setupEmptyConf(t)

	if err := patchSingleScalarKey("unknownkey", "value"); err == nil {
		t.Fatal("expected error for unknown key")
	}
	if _, err := os.Stat(hrConfPath); !os.IsNotExist(err) {
		t.Fatalf("file should not be written on validation error, stat err=%v", err)
	}
}

func TestPatchMultiValueKeys_UnknownKey(t *testing.T) {
	setupEmptyConf(t)

	err := patchMultiValueKeys(
		[]string{"geoipfile", "unknownkey"},
		map[string][]string{"geoipfile": nil, "unknownkey": {"x"}},
	)
	if err == nil {
		t.Fatal("expected error for unknown key")
	}
}

func TestPatchMultiValueKeys_UpdateKeyMissingFromOrder(t *testing.T) {
	setupEmptyConf(t)

	err := patchMultiValueKeys(
		[]string{"geoipfile"},
		map[string][]string{
			"geoipfile":   nil,
			"geositefile": nil,
		},
	)
	if err == nil {
		t.Fatal("expected error for key missing from patch order")
	}
}

func TestPatchMultiValueKeys_OrderKeyMissingFromUpdates(t *testing.T) {
	setupEmptyConf(t)

	err := patchMultiValueKeys(
		[]string{"geoipfile", "geositefile"},
		map[string][]string{
			"geoipfile": nil,
		},
	)
	if err == nil {
		t.Fatal("expected error for key missing from patch updates")
	}
}

func TestHealInvalidRuntimeConfig_ZeroToDefault(t *testing.T) {
	content := cleanIssue144Conf + "IpsetMaxElem=0\n"
	setupTestConf(t, content)

	changed, _, err := HealInvalidRuntimeConfig()
	if err != nil {
		t.Fatalf("HealInvalidRuntimeConfig: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	got, _ := os.ReadFile(hrConfPath)
	if !strContains(string(got), fmt.Sprintf("IpsetMaxElem=%d", defaultMaxElem)) {
		t.Fatalf("expected healed maxelem, got:\n%s", string(got))
	}
}

func TestHealInvalidRuntimeConfig_NegativeToDefault(t *testing.T) {
	content := cleanIssue144Conf + "IpsetMaxElem=-5\n"
	setupTestConf(t, content)

	changed, _, err := HealInvalidRuntimeConfig()
	if err != nil {
		t.Fatalf("HealInvalidRuntimeConfig: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	got, _ := os.ReadFile(hrConfPath)
	if !strContains(string(got), fmt.Sprintf("IpsetMaxElem=%d", defaultMaxElem)) {
		t.Fatalf("expected healed maxelem, got:\n%s", string(got))
	}
}

func TestHealInvalidRuntimeConfig_ValidUnchanged(t *testing.T) {
	content := cleanIssue144Conf + "IpsetMaxElem=131072\n"
	setupTestConf(t, content)

	before, _ := os.ReadFile(hrConfPath)
	changed, _, err := HealInvalidRuntimeConfig()
	if err != nil {
		t.Fatalf("HealInvalidRuntimeConfig: %v", err)
	}
	if changed {
		t.Fatal("expected changed=false")
	}
	after, _ := os.ReadFile(hrConfPath)
	if string(before) != string(after) {
		t.Fatalf("file changed unexpectedly\nbefore:\n%s\nafter:\n%s", string(before), string(after))
	}
}

func TestHealInvalidRuntimeConfig_MissingKeyUnchanged(t *testing.T) {
	setupTestConf(t, cleanIssue144Conf)
	before, _ := os.ReadFile(hrConfPath)
	changed, _, err := HealInvalidRuntimeConfig()
	if err != nil {
		t.Fatalf("HealInvalidRuntimeConfig: %v", err)
	}
	if changed {
		t.Fatal("expected changed=false")
	}
	after, _ := os.ReadFile(hrConfPath)
	if string(before) != string(after) {
		t.Fatalf("file changed unexpectedly\nbefore:\n%s\nafter:\n%s", string(before), string(after))
	}
}

func TestHealInvalidRuntimeConfig_PreservesKeyCasing(t *testing.T) {
	content := cleanIssue144Conf + "ipsetmaxelem=0\n"
	setupTestConf(t, content)
	changed, _, err := HealInvalidRuntimeConfig()
	if err != nil {
		t.Fatalf("HealInvalidRuntimeConfig: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	got, _ := os.ReadFile(hrConfPath)
	if !strContains(string(got), fmt.Sprintf("ipsetmaxelem=%d", defaultMaxElem)) {
		t.Fatalf("expected preserved casing, got:\n%s", string(got))
	}
}

func TestHealInvalidRuntimeConfig_Duplicates_KeepValidValue(t *testing.T) {
	content := cleanIssue144Conf + "IpsetMaxElem=0\nipsetmaxelem=131072\n"
	setupTestConf(t, content)
	changed, _, err := HealInvalidRuntimeConfig()
	if err != nil {
		t.Fatalf("HealInvalidRuntimeConfig: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	got := mustRead(t, hrConfPath)
	if !strContains(got, "IpsetMaxElem=131072") {
		t.Fatalf("expected healed chosen valid value, got:\n%s", got)
	}
	if countIpsetMaxElemKeys(got) != 1 {
		t.Fatalf("expected single IpsetMaxElem key, got:\n%s", got)
	}
}

func TestHealInvalidRuntimeConfig_Duplicates_ValidThenInvalid(t *testing.T) {
	content := cleanIssue144Conf + "IpsetMaxElem=131072\nipsetmaxelem=0\n"
	setupTestConf(t, content)
	changed, _, err := HealInvalidRuntimeConfig()
	if err != nil {
		t.Fatalf("HealInvalidRuntimeConfig: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	got := mustRead(t, hrConfPath)
	if !strContains(got, "IpsetMaxElem=131072") {
		t.Fatalf("expected kept valid value, got:\n%s", got)
	}
	if countIpsetMaxElemKeys(got) != 1 {
		t.Fatalf("expected single IpsetMaxElem key, got:\n%s", got)
	}
}

func TestHealInvalidRuntimeConfig_Duplicates_AllInvalid_Default(t *testing.T) {
	content := cleanIssue144Conf + "ipsetmaxelem=0\nIpsetMaxElem=bad\n"
	setupTestConf(t, content)
	changed, _, err := HealInvalidRuntimeConfig()
	if err != nil {
		t.Fatalf("HealInvalidRuntimeConfig: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	got := mustRead(t, hrConfPath)
	if !strContains(got, fmt.Sprintf("ipsetmaxelem=%d", defaultMaxElem)) {
		t.Fatalf("expected default healed value with first-key casing, got:\n%s", got)
	}
	if countIpsetMaxElemKeys(got) != 1 {
		t.Fatalf("expected single IpsetMaxElem key, got:\n%s", got)
	}
}

func TestHealInvalidRuntimeConfig_PreservesFirstKeyPosition(t *testing.T) {
	content := "CIDR=true\nIpsetMaxElem=0\nPolicyOrder=\n"
	setupTestConf(t, content)
	changed, _, err := HealInvalidRuntimeConfig()
	if err != nil {
		t.Fatalf("HealInvalidRuntimeConfig: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	got := mustRead(t, hrConfPath)
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("unexpected line count: %d\n%s", len(lines), got)
	}
	if lines[1] != fmt.Sprintf("IpsetMaxElem=%d", defaultMaxElem) {
		t.Fatalf("IpsetMaxElem position/value changed unexpectedly:\n%s", got)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func countIpsetMaxElemKeys(text string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "IpsetMaxElem") {
			n++
		}
	}
	return n
}

// stockHrneoConf — ровно то, что кладёт ipk HR Neo
// (Neo/source/ipk/rootfs/opt/etc/HydraRoute/hrneo.conf). Остальные ключи
// демон берёт из встроенных дефолтов.
const stockHrneoConf = `log=off
logfile=/opt/var/log/LOGhrneo.log
PolicyOrder=HydraRoute
DirectRouteEnabled=false
`

// Issue #767: на стоковом конфиге наши дефолты для отсутствующих ключей
// расходились с дефолтами демона, и первое же сохранение настроек HR
// дописывало в файл CIDR=false (демон перестаёт читать ip.list) и
// autoStart=false (демон завершается сразу после старта).
func TestReadWriteConfig_StockConfKeepsDaemonDefaults(t *testing.T) {
	setupTestConf(t, stockHrneoConf)

	cfg, err := ReadConfig()
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if !cfg.CIDR {
		t.Errorf("CIDR: got false, want true (дефолт демона при отсутствии ключа)")
	}
	if !cfg.AutoStart {
		t.Errorf("AutoStart: got false, want true (дефолт демона при отсутствии ключа)")
	}
	if !cfg.ClearIPSet {
		t.Errorf("ClearIPSet: got false, want true (дефолт демона при отсутствии ключа)")
	}
	if !cfg.IpsetEnableTimeout {
		t.Errorf("IpsetEnableTimeout: got false, want true (дефолт демона при отсутствии ключа)")
	}
	if cfg.IpsetTimeout != defaultIpsetTimeout {
		t.Errorf("IpsetTimeout: got %d, want %d (дефолт демона)", cfg.IpsetTimeout, defaultIpsetTimeout)
	}
	// Ключ в файле есть — читаем его, а не дефолт.
	if cfg.DirectRouteEnabled {
		t.Errorf("DirectRouteEnabled: got true, want false (значение из файла)")
	}

	if err := WriteConfig(cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	out := mustRead(t, hrConfPath)
	for _, forbidden := range []string{"CIDR=false", "autoStart=false", "clearIPSet=false", "IpsetEnableTimeout=false"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("round-trip стокового конфига записал %q:\n%s", forbidden, out)
		}
	}
	if strings.Contains(out, "IpsetTimeout=0") {
		t.Errorf("round-trip стокового конфига записал IpsetTimeout=0:\n%s", out)
	}
}

// awgmBrokenConf — как выглядел hrneo.conf после того, как старая
// версия AWGM сохранила настройки поверх стокового файла (#767).
const awgmBrokenConf = `log=off
logfile=/opt/var/log/LOGhrneo.log
PolicyOrder=HydraRoute
DirectRouteEnabled=false
autoStart=false
clearIPSet=false
CIDR=false
IpsetEnableTimeout=false
IpsetTimeout=0
IpsetMaxElem=65536
GlobalRouting=false
ConntrackFlush=true
`

func TestHealBrokenDefaults_RestoresDaemonDefaults(t *testing.T) {
	setupTestConf(t, awgmBrokenConf)

	healed, err := HealBrokenDefaults()
	if err != nil {
		t.Fatalf("HealBrokenDefaults: %v", err)
	}
	if len(healed) != 6 {
		t.Fatalf("healed keys: want 6, got %d (%v)", len(healed), healed)
	}

	got := mustRead(t, hrConfPath)
	for _, want := range []string{
		"autoStart=true", "clearIPSet=true", "CIDR=true",
		"IpsetEnableTimeout=true",
		fmt.Sprintf("IpsetTimeout=%d", defaultIpsetTimeout),
		fmt.Sprintf("IpsetMaxElem=%d", defaultMaxElem),
	} {
		if !strContains(got, want) {
			t.Errorf("missing %q after heal:\n%s", want, got)
		}
	}
	// Не наши ключи остаются как были.
	if !strContains(got, "DirectRouteEnabled=false") || !strContains(got, "PolicyOrder=HydraRoute") {
		t.Errorf("heal touched unmanaged keys:\n%s", got)
	}

	// Идемпотентность: отпечатка больше нет, второй проход — no-op.
	again, err := HealBrokenDefaults()
	if err != nil {
		t.Fatalf("second HealBrokenDefaults: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second pass healed %v, want no-op", again)
	}
}

func TestHealBrokenDefaults_NoFingerprint_NoOp(t *testing.T) {
	// CIDR=false без нашего отпечатка — осознанный выбор пользователя.
	content := "CIDR=false\nIpsetEnableTimeout=true\nIpsetTimeout=21600\n"
	setupTestConf(t, content)

	healed, err := HealBrokenDefaults()
	if err != nil {
		t.Fatalf("HealBrokenDefaults: %v", err)
	}
	if len(healed) != 0 {
		t.Fatalf("healed %v, want no-op without fingerprint", healed)
	}
	if got := mustRead(t, hrConfPath); got != content {
		t.Errorf("file changed without fingerprint:\ngot:\n%s\nwant:\n%s", got, content)
	}
}

func TestHealBrokenDefaults_KeepsUserEditedValues(t *testing.T) {
	// Отпечаток на месте, но CIDR и IpsetMaxElem пользователь уже
	// поправил сам — эти значения трогать нельзя.
	content := "CIDR=true\nautoStart=false\nIpsetEnableTimeout=false\nIpsetTimeout=0\nIpsetMaxElem=131072\n"
	setupTestConf(t, content)

	healed, err := HealBrokenDefaults()
	if err != nil {
		t.Fatalf("HealBrokenDefaults: %v", err)
	}
	got := mustRead(t, hrConfPath)
	if !strContains(got, "IpsetMaxElem=131072") {
		t.Errorf("user-set IpsetMaxElem overwritten:\n%s", got)
	}
	if !strContains(got, "autoStart=true") || !strContains(got, "CIDR=true") {
		t.Errorf("expected autoStart healed and CIDR kept:\n%s", got)
	}
	for _, k := range healed {
		if strings.EqualFold(k, "IpsetMaxElem") {
			t.Errorf("IpsetMaxElem must not be reported as healed: %v", healed)
		}
	}
}

// Проводка: сервис чинит конфиг сам, без вызова HealBrokenDefaults извне.
func TestService_HealInvalidRuntimeConfig_HealsBrokenDefaults(t *testing.T) {
	setupTestConf(t, awgmBrokenConf)

	s := NewService(nil, nil)
	s.SetStatusForTest(true)
	s.HealInvalidRuntimeConfig()
	// Ремонт планирует neo restart — гасим таймер, иначе он попытается
	// запустить демона уже после завершения теста.
	if s.restartTimer != nil {
		s.restartTimer.Stop()
	}

	got := mustRead(t, hrConfPath)
	if !strContains(got, "CIDR=true") || !strContains(got, "autoStart=true") {
		t.Errorf("service did not heal broken defaults:\n%s", got)
	}
}
