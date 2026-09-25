package kmod

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	// ExpectedKmodVersion is the kernel module version expected by this build.
	ExpectedKmodVersion = "3.1.20260906"

	// versionFile is the filename that stores the on-disk module version.
	versionFile = "amneziawg.version"
	// modelFile — модель, под которую выбран amneziawg.ko на диске (#953).
	modelFile = "amneziawg.model"
)

// writeModel пишет метку через временный файл и rename: неполная запись не
// может лечь под целевым именем и прочитаться как метка чужой модели. Без
// fsync после сбоя питания метка может оказаться пустой — это «метки нет»,
// модуль грузится как прежде.
func writeModel(model string) error {
	path := filepath.Join(ModulesDir, modelFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(model), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readModel() string {
	data, err := os.ReadFile(filepath.Join(ModulesDir, modelFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func writeVersion(version string) error {
	if err := os.MkdirAll(ModulesDir, 0755); err != nil {
		return err
	}
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
