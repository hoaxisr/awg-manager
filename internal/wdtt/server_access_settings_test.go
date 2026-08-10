package wdtt

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

func TestSetServerPolicy_RejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	if _, err := s.SetServerPolicy(context.Background(), DefaultInstanceID, ""); err == nil {
		t.Fatal("expected error for empty policy")
	}
}

func TestSetServerNATMode_NoOpWhenSame(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	if _, err := s.GetConfig(); err != nil {
		t.Fatal(err)
	}

	saved, err := s.SetServerNATMode(context.Background(), DefaultInstanceID, "full")
	if err != nil {
		t.Fatalf("SetServerNATMode: %v", err)
	}
	if saved.NatMode != "full" {
		t.Fatalf("NatMode = %q", saved.NatMode)
	}
}

func TestSetServerPolicy_UnknownPolicy(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	s.SetNDMSPolicyRouting(nil, fakePolicyLister{policies: []ndms.Policy{{Name: "Policy0"}}}, nil)
	if _, err := s.GetConfig(); err != nil {
		t.Fatal(err)
	}

	if _, err := s.SetServerPolicy(context.Background(), DefaultInstanceID, "Policy999"); err == nil {
		t.Fatal("expected error for unknown policy")
	}
}

type fakePolicyLister struct {
	policies []ndms.Policy
}

func (f fakePolicyLister) List(context.Context) ([]ndms.Policy, error) {
	return f.policies, nil
}
