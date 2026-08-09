// internal/accesspolicy/marks_test.go
package accesspolicy

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

type fakePolicyMarkSource struct {
	mark      string
	err       error
	wantIface string
	exits     []query.PolicyDefaultExit
	listErr   error
}

func (f *fakePolicyMarkSource) Get(ctx context.Context, name string) (string, error) {
	return f.mark, f.err
}

func (f *fakePolicyMarkSource) ListByDefaultInterface(_ context.Context, iface string) ([]query.PolicyDefaultExit, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if iface != f.wantIface {
		return nil, nil
	}
	return f.exits, nil
}

func TestServiceImpl_GetPolicyMark_Found(t *testing.T) {
	s := &ServiceImpl{policyMarks: &fakePolicyMarkSource{mark: "0xffffaaa"}}
	mark, err := s.GetPolicyMark(context.Background(), "Policy0")
	if err != nil {
		t.Fatalf("GetPolicyMark: %v", err)
	}
	if mark != "0xffffaaa" {
		t.Errorf("want 0xffffaaa, got %q", mark)
	}
}

func TestServiceImpl_GetPolicyMark_NotFound(t *testing.T) {
	s := &ServiceImpl{policyMarks: &fakePolicyMarkSource{err: query.ErrPolicyMarkNotFound}}
	_, err := s.GetPolicyMark(context.Background(), "Policy0")
	if !errors.Is(err, query.ErrPolicyMarkNotFound) {
		t.Errorf("expected ErrPolicyMarkNotFound, got %v", err)
	}
}

func TestServiceImpl_GetPolicyMark_NilSource(t *testing.T) {
	s := &ServiceImpl{}
	if _, err := s.GetPolicyMark(context.Background(), "Policy0"); err == nil {
		t.Error("expected error when policyMarks nil, got nil")
	}
}

func TestListPolicyExits(t *testing.T) {
	src := &fakePolicyMarkSource{
		wantIface: "OpkgTun3",
		exits:     []query.PolicyDefaultExit{{Name: "Policy1", Mark: "0xffffaab"}},
	}
	s := &ServiceImpl{policyMarks: src}

	got, err := s.ListPolicyExits(context.Background(), "OpkgTun3")
	if err != nil {
		t.Fatalf("ListPolicyExits: %v", err)
	}
	if len(got) != 1 || got[0].Mark != "0xffffaab" {
		t.Errorf("got %+v, want one exit with mark 0xffffaab", got)
	}
}

func TestListPolicyExits_NoSource(t *testing.T) {
	s := &ServiceImpl{}
	if _, err := s.ListPolicyExits(context.Background(), "OpkgTun3"); !errors.Is(err, ErrNoMarkSource) {
		t.Errorf("want ErrNoMarkSource, got %v", err)
	}
}
