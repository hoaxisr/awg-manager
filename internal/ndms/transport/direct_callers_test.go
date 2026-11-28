package transport

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Запрос к :79 мимо SharedTransport уходит без токена: W «obsoleted
// unauthenticated» на 5.2 и без SCGI-обхода на 5.1.9.
func TestDirectRCICallersUseSharedTransport(t *testing.T) {
	for _, root := range []string{"../..", "../../../cmd"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
				strings.Contains(filepath.ToSlash(path), "ndms/transport/") {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, line := range strings.Split(string(b), "\n") {
				if strings.Contains(line, `"http://127.0.0.1:79`) || strings.Contains(line, `"http://localhost:79`) {
					if !strings.Contains(string(b), "SharedTransport()") {
						t.Errorf("%s: запрос к RCI без transport.SharedTransport()", path)
					}
					break
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
