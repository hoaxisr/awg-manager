package serveringress

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// F835: архив журнала один — предыдущие удаляются, а не копятся на флеше.
func TestArchiveJournalKeepsOnlyLatest(t *testing.T) {
	dir := t.TempDir()
	j := filepath.Join(dir, "server-ingress-transaction.json")
	for i, kind := range []string{"committed", "failed", "committed"} {
		if err := os.WriteFile(j, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := archiveJournal(j, kind, fmt.Sprintf("tx%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	left, _ := filepath.Glob(j + ".*")
	if len(left) != 1 || filepath.Base(left[0]) != "server-ingress-transaction.json.committed.tx2" {
		t.Fatalf("archives left: %v", left)
	}
}
