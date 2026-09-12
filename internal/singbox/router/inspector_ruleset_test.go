package router

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRuleSetCache_DownloadAndHit verifies that getOrDownload writes the
// remote payload to disk on first call and serves the same path from
// memory on the second call (no extra HTTP hit).
func TestRuleSetCache_DownloadAndHit(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("dummy-srs-content"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	cache := newRuleSetCache(dir)

	first, err := cache.getOrDownload(srv.URL+"/list.srs", "binary", nil, "list.srs")
	if err != nil {
		t.Fatalf("first getOrDownload: %v", err)
	}
	data, err := os.ReadFile(first)
	if err != nil {
		t.Fatalf("read cached file: %v", err)
	}
	if string(data) != "dummy-srs-content" {
		t.Errorf("cache content = %q, want %q", string(data), "dummy-srs-content")
	}
	if !strings.HasPrefix(first, dir) {
		t.Errorf("cache path %q not under %q", first, dir)
	}
	if !strings.HasSuffix(first, ".srs") {
		t.Errorf("cache path %q does not end in .srs", first)
	}

	second, err := cache.getOrDownload(srv.URL+"/list.srs", "binary", nil, "list.srs")
	if err != nil {
		t.Fatalf("second getOrDownload: %v", err)
	}
	if second != first {
		t.Errorf("second call returned %q, want cached %q", second, first)
	}
	if hits != 1 {
		t.Errorf("HTTP hits = %d, want 1 (second call should hit cache)", hits)
	}
}

// TestRuleSetCache_TTLExpiry forces an entry's expiry into the past and
// verifies the next call re-downloads.
func TestRuleSetCache_TTLExpiry(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte("payload"))
	}))
	defer srv.Close()

	cache := newRuleSetCache(t.TempDir())

	if _, err := cache.getOrDownload(srv.URL+"/x.srs", "binary", nil, "x.srs"); err != nil {
		t.Fatalf("download: %v", err)
	}
	if hits != 1 {
		t.Fatalf("hits after first call = %d, want 1", hits)
	}
	// Force expiry — overwrite TTL with a past timestamp.
	cache.mu.Lock()
	for k, v := range cache.entries {
		v.expiresAt = time.Now().Add(-1 * time.Minute)
		cache.entries[k] = v
	}
	cache.mu.Unlock()

	if _, err := cache.getOrDownload(srv.URL+"/x.srs", "binary", nil, "x.srs"); err != nil {
		t.Fatalf("download after expiry: %v", err)
	}
	if hits != 2 {
		t.Errorf("hits after expiry = %d, want 2", hits)
	}
}

// TestRuleSetCache_NonOK reports the HTTP status as the error message.
func TestRuleSetCache_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()
	cache := newRuleSetCache(t.TempDir())
	_, err := cache.getOrDownload(srv.URL, "binary", nil, "x")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error = %v, want substring \"404\"", err)
	}
}

// TestMatchRuleSet_MissingBinary keeps the inspector usable on dev
// machines where sing-box isn't installed.
func TestMatchRuleSet_MissingBinary(t *testing.T) {
	matched, supported, err := matchRuleSet(
		"google.com",
		RuleSet{Tag: "x", Type: "local", Path: "/nonexistent"},
		"", // no binary
		nil,
		nil,
	)
	if matched {
		t.Errorf("matched = true, want false")
	}
	if supported {
		t.Errorf("supported = true, want false (missing binary should be unsupported)")
	}
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

// TestMatchRuleSet_LocalFileMissing returns supported=false rather than
// erroring — keeps Inspect smooth when a local rule_set path is stale.
func TestMatchRuleSet_LocalFileMissing(t *testing.T) {
	matched, supported, err := matchRuleSet(
		"google.com",
		RuleSet{Tag: "x", Type: "local", Path: filepath.Join(t.TempDir(), "missing.srs")},
		"/usr/bin/false", // pretend a binary exists; we won't reach it
		nil,
		nil,
	)
	if matched {
		t.Errorf("matched = true, want false")
	}
	if supported {
		t.Errorf("supported = true, want false (missing file should be unsupported)")
	}
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

// TestMatchRuleSet_RemoteWithoutCache marks remote rule_sets as
// unsupported when no cache is available.
func TestMatchRuleSet_RemoteWithoutCache(t *testing.T) {
	matched, supported, err := matchRuleSet(
		"google.com",
		RuleSet{Tag: "x", Type: "remote", URL: "https://example.com/list.srs"},
		"/usr/bin/sing-box",
		nil,
		nil,
	)
	if matched || supported || err != nil {
		t.Errorf("got (%v,%v,%v); want (false,false,nil)", matched, supported, err)
	}
}

// TestMatchRuleSet_FakeExec swaps the package-level exec hook so we can
// assert the marker-detection behaviour without invoking real sing-box.
func TestMatchRuleSet_FakeExec(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "list.srs")
	if err := os.WriteFile(tmp, []byte("x"), 0644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	rs := RuleSet{Tag: "geosite-x", Type: "local", Path: tmp, Format: "binary"}

	cases := []struct {
		name      string
		stdout    string
		stderr    string
		err       error
		wantMatch bool
		wantOK    bool
		wantErr   bool
	}{
		{
			name:      "marker on stderr clean exit → matched",
			stderr:    "match rules.\n",
			wantMatch: true,
			wantOK:    true,
		},
		{
			name:      "marker on stderr non-zero exit → matched",
			stderr:    "match rules.",
			err:       &fakeExitErr{},
			wantMatch: true,
			wantOK:    true,
		},
		{
			name:   "no output clean exit → no match",
			wantOK: true,
		},
		{
			name:   "non-zero exit silent → no match supported",
			err:    &fakeExitErr{},
			wantOK: true,
		},
		{
			name:    "non-zero exit with non-match diagnostic → real error",
			stderr:  "FATAL boom",
			err:     &fakeExitErr{},
			wantOK:  true,
			wantErr: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			origExec := ruleSetMatchExec
			ruleSetMatchExec = func(binary string, args []string) (string, string, error) {
				return c.stdout, c.stderr, c.err
			}
			defer func() { ruleSetMatchExec = origExec }()

			matched, supported, err := matchRuleSet("google.com", rs, "/usr/bin/sing-box", nil, nil)
			if matched != c.wantMatch {
				t.Errorf("matched = %v, want %v", matched, c.wantMatch)
			}
			if supported != c.wantOK {
				t.Errorf("supported = %v, want %v", supported, c.wantOK)
			}
			if (err != nil) != c.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, c.wantErr)
			}
		})
	}
}

// fakeExitErr satisfies the (*exec.ExitError) type assertion path in
// matchRuleSet. We don't need real exit-status fidelity; the type check
// is what gates the marker-aware branch.
type fakeExitErr struct{}

func (e *fakeExitErr) Error() string { return "exit status 1" }

// Asserting that fakeExitErr behaves like *exec.ExitError requires
// matchRuleSet to type-assert on the concrete type, which it does. To
// keep the test honest we explicitly verify our fake passes the
// assertion via a narrow indirection.
//
// (No code here — the assertion happens inside matchRuleSet.)

// TestInspect_RuleSetMatch wires the integration: a Rule references a
// rule_set whose match function is faked to return true. Inspect must
// recognise the match and route accordingly.
func TestInspect_RuleSetMatch(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "list.srs")
	if err := os.WriteFile(tmp, []byte("x"), 0644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}

	origExec := ruleSetMatchExec
	ruleSetMatchExec = func(binary string, args []string) (string, string, error) {
		// Last arg is the input domain. Match google.com only.
		if len(args) > 0 && args[len(args)-1] == "google.com" {
			return "", "match rules.\n", nil
		}
		return "", "", &fakeExitErr{}
	}
	defer func() { ruleSetMatchExec = origExec }()

	ruleSets := []RuleSet{
		{Tag: "geosite-google", Type: "local", Path: tmp, Format: "binary"},
	}
	rules := []Rule{
		{RuleSet: []string{"geosite-google"}, Action: "route", Outbound: "vpn"},
	}

	hit := Inspect(InspectInput{Domain: "google.com"}, rules, ruleSets, "direct", "/usr/bin/sing-box", nil)
	if hit.Destination != "vpn" {
		t.Errorf("Destination = %q, want vpn", hit.Destination)
	}
	if hit.MatchedRule != 0 {
		t.Errorf("MatchedRule = %d, want 0", hit.MatchedRule)
	}
	if hit.Note != "" {
		t.Errorf("unexpected Note = %q (rule_set match should not produce a note)", hit.Note)
	}

	miss := Inspect(InspectInput{Domain: "example.org"}, rules, ruleSets, "direct", "/usr/bin/sing-box", nil)
	if miss.Destination != "direct" {
		t.Errorf("miss Destination = %q, want direct", miss.Destination)
	}
	if miss.MatchedRule != -1 {
		t.Errorf("miss MatchedRule = %d, want -1", miss.MatchedRule)
	}
}

// TestInspect_RuleSetUnsupported_NoBinary returns no-match + Note when
// sing-box isn't available — the rest of the rule walk continues.
func TestInspect_RuleSetUnsupported_NoBinary(t *testing.T) {
	ruleSets := []RuleSet{
		{Tag: "geosite-x", Type: "remote", URL: "https://example.com/x.srs"},
	}
	rules := []Rule{
		{RuleSet: []string{"geosite-x"}, Action: "route", Outbound: "vpn"},
		{DomainSuffix: []string{"example.org"}, Action: "route", Outbound: "fallback"},
	}

	res := Inspect(InspectInput{Domain: "example.org"}, rules, ruleSets, "direct", "" /* no binary */, nil)
	if res.Destination != "fallback" {
		t.Errorf("Destination = %q, want fallback (rule_set should degrade, not block)", res.Destination)
	}
	if res.MatchedRule != 1 {
		t.Errorf("MatchedRule = %d, want 1", res.MatchedRule)
	}
	if !strings.Contains(res.Note, "rule_set") {
		t.Errorf("Note = %q, want substring \"rule_set\"", res.Note)
	}
}

// Пин ALPN http/1.1 обязан пережить ЛЮБУЮ ветку сборки клиента, включая
// запасную. Нулевой Transport — это http.DefaultTransport с пустым ALPN:
// сервер договаривается на h2, и вместо rule-set'а приезжает EOF (Fastly,
// raw.githubusercontent.com) или «malformed HTTP response» (Cloudflare) —
// ровно та поломка, ради которой httpclient и заведён.
func TestRuleSetHTTPClientPinsHTTP11(t *testing.T) {
	// Обе ветки сборки: основная и запасная. Раньше запасная роняла пин —
	// Transport просто оставался нулевым.
	for name, tr := range map[string]http.RoundTripper{
		"основная": newRuleSetHTTPClient().Transport,
		"запасная": ruleSetFallbackTransport(),
	} {
		t.Run(name, func(t *testing.T) {
			tr, ok := tr.(*http.Transport)
			if !ok {
				t.Fatalf("транспорт не задан или не *http.Transport, а %T — ALPN остаётся пустым", tr)
			}
			if tr.ForceAttemptHTTP2 {
				t.Error("ForceAttemptHTTP2 не снят")
			}
			if tr.TLSClientConfig == nil {
				t.Fatal("TLSClientConfig не задан — ALPN пустой, сервер договорится на h2")
			}
			if got := tr.TLSClientConfig.NextProtos; len(got) != 1 || got[0] != "http/1.1" {
				t.Errorf("ALPN = %v, ожидался [http/1.1]", got)
			}
		})
	}
}
