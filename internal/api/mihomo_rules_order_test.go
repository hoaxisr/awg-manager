package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/mihomo"
	"github.com/hoaxisr/awg-manager/internal/mihomonative"
)

type orderMockMutationApplier struct {
	applyCount int
	outcome    *mihomo.MutationOutcome
}

func (m *orderMockMutationApplier) ApplyNativeMutation(ctx context.Context, mutateFn func() error) error {
	m.applyCount++
	return mutateFn()
}

func (m *orderMockMutationApplier) ApplyNativeMutationWithOutcome(ctx context.Context, mutateFn func() error) (*mihomo.MutationOutcome, error) {
	m.applyCount++
	err := mutateFn()
	if err != nil {
		return nil, err
	}
	outcome := m.outcome
	if outcome == nil {
		outcome = &mihomo.MutationOutcome{
			TxID:         "tx-order-test-123",
			ApplyPath:    mihomo.ApplyPathHotReload,
			Generation:   42,
			ServerTiming: "hot_reload;dur=12.5,validate;dur=1.2",
		}
	}
	return outcome, nil
}

func (m *orderMockMutationApplier) ApplyDraftOnly(ctx context.Context, mutateFn func() error) error {
	return mutateFn()
}

func (m *orderMockMutationApplier) ApplyPendingDraft(ctx context.Context) error {
	return nil
}

func (m *orderMockMutationApplier) IsDegraded() bool {
	return false
}

func (m *orderMockMutationApplier) CheckMutationAllowed() error {
	return nil
}

func (m *orderMockMutationApplier) Reconcile(ctx context.Context, action string, force bool) error {
	return nil
}

func (m *orderMockMutationApplier) ExportEvidence(ctx context.Context) (*mihomo.RecoveryEvidenceDTO, error) {
	return nil, nil
}

func setupRulesOrderTestHandler(t *testing.T) (*MihomoHandler, *mihomonative.Store, *orderMockMutationApplier) {
	storePath := filepath.Join(t.TempDir(), "mihomo-rules.json")
	store, err := mihomonative.NewStore(storePath)
	if err != nil {
		t.Fatalf("mihomonative.NewStore: %v", err)
	}

	applier := &orderMockMutationApplier{}
	engine := &fakeMihomoEngine{running: true, pid: 1234}
	h := NewMihomoHandler(engine)
	h.SetNativeStore(store)
	h.SetMutationApplier(applier)
	return h, store, applier
}

func TestNativeRules_Order_CAS_Success(t *testing.T) {
	h, store, applier := setupRulesOrderTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	r1, err := store.CreateRule(mihomonative.RuleInput{Type: "DOMAIN", Payload: "a.com", Outbound: "DIRECT"})
	if err != nil {
		t.Fatalf("CreateRule r1: %v", err)
	}
	r2, err := store.CreateRule(mihomonative.RuleInput{Type: "DOMAIN", Payload: "b.com", Outbound: "DIRECT"})
	if err != nil {
		t.Fatalf("CreateRule r2: %v", err)
	}

	currentRev := store.SnapshotRevision()

	// Reorder r2, r1 with matching baseRevision
	body, _ := json.Marshal(NativeRuleOrderRequest{
		IDs:          []string{r2.ID, r1.ID},
		BaseRevision: &currentRev,
		OperationID:  "op-test-1",
	})
	req := httptest.NewRequest(http.MethodPut, "/api/mihomo/native/rules/order", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify headers
	if rec.Header().Get("Server-Timing") == "" {
		t.Fatalf("expected Server-Timing header")
	}
	if rec.Header().Get("X-Transaction-ID") != "tx-order-test-123" {
		t.Fatalf("expected X-Transaction-ID header, got %q", rec.Header().Get("X-Transaction-ID"))
	}
	if rec.Header().Get("X-Apply-Path") != "hot_reload" {
		t.Fatalf("expected X-Apply-Path hot_reload, got %q", rec.Header().Get("X-Apply-Path"))
	}

	var resp struct {
		Success bool                    `json:"success"`
		Data    NativeRuleOrderResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if !resp.Data.Reordered {
		t.Fatalf("expected reordered: true")
	}
	if len(resp.Data.Items) != 2 || resp.Data.Items[0].ID != r2.ID || resp.Data.Items[1].ID != r1.ID {
		t.Fatalf("unexpected rule order: %v", resp.Data.Items)
	}
	if resp.Data.Revision <= currentRev {
		t.Fatalf("expected new revision > %d, got %d", currentRev, resp.Data.Revision)
	}
	if resp.Data.ApplyPath != "hot_reload" {
		t.Fatalf("expected applyPath hot_reload, got %s", resp.Data.ApplyPath)
	}

	// Test idempotency: repeat with same operationId
	req2 := httptest.NewRequest(http.MethodPut, "/api/mihomo/native/rules/order", bytes.NewReader(body))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("idempotent replay: expected 200, got %d", rec2.Code)
	}
	// Applier should not have been called a second time
	if applier.applyCount != 1 {
		t.Fatalf("expected exactly 1 mutation apply, got %d", applier.applyCount)
	}
}

func TestNativeRules_Order_CAS_StaleRejection(t *testing.T) {
	h, store, applier := setupRulesOrderTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	r1, _ := store.CreateRule(mihomonative.RuleInput{Type: "DOMAIN", Payload: "a.com", Outbound: "DIRECT"})
	r2, _ := store.CreateRule(mihomonative.RuleInput{Type: "DOMAIN", Payload: "b.com", Outbound: "DIRECT"})

	staleRev := store.SnapshotRevision() - 1

	body, _ := json.Marshal(NativeRuleOrderRequest{
		IDs:          []string{r2.ID, r1.ID},
		BaseRevision: &staleRev,
	})
	req := httptest.NewRequest(http.MethodPut, "/api/mihomo/native/rules/order", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409 Conflict, got %d: %s", rec.Code, rec.Body.String())
	}

	var errResp struct {
		Error   bool   `json:"error"`
		Code    string `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Items    []mihomonative.Rule `json:"items"`
			Revision uint64              `json:"revision"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}

	if errResp.Code != "MIHOMO_RULES_STALE" {
		t.Fatalf("expected code MIHOMO_RULES_STALE, got %q", errResp.Code)
	}
	if len(errResp.Data.Items) != 2 {
		t.Fatalf("expected 2 current items returned in 409 response, got %d", len(errResp.Data.Items))
	}
	if errResp.Data.Revision != store.SnapshotRevision() {
		t.Fatalf("expected current revision %d, got %d", store.SnapshotRevision(), errResp.Data.Revision)
	}
	if applier.applyCount != 0 {
		t.Fatalf("mutation applier must not be called on stale revision check")
	}
}

func TestNativeRules_Order_IdempotencyRejectsDifferentPayload(t *testing.T) {
	h, store, applier := setupRulesOrderTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)
	r1, _ := store.CreateRule(mihomonative.RuleInput{Type: "DOMAIN", Payload: "a.com", Outbound: "DIRECT"})
	r2, _ := store.CreateRule(mihomonative.RuleInput{Type: "DOMAIN", Payload: "b.com", Outbound: "DIRECT"})
	rev := store.SnapshotRevision()

	first, _ := json.Marshal(NativeRuleOrderRequest{IDs: []string{r2.ID, r1.ID}, BaseRevision: &rev, OperationID: "op-collision"})
	rec1 := httptest.NewRecorder()
	mux.ServeHTTP(rec1, httptest.NewRequest(http.MethodPut, "/api/mihomo/native/rules/order", bytes.NewReader(first)))
	if rec1.Code != http.StatusOK {
		t.Fatalf("first operation failed: %d %s", rec1.Code, rec1.Body.String())
	}

	second, _ := json.Marshal(NativeRuleOrderRequest{IDs: []string{r1.ID, r2.ID}, OperationID: "op-collision"})
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodPut, "/api/mihomo/native/rules/order", bytes.NewReader(second)))
	if rec2.Code != http.StatusConflict || !strings.Contains(rec2.Body.String(), "MIHOMO_OPERATION_ID_CONFLICT") {
		t.Fatalf("expected operation id conflict, got %d %s", rec2.Code, rec2.Body.String())
	}
	if applier.applyCount != 1 {
		t.Fatalf("conflicting replay must not apply mutation, got %d applies", applier.applyCount)
	}
}

func TestNativeRules_List_IncludesRevision(t *testing.T) {
	h, store, _ := setupRulesOrderTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	_, _ = store.CreateRule(mihomonative.RuleInput{Type: "DOMAIN", Payload: "a.com", Outbound: "DIRECT"})

	req := httptest.NewRequest(http.MethodGet, "/api/mihomo/native/rules", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			Items    []mihomonative.Rule `json:"items"`
			Revision uint64              `json:"revision"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if len(resp.Data.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(resp.Data.Items))
	}
	if resp.Data.Revision != store.SnapshotRevision() {
		t.Fatalf("expected revision %d, got %d", store.SnapshotRevision(), resp.Data.Revision)
	}
}
