package singbox

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func cachePathOf(t *testing.T, base map[string]any) string {
	t.Helper()
	p := currentCacheFilePath(base)
	if p == "" {
		t.Fatalf("cache_file block missing: %#v", base["experimental"])
	}
	return p
}

// baseWithCachePath — база с cache_file.path; has=false — блока нет.
func baseWithCachePath(has bool, path string) map[string]any {
	base := map[string]any{"experimental": map[string]any{}}
	if has {
		base["experimental"].(map[string]any)["cache_file"] = map[string]any{"enabled": true, "path": path}
	}
	return base
}

// ageFile отодвигает mtime на час назад, чтобы assertUntouched отличил
// «файл не переписан» от «переписан теми же байтами в ту же секунду».
func ageFile(t *testing.T, path string) time.Time {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	return old
}

func assertUntouched(t *testing.T, path string, aged time.Time, what string) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !st.ModTime().Equal(aged) {
		t.Errorf("%s: файл переписан без правки (mtime %v)", what, st.ModTime())
	}
}

// redirectCacheDBPaths уводит оба наших пути cache.db в t.TempDir(), чтобы
// снос устаревшего кэша не касался хоста.
func redirectCacheDBPaths(t *testing.T, dir string) (flash, tmp string) {
	t.Helper()
	prevDefault, prevTemp := defaultCacheDBPath, tempCacheDBPath
	defaultCacheDBPath = filepath.Join(dir, "flash-cache.db")
	tempCacheDBPath = filepath.Join(dir, "tmp-cache.db")
	t.Cleanup(func() { defaultCacheDBPath, tempCacheDBPath = prevDefault, prevTemp })
	return defaultCacheDBPath, tempCacheDBPath
}

// Настройка задана — путём владеет она; пуста — рукописный абсолютный путь
// из base остаётся, заведомо негодный заменяется дефолтом (cacheDBPathFor).
// Та же таблица гонит reconcileCacheFile: изменена ли base — ровно тогда,
// когда эффективный путь отличается от текущего.
func TestCacheDBPathFor_AndReconcile(t *testing.T) {
	custom := "/srv/sing-box/my-cache.db"
	cases := []struct {
		name     string
		has      bool   // есть ли блок cache_file
		path     string // его path
		location string
		want     string
	}{
		{name: "пусто: блока нет → дефолт", location: "", want: defaultCacheDBPath},
		{name: "пусто: относительный → дефолт", has: true, path: "cache.db", location: "", want: defaultCacheDBPath},
		{name: "пусто: legacy → дефолт", has: true, path: legacyCacheFilePath, location: "", want: defaultCacheDBPath},
		{name: "пусто: рукописный остаётся", has: true, path: custom, location: "", want: custom},
		{name: "пусто: tmp остаётся", has: true, path: tempCacheDBPath, location: "", want: tempCacheDBPath},
		{name: "flash: рукописный → дефолт", has: true, path: custom, location: storage.CacheFileLocationFlash, want: defaultCacheDBPath},
		{name: "flash: tmp → дефолт", has: true, path: tempCacheDBPath, location: storage.CacheFileLocationFlash, want: defaultCacheDBPath},
		{name: "tmp: дефолт → tmp", has: true, path: defaultCacheDBPath, location: storage.CacheFileLocationTmp, want: tempCacheDBPath},
		{name: "tmp: рукописный → tmp", has: true, path: custom, location: storage.CacheFileLocationTmp, want: tempCacheDBPath},
		{name: "tmp: блока нет → tmp", location: storage.CacheFileLocationTmp, want: tempCacheDBPath},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cacheDBPathFor(c.location, baseWithCachePath(c.has, c.path)); got != c.want {
				t.Errorf("cacheDBPathFor = %q, want %q", got, c.want)
			}
			base := baseWithCachePath(c.has, c.path)
			// baseWithCachePath никогда не ставит store_dns, поэтому даже при
			// неизменном пути под /tmp реконсиляция обязана его добавить
			// (store_dns, sing-box 1.14) — это тоже правка base.
			wantChanged := !c.has || c.path != c.want || router.StoreDNSForCachePath(c.want)
			want, changed := reconcileCacheFile(base, c.location)
			if changed != wantChanged || want != c.want {
				t.Errorf("reconcile = (%q, %v), want (%q, %v)", want, changed, c.want, wantChanged)
			}
			if got := cachePathOf(t, base); got != c.want {
				t.Errorf("path = %q, want %q", got, c.want)
			}
		})
	}
}

// Пустой config.d: свежая база сразу несёт путь по настройке.
func TestEnsureBaseConfig_FreshWriteCarriesCachePath(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config.d")
	ensureBaseConfig(configDir, "info", "", 0, storage.CacheFileLocationTmp)
	if got := cachePathOf(t, readBaseFixture(t, filepath.Join(configDir, "00-base.json"))); got != tempCacheDBPath {
		t.Errorf("path = %q, want %q", got, tempCacheDBPath)
	}
}

// Без блока experimental вовсе (не только cache_file) — достраиваем.
func TestReconcileCacheFile_BuildsExperimental(t *testing.T) {
	base := map[string]any{}
	if _, changed := reconcileCacheFile(base, storage.CacheFileLocationTmp); !changed {
		t.Fatal("changed = false")
	}
	if got := cachePathOf(t, base); got != tempCacheDBPath {
		t.Errorf("path = %q, want %q", got, tempCacheDBPath)
	}
}

// Стартовый шаг пишет файл и логирует старый путь; отсутствующий блок в логе
// назван явно, а не пустой строкой.
func TestPatchBaseCacheFilePath_WritesAndLogs(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	dir := t.TempDir()
	p := filepath.Join(dir, "00-base.json")
	writeFixtureJSON(t, p, baseWithCachePath(true, "cache.db"))

	patchBaseCacheFilePath(p, storage.CacheFileLocationTmp, log)
	if got := cachePathOf(t, readBaseFixture(t, p)); got != tempCacheDBPath {
		t.Errorf("path = %q, want %q", got, tempCacheDBPath)
	}
	if !strings.Contains(buf.String(), "oldCachePath=cache.db") || !strings.Contains(buf.String(), "newCachePath="+tempCacheDBPath) {
		t.Errorf("лог без старого/нового пути: %s", buf.String())
	}

	buf.Reset()
	writeFixtureJSON(t, p, baseWithCachePath(false, ""))
	patchBaseCacheFilePath(p, storage.CacheFileLocationTmp, log)
	if !strings.Contains(buf.String(), "oldCachePath=<none>") {
		t.Errorf("отсутствующий блок не назван в логе: %s", buf.String())
	}
}

// Апгрейд существующей tmp-установки: путь уже верный, добавляется только
// store_dns — changed=true по этой причине, но живой cache.db на месте
// сносить нельзя (регрессия: finishCacheFileChange сносил его без проверки
// was == want).
func TestPatchBaseCacheFilePath_StoreDNSAddedKeepsLiveFile(t *testing.T) {
	dir := t.TempDir()
	_, tmpDB := redirectCacheDBPaths(t, dir)
	if err := os.WriteFile(tmpDB, []byte("live"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "00-base.json")
	writeFixtureJSON(t, p, baseWithCachePath(true, tmpDB))

	patchBaseCacheFilePath(p, storage.CacheFileLocationTmp, slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)))

	base := readBaseFixture(t, p)
	cf := base["experimental"].(map[string]any)["cache_file"].(map[string]any)
	if cf["store_dns"] != true {
		t.Errorf("store_dns = %v, want true", cf["store_dns"])
	}
	if _, err := os.Stat(tmpDB); err != nil {
		t.Errorf("живой cache.db снесён при неизменном пути: %v", err)
	}
}

// Смысл настройки "tmp" — беречь флеш: стартовое примирение обязано
// привести путь за одну запись, снести устаревший cache.db на флеше (как и
// живое применение) и не трогать файл на следующих бутах.
func TestReconcileConfigSteps_TmpCachePathStableAcrossBoots(t *testing.T) {
	dir := t.TempDir()
	flashDB, _ := redirectCacheDBPaths(t, dir)
	if err := os.WriteFile(flashDB, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(dir, "config.d")
	ensureBaseConfig(configDir, "info", "", 0, "")
	basePath := filepath.Join(configDir, "00-base.json")

	run := func() {
		for _, s := range reconcileConfigSteps(dir, configDir, "info", "", 0, storage.CacheFileLocationTmp, nil) {
			s.run()
		}
	}
	run()
	if got := cachePathOf(t, readBaseFixture(t, basePath)); got != tempCacheDBPath {
		t.Fatalf("after first boot path = %q, want %q", got, tempCacheDBPath)
	}
	if _, err := os.Stat(flashDB); !os.IsNotExist(err) {
		t.Errorf("устаревший cache.db на флеше пережил стартовый переезд в tmp: %v", err)
	}

	aged := ageFile(t, basePath)
	run()
	assertUntouched(t, basePath, aged, "second boot")
}

// Живое применение переключает путь в обе стороны, а лог пишется только по
// факту правки: совпадающий путь и отсутствующий config.d — без строки.
// Переезд сносит кэш по покинутому пути в обе стороны, откат его не
// воскрешает.
func TestOperator_ApplyCacheFileLocation(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	basePath := baseWithServers(t, configDir, []any{bootstrapEntry("1.1.1.1")})
	flashDB, tmpDB := redirectCacheDBPaths(t, dir)
	if err := os.WriteFile(flashDB, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	op := newOrchedOperatorWithDeps(t, OperatorDeps{Dir: dir, Log: slog.New(slog.NewTextHandler(&buf, nil))})
	logged := func() bool {
		defer buf.Reset()
		return strings.Contains(buf.String(), stepPatchBaseCacheFile)
	}
	buf.Reset() // строки стартового примирения не в счёт

	if err := op.ApplyCacheFileLocation(storage.CacheFileLocationTmp); err != nil {
		t.Fatalf("ApplyCacheFileLocation(tmp): %v", err)
	}
	if got := cachePathOf(t, readBaseFixture(t, basePath)); got != tempCacheDBPath {
		t.Errorf("tmp: path = %q, want %q", got, tempCacheDBPath)
	}
	if !logged() {
		t.Error("tmp: перезапись пути не залогирована")
	}
	if _, err := os.Stat(flashDB); !os.IsNotExist(err) {
		t.Errorf("старый cache.db на флеше не удалён: %v", err)
	}

	// Повтор: файл переформатирован компактно, чтобы байт-гейт оркестратора
	// не спас мутатор, вернувший «изменено» без правки, — «не пишем» обязан
	// держать сам reconcileCacheFile.
	compact, err := json.Marshal(readBaseFixture(t, basePath))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(basePath, compact, 0o644); err != nil {
		t.Fatal(err)
	}
	aged := ageFile(t, basePath)
	if err := op.ApplyCacheFileLocation(storage.CacheFileLocationTmp); err != nil {
		t.Fatalf("ApplyCacheFileLocation(tmp) repeat: %v", err)
	}
	if logged() {
		t.Error("repeat: лог без правки")
	}
	assertUntouched(t, basePath, aged, "repeat")

	if err := os.WriteFile(tmpDB, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := op.ApplyCacheFileLocation(storage.CacheFileLocationFlash); err != nil {
		t.Fatalf("ApplyCacheFileLocation(flash): %v", err)
	}
	if got := cachePathOf(t, readBaseFixture(t, basePath)); got != flashDB {
		t.Errorf("flash: path = %q, want %q", got, flashDB)
	}
	if !logged() {
		t.Error("flash: перезапись пути не залогирована")
	}
	if _, err := os.Stat(tmpDB); !os.IsNotExist(err) {
		t.Errorf("старый cache.db в RAM не удалён при откате: %v", err)
	}

	// Рукописный путь → flash: рукописный файл не наш, не трогаем.
	custom := filepath.Join(dir, "my-cache.db")
	if err := os.WriteFile(custom, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFixtureJSON(t, basePath, baseWithCachePath(true, custom))
	if err := op.ApplyCacheFileLocation(storage.CacheFileLocationFlash); err != nil {
		t.Fatalf("ApplyCacheFileLocation(flash) from custom: %v", err)
	}
	if _, err := os.Stat(custom); err != nil {
		t.Errorf("рукописный cache.db снесён: %v", err)
	}
	buf.Reset()

	if err := os.RemoveAll(configDir); err != nil {
		t.Fatal(err)
	}
	if err := op.ApplyCacheFileLocation(storage.CacheFileLocationTmp); err != nil {
		t.Fatalf("ApplyCacheFileLocation without config.d: %v", err)
	}
	if logged() {
		t.Error("без config.d: лог без правки")
	}
}

// Эффективный путь для overlay и статуса: рукописный путь из 00-base.json
// при пустой настройке, путь по настройке при заданной, дефолт без файла.
func TestOperator_CacheDBPath(t *testing.T) {
	custom := "/srv/sing-box/my-cache.db"
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	basePath := filepath.Join(configDir, "00-base.json")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureJSON(t, basePath, baseWithCachePath(true, custom))
	location := ""
	op := NewOperator(OperatorDeps{Dir: dir, CacheFileLocation: func() string { return location }})

	if got := op.CacheDBPath(); got != custom {
		t.Errorf("пусто: %q, want %q (рукописный путь)", got, custom)
	}
	location = storage.CacheFileLocationTmp
	if got := op.CacheDBPath(); got != tempCacheDBPath {
		t.Errorf("tmp: %q, want %q", got, tempCacheDBPath)
	}
	location = ""
	if err := os.Remove(basePath); err != nil {
		t.Fatal(err)
	}
	if got := op.CacheDBPath(); got != defaultCacheDBPath {
		t.Errorf("без файла: %q, want %q", got, defaultCacheDBPath)
	}
}

// Настройка "tmp" доезжает до 00-base.json уже на старте оператора.
func TestNewOperator_CacheFileLocationTmp(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	basePath := baseWithServers(t, configDir, []any{bootstrapEntry("1.1.1.1")})

	NewOperator(OperatorDeps{Dir: dir, CacheFileLocation: func() string { return storage.CacheFileLocationTmp }})

	if got := cachePathOf(t, readBaseFixture(t, basePath)); got != tempCacheDBPath {
		t.Errorf("path = %q, want %q", got, tempCacheDBPath)
	}
}

// store_dns (sing-box 1.14) — DNS-кэш в cache.db. Только при cache.db в tmp:
// на флеше это лишний износ ради секунд после перезапуска (решение владельца
// 2026-09-06).
func TestReconcileCacheFile_StoreDNSOnlyInTmp(t *testing.T) {
	base := map[string]any{}
	reconcileCacheFile(base, storage.CacheFileLocationTmp)
	cf := base["experimental"].(map[string]any)["cache_file"].(map[string]any)
	if cf["store_dns"] != true {
		t.Errorf("tmp: store_dns = %v, want true", cf["store_dns"])
	}
	if _, changed := reconcileCacheFile(base, storage.CacheFileLocationTmp); changed {
		t.Error("second reconcile must be a no-op")
	}
	if _, changed := reconcileCacheFile(base, storage.CacheFileLocationFlash); !changed {
		t.Error("switch to flash must report change")
	}
	if _, has := cf["store_dns"]; has {
		t.Errorf("flash: store_dns must be absent, got %v", cf["store_dns"])
	}

	// Ручная правка store_dns=false под tmp-путём — значение, а не только
	// присутствие ключа, должно расходиться с желаемым и требовать правки.
	reconcileCacheFile(base, storage.CacheFileLocationTmp)
	cf["store_dns"] = false
	if _, changed := reconcileCacheFile(base, storage.CacheFileLocationTmp); !changed {
		t.Error("hand-written store_dns=false under tmp must be corrected")
	}
	if cf["store_dns"] != true {
		t.Errorf("tmp: store_dns = %v, want true", cf["store_dns"])
	}
}
