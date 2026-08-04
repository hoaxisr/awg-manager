package orchestrator

import (
	"context"
	"errors"
	"os"
	"testing"
)

// recordingAloneValidator записывает список файлов в снапшоте и отдаёт
// заранее заданную ошибку.
type recordingAloneValidator struct {
	seenFiles []string
	fail      error
}

func (v *recordingAloneValidator) Validate(_ context.Context, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	v.seenFiles = v.seenFiles[:0]
	for _, e := range entries {
		v.seenFiles = append(v.seenFiles, e.Name())
	}
	return v.fail
}

func newAloneTestOrch(t *testing.T) (*Orchestrator, *recordingAloneValidator) {
	t.Helper()
	dir := t.TempDir()
	o := New(dir, nil)
	if err := o.Register(SlotMeta{Slot: SlotRouting, Filename: "20-router.json"}); err != nil {
		t.Fatal(err)
	}
	if err := o.Register(SlotMeta{Slot: SlotSubscriptions, Filename: "40-subscriptions.json"}); err != nil {
		t.Fatal(err)
	}
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	// Сосед включён и с файлом — merged-снапшот содержал бы его.
	if err := o.Save(SlotRouting, []byte(`{"outbounds":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEnabled(SlotRouting, true); err != nil {
		t.Fatal(err)
	}
	v := &recordingAloneValidator{}
	o.SetValidator(v)
	return o, v
}

// CheckSlotAlone кладёт в снапшот ТОЛЬКО целевой слот (issue #491: дешёвая
// итеративная проверка без пересъёма всех слотов).
func TestCheckSlotAlone_SnapshotsOnlyTargetSlot(t *testing.T) {
	o, v := newAloneTestOrch(t)
	res, err := o.CheckSlotAlone(SlotSubscriptions, []byte(`{"outbounds":[{"type":"direct","tag":"x"}]}`))
	if err != nil {
		t.Fatalf("CheckSlotAlone: %v", err)
	}
	if !res.Ok() {
		t.Fatalf("expected ok, got %v", res.Errors)
	}
	if len(v.seenFiles) != 1 || v.seenFiles[0] != "40-subscriptions.json" {
		t.Errorf("snapshot files = %v, want only 40-subscriptions.json", v.seenFiles)
	}
}

// Ошибки обеих форм sing-box атрибутируются к слоту и локальному индексу —
// в standalone-снапшоте merged-индекс совпадает с локальным.
func TestCheckSlotAlone_AttributesOutboundIndex(t *testing.T) {
	o, v := newAloneTestOrch(t)
	v.fail = errors.New("FATAL[0000] initialize outbound[1]: unknown method: Join,telegram")
	res, err := o.CheckSlotAlone(SlotSubscriptions, []byte(`{"outbounds":[{"type":"direct","tag":"a"},{"type":"direct","tag":"b"}]}`))
	if err != nil {
		t.Fatalf("CheckSlotAlone: %v", err)
	}
	if res.Ok() || len(res.Errors) != 1 {
		t.Fatalf("expected 1 error, got %+v", res.Errors)
	}
	e := res.Errors[0]
	if e.OutboundIndex == nil || *e.OutboundIndex != 1 || e.OutboundSlot != SlotSubscriptions {
		t.Errorf("attribution = slot=%q idx=%v, want subscriptions/1", e.OutboundSlot, e.OutboundIndex)
	}
}

// Неизвестный слот — ErrUnknownSlot; nil-валидатор — Ok (паритет с CheckMerged).
func TestCheckSlotAlone_Guards(t *testing.T) {
	o, _ := newAloneTestOrch(t)
	if _, err := o.CheckSlotAlone(Slot("nope"), []byte(`{}`)); !errors.Is(err, ErrUnknownSlot) {
		t.Fatalf("expected ErrUnknownSlot, got %v", err)
	}
	o.SetValidator(nil)
	res, err := o.CheckSlotAlone(SlotSubscriptions, []byte(`{}`))
	if err != nil || !res.Ok() {
		t.Fatalf("nil validator must pass: res=%v err=%v", res, err)
	}
}
