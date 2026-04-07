package updater

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

// --- archSuffix sanity check (the function lives in repo.go now) ---

func TestArchSuffix(t *testing.T) {
	got := archSuffix()
	switch runtime.GOARCH {
	case "mipsle":
		if got != "mipsel-3.4" {
			t.Errorf("archSuffix() = %q, want mipsel-3.4", got)
		}
	case "mips":
		if got != "mips-3.4" {
			t.Errorf("archSuffix() = %q, want mips-3.4", got)
		}
	case "arm64":
		if got != "aarch64-3.10" {
			t.Errorf("archSuffix() = %q, want aarch64-3.10", got)
		}
	default:
		if got != runtime.GOARCH {
			t.Errorf("archSuffix() = %q, want %q (fallback)", got, runtime.GOARCH)
		}
	}
}

// --- Check with mock HTTP server returning gzipped Packages ---

// newMockEntwareServer returns an httptest server that serves gzipped Packages
// content for any /<arch>/Packages.gz path. statusCode is the response code
// (use 200 for success cases). packagesContent is the plain (un-gzipped) text
// of the index — gzipBytes is applied here to match the real server.
func newMockEntwareServer(t *testing.T, packagesContent string, statusCode int) *httptest.Server {
	gzData := gzipBytes(t, packagesContent)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(statusCode)
		if statusCode == http.StatusOK {
			w.Write(gzData)
		}
	}))
}

// withMockRepo points entwareRepoURL at srv.URL for the duration of the test.
func withMockRepo(t *testing.T, srv *httptest.Server) {
	t.Helper()
	old := entwareRepoURL
	entwareRepoURL = srv.URL
	t.Cleanup(func() { entwareRepoURL = old })
}

func TestCheck_UpdateAvailable(t *testing.T) {
	arch := archSuffix()
	ipkName := "awg-manager_9.9.9_" + arch + "-kn.ipk"
	body := "Package: awg-manager\nVersion: 9.9.9\nFilename: " + ipkName + "\n"

	srv := newMockEntwareServer(t, body, http.StatusOK)
	defer srv.Close()
	withMockRepo(t, srv)

	info := Check(context.Background(), "2.0.0")

	if !info.Available {
		t.Fatal("expected Available=true")
	}
	if info.LatestVersion != "9.9.9" {
		t.Errorf("LatestVersion = %q, want 9.9.9", info.LatestVersion)
	}
	wantURL := srv.URL + "/" + archSuffixToRepoDir(arch) + "/" + ipkName
	if info.DownloadURL != wantURL {
		t.Errorf("DownloadURL = %q, want %q", info.DownloadURL, wantURL)
	}
	if info.Error != "" {
		t.Errorf("unexpected error: %s", info.Error)
	}
}

func TestCheck_PicksHighestOfMultipleBlocks(t *testing.T) {
	arch := archSuffix()
	body := `Package: awg-manager
Version: 2.6.5
Filename: awg-manager_2.6.5_` + arch + `-kn.ipk

Package: awg-manager
Version: 2.7.10
Filename: awg-manager_2.7.10_` + arch + `-kn.ipk

Package: awg-manager
Version: 2.7.3
Filename: awg-manager_2.7.3_` + arch + `-kn.ipk
`
	srv := newMockEntwareServer(t, body, http.StatusOK)
	defer srv.Close()
	withMockRepo(t, srv)

	info := Check(context.Background(), "2.0.0")
	if !info.Available {
		t.Fatal("expected Available=true")
	}
	if info.LatestVersion != "2.7.10" {
		t.Errorf("LatestVersion = %q, want 2.7.10", info.LatestVersion)
	}
}

func TestCheck_AlreadyUpToDate(t *testing.T) {
	arch := archSuffix()
	body := "Package: awg-manager\nVersion: 2.3.11\nFilename: awg-manager_2.3.11_" + arch + "-kn.ipk\n"

	srv := newMockEntwareServer(t, body, http.StatusOK)
	defer srv.Close()
	withMockRepo(t, srv)

	info := Check(context.Background(), "2.3.11")
	if info.Available {
		t.Fatal("expected Available=false (same version)")
	}
	if info.Error != "" {
		t.Errorf("unexpected error: %s", info.Error)
	}
}

func TestCheck_NewerThanRepo(t *testing.T) {
	arch := archSuffix()
	body := "Package: awg-manager\nVersion: 2.3.10\nFilename: awg-manager_2.3.10_" + arch + "-kn.ipk\n"

	srv := newMockEntwareServer(t, body, http.StatusOK)
	defer srv.Close()
	withMockRepo(t, srv)

	info := Check(context.Background(), "2.3.11")
	if info.Available {
		t.Fatal("expected Available=false (current is newer)")
	}
}

func TestCheck_PackageMissing(t *testing.T) {
	body := "Package: curl\nVersion: 8.0.1\nFilename: curl_8.0.1.ipk\n"

	srv := newMockEntwareServer(t, body, http.StatusOK)
	defer srv.Close()
	withMockRepo(t, srv)

	info := Check(context.Background(), "2.0.0")
	if info.Available {
		t.Fatal("expected Available=false when package not found in index")
	}
	if info.Error == "" {
		t.Fatal("expected error mentioning missing package")
	}
}

func TestCheck_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()
	withMockRepo(t, srv)

	info := Check(context.Background(), "2.0.0")
	if info.Available {
		t.Fatal("expected Available=false on HTTP 500")
	}
	if info.Error == "" {
		t.Fatal("expected error message")
	}
}
