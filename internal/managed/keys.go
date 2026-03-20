package managed

import (
	"context"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

const awgBin = "/opt/sbin/awg"

// GeneratePrivateKey generates a WireGuard private key via awg genkey.
func GeneratePrivateKey(ctx context.Context) (string, error) {
	result, err := exec.Run(ctx, awgBin, "genkey")
	if err != nil {
		return "", fmt.Errorf("awg genkey: %w", exec.FormatError(result, err))
	}
	key := strings.TrimSpace(result.Stdout)
	if key == "" {
		return "", fmt.Errorf("awg genkey: empty output")
	}
	return key, nil
}

// DerivePublicKey derives a public key from a private key via awg pubkey.
func DerivePublicKey(ctx context.Context, privateKey string) (string, error) {
	result, err := exec.RunWithOptions(ctx, awgBin, []string{"pubkey"},
		exec.Options{Stdin: strings.NewReader(privateKey + "\n")})
	if err != nil {
		return "", fmt.Errorf("awg pubkey: %w", exec.FormatError(result, err))
	}
	key := strings.TrimSpace(result.Stdout)
	if key == "" {
		return "", fmt.Errorf("awg pubkey: empty output")
	}
	return key, nil
}

// GeneratePresharedKey generates a WireGuard preshared key via awg genpsk.
func GeneratePresharedKey(ctx context.Context) (string, error) {
	result, err := exec.Run(ctx, awgBin, "genpsk")
	if err != nil {
		return "", fmt.Errorf("awg genpsk: %w", exec.FormatError(result, err))
	}
	key := strings.TrimSpace(result.Stdout)
	if key == "" {
		return "", fmt.Errorf("awg genpsk: empty output")
	}
	return key, nil
}

// GenerateKeyPair generates a private/public key pair.
func GenerateKeyPair(ctx context.Context) (privateKey, publicKey string, err error) {
	privateKey, err = GeneratePrivateKey(ctx)
	if err != nil {
		return "", "", err
	}
	publicKey, err = DerivePublicKey(ctx, privateKey)
	if err != nil {
		return "", "", err
	}
	return privateKey, publicKey, nil
}
