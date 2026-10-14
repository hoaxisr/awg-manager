package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssistantFileReadMasksSecrets(t *testing.T) {
	h, root := newSystemToolsForTest(t, "expert")
	path := filepath.Join(root, "app.conf")
	content := "listen=8080\napi_key=very-secret\nurl=https://user:password@example.test/path\nAuthorization: Bearer abc.def\n-----BEGIN PRIVATE KEY-----\nprivate-data\n-----END PRIVATE KEY-----\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	value, err := h.AssistantFileRead(path)
	if err != nil {
		t.Fatal(err)
	}
	result := value.(map[string]any)
	text := result["content"].(string)
	for _, secret := range []string{"very-secret", "user:password", "abc.def", "private-data"} {
		if strings.Contains(text, secret) {
			t.Fatalf("secret %q leaked in %q", secret, text)
		}
	}
	if !strings.Contains(text, "listen=8080") || !strings.Contains(text, "[REDACTED") {
		t.Fatalf("useful content or redaction marker missing: %q", text)
	}
}

func TestAssistantFilesStayInsideSandbox(t *testing.T) {
	h, root := newSystemToolsForTest(t, "expert")
	if _, err := h.AssistantFilesList(root); err != nil {
		t.Fatalf("allowed root rejected: %v", err)
	}
	if _, err := h.AssistantFilesList(filepath.Dir(root)); err == nil {
		t.Fatal("directory outside sandbox must be rejected")
	}
	if _, err := h.AssistantFileRead(filepath.Join(filepath.Dir(root), "outside.conf")); err == nil {
		t.Fatal("file outside sandbox must be rejected")
	}
}
