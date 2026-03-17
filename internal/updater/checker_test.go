package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

// --- compareVersions ---

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"2.3.10", "2.3.11", -1},
		{"2.3.11", "2.3.10", 1},
		{"2.3.11", "2.3.11", 0},
		{"2.4.0", "2.3.99", 1},
		{"1.0.0", "2.0.0", -1},
		{"2.3", "2.3.0", 0},
		{"2.3.0", "2.3", 0},
		{"2.3", "2.3.1", -1},
		{"10.0.0", "9.99.99", 1},
		{"0.0.1", "0.0.2", -1},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_vs_%s", tt.a, tt.b), func(t *testing.T) {
			got := compareVersions(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// --- isTLSError ---

func TestIsTLSError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"certificate error", fmt.Errorf("x509: certificate signed by unknown authority"), true},
		{"tls error", fmt.Errorf("tls: handshake failure"), true},
		{"x509 error", fmt.Errorf("x509: failed to load"), true},
		{"network error", fmt.Errorf("dial tcp: connection refused"), false},
		{"timeout", fmt.Errorf("context deadline exceeded"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTLSError(tt.err)
			if got != tt.want {
				t.Errorf("isTLSError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// --- archSuffix ---

func TestArchSuffix(t *testing.T) {
	// We can only test the current architecture mapping
	got := archSuffix()
	switch runtime.GOARCH {
	case "mipsle":
		if got != "mipsel-3.4" {
			t.Errorf("archSuffix() = %q for mipsle, want mipsel-3.4", got)
		}
	case "mips":
		if got != "mips-3.4" {
			t.Errorf("archSuffix() = %q for mips, want mips-3.4", got)
		}
	case "arm64":
		if got != "aarch64-3.10" {
			t.Errorf("archSuffix() = %q for arm64, want aarch64-3.10", got)
		}
	default:
		// On dev machines (amd64), falls through to runtime.GOARCH
		if got != runtime.GOARCH {
			t.Errorf("archSuffix() = %q, want %q (fallback)", got, runtime.GOARCH)
		}
	}
}

// --- Check with mock HTTP server ---

func newMockGitHubServer(release githubRelease, statusCode int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		json.NewEncoder(w).Encode(release)
	}))
}

func TestCheck_UpdateAvailable(t *testing.T) {
	arch := archSuffix()
	ipkName := fmt.Sprintf("awg-manager_9.9.9_%s-kn.ipk", arch)

	release := githubRelease{
		TagName: "v9.9.9",
		Assets: []githubAsset{
			{Name: ipkName, BrowserDownloadURL: "https://example.com/" + ipkName},
		},
	}

	srv := newMockGitHubServer(release, http.StatusOK)
	defer srv.Close()

	old := githubAPIURL
	githubAPIURL = srv.URL
	defer func() { githubAPIURL = old }()

	info := Check(context.Background(), "2.0.0")

	if !info.Available {
		t.Fatal("expected Available=true")
	}
	if info.LatestVersion != "9.9.9" {
		t.Errorf("LatestVersion = %q, want %q", info.LatestVersion, "9.9.9")
	}
	if info.DownloadURL != "https://example.com/"+ipkName {
		t.Errorf("DownloadURL = %q, want %q", info.DownloadURL, "https://example.com/"+ipkName)
	}
	if info.Error != "" {
		t.Errorf("unexpected error: %s", info.Error)
	}
}

func TestCheck_AlreadyUpToDate(t *testing.T) {
	release := githubRelease{
		TagName: "v2.3.11",
		Assets:  []githubAsset{},
	}

	srv := newMockGitHubServer(release, http.StatusOK)
	defer srv.Close()

	old := githubAPIURL
	githubAPIURL = srv.URL
	defer func() { githubAPIURL = old }()

	info := Check(context.Background(), "2.3.11")

	if info.Available {
		t.Fatal("expected Available=false (same version)")
	}
	if info.Error != "" {
		t.Errorf("unexpected error: %s", info.Error)
	}
}

func TestCheck_NewerThanRelease(t *testing.T) {
	release := githubRelease{
		TagName: "v2.3.10",
		Assets:  []githubAsset{},
	}

	srv := newMockGitHubServer(release, http.StatusOK)
	defer srv.Close()

	old := githubAPIURL
	githubAPIURL = srv.URL
	defer func() { githubAPIURL = old }()

	info := Check(context.Background(), "2.3.11")

	if info.Available {
		t.Fatal("expected Available=false (current is newer)")
	}
}

func TestCheck_NoMatchingAsset(t *testing.T) {
	release := githubRelease{
		TagName: "v9.9.9",
		Assets: []githubAsset{
			{Name: "awg-manager_9.9.9_unknown-arch-kn.ipk", BrowserDownloadURL: "https://example.com/nope.ipk"},
		},
	}

	srv := newMockGitHubServer(release, http.StatusOK)
	defer srv.Close()

	old := githubAPIURL
	githubAPIURL = srv.URL
	defer func() { githubAPIURL = old }()

	info := Check(context.Background(), "2.0.0")

	if info.Available {
		t.Fatal("expected Available=false (no matching asset)")
	}
	if info.Error == "" {
		t.Fatal("expected error about missing architecture")
	}
}

func TestCheck_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	old := githubAPIURL
	githubAPIURL = srv.URL
	defer func() { githubAPIURL = old }()

	info := Check(context.Background(), "2.0.0")

	if info.Available {
		t.Fatal("expected Available=false on API error")
	}
	if info.Error == "" {
		t.Fatal("expected error message")
	}
}
