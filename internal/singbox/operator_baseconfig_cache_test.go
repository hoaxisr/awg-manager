package singbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func cachePathOf(t *testing.T, base map[string]any) string {
	t.Helper()
	exp, ok := base["experimental"].(map[string]any)
	if !ok {
		t.Fatalf("experimental block missing: %#v", base["experimental"])
	}
	cf, ok := exp["cache_file"].(map[string]any)
	if !ok {
		t.Fatalf("cache_file block missing: %#v", exp["cache_file"])
	}
	p, _ := cf["path"].(string)
	return p
}

func TestResolveCacheDBPath(t *testing.T) {
	cases := []struct {
		name     string
		location string
		want     string
	}{
		{"empty string defaults to flash", "", DefaultCacheDBPath()},
		{"flash explicitly selects flash", "flash", DefaultCacheDBPath()},
		{"tmp selects RAM tmpfs path", "tmp", TempCacheDBPath},
		{"unknown defaults to flash", "other", DefaultCacheDBPath()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveCacheDBPath(tc.location)
			if got != tc.want {
				t.Errorf("ResolveCacheDBPath(%q) = %q, want %q", tc.location, got, tc.want)
			}
		})
	}
}

func TestFreshBaseConfig_CachePath(t *testing.T) {
	baseDefault := freshBaseConfig("info", "", 0)
	if got := cachePathOf(t, baseDefault); got != DefaultCacheDBPath() {
		t.Errorf("freshBaseConfig() default cache_file.path = %q, want %q", got, DefaultCacheDBPath())
	}

	baseTmp := freshBaseConfig("info", "", 0, TempCacheDBPath)
	if got := cachePathOf(t, baseTmp); got != TempCacheDBPath {
		t.Errorf("freshBaseConfig() tmp cache_file.path = %q, want %q", got, TempCacheDBPath)
	}
}

func TestPatchBaseCacheFileTo(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "00-base.json")

	raw := map[string]any{
		"experimental": map[string]any{
			"cache_file": map[string]any{
				"enabled": true,
				"path":    DefaultCacheDBPath(),
			},
		},
	}
	bytes, _ := json.Marshal(raw)
	if err := os.WriteFile(basePath, bytes, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	patchBaseCacheFileTo(basePath, TempCacheDBPath)
	data, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	var updated map[string]any
	if err := json.Unmarshal(data, &updated); err != nil {
		t.Fatalf("unmarshal file: %v", err)
	}
	if got := cachePathOf(t, updated); got != TempCacheDBPath {
		t.Errorf("after patch to tmp: path = %q, want %q", got, TempCacheDBPath)
	}

	patchBaseCacheFileTo(basePath, DefaultCacheDBPath())
	data, _ = os.ReadFile(basePath)
	_ = json.Unmarshal(data, &updated)
	if got := cachePathOf(t, updated); got != DefaultCacheDBPath() {
		t.Errorf("after patch to flash: path = %q, want %q", got, DefaultCacheDBPath())
	}
}

func TestOperator_ApplyCacheFileLocation(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	basePath := baseWithServers(t, configDir, []any{bootstrapEntry("1.1.1.1")})

	op := newOrchedOperatorWithDeps(t, OperatorDeps{Dir: dir})

	// Apply "tmp"
	if err := op.ApplyCacheFileLocation("tmp"); err != nil {
		t.Fatalf("ApplyCacheFileLocation(tmp): %v", err)
	}

	parsed := readBaseFixture(t, basePath)
	if got := cachePathOf(t, parsed); got != TempCacheDBPath {
		t.Errorf("ApplyCacheFileLocation(tmp) = %q, want %q", got, TempCacheDBPath)
	}

	// Apply "flash"
	if err := op.ApplyCacheFileLocation("flash"); err != nil {
		t.Fatalf("ApplyCacheFileLocation(flash): %v", err)
	}

	parsed = readBaseFixture(t, basePath)
	if got := cachePathOf(t, parsed); got != DefaultCacheDBPath() {
		t.Errorf("ApplyCacheFileLocation(flash) = %q, want %q", got, DefaultCacheDBPath())
	}
}
