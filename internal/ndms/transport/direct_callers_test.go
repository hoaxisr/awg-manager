package transport

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Запрос к :79 мимо Client уходит без токена (W «obsoleted unauthenticated» на
// 5.2), без SCGI-обхода на 5.1.9 и мимо семафора.
func TestNoDirectRCICallers(t *testing.T) {
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
			for _, url := range []string{`"http://127.0.0.1:79`, `"http://localhost:79`} {
				if strings.Contains(string(b), url) {
					t.Errorf("%s: прямой запрос к RCI, нужен transport.Client", path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
