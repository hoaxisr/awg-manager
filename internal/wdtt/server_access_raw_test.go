package wdtt

import (
	"context"
	"testing"
)

type fakePolicyMarkGetter struct {
	mark string
	err  error
}

func (f *fakePolicyMarkGetter) GetPolicyMark(context.Context, string) (string, error) {
	return f.mark, f.err
}

func TestApplyRawServerPolicyNoneRemovesMark(t *testing.T) {
	svc := NewService(t.TempDir(), t.TempDir(), "", "")
	svc.SetPolicyMarkGetter(&fakePolicyMarkGetter{mark: "0xffffaaa"})
	cfg := ndmsServerConfig()
	cfg.Policy = "none"
	if err := svc.applyRawServerPolicy(context.Background(), "srv1", cfg); err != nil {
		t.Fatalf("applyRawServerPolicy: %v", err)
	}
}

func TestApplyRawServerPolicyWithoutGetter(t *testing.T) {
	svc := NewService(t.TempDir(), t.TempDir(), "", "")
	cfg := ndmsServerConfig()
	cfg.Policy = "Policy3"
	if err := svc.applyRawServerPolicy(context.Background(), "srv1", cfg); err != nil {
		t.Fatalf("applyRawServerPolicy: %v", err)
	}
}

func TestApplyRawServerPolicyWithGetterNonLinux(t *testing.T) {
	svc := NewService(t.TempDir(), t.TempDir(), "", "")
	svc.SetPolicyMarkGetter(&fakePolicyMarkGetter{mark: "0xffffaaa"})
	cfg := ndmsServerConfig()
	cfg.Policy = "Policy3"
	if err := svc.applyRawServerPolicy(context.Background(), "srv1", cfg); err != nil {
		t.Fatalf("applyRawServerPolicy: %v", err)
	}
}
