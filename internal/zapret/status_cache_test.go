package zapret

import "testing"

// На роутере без Zapret статус спрашивают при каждом заходе на страницу:
// без установок /proc не сканируется.
func TestGetStatusWithoutInstallDoesNotScanProcesses(t *testing.T) {
	oldRoot, oldDirs, oldProcesses := optRoot, initDirs, nfqProcessesHook
	optRoot, initDirs = t.TempDir(), nil
	scans := 0
	nfqProcessesHook = func() []nfqProc {
		scans++
		return nil
	}
	t.Cleanup(func() { optRoot, initDirs, nfqProcessesHook = oldRoot, oldDirs, oldProcesses })

	got := NewService(t.TempDir(), nil).GetStatus()
	if got.Installed || got.ProcessState != StateNotInstalled {
		t.Fatalf("status = %+v, want absent", got)
	}
	if scans != 0 {
		t.Fatalf("process scans without an installation = %d, want 0", scans)
	}
}
