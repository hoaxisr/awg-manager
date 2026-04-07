package updater

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"
)

// PackageEntry is a single Debian-style package block from a Packages index,
// limited to the fields we care about.
type PackageEntry struct {
	Version  string
	Filename string
}

// parsePackagesGz reads a gzipped Debian-style Packages index from r and
// returns the entry with the highest Version field whose Package name matches
// pkgName. Returns an error if the gzip stream is invalid or no matching
// package is found.
func parsePackagesGz(r io.Reader, pkgName string) (PackageEntry, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return PackageEntry{}, fmt.Errorf("gunzip: %w", err)
	}
	defer gz.Close()

	var best PackageEntry
	var current struct {
		pkg, ver, fn string
	}
	flush := func() {
		if current.pkg == pkgName && current.ver != "" && current.fn != "" {
			if best.Version == "" || compareVersions(current.ver, best.Version) > 0 {
				best = PackageEntry{Version: current.ver, Filename: current.fn}
			}
		}
		current.pkg, current.ver, current.fn = "", "", ""
	}

	scanner := bufio.NewScanner(gz)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		switch {
		case strings.HasPrefix(line, "Package: "):
			current.pkg = line[len("Package: "):]
		case strings.HasPrefix(line, "Version: "):
			current.ver = line[len("Version: "):]
		case strings.HasPrefix(line, "Filename: "):
			current.fn = line[len("Filename: "):]
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		return PackageEntry{}, fmt.Errorf("scan: %w", err)
	}

	if best.Version == "" {
		return PackageEntry{}, fmt.Errorf("package %q not found in index", pkgName)
	}
	return best, nil
}

// compareVersions compares two semver-like version strings ("a.b.c").
// Returns -1 if a < b, 0 if a == b, 1 if a > b. Missing components are 0.
// Non-numeric components are treated as 0.
func compareVersions(a, b string) int {
	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")
	maxLen := len(partsA)
	if len(partsB) > maxLen {
		maxLen = len(partsB)
	}
	for i := 0; i < maxLen; i++ {
		var numA, numB int
		if i < len(partsA) {
			numA, _ = strconv.Atoi(partsA[i])
		}
		if i < len(partsB) {
			numB, _ = strconv.Atoi(partsB[i])
		}
		if numA < numB {
			return -1
		}
		if numA > numB {
			return 1
		}
	}
	return 0
}

// archSuffix returns the entware architecture string for the current platform,
// matching the format used in .ipk filenames (e.g. "aarch64-3.10").
func archSuffix() string {
	switch runtime.GOARCH {
	case "mipsle":
		return "mipsel-3.4"
	case "mips":
		return "mips-3.4"
	case "arm64":
		return "aarch64-3.10"
	default:
		return runtime.GOARCH
	}
}

// archSuffixToRepoDir converts an entware filename arch ("aarch64-3.10") to
// the entware repo directory name ("aarch64-k3.10"). The conversion inserts
// "k" before the kernel-version digit. Mirrors the sed expression in
// scripts/install.sh.
func archSuffixToRepoDir(suffix string) string {
	for i := 0; i < len(suffix)-1; i++ {
		if suffix[i] == '-' && suffix[i+1] >= '0' && suffix[i+1] <= '9' {
			return suffix[:i+1] + "k" + suffix[i+1:]
		}
	}
	return suffix
}
