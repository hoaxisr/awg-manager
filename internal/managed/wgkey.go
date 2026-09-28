package managed

import (
	"context"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// wgBin is wireguard-tools' wg, the only tool that speaks to NDMS-native
// WireguardN/nwgN interfaces (genl family "wireguard"; our bundled
// amneziawg-tools awg only knows family "amneziawg" and fails against them).
const wgBin = "/opt/bin/wg"

// wgRunner is the indirection seam for tests. Production wires
// realWgRunner (which calls internal/sys/exec.Run on wgBin); tests pass
// stubs without forking real binaries.
type wgRunner func(ctx context.Context, name string, args ...string) (string, error)

func realWgRunner(ctx context.Context, name string, args ...string) (string, error) {
	result, err := exec.Run(ctx, name, args...)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, exec.FormatError(result, err))
	}
	return result.Stdout, nil
}

func readKernelPrivateKeyWith(ctx context.Context, kernelName string, run wgRunner) (string, error) {
	if kernelName == "" {
		return "", fmt.Errorf("readKernelPrivateKey: empty kernel name")
	}
	out, err := run(ctx, wgBin, "show", kernelName, "private-key")
	if err == nil {
		return strings.TrimSpace(out), nil
	}
	if isBinaryMissingError(err) {
		return "", fmt.Errorf("wireguard-tools (%s) is required: %w", wgBin, err)
	}
	return "", err
}

func isBinaryMissingError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "file not found")
}
