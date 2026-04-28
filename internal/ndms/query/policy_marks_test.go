// internal/ndms/query/policy_marks_test.go
package query

import (
	"context"
	"errors"
	"testing"
)

func TestPolicyMarkStore_Found(t *testing.T) {
	// /show/ip/policy returns a top-level map of policy-name → policy-object
	// (NOT wrapped in {"policy": {...}}). Verified on hardware:
	//   curl http://localhost:79/rci/show/ip/policy
	//   {"Policy0":{"description":"IoT_VPN","mark":"ffffaaa","table4":4096,...},
	//    "Policy1":{"description":"Only_Letai","mark":"ffffaab","table4":4098,...}}
	fg := NewFakeGetter()
	fg.SetRaw("/show/ip/policy", []byte(`{"Policy0":{"description":"IoT_VPN","mark":"ffffaaa"},"Policy1":{"description":"Only_Letai","mark":"ffffaab"}}`))
	s := NewPolicyMarkStore(fg, NopLogger())

	mark, err := s.Get(context.Background(), "Policy0")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if mark != "0xffffaaa" {
		t.Errorf("want 0xffffaaa, got %q", mark)
	}
}

func TestPolicyMarkStore_NotFound(t *testing.T) {
	fg := NewFakeGetter()
	fg.SetRaw("/show/ip/policy", []byte(`{"Policy0":{"mark":"ffffaaa"}}`))
	s := NewPolicyMarkStore(fg, NopLogger())

	_, err := s.Get(context.Background(), "PolicyMissing")
	if !errors.Is(err, ErrPolicyMarkNotFound) {
		t.Errorf("expected ErrPolicyMarkNotFound, got %v", err)
	}
}

func TestPolicyMarkStore_EmptyMark(t *testing.T) {
	fg := NewFakeGetter()
	fg.SetRaw("/show/ip/policy", []byte(`{"Policy0":{"mark":""}}`))
	s := NewPolicyMarkStore(fg, NopLogger())

	_, err := s.Get(context.Background(), "Policy0")
	if !errors.Is(err, ErrPolicyMarkNotFound) {
		t.Errorf("expected ErrPolicyMarkNotFound for empty mark, got %v", err)
	}
}

func TestPolicyMarkStore_RCIError(t *testing.T) {
	want := errors.New("transport boom")
	fg := NewFakeGetter()
	fg.SetError("/show/ip/policy", want)
	s := NewPolicyMarkStore(fg, NopLogger())

	_, err := s.Get(context.Background(), "Policy0")
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped transport error, got %v", err)
	}
}
