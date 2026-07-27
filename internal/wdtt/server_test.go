package wdtt

import (
	"slices"
	"testing"
)

func TestBuildServerArgsNoNAT(t *testing.T) {
	got := buildServerArgs(ServerConfig{
		Listen:   "0.0.0.0:56002",
		WgPort:   56001,
		Password: "secret",
	})
	want := []string{
		"-listen", "0.0.0.0:56002",
		"-wg-port", "56001",
		"-password", "secret",
		"-no-nat",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("buildServerArgs() = %v, want %v", got, want)
	}
}
