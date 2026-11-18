package service

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestUplinkServiceGuardBeforeRuntimeOrPersistence(t *testing.T) {
	blocked := errors.New("policy conflict")
	s := &ServiceImpl{}
	var operations []string
	s.SetUplinkMutationGuard(func(ctx context.Context, id, operation string) (func(), error) {
		operations = append(operations, operation)
		return nil, blocked
	})
	ctx := context.Background()
	old := &storage.AWGTunnel{ID: "awg50", Name: "before"}
	next := *old
	next.ISPInterface = "ISP2"
	for _, call := range []func() error{
		func() error { return s.Update(ctx, old, &next) },
		func() error { return s.ReplaceConfig(ctx, "awg50", "invalid", "new") },
		func() error { return s.Delete(ctx, "awg50") },
		func() error { return s.Start(ctx, "awg50") },
		func() error { return s.Stop(ctx, "awg50") },
		func() error { return s.Restart(ctx, "awg50") },
		func() error { return s.SetEnabled(ctx, "awg50", false) },
	} {
		if err := call(); !errors.Is(err, blocked) {
			t.Fatal(err)
		}
	}
	if len(operations) != 7 || operations[0] != "transport" || operations[1] != "transport" || operations[2] != "delete" {
		t.Fatal(operations)
	}
	next = *old
	next.Name = "renamed"
	if err := s.Update(ctx, old, &next); !errors.Is(err, blocked) || operations[len(operations)-1] != "metadata" {
		t.Fatal(err, operations)
	}
}
func TestUplinkServiceLeaseReleasedOnLifecycleFailure(t *testing.T) {
	s := &ServiceImpl{}
	released := 0
	s.SetUplinkMutationGuard(func(context.Context, string, string) (func(), error) { return func() { released++ }, nil })
	if err := s.Start(context.Background(), "awg50"); err == nil || released != 1 {
		t.Fatal(err, released)
	}
}
