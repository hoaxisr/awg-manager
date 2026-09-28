package managed

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestReadKernelPrivateKey_ParsesBase64(t *testing.T) {
	stub := func(ctx context.Context, name string, args ...string) (string, error) {
		if name != wgBin || len(args) != 3 || args[0] != "show" || args[1] != "nwg63" || args[2] != "private-key" {
			t.Fatalf("unexpected argv: %s %v", name, args)
		}
		return "yKOZJtI2nQbSWzo8zvmBSjjnSkn89AkLXbekWTgKQ08=\n", nil
	}
	key, err := readKernelPrivateKeyWith(context.Background(), "nwg63", stub)
	if err != nil {
		t.Fatalf("readKernelPrivateKey: %v", err)
	}
	if key != "yKOZJtI2nQbSWzo8zvmBSjjnSkn89AkLXbekWTgKQ08=" {
		t.Errorf("key: got %q", key)
	}
}

func TestReadKernelPrivateKey_BinaryMissing(t *testing.T) {
	stub := func(ctx context.Context, name string, args ...string) (string, error) {
		return "", errors.New("no such file or directory")
	}
	_, err := readKernelPrivateKeyWith(context.Background(), "nwg99", stub)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "wireguard-tools") || !strings.Contains(err.Error(), wgBin) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadKernelPrivateKey_PropagatesToolError(t *testing.T) {
	stub := func(ctx context.Context, name string, args ...string) (string, error) {
		return "", errors.New("Unable to access interface: No such device")
	}
	_, err := readKernelPrivateKeyWith(context.Background(), "nwg99", stub)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if strings.Contains(err.Error(), "wireguard-tools") {
		t.Fatalf("unexpected wireguard-tools framing on non-missing error: %v", err)
	}
}
