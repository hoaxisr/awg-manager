package probe

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeCurl installs a shell script as the curl binary for the test process.
// The script must start with #!/bin/sh so exec uses its shebang. System
// directories stay on PATH after the fake dir so script helpers such as cat
// remain available inside the fake binary.
func fakeCurl(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "curl")
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	return path
}

// stubPort repoints the HTTPS probe at a local test listener. The restore is
// registered through t.Cleanup.
func stubPort(t *testing.T, port int) {
	t.Helper()
	old := httpsPort
	httpsPort = port
	t.Cleanup(func() { httpsPort = old })
}
