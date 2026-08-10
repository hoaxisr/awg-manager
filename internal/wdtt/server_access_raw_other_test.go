//go:build !linux

package wdtt

import (
	"context"
	"testing"
)

// applyRawServerPolicyMark на non-linux — stub (server_raw_policy_other.go),
// вызывает applyRawServerPolicy без обращения к iptables.
func TestApplyRawServerPolicyWithGetterNonLinux(t *testing.T) {
	svc := NewService(t.TempDir(), t.TempDir(), "", "")
	svc.SetPolicyMarkGetter(&fakePolicyMarkGetter{mark: "0xffffaaa"})
	cfg := ndmsServerConfig()
	cfg.Policy = "Policy3"
	if _, err := svc.applyRawServerPolicy(context.Background(), "srv1", cfg); err != nil {
		t.Fatalf("applyRawServerPolicy: %v", err)
	}
}
