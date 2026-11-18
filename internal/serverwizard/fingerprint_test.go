package serverwizard

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/cdndispatcher"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

type mockXrayReader struct {
	cfg vlessserver.Config
	st  vlessserver.Status
}

func (m *mockXrayReader) GetConfig() vlessserver.Config { return m.cfg }
func (m *mockXrayReader) GetStatus() vlessserver.Status { return m.st }

type mockDispReader struct {
	cfg     cdndispatcher.Config
	running bool
}

func (m *mockDispReader) GetConfig() cdndispatcher.Config { return m.cfg }
func (m *mockDispReader) IsRunning() bool                 { return m.running }

type mockTgReader struct {
	cfg tgwebproxy.PublicConfig
	st  tgwebproxy.Status
}

func (m *mockTgReader) GetConfig() tgwebproxy.PublicConfig { return m.cfg }
func (m *mockTgReader) GetStatus() tgwebproxy.Status       { return m.st }

type mockRecReader struct {
	required bool
	reason   string
}

func (m *mockRecReader) IsRecoveryRequired() (bool, string) { return m.required, m.reason }

type mockEgressReader struct {
	summary string
}

func (m *mockEgressReader) EgressSummary(ctx context.Context) string { return m.summary }

func TestFingerprintEngine_Determinism(t *testing.T) {
	tempDir := t.TempDir()
	procDir := filepath.Join(tempDir, "proc")
	procNetDir := filepath.Join(procDir, "net")
	_ = os.MkdirAll(procNetDir, 0755)
	_ = os.WriteFile(filepath.Join(procNetDir, "tcp"), []byte("  sl  local_address rem_address   st\n"), 0644)
	_ = os.WriteFile(filepath.Join(procNetDir, "tcp6"), []byte("  sl  local_address rem_address   st\n"), 0644)

	initScript := filepath.Join(tempDir, "S99test")
	_ = os.WriteFile(initScript, []byte("#!/bin/sh\n"), 0755)

	xray := &mockXrayReader{cfg: vlessserver.Config{Enabled: true, ListenPort: 9008}}
	disp := &mockDispReader{cfg: cdndispatcher.Config{ListenAddr: ":9009"}, running: true}
	tg := &mockTgReader{cfg: tgwebproxy.PublicConfig{Enabled: true, ListenPort: 8085}}
	rec := &mockRecReader{required: false}
	egress := &mockEgressReader{summary: "wan:eth0|awgm0:up"}

	engine := NewFingerprintEngine(xray, disp, tg, rec, egress)
	engine.SetProcDir(procDir)
	engine.SetInitScripts([]string{initScript})
	engine.SetTargetPorts([]int{9008, 9009})

	ctx := context.Background()
	fp1 := engine.Compute(ctx)
	fp2 := engine.Compute(ctx)

	if fp1 == "" || fp1 != fp2 {
		t.Fatalf("expected identical non-empty fingerprints, got fp1=%s, fp2=%s", fp1, fp2)
	}
}

func TestFingerprintEngine_DriftDetection(t *testing.T) {
	tempDir := t.TempDir()
	procDir := filepath.Join(tempDir, "proc")
	procNetDir := filepath.Join(procDir, "net")
	_ = os.MkdirAll(procNetDir, 0755)
	_ = os.WriteFile(filepath.Join(procNetDir, "tcp"), []byte("  sl  local_address rem_address   st\n"), 0644)
	_ = os.WriteFile(filepath.Join(procNetDir, "tcp6"), []byte("  sl  local_address rem_address   st\n"), 0644)

	initScript := filepath.Join(tempDir, "S99test")
	_ = os.WriteFile(initScript, []byte("#!/bin/sh\n"), 0755)

	xray := &mockXrayReader{cfg: vlessserver.Config{Enabled: true, ListenPort: 9008}}
	disp := &mockDispReader{cfg: cdndispatcher.Config{ListenAddr: ":9009"}, running: true}
	tg := &mockTgReader{cfg: tgwebproxy.PublicConfig{Enabled: true, ListenPort: 8085}}
	rec := &mockRecReader{required: false}
	egress := &mockEgressReader{summary: "wan:eth0|awgm0:up"}

	engine := NewFingerprintEngine(xray, disp, tg, rec, egress)
	engine.SetProcDir(procDir)
	engine.SetInitScripts([]string{initScript})
	engine.SetTargetPorts([]int{9008, 9009})

	ctx := context.Background()
	baseFP := engine.Compute(ctx)

	// 1. Xray config drift
	xray.cfg.ListenPort = 9010
	driftFP := engine.Compute(ctx)
	if driftFP == baseFP {
		t.Fatalf("expected drift on Xray config change")
	}
	xray.cfg.ListenPort = 9008

	// 2. Dispatcher running state drift
	disp.running = false
	driftFP = engine.Compute(ctx)
	if driftFP == baseFP {
		t.Fatalf("expected drift on Dispatcher running state change")
	}
	disp.running = true

	// 3. TG config drift
	tg.cfg.PublicHostname = "new.host.com"
	driftFP = engine.Compute(ctx)
	if driftFP == baseFP {
		t.Fatalf("expected drift on TG config change")
	}
	tg.cfg.PublicHostname = ""

	// 4. Recovery required drift
	rec.required = true
	rec.reason = "disk corruption"
	driftFP = engine.Compute(ctx)
	if driftFP == baseFP {
		t.Fatalf("expected drift on recovery state change")
	}
	rec.required = false
	rec.reason = ""

	// 5. Egress summary drift
	egress.summary = "wan:eth0|awgm0:down"
	driftFP = engine.Compute(ctx)
	if driftFP == baseFP {
		t.Fatalf("expected drift on egress change")
	}
	egress.summary = "wan:eth0|awgm0:up"

	// 6. Init script drift
	time.Sleep(10 * time.Millisecond)
	_ = os.WriteFile(initScript, []byte("#!/bin/sh\n# modified\n"), 0755)
	driftFP = engine.Compute(ctx)
	if driftFP == baseFP {
		t.Fatalf("expected drift on init script modification")
	}
}

func TestFingerprintStrictProbeFailureBlocksTransaction(t *testing.T) {
	tempDir := t.TempDir()
	procDir := filepath.Join(tempDir, "proc")
	procNetDir := filepath.Join(procDir, "net")
	_ = os.MkdirAll(procNetDir, 0755)
	_ = os.WriteFile(filepath.Join(procNetDir, "tcp"), []byte("  sl  local_address rem_address   st\n"), 0644)
	_ = os.WriteFile(filepath.Join(procNetDir, "tcp6"), []byte("  sl  local_address rem_address   st\n"), 0644)

	xray := &mockXrayReader{cfg: vlessserver.Config{Enabled: true, ListenPort: 9008}}
	disp := &mockDispReader{cfg: cdndispatcher.Config{ListenAddr: ":9009"}, running: true}
	tg := &mockTgReader{cfg: tgwebproxy.PublicConfig{Enabled: true, ListenPort: 8085}}
	rec := &mockRecReader{required: false}
	egress := &mockEgressReader{summary: "wan:eth0|awgm0:up"}

	engine := NewFingerprintEngine(xray, disp, tg, rec, egress)
	engine.SetProcDir(procDir)

	// 1. Success case with normal context
	ctx := context.Background()
	fp, err := engine.ComputeStrict(ctx)
	if err != nil || fp == "" {
		t.Fatalf("expected clean strict computation, got fp=%q, err=%v", fp, err)
	}

	// 2. Canceled context must fail closed
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = engine.ComputeStrict(cancelCtx)
	if err == nil {
		t.Fatalf("expected error on canceled context, got nil")
	}

	// 3. Corrupt or unreadable procfs should fail closed
	badProcDir := filepath.Join(tempDir, "non_existent_proc")
	engine.SetProcDir(badProcDir)
	_, err = engine.ComputeStrict(ctx)
	if err == nil {
		t.Fatalf("expected error on missing procfs, got nil")
	}
}
