package awg3endpoint

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	obox "github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// capProbeConfig — минимальный, но структурно валидный awg-endpoint. На
// awg-capable бинаре `check` его принимает (exit 0); upstream sing-box без
// `-tags with_awg` отвергает (стаб возвращает "not included in this build").
// Служит гейтом awg-capability до основного check.
const capProbeConfig = `{"endpoints":[{"type":"awg","tag":"cap-probe",` +
	`"private_key":"cGVlclByaXZhdGVLZXlCYXNlNjRFeGFtcGxlMDAwMDAwMD0=",` +
	`"address":["10.0.0.2/32"],"peers":[{` +
	`"public_key":"c2VydmVyUHVibGljS2V5QmFzZTY0RXhhbXBsZTAwMD0=",` +
	`"address":"192.0.2.1","port":51820,"allowed_ips":["0.0.0.0/0"]}]}]}`

// runSingboxCheck прогоняет `<bin> check -c <config>` и возвращает
// объединённый stdout+stderr и признак успеха (exit 0). check — офлайн.
func runSingboxCheck(t *testing.T, bin string, config []byte) (string, bool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, config, 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "check", "-c", path).CombinedOutput()
	return string(out), err == nil
}

// TestIntegration_RouteboxToSlotSingboxCheck — сквозной путь Task 6:
// golden RouteBox JSON → Parse → Store.Add → Service.Sync (fake orch,
// захватывающий slot-байты) → минимальный sing-box-конфиг из slot →
// `sing-box check` против awg3-форк-бинаря → PASS.
//
// Бинарь — из env AWG3_SINGBOX_BIN (host-сборка hoaxisr/amnezia-box@awg-1.14
// с `-tags with_awg`). Не берём sing-box из PATH: там может оказаться upstream
// без AWG → ложный pass/skip.
func TestIntegration_RouteboxToSlotSingboxCheck(t *testing.T) {
	bin := os.Getenv("AWG3_SINGBOX_BIN")
	if bin == "" {
		t.Skip("set AWG3_SINGBOX_BIN to the awg3 fork binary")
	}

	// Гейт awg-capability: валидный awg-endpoint должен пройти check.
	if out, ok := runSingboxCheck(t, bin, []byte(capProbeConfig)); !ok {
		t.Fatalf("AWG3_SINGBOX_BIN is not awg-capable "+
			"(check rejected a valid awg endpoint):\n%s", out)
	}

	// 1. golden RouteBox → Parse → Store → Sync (реальный конвейер).
	raw, err := os.ReadFile("testdata/routebox-client.json")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := Parse(raw, "awg-amsterdam", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	store := NewStore(filepath.Join(t.TempDir(), "awg3.json"))
	if err := store.Add(rec); err != nil {
		t.Fatal(err)
	}
	orch := &fakeOrch{}
	if err := NewService(store, orch).Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	slot := orch.saved[obox.SlotAwg3]
	if len(slot) == 0 {
		t.Fatal("SlotAwg3 not captured")
	}

	// 2. Захваченный slot — уже валидный sing-box-конфиг ({"endpoints":[...]}).
	// Единственная правка: peer address → литеральный IP. `check` инициализирует
	// endpoint и резолвит peer по DNS; golden-хост vpn.example.com не резолвится
	// → тест стал бы сетезависимым. Литеральный IP держит check офлайн и
	// детерминированным, не затрагивая проверяемую AWG3-схему.
	var cfg struct {
		Endpoints []map[string]json.RawMessage `json:"endpoints"`
	}
	if err := json.Unmarshal(slot, &cfg); err != nil {
		t.Fatalf("unmarshal slot: %v", err)
	}
	if len(cfg.Endpoints) != 1 {
		t.Fatalf("endpoints = %d, want 1", len(cfg.Endpoints))
	}
	var peers []map[string]json.RawMessage
	if err := json.Unmarshal(cfg.Endpoints[0]["peers"], &peers); err != nil {
		t.Fatalf("unmarshal peers: %v", err)
	}
	peers[0]["address"] = json.RawMessage(`"192.0.2.1"`)
	peersJSON, _ := json.Marshal(peers)
	cfg.Endpoints[0]["peers"] = peersJSON
	config, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// 3. Основной check: AWG3-endpoint из golden проходит валидацию форка.
	if out, ok := runSingboxCheck(t, bin, config); !ok {
		t.Fatalf("sing-box check failed on materialized AWG3 slot:\n%s", out)
	} else if strings.Contains(out, "FATAL") {
		t.Fatalf("sing-box check reported FATAL despite exit 0:\n%s", out)
	}
}
