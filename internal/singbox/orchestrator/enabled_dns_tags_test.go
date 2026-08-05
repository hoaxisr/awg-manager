package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

// EnabledDNSServerTags — DNS-аналог EnabledOutboundTags: та же видимость, по
// которой судит кросс-слотовая валидация. Нужен продюсеру, который проверяет
// ссылку domain_resolver на своей мутации: резолвером законно служит сервер
// соседнего слота (dns-bootstrap из 00-base.json).
func TestEnabledDNSServerTags(t *testing.T) {
	dir := t.TempDir()
	o := New(dir, nil)
	for _, meta := range KnownSlots() {
		switch meta.Slot {
		case SlotBase, SlotRouting, SlotFakeIP:
			if err := o.Register(meta); err != nil {
				t.Fatalf("register %s: %v", meta.Slot, err)
			}
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("00-base.json", `{"dns":{"servers":[{"tag":"dns-bootstrap","type":"udp","server":"1.1.1.1"}]}}`)
	write("21-routing.json", `{"dns":{"servers":[{"tag":"user-doh","type":"https","server":"cloudflare-dns.com"}]}}`)
	// Режимный слот выключен: файл припаркован в disabled/.
	if err := os.MkdirAll(filepath.Join(dir, disabledSubdir), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, disabledSubdir, "20-fakeip.json"),
		[]byte(`{"dns":{"servers":[{"tag":"real","type":"udp","server":"8.8.8.8"}]}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := o.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	all := o.EnabledDNSServerTags()
	if !all["dns-bootstrap"] || !all["user-doh"] {
		t.Errorf("теги включённых слотов не видны: %v", all)
	}
	if all["real"] {
		t.Errorf("тег выключенного слота виден: %v", all)
	}

	own := o.EnabledDNSServerTags(SlotRouting)
	if !own["dns-bootstrap"] {
		t.Errorf("dns-bootstrap из 00-base.json обязан быть виден: %v", own)
	}
	if own["user-doh"] {
		t.Errorf("исключённый слот отдал свои теги: %v", own)
	}
}
