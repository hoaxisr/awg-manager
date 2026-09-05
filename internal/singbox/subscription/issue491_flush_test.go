package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// issue491Validator скриптует sing-box check: outbound с тегом, содержащим
// "bad", валится decode-ошибкой с индексом; считает вызовы отдельно для
// standalone-снапшота (в директории ТОЛЬКО 40-subscriptions.json) и merged
// (есть соседние слоты).
type issue491Validator struct {
	standaloneCalls int
	mergedCalls     int
}

func (v *issue491Validator) Validate(_ context.Context, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) == 1 {
		v.standaloneCalls++
	} else {
		v.mergedCalls++
	}
	path := filepath.Join(dir, "40-subscriptions.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil // наш слот не в снапшоте — нечего валидировать
	}
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	for i, ob := range cfg.Outbounds {
		if tag, _ := ob["tag"].(string); strings.Contains(tag, "bad") {
			return fmt.Errorf("decode config at %s: outbounds[%d]: unknown transport type: xhttp", path, i)
		}
	}
	return nil
}

func newIssue491Harness(t *testing.T) (*OperatorAdapter, *issue491Validator) {
	t.Helper()
	dir := t.TempDir()
	orch := orchestrator.NewWithAppliedPath(dir, nil, filepath.Join(t.TempDir(), "singbox-applied.json"))
	// Соседний слот, чтобы merged-снапшот отличался от standalone (2 файла vs 1).
	if err := orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotRouter, Filename: "20-router.json"}); err != nil {
		t.Fatal(err)
	}
	if err := orch.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := orch.Save(orchestrator.SlotRouter, []byte(`{"outbounds":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := orch.SetEnabled(orchestrator.SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	v := &issue491Validator{}
	orch.SetValidator(v)
	return NewOperatorAdapter(orch, nil, nil), v
}

// Issue #491: после первой merged-отбраковки остальные невалидные
// outbound'ы дренируются по standalone-снапшоту одного слота (дёшево);
// merged-чеков ровно два — стартовый и финальный, не по одному на дроп.
func TestFlush_DropLoopRunsStandalone(t *testing.T) {
	adapter, v := newIssue491Harness(t)

	tags := []string{"sub-x-good1", "sub-x-bad1", "sub-x-good2", "sub-x-bad2", "sub-x-good3"}
	for i, tag := range tags {
		if err := adapter.AddOutbound(tag, validVlessJSON(fmt.Sprintf("10.0.0.%d", i+1))); err != nil {
			t.Fatalf("AddOutbound %s: %v", tag, err)
		}
	}
	if err := adapter.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	declared := strings.Join(adapter.DeclaredOutboundTags(), ",")
	if strings.Contains(declared, "bad") {
		t.Errorf("bad outbounds must be dropped, declared: %s", declared)
	}
	for _, g := range []string{"good1", "good2", "good3"} {
		if !strings.Contains(declared, g) {
			t.Errorf("good outbound %s lost, declared: %s", g, declared)
		}
	}
	// merged №1 ловит bad1 (дроп в merged-цикле), drain: standalone №1
	// ловит bad2, standalone №2 — Ok; merged №2 подтверждает чистый слот.
	if v.standaloneCalls != 2 {
		t.Errorf("standalone checks = %d, want 2", v.standaloneCalls)
	}
	if v.mergedCalls != 2 {
		t.Errorf("merged checks = %d, want 2 (initial + final, not one per drop)", v.mergedCalls)
	}
	// Причины дропов зафиксированы для UI.
	if dropped := adapter.LastFilterDrops(); len(dropped) != 2 {
		t.Errorf("LastDropped = %d entries, want 2: %+v", len(dropped), dropped)
	}
}

// Исчерпание cap по числу дропов — честная ошибка, а не тихий переход в
// медленный merged-цикл и не бесконечный спиннер.
func TestFlush_DropLoopCapFailsHonestly(t *testing.T) {
	oldMax := dropLoopMaxDrops
	dropLoopMaxDrops = 2
	defer func() { dropLoopMaxDrops = oldMax }()

	adapter, _ := newIssue491Harness(t)
	for i := 0; i < 4; i++ {
		if err := adapter.AddOutbound(fmt.Sprintf("sub-x-bad%d", i), validVlessJSON(fmt.Sprintf("10.0.1.%d", i+1))); err != nil {
			t.Fatal(err)
		}
	}
	err := adapter.Reload(context.Background())
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation on cap exhaustion, got %v", err)
	}
	if !strings.Contains(err.Error(), "слишком много неподдерживаемых") {
		t.Errorf("error must explain the cap: %v", err)
	}
}
