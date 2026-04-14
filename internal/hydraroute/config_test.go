package hydraroute

import (
	"os"
	"path/filepath"
	"testing"
)

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

	// Known keys must be updated.
	if !strContains(text, "AutoStart=true") {
		t.Errorf("AutoStart not updated\nfull output:\n%s", text)
	}
	if !strContains(text, "DirectRouteEnabled=true") {
		t.Errorf("DirectRouteEnabled not updated\nfull output:\n%s", text)
	}
	if !strContains(text, "ConntrackFlush=true") {
		t.Errorf("ConntrackFlush not updated\nfull output:\n%s", text)
	}
	if !strContains(text, "Log=debug") {
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
