package bypassset

import (
	"context"
	"fmt"
	"strings"

	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
)

const (
	// CloudSetName is the ipset name used for Keenetic Cloud / KeenDNS relays.
	CloudSetName = "AWGM-CLOUD"

	// CloudSetMaxElem is the maximum number of entries for the cloud set.
	CloudSetMaxElem = 65536
)

// CreateCloudSet creates the AWGM-CLOUD ipset (hash:net) if it does not already exist.
func CreateCloudSet(ctx context.Context) error {
	bin, err := ipsetBin()
	if err != nil {
		return err
	}
	res, err := runIpsetCtl(ctx, bin,
		"create", CloudSetName, "hash:net",
		"maxelem", fmt.Sprintf("%d", CloudSetMaxElem),
		"family", "inet",
		"-exist",
	)
	if err != nil {
		combined := ""
		if res != nil {
			combined = res.Stdout + res.Stderr
		}
		if strings.Contains(combined, "already exists") {
			return nil
		}
		return sysexec.FormatError(res, fmt.Errorf("ipset create %s: %w", CloudSetName, err))
	}
	return nil
}

// DestroyCloudSet removes the AWGM-CLOUD ipset. Idempotent.
func DestroyCloudSet(ctx context.Context) error {
	return DestroyNamedSet(ctx, CloudSetName)
}

// CloudSetExists reports whether the AWGM-CLOUD ipset exists in the kernel.
func CloudSetExists(ctx context.Context) bool {
	bin, err := ipsetBin()
	if err != nil {
		return false
	}
	res, err := runIpsetCtl(ctx, bin, "list", "-name")
	if err != nil || res == nil {
		return false
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		if strings.TrimSpace(line) == CloudSetName {
			return true
		}
	}
	return false
}

// AddCloudEntry adds an IP or CIDR to the AWGM-CLOUD ipset in O(1) time with -exist.
func AddCloudEntry(ctx context.Context, entry string) error {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return nil
	}
	bin, err := ipsetBin()
	if err != nil {
		return err
	}
	res, err := runIpsetCtl(ctx, bin, "add", CloudSetName, entry, "-exist")
	if err != nil {
		return sysexec.FormatError(res, fmt.Errorf("ipset add %s %s: %w", CloudSetName, entry, err))
	}
	return nil
}

// PopulateCloudSet adds multiple CIDRs or IPs to the AWGM-CLOUD ipset.
func PopulateCloudSet(ctx context.Context, entries []string) error {
	if len(entries) == 0 {
		return nil
	}
	if err := CreateCloudSet(ctx); err != nil {
		return err
	}
	for _, e := range entries {
		_ = AddCloudEntry(ctx, e)
	}
	return nil
}
