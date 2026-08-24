package singbox

import (
	"os"
	"path/filepath"
	"testing"
)

// removeDNSFinalFromBase стрижёт dns.final безусловно и НЕ трогает соседние
// ключи блока dns. Отдельный тест, потому что оба скаляра живут рядом, а
// границы шагов легко размываются при правках.
func TestRemoveDNSFinalFromBase_NeverTouchesStrategy(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "00-base.json")
	if err := os.WriteFile(basePath,
		[]byte(`{"dns":{"final":"dns-bootstrap","strategy":"prefer_ipv4"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20-router.json"),
		[]byte(`{"dns":{"strategy":"ipv4_only"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	removeDNSFinalFromBase(basePath)

	dns, _ := readJSONMap(t, basePath)["dns"].(map[string]any)
	if _, has := dns["final"]; has {
		t.Fatalf("dns.final обязан стричься безусловно: %v", dns)
	}
	if dns["strategy"] != "prefer_ipv4" {
		t.Fatalf("strategy не забота remove-dns-final: %v", dns)
	}
}

// Рантайм-вариант шага: метод Operator пишет исцелённый base (orch == nil —
// прямая запись, как у ApplyLogLevel).
