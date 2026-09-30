package query_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPointReads_OnlyInsideQuery — точечное чтение интерфейса по имени
// (show interface <name>, show rc interface <name>, резолвер system-name)
// допустимо только в internal/ndms/query: там оно защищено Present и
// выселением по «unable to find» (F546). Литерал в любом другом пакете —
// обход шлюза, и NDMS снова пишет E по отсутствующему имени.
func TestPointReads_OnlyInsideQuery(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("корень репозитория: %v", err)
	}
	bad := []string{
		`.ShowInterface(`,
		`.ShowQuery([]string{"interface"`,
		`"/show/interface/"+`, `"/show/interface/" +`,
		`"/show/rc/interface/"+`, `"/show/rc/interface/" +`,
		`"interface", "system-name"`,
	}
	var offenders []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			switch {
			case rel == "vendor", rel == "frontend", rel == "graphify-out", rel == "docs", rel == ".git",
				rel == ".claude", rel == "build",
				rel == filepath.Join("internal", "ndms", "query"):
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			for _, b := range bad {
				if strings.Contains(line, b) {
					offenders = append(offenders, fmt.Sprintf("%s:%d: %s", rel, i+1, b))
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("обход дерева: %v", walkErr)
	}
	for _, o := range offenders {
		t.Errorf("%s — читать по имени только через query.InterfaceStore (Lookup + ShowRaw)", o)
	}
}
