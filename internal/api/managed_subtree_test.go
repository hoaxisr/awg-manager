package api

import (
	"net/url"
	"strings"
	"testing"
)

// TestSplitPath_PreservesPubkeySlashes is the regression for C1: when the
// frontend percent-encodes a base64 pubkey containing '/' (≈45% of real
// WireGuard pubkeys), the dispatcher must NOT split the path through the
// decoded slash. We pass r.URL.EscapedPath() into splitPath so the '/' stays
// as %2F during the split and is decoded only as part of the segment value.
func TestSplitPath_PreservesPubkeySlashes(t *testing.T) {
	// 44-char base64 ending in '=', containing '/' and '+'.
	pk := "AB/CD+EF" + strings.Repeat("a", 35) + "="
	if len(pk) != 44 {
		t.Fatalf("test setup: pk len = %d, want 44", len(pk))
	}
	if !isValidWGKey(pk) {
		t.Fatalf("test setup: pubkey is not valid base64 — adjust")
	}

	escaped := "/api/managed-servers/Wireguard5/peers/" + url.PathEscape(pk)
	parts, ok := splitPath(escaped, "/api/managed-servers/")
	if !ok {
		t.Fatal("splitPath rejected legal path")
	}
	if len(parts) != 3 {
		t.Fatalf("len(parts)=%d, want 3: %#v", len(parts), parts)
	}
	if parts[0] != "Wireguard5" {
		t.Errorf("parts[0]=%q, want %q", parts[0], "Wireguard5")
	}
	if parts[1] != "peers" {
		t.Errorf("parts[1]=%q, want %q", parts[1], "peers")
	}
	if parts[2] != pk {
		t.Errorf("parts[2]=%q, want %q (pubkey was truncated by '/')", parts[2], pk)
	}
}

// TestSplitPath_PreservesPubkeySlashes_LeafRoute exercises the same fix on
// the 4-segment leaf form (.../{pubkey}/conf), proving that pubkey
// extraction at parts[2] survives a slash-bearing pubkey AND the trailing
// "conf" leaf still appears at parts[3].
func TestSplitPath_PreservesPubkeySlashes_LeafRoute(t *testing.T) {
	pk := "AB/CD+EF" + strings.Repeat("a", 35) + "="
	escaped := "/api/managed-servers/Wireguard5/peers/" + url.PathEscape(pk) + "/conf"
	parts, ok := splitPath(escaped, "/api/managed-servers/")
	if !ok {
		t.Fatal("splitPath rejected legal path")
	}
	if len(parts) != 4 {
		t.Fatalf("len(parts)=%d, want 4: %#v", len(parts), parts)
	}
	if parts[2] != pk {
		t.Errorf("parts[2]=%q, want %q", parts[2], pk)
	}
	if parts[3] != "conf" {
		t.Errorf("parts[3]=%q, want %q", parts[3], "conf")
	}
}

// TestSplitPath_RejectsTraversal proves the path-traversal guard rejects
// both literal and percent-encoded ".." segments, but still allows
// non-traversal strings that happen to contain a "..".
func TestSplitPath_RejectsTraversal(t *testing.T) {
	bad := []string{
		"/api/managed-servers/..",
		"/api/managed-servers/Wireguard5/%2E%2E", // encoded ..
		"/api/managed-servers/.",
		"/api/managed-servers/Wireguard5/%2E", // encoded .
	}
	for _, c := range bad {
		if _, ok := splitPath(c, "/api/managed-servers/"); ok {
			t.Errorf("splitPath accepted traversal: %s", c)
		}
	}
}

// TestSplitPath_AllowsDotsInsideSegments confirms that the tightened
// "exact equality" traversal check no longer rejects benign segments that
// merely contain a "..". Pubkeys are base64 (no dots) and ids are
// ^Wireguard\d+$ (no dots), so this is mostly future-proofing, but it
// removes the code smell of a substring-based ban.
func TestSplitPath_AllowsDotsInsideSegments(t *testing.T) {
	cases := []string{
		"/api/managed-servers/foo..bar",
		"/api/managed-servers/Wireguard5/sub..thing",
	}
	for _, c := range cases {
		if _, ok := splitPath(c, "/api/managed-servers/"); !ok {
			t.Errorf("splitPath wrongly rejected non-traversal segment: %s", c)
		}
	}
}

// TestSplitPath_EmptyRoot confirms the prefix-only path returns the
// "fall-through to Collection" sentinel: empty parts, ok=true. Only the
// trailing-slash form is actually routed here by the server mux —
// /api/managed-servers (no slash) goes to Collection directly.
func TestSplitPath_EmptyRoot(t *testing.T) {
	parts, ok := splitPath("/api/managed-servers/", "/api/managed-servers/")
	if !ok {
		t.Errorf("splitPath rejected, want ok=true")
	}
	if len(parts) != 0 {
		t.Errorf("parts=%#v, want empty", parts)
	}
}

// TestSplitPath_RejectsConsecutiveSlashes is a defense-in-depth check —
// "//" between segments produces an empty raw segment which we reject so
// callers don't have to handle len(parts) anomalies.
func TestSplitPath_RejectsConsecutiveSlashes(t *testing.T) {
	if _, ok := splitPath("/api/managed-servers/Wireguard5//peers", "/api/managed-servers/"); ok {
		t.Errorf("splitPath accepted consecutive slashes")
	}
}
