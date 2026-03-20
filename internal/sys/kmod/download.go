package kmod

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	// ExpectedKmodVersion is the kernel module version expected by this build.
	ExpectedKmodVersion = "1.0.3"

	// versionFile is the filename that stores the on-disk module version.
	versionFile = "amneziawg.version"
)

func writeVersion(version string) error {
	path := filepath.Join(ModulesDir, versionFile)
	return os.WriteFile(path, []byte(version), 0644)
}

func readVersion() string {
	path := filepath.Join(ModulesDir, versionFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
