// internal/accesspolicy/marks_test.go
package accesspolicy

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

type fakePolicyMarkSource struct {
	mark string
	err  error
}

func (f *fakePolicyMarkSource) Get(ctx context.Context, name string) (string, error) {
	return f.mark, f.err
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
