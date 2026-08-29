package singbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Порт из настроек доезжает и до свежей базы, и до существующей: 00-base.json
// остаётся выигрывающей позицией merge, а значением владеем мы (ADR 0001).
func TestClashPort_ReachesBaseConfig(t *testing.T) {
	readController := func(t *testing.T, basePath string) string {
		t.Helper()
		raw, err := os.ReadFile(basePath)
		if err != nil {
			t.Fatal(err)
		}
		var m struct {
			Experimental struct {
				ClashAPI struct {
					ExternalController string `json:"external_controller"`
				} `json:"clash_api"`
			} `json:"experimental"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return m.Experimental.ClashAPI.ExternalController
	}

	t.Run("fresh base uses configured port", func(t *testing.T) {
		configDir := filepath.Join(t.TempDir(), "config.d")
		ensureBaseConfig(configDir, "info", "", 9500)
		if got := readController(t, filepath.Join(configDir, "00-base.json")); got != "127.0.0.1:9500" {
			t.Errorf("want 127.0.0.1:9500, got %q", got)
		}
	})

	t.Run("port 0 falls back to default", func(t *testing.T) {
		configDir := filepath.Join(t.TempDir(), "config.d")
		ensureBaseConfig(configDir, "info", "", 0)
		if got := readController(t, filepath.Join(configDir, "00-base.json")); got != ClashAddr(DefaultClashPort) {
			t.Errorf("want default, got %q", got)
		}
	})

	t.Run("existing base is repointed", func(t *testing.T) {
		configDir := filepath.Join(t.TempDir(), "config.d")
		if err := os.MkdirAll(configDir, 0755); err != nil {
			t.Fatal(err)
		}
		basePath := filepath.Join(configDir, "00-base.json")
		stale := `{"experimental":{"clash_api":{"external_controller":"127.0.0.1:9099"},"cache_file":{"enabled":true}}}`
		if err := os.WriteFile(basePath, []byte(stale), 0644); err != nil {
			t.Fatal(err)
		}
		ensureBaseConfig(configDir, "info", "", 9500)
		if got := readController(t, basePath); got != "127.0.0.1:9500" {
			t.Errorf("want 127.0.0.1:9500, got %q", got)
		}
		// Соседние ключи внутри experimental не должны пострадать.
		raw, _ := os.ReadFile(basePath)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		exp := m["experimental"].(map[string]any)
		if _, has := exp["cache_file"]; !has {
			t.Errorf("cache_file вымыт правкой порта: %s", raw)
		}
	})
}

// setClashController создаёт недостающие уровни: отсутствие блока — не воля
// пользователя, а состояние, которое мы чиним.
func TestSetClashController(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		changed bool
	}{
		{"empty config", `{}`, "127.0.0.1:9500", true},
		{"no clash_api", `{"experimental":{"cache_file":{"enabled":true}}}`, "127.0.0.1:9500", true},
		{"null clash_api", `{"experimental":{"clash_api":null}}`, "127.0.0.1:9500", true},
		{"already correct", `{"experimental":{"clash_api":{"external_controller":"127.0.0.1:9500"}}}`, "127.0.0.1:9500", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal([]byte(c.in), &m); err != nil {
				t.Fatal(err)
			}
			if got := setClashController(m, "127.0.0.1:9500"); got != c.changed {
				t.Errorf("changed: want %v, got %v", c.changed, got)
			}
			exp := m["experimental"].(map[string]any)
			clash := exp["clash_api"].(map[string]any)
			if got := clash["external_controller"]; got != c.want {
				t.Errorf("want %q, got %v", c.want, got)
			}
		})
	}
}

// ApplyClashPort целиком: запись в базу и переустановка клиента, включая
// порядок этих двух шагов и поведение при отсутствующем 00-base.json.
func TestOperator_ApplyClashPort(t *testing.T) {
	controllerOf := func(t *testing.T, basePath string) string {
		t.Helper()
		raw, err := os.ReadFile(basePath)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		exp, _ := m["experimental"].(map[string]any)
		clash, _ := exp["clash_api"].(map[string]any)
		addr, _ := clash["external_controller"].(string)
		return addr
	}

	t.Run("база и клиент переезжают вместе", func(t *testing.T) {
		op := newOrchedOperatorWithDeps(t, OperatorDeps{Dir: t.TempDir()})
		basePath := filepath.Join(op.ConfigDir(), "00-base.json")

		if err := op.ApplyClashPort(9500); err != nil {
			t.Fatalf("ApplyClashPort: %v", err)
		}
		if got := controllerOf(t, basePath); got != "127.0.0.1:9500" {
			t.Errorf("в конфиге %q, want 127.0.0.1:9500", got)
		}
		if got := op.Clash().Address(); got != "127.0.0.1:9500" {
			t.Errorf("клиент смотрит в %q, want 127.0.0.1:9500", got)
		}
	})

	t.Run("повторное применение того же порта — no-op без ошибки", func(t *testing.T) {
		op := newOrchedOperatorWithDeps(t, OperatorDeps{Dir: t.TempDir(), ClashPort: func() int { return 9500 }})
		if err := op.ApplyClashPort(9500); err != nil {
			t.Fatalf("ApplyClashPort: %v", err)
		}
		if got := op.Clash().Address(); got != "127.0.0.1:9500" {
			t.Errorf("клиент смотрит в %q, want 127.0.0.1:9500", got)
		}
	})

	// Файл пропасть посреди жизни может только при ручном вмешательстве.
	// Прежде mutateBase такое молча пропускал: расхождение самолечилось лишь
	// на следующем буте, когда NewOperator пересоздаст базу с портом из
	// настроек, — а роутер может не перезагружаться месяцами, показывая
	// настройку применённой. Теперь база восстанавливается сразу, вместе с
	// прочими условно-своими скалярами. Симметрично трактовке отсутствующего
	// блока clash_api в ADR-0001: отсутствие — не намерение пользователя.
	t.Run("пропавшая база при живом каталоге — восстанавливается", func(t *testing.T) {
		op := newOrchedOperatorWithDeps(t, OperatorDeps{Dir: t.TempDir()})
		basePath := filepath.Join(op.ConfigDir(), "00-base.json")
		if err := os.Remove(basePath); err != nil {
			t.Fatal(err)
		}
		if err := op.ApplyClashPort(9500); err != nil {
			t.Fatalf("ApplyClashPort: %v", err)
		}
		if got := controllerOf(t, basePath); got != "127.0.0.1:9500" {
			t.Errorf("в восстановленной базе %q, want 127.0.0.1:9500", got)
		}
		if got := op.Clash().Address(); got != "127.0.0.1:9500" {
			t.Errorf("клиент смотрит в %q, want 127.0.0.1:9500", got)
		}
	})

	// Провалившаяся запись не должна оставлять клиент смотреть в порт,
	// которого нет в конфиге: SetAddress зовётся строго после успеха.
	t.Run("битая база — клиент остаётся на старом порту", func(t *testing.T) {
		op := newOrchedOperatorWithDeps(t, OperatorDeps{Dir: t.TempDir()})
		basePath := filepath.Join(op.ConfigDir(), "00-base.json")
		if err := os.WriteFile(basePath, []byte("{не json"), 0644); err != nil {
			t.Fatal(err)
		}
		before := op.Clash().Address()
		if err := op.ApplyClashPort(9500); err == nil {
			t.Fatal("ожидалась ошибка разбора 00-base.json")
		}
		if got := op.Clash().Address(); got != before {
			t.Errorf("клиент переставлен при провале записи: %q, было %q", got, before)
		}
	})
}

func TestEffectiveClashPort(t *testing.T) {
	for _, c := range []struct{ in, want int }{
		{0, DefaultClashPort},
		{-1, DefaultClashPort},
		{9500, 9500},
	} {
		if got := EffectiveClashPort(c.in); got != c.want {
			t.Errorf("EffectiveClashPort(%d): want %d, got %d", c.in, c.want, got)
		}
	}
}

// Адрес клиента меняется в рантайме и читается конкурентно — гонки быть не
// должно (гоняется под -race).
func TestClashClient_SetAddressConcurrent(t *testing.T) {
	c := NewClashClient(ClashAddr(0))
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			c.SetAddress("127.0.0.1:9500")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = c.Address()
		}
	}()
	wg.Wait()
	if got := c.Address(); got != "127.0.0.1:9500" {
		t.Errorf("want 127.0.0.1:9500, got %q", got)
	}
}
