package common

import (
	"context"
	"net"
	"testing"
)

func TestDialTURNNoCandidates(t *testing.T) {
	t.Parallel()
	getCreds := func(context.Context, int) (string, string, []string, error) {
		return "u", "p", nil, nil
	}
	peer := &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 1}
	_, err := DialTURN(context.Background(), "", "", false, peer, 0, getCreds)
	if err == nil {
		t.Fatal("expected error on empty candidate list")
	}
}
