package systemtunnel

import (
	"context"
	"errors"
	"testing"
)

func TestSystemUplinkGuardRunsBeforeASCQueriesAndMutation(t *testing.T) {
	want := errors.New("policy owns system interface")
	s := New(nil, nil, nil, nil)
	called := false
	s.SetUplinkGuard(func(_ context.Context, id string) (func(), error) { called = id == "Wireguard6"; return nil, want })
	if err := s.SetASCParams(context.Background(), "Wireguard6", []byte(`{}`)); !errors.Is(err, want) || !called {
		t.Fatal(called, err)
	}
}
