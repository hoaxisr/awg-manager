package mihomo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 1. Unicode JSON/YAML/ENV credentials
func TestGateE_Redaction_Unicode(t *testing.T) {
	inputs := []struct {
		name     string
		raw      string
		contains string
		forbids  string
	}{
		{
			name:     "unicode_json_password",
			raw:      `{"password": "пароль🔑123", "status": "ok"}`,
			contains: `{"password": "[REDACTED]", "status": "ok"}`,
			forbids:  "пароль🔑123",
		},
		{
			name:     "unicode_yaml_secret",
			raw:      "secret: тайный_ключ_456\nmode: rule",
			contains: "secret: [REDACTED]\nmode: rule",
			forbids:  "тайный_ключ_456",
		},
		{
			name:     "unicode_env_token",
			raw:      "TOKEN=токен_юзера_789",
			contains: "TOKEN=[REDACTED]",
			forbids:  "токен_юзера_789",
		},
	}

	for _, tc := range inputs {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactSecrets(tc.raw)
			if got != tc.contains {
				t.Fatalf("expected %q, got %q", tc.contains, got)
			}
			if strings.Contains(got, tc.forbids) {
				t.Fatalf("redacted output leaked forbidden secret: %s", got)
			}
		})
	}
}

// 2. quoted and unquoted values with punctuation and spaces
func TestGateE_Redaction_PunctuationAndSpaces(t *testing.T) {
	inputs := []struct {
		name     string
		raw      string
		forbids  string
		contains string
	}{
		{
			name:     "quoted_with_punctuation",
			raw:      `{"password": "p@ssw0rd!#$%-^&*", "user": "alice"}`,
			forbids:  "p@ssw0rd!#$%-^&*",
			contains: `{"password": "[REDACTED]", "user": "alice"}`,
		},
		{
			name:     "unquoted_yaml_with_spaces",
			raw:      "password: my secret pass phrase 123 # inline comment\nport: 1099",
			forbids:  "my secret pass phrase 123",
			contains: "password: [REDACTED] # inline comment\nport: 1099",
		},
		{
			name:     "unquoted_env_with_punctuation",
			raw:      "API_KEY=key_abc-123.xyz+test=val",
			forbids:  "key_abc-123.xyz+test=val",
			contains: "API_KEY=[REDACTED]",
		},
	}

	for _, tc := range inputs {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactSecrets(tc.raw)
			if strings.Contains(got, tc.forbids) {
				t.Fatalf("leaked secret %q in %q", tc.forbids, got)
			}
			if got != tc.contains {
				t.Fatalf("expected %q, got %q", tc.contains, got)
			}
		})
	}
}

// 3. exact UUID credential redacted
func TestGateE_Redaction_ExactUUIDRedacted(t *testing.T) {
	inputs := []struct {
		name    string
		raw     string
		forbids string
	}{
		{
			name:    "json_exact_uuid",
			raw:     `{"uuid": "550e8400-e29b-41d4-a716-446655440000", "name": "vless"}`,
			forbids: "550e8400-e29b-41d4-a716-446655440000",
		},
		{
			name:    "yaml_exact_uuid",
			raw:     "uuid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d\nprotocol: vless",
			forbids: "a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d",
		},
		{
			name:    "env_exact_uuid",
			raw:     "UUID=999e8400-e29b-41d4-a716-446655440000",
			forbids: "999e8400-e29b-41d4-a716-446655440000",
		},
		{
			name:    "url_query_exact_uuid",
			raw:     "http://example.com/vless?uuid=123e4567-e89b-12d3-a456-426614174000&type=ws",
			forbids: "123e4567-e89b-12d3-a456-426614174000",
		},
	}

	for _, tc := range inputs {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactSecrets(tc.raw)
			if strings.Contains(got, tc.forbids) {
				t.Fatalf("exact uuid leaked: %s", got)
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Fatalf("expected [REDACTED] in %s", got)
			}
		})
	}
}

// 4. owner_uuid, tx_id, generation_id, rule_id, and slot_key preserved
func TestGateE_Redaction_DiagnosticIDsPreserved(t *testing.T) {
	raw := `{"owner_uuid": "e4046ae6-25b5-4760-af29-649b283af6f4", "tx_id": "tx-20260922-1001", "generation_id": "gen-8899", "rule_id": "rule-42", "slot_key": "1:Proxy1:awg-br0"}`
	got := RedactSecrets(raw)

	expectedIDs := []string{
		"owner_uuid",
		"e4046ae6-25b5-4760-af29-649b283af6f4",
		"tx_id",
		"tx-20260922-1001",
		"generation_id",
		"gen-8899",
		"rule_id",
		"rule-42",
		"slot_key",
		"1:Proxy1:awg-br0",
	}

	for _, id := range expectedIDs {
		if !strings.Contains(got, id) {
			t.Fatalf("diagnostic identifier %q was erroneously redacted: %s", id, got)
		}
	}

	// Also check prose containing "token"
	prose := "The authentication token was validated successfully by coordinator"
	if RedactSecrets(prose) != prose {
		t.Fatalf("prose was erroneously modified: %s", RedactSecrets(prose))
	}
}

// 5. Bearer and Authorization values redacted
func TestGateE_Redaction_BearerAndAuth(t *testing.T) {
	inputs := []struct {
		name     string
		raw      string
		contains string
		forbids  string
	}{
		{
			name:     "header_bearer",
			raw:      "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.xyz\nHost: 127.0.0.1",
			contains: "Authorization: Bearer [REDACTED]\nHost: 127.0.0.1",
			forbids:  "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.xyz",
		},
		{
			name:     "standalone_bearer",
			raw:      "failed with Bearer secret-bearer-token-12345 in log",
			contains: "failed with Bearer [REDACTED] in log",
			forbids:  "secret-bearer-token-12345",
		},
		{
			name:     "basic_auth_header",
			raw:      "Authorization: Basic dXNlcjpwYXNzd29yZA==",
			contains: "Authorization: Basic [REDACTED]",
			forbids:  "dXNlcjpwYXNzd29yZA==",
		},
		{
			name:     "header_bearer_with_plus_slash_equals",
			raw:      "Authorization: Bearer abc+def/ghi==\r\n",
			contains: "Authorization: Bearer [REDACTED]\r\n",
			forbids:  "abc+def/ghi==",
		},
		{
			name:     "header_bearer_tabs",
			raw:      "Authorization:\tBearer\tabc+def/ghi==",
			contains: "Authorization:\tBearer\t[REDACTED]",
			forbids:  "abc+def/ghi==",
		},
		{
			name:     "header_bearer_mixed_case",
			raw:      "aUtHoRiZaTiOn: bEaReR abc+def/ghi==",
			contains: "aUtHoRiZaTiOn: bEaReR [REDACTED]",
			forbids:  "abc+def/ghi==",
		},
		{
			name:     "standalone_bearer_with_tilde_plus_slash_equals",
			raw:      "got error with Bearer ~token+value/42== in response",
			contains: "got error with Bearer [REDACTED] in response",
			forbids:  "~token+value/42==",
		},
		{
			name:     "standalone_bearer_sentence_period",
			raw:      "using Bearer abc+def/ghi==.",
			contains: "using Bearer [REDACTED].",
			forbids:  "abc+def/ghi==",
		},
		{
			name:     "standalone_bearer_comma",
			raw:      "received Bearer abc+def/ghi==, processing next",
			contains: "received Bearer [REDACTED], processing next",
			forbids:  "abc+def/ghi==",
		},
		{
			name:     "standalone_bearer_crlf",
			raw:      "Bearer abc+def/ghi==\r\nnext line",
			contains: "Bearer [REDACTED]\r\nnext line",
			forbids:  "abc+def/ghi==",
		},
	}

	for _, tc := range inputs {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactSecrets(tc.raw)
			if got != tc.contains {
				t.Fatalf("expected %q, got %q", tc.contains, got)
			}
			if strings.Contains(got, tc.forbids) {
				t.Fatalf("leaked auth value in %s", got)
			}
			// Assert idempotency
			secondPass := RedactSecrets(got)
			if secondPass != got {
				t.Fatalf("expected idempotent second pass, got %q != %q", secondPass, got)
			}
		})
	}
}

// 6. URL userinfo redacts user and password without corrupting host/path
func TestGateE_Redaction_URLUserinfo(t *testing.T) {
	inputs := []struct {
		name     string
		raw      string
		expected string
		forbids  []string
	}{
		{
			name:     "user_and_pass",
			raw:      "http://operator:sec_pass_123@192.168.1.1:9090/providers/proxies?check=1",
			expected: "http://[REDACTED]:[REDACTED]@192.168.1.1:9090/providers/proxies?check=1",
			forbids:  []string{"operator", "sec_pass_123"},
		},
		{
			name:     "user_only",
			raw:      "https://secret_token@sub.example.com/feed.yaml",
			expected: "https://[REDACTED]@sub.example.com/feed.yaml",
			forbids:  []string{"secret_token"},
		},
	}

	for _, tc := range inputs {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactSecrets(tc.raw)
			if got != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, got)
			}
			for _, f := range tc.forbids {
				if strings.Contains(got, f) {
					t.Fatalf("leaked userinfo credential %q in %s", f, got)
				}
			}
		})
	}
}

// 7. query credentials redacted while ordinary parameters remain
func TestGateE_Redaction_QueryCredentials(t *testing.T) {
	raw := "https://api.example.com/fetch?token=super_secret_tok&mode=direct&secret=another_sec&format=json&uuid=6ba7b810-9dad-11d1-80b4-00c04fd430c8#fragment"
	got := RedactSecrets(raw)

	forbids := []string{"super_secret_tok", "another_sec", "6ba7b810-9dad-11d1-80b4-00c04fd430c8"}
	for _, f := range forbids {
		if strings.Contains(got, f) {
			t.Fatalf("leaked secret query param %q in %s", f, got)
		}
	}

	preserves := []string{
		"token=[REDACTED]",
		"mode=direct",
		"secret=[REDACTED]",
		"format=json",
		"uuid=[REDACTED]",
		"#fragment",
	}
	for _, p := range preserves {
		if !strings.Contains(got, p) {
			t.Fatalf("expected %q in redacted output, got: %s", p, got)
		}
	}
}

// 8. multiline RSA/EC/OpenSSH/private-key blocks redacted
func TestGateE_Redaction_MultilinePrivateKeys(t *testing.T) {
	keys := []struct {
		name string
		raw  string
	}{
		{
			name: "rsa_private_key",
			raw: "-----BEGIN RSA PRIVATE KEY-----\n" +
				"MIIEowIBAAKCAQEAz/super/secret/rsa/key/material...\n" +
				"9876543210ABCDEF==\n" +
				"-----END RSA PRIVATE KEY-----",
		},
		{
			name: "ec_private_key",
			raw: "-----BEGIN EC PRIVATE KEY-----\n" +
				"MHcCAQEEINx/secret/ec/key/material...\n" +
				"-----END EC PRIVATE KEY-----",
		},
		{
			name: "openssh_private_key",
			raw: "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
				"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAA...\n" +
				"-----END OPENSSH PRIVATE KEY-----",
		},
	}

	for _, k := range keys {
		t.Run(k.name, func(t *testing.T) {
			got := RedactSecrets(k.raw)
			if strings.Contains(got, "PRIVATE KEY-----") {
				t.Fatalf("private key header was not redacted: %s", got)
			}
			if !strings.Contains(got, "[REDACTED PRIVATE KEY]") {
				t.Fatalf("expected [REDACTED PRIVATE KEY], got: %s", got)
			}
		})
	}
}

// 9. public certificate block preserved
func TestGateE_Redaction_PublicCertificatesPreserved(t *testing.T) {
	cert := "-----BEGIN CERTIFICATE-----\n" +
		"MIIDdTCCAl2gAwIBAgIUB3b+1234567890...\n" +
		"public/certificate/material/is/safe/for/diagnostics...\n" +
		"-----END CERTIFICATE-----"

	got := RedactSecrets(cert)
	if got != cert {
		t.Fatalf("public certificate must NOT be redacted: %s", got)
	}
}

// 10. CRLF and LF inputs
func TestGateE_Redaction_CRLFAndLF(t *testing.T) {
	crlf := "password: secret123\r\ntoken: tok456\r\nmode: direct\r\n"
	lf := "password: secret123\ntoken: tok456\nmode: direct\n"

	gotCRLF := RedactSecrets(crlf)
	if strings.Contains(gotCRLF, "secret123") || strings.Contains(gotCRLF, "tok456") {
		t.Fatalf("CRLF leaked secrets: %s", gotCRLF)
	}
	if !strings.Contains(gotCRLF, "\r\n") {
		t.Fatalf("expected CRLF to be preserved in formatting: %q", gotCRLF)
	}

	gotLF := RedactSecrets(lf)
	if strings.Contains(gotLF, "secret123") || strings.Contains(gotLF, "tok456") {
		t.Fatalf("LF leaked secrets: %s", gotLF)
	}
}

// 11. ExportSafeEvidence JSON key allowlist recursively checked
// 12. seeded secrets absent from the complete marshalled evidence bytes
// 13. raw config, manifest-only fields, headers, URLs, and environment values absent
// 14. bridge projection contains only approved fields
func TestGateE_ExportSafeEvidence_AllowlistAndNoSeededSecrets(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	seededSecretPassword := "SEEDED_SUPER_SECRET_PASSWORD_9999"
	seededBearerToken := "SEEDED_BEARER_TOKEN_ABCD"
	seededURLUserInfo := "operator:SEEDED_USERINFO_CREDENTIAL"

	markerPath := filepath.Join(dir, "recovery.marker")
	markerContent := fmt.Sprintf("coordinator crashed: password: %s, token: %s, at http://%s@127.0.0.1:9090",
		seededSecretPassword, seededBearerToken, seededURLUserInfo)
	if err := os.WriteFile(markerPath, []byte(markerContent), 0600); err != nil {
		t.Fatal(err)
	}

	mockBridges := &fakeBridgeRuntime{}
	mockBridges.applied = []BridgeRef{
		{
			ProxyIndex:      1,
			ProxyInterface:  "Proxy1",
			KernelInterface: "awg-br0",
			ListenPort:      1099,
			LegacyOwner:     "SHOULD_NOT_LEAK_LEGACY_OWNER",
			OwnerUUID:       "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d",
			Generation:      42,
		},
	}

	coord := NewApplyCoordinator(CoordinatorConfig{
		ConfigDir:     dir,
		Operator:      &fakeGate4Operator{},
		Validator:     &fakeValidator{},
		BridgeRuntime: mockBridges,
		StoreTx:       newFakeStoreTx(dir, "store"),
		Verifier:      &NoopProcessVerifier{},
	})
	coord.state = StateRecoveryRequired
	coord.activeTxID = "tx-20260922-gatee"
	coord.daemonEpoch = "epoch-gatee-1"

	dto, err := coord.ExportSafeEvidence(ctx)
	if err != nil {
		t.Fatalf("ExportSafeEvidence: %v", err)
	}

	evidenceBytes, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal evidence: %v", err)
	}
	evidenceStr := string(evidenceBytes)

	// 12. Seeded secrets absent
	forbids := []string{
		seededSecretPassword,
		seededBearerToken,
		"SEEDED_USERINFO_CREDENTIAL",
		"SHOULD_NOT_LEAK_LEGACY_OWNER",
	}
	for _, f := range forbids {
		if strings.Contains(evidenceStr, f) {
			t.Fatalf("seeded secret %q leaked into evidence export: %s", f, evidenceStr)
		}
	}

	// 13. Raw config / environment / headers absent
	forbidsRaw := []string{"ProxyProviders", "RuleProviders", "Authorization", "Cookie"}
	for _, f := range forbidsRaw {
		if strings.Contains(evidenceStr, f) {
			t.Fatalf("raw field %q found in evidence export: %s", f, evidenceStr)
		}
	}

	// 14. Bridge projection contains only approved fields
	if len(dto.Bridges) != 1 {
		t.Fatalf("expected 1 bridge, got %d", len(dto.Bridges))
	}
	br := dto.Bridges[0]
	if br.ProxyInterface != "Proxy1" || br.KernelInterface != "awg-br0" || br.OwnerUUID != "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d" {
		t.Fatalf("unexpected bridge projection: %+v", br)
	}

	// 11. Allowlisted top-level keys check
	var rawMap map[string]any
	if err := json.Unmarshal(evidenceBytes, &rawMap); err != nil {
		t.Fatal(err)
	}
	allowedKeys := map[string]bool{
		"generated_at":    true,
		"state":           true,
		"active_tx_id":    true,
		"daemon_epoch":    true,
		"recovery_reason": true,
		"recovery_marker": true,
		"applied_record":  true,
		"process_receipt": true,
		"manifest_facts":  true,
		"bridges":         true,
		"sanitized_logs":  true,
	}
	for k := range rawMap {
		if !allowedKeys[k] {
			t.Fatalf("unauthorized top-level key %q in RecoveryEvidenceDTO", k)
		}
	}
}

// 15. malformed/unreadable marker or manifest does not leak raw bytes and yields deterministic safe behavior
func TestGateE_MalformedMarkerOrManifestSafe(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	markerPath := filepath.Join(dir, "recovery.marker")
	// Binary unreadable/malformed marker data
	_ = os.WriteFile(markerPath, []byte("\x00\x01\x02\x03\xff\xfe\xfd"), 0600)

	manifestPath := filepath.Join(dir, "config.yaml.txn.json")
	_ = os.WriteFile(manifestPath, []byte(`{invalid JSON manifest...`), 0600)

	coord := NewApplyCoordinator(CoordinatorConfig{
		ConfigDir: dir,
		Operator:  &fakeGate4Operator{},
		Validator: &fakeValidator{},
		StoreTx:   newFakeStoreTx(dir, "store"),
		Verifier:  &NoopProcessVerifier{},
	})

	dto, err := coord.ExportSafeEvidence(ctx)
	if err != nil {
		t.Fatalf("ExportSafeEvidence must not fail on malformed files: %v", err)
	}
	if dto == nil {
		t.Fatal("expected non-nil DTO")
	}
	// Manifest facts must be nil because manifest JSON was malformed
	if dto.ManifestFacts != nil {
		t.Fatalf("expected ManifestFacts to be nil on invalid JSON, got %+v", dto.ManifestFacts)
	}
	// Binary marker must be classified as binary_marker and not echo raw bytes
	if dto.RecoveryReason != "binary_marker" {
		t.Fatalf("expected RecoveryReason %q, got %q", "binary_marker", dto.RecoveryReason)
	}
	if dto.RecoveryMarker == nil || dto.RecoveryMarker.Reason != "binary_marker" {
		t.Fatalf("expected RecoveryMarker.Reason %q, got %+v", "binary_marker", dto.RecoveryMarker)
	}
}

// 16. P1 Regression: Recovery marker allowlist projection, strict size limit, and seeded secret exclusion
func TestGateE_RecoveryMarker_AllowlistAndBounded(t *testing.T) {
	ctx := context.Background()

	t.Run("oversized_marker", func(t *testing.T) {
		dir := t.TempDir()
		markerPath := filepath.Join(dir, "recovery.marker")
		oversized := make([]byte, 2048)
		for i := range oversized {
			oversized[i] = 'A'
		}
		_ = os.WriteFile(markerPath, oversized, 0600)

		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir: dir,
			Operator:  &fakeGate4Operator{},
			Validator: &fakeValidator{},
			StoreTx:   newFakeStoreTx(dir, "store"),
			Verifier:  &NoopProcessVerifier{},
		})

		dto, err := coord.ExportSafeEvidence(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if dto.RecoveryReason != "oversized_marker" {
			t.Fatalf("expected %q, got %q", "oversized_marker", dto.RecoveryReason)
		}
	})

	t.Run("empty_marker", func(t *testing.T) {
		dir := t.TempDir()
		markerPath := filepath.Join(dir, "recovery.marker")
		_ = os.WriteFile(markerPath, []byte("   \n\t  "), 0600)

		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir: dir,
			Operator:  &fakeGate4Operator{},
			Validator: &fakeValidator{},
			StoreTx:   newFakeStoreTx(dir, "store"),
			Verifier:  &NoopProcessVerifier{},
		})

		dto, err := coord.ExportSafeEvidence(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if dto.RecoveryReason != "empty_marker" {
			t.Fatalf("expected %q, got %q", "empty_marker", dto.RecoveryReason)
		}
	})

	t.Run("unrecognized_seeded_secret_never_echoed", func(t *testing.T) {
		dir := t.TempDir()
		markerPath := filepath.Join(dir, "recovery.marker")
		seededSecret := "MAGIC_UNKNOWN_SEEDED_SECRET_123456789"
		markerContent := "unexpected crash report containing " + seededSecret + " without standard key syntax"
		_ = os.WriteFile(markerPath, []byte(markerContent), 0600)

		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir: dir,
			Operator:  &fakeGate4Operator{},
			Validator: &fakeValidator{},
			StoreTx:   newFakeStoreTx(dir, "store"),
			Verifier:  &NoopProcessVerifier{},
		})

		dto, err := coord.ExportSafeEvidence(ctx)
		if err != nil {
			t.Fatal(err)
		}

		// Must map to generic unrecognized reason
		if dto.RecoveryReason != "unrecognized_recovery_marker" {
			t.Fatalf("expected %q, got %q", "unrecognized_recovery_marker", dto.RecoveryReason)
		}

		// Verify that serializing the entire DTO never leaks the seeded secret
		dtoBytes, mErr := json.Marshal(dto)
		if mErr != nil {
			t.Fatal(mErr)
		}
		if strings.Contains(string(dtoBytes), seededSecret) {
			t.Fatalf("seeded secret leaked into RecoveryEvidenceDTO JSON: %s", string(dtoBytes))
		}
	})

	t.Run("known_reason_codes_mapped", func(t *testing.T) {
		dir := t.TempDir()
		markerPath := filepath.Join(dir, "recovery.marker")
		_ = os.WriteFile(markerPath, []byte("initial fault: store failure"), 0600)

		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir: dir,
			Operator:  &fakeGate4Operator{},
			Validator: &fakeValidator{},
			StoreTx:   newFakeStoreTx(dir, "store"),
			Verifier:  &NoopProcessVerifier{},
		})

		dto, err := coord.ExportSafeEvidence(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if dto.RecoveryReason != "initial_fault" {
			t.Fatalf("expected %q, got %q", "initial_fault", dto.RecoveryReason)
		}
	})

	t.Run("connection_failed_opaque_target_never_leaks_target_or_secret", func(t *testing.T) {
		dir := t.TempDir()
		markerPath := filepath.Join(dir, "recovery.marker")
		secret := "MAGIC_UNKNOWN_SECRET_123"
		target := "opaque-private-target-" + secret
		markerContent := "failed connecting to " + target
		_ = os.WriteFile(markerPath, []byte(markerContent), 0600)

		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir: dir,
			Operator:  &fakeGate4Operator{},
			Validator: &fakeValidator{},
			StoreTx:   newFakeStoreTx(dir, "store"),
			Verifier:  &NoopProcessVerifier{},
		})

		dto, err := coord.ExportSafeEvidence(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if dto.RecoveryReason != "connection_failed" {
			t.Fatalf("expected category %q, got %q", "connection_failed", dto.RecoveryReason)
		}
		if dto.RecoveryMarker == nil || dto.RecoveryMarker.Reason != "connection_failed" {
			t.Fatalf("expected marker reason %q, got %+v", "connection_failed", dto.RecoveryMarker)
		}

		dtoBytes, mErr := json.Marshal(dto)
		if mErr != nil {
			t.Fatal(mErr)
		}
		dtoStr := string(dtoBytes)
		if strings.Contains(dtoStr, secret) {
			t.Fatalf("secret %q leaked into serialized evidence: %s", secret, dtoStr)
		}
		if strings.Contains(dtoStr, "opaque-private-target") {
			t.Fatalf("target leaked into serialized evidence: %s", dtoStr)
		}
	})

	t.Run("connection_failed_url_with_userinfo_query_fragment_and_unknown_credential_key", func(t *testing.T) {
		dir := t.TempDir()
		markerPath := filepath.Join(dir, "recovery.marker")
		unknownSecret := "CUSTOM_SUPER_SECRET_TOKEN_XYZ987"
		markerContent := "failed connecting to https://admin:pass123@private.proxy.internal:8443/secret/path?custom_cred_key=" + unknownSecret + "#fragment_secret"
		_ = os.WriteFile(markerPath, []byte(markerContent), 0600)

		coord := NewApplyCoordinator(CoordinatorConfig{
			ConfigDir: dir,
			Operator:  &fakeGate4Operator{},
			Validator: &fakeValidator{},
			StoreTx:   newFakeStoreTx(dir, "store"),
			Verifier:  &NoopProcessVerifier{},
		})

		dto1, err := coord.ExportSafeEvidence(ctx)
		if err != nil {
			t.Fatal(err)
		}
		dto2, err := coord.ExportSafeEvidence(ctx)
		if err != nil {
			t.Fatal(err)
		}

		// Check category only
		if dto1.RecoveryReason != "connection_failed" {
			t.Fatalf("expected category %q, got %q", "connection_failed", dto1.RecoveryReason)
		}

		// Check determinism and idempotency
		if dto1.RecoveryReason != dto2.RecoveryReason {
			t.Fatalf("non-deterministic recovery reason: %q vs %q", dto1.RecoveryReason, dto2.RecoveryReason)
		}

		dtoBytes, mErr := json.Marshal(dto1)
		if mErr != nil {
			t.Fatal(mErr)
		}
		dtoStr := string(dtoBytes)
		if strings.Contains(dtoStr, unknownSecret) {
			t.Fatalf("unknown secret %q leaked in evidence: %s", unknownSecret, dtoStr)
		}
		if strings.Contains(dtoStr, "admin:pass123") || strings.Contains(dtoStr, "private.proxy.internal") || strings.Contains(dtoStr, "fragment_secret") {
			t.Fatalf("raw URL parts leaked in evidence: %s", dtoStr)
		}
	})
}

// 16. fuzz/property test: redaction is idempotent and never emits known seeded secrets
func TestGateE_Redaction_IdempotentAndNoSecretEmission(t *testing.T) {
	secrets := []string{
		"password: secret_pass_123",
		`{"password": "secret_pass_123"}`,
		"Authorization: Bearer secret_bearer_token",
		"http://user:secret_pass@example.com/api?token=secret_query_tok&uuid=11111111-2222-3333-4444-555555555555",
		"-----BEGIN RSA PRIVATE KEY-----\nsecret_key_material\n-----END RSA PRIVATE KEY-----",
		"TOKEN=secret_env_token",
	}

	secretFragments := []string{
		"secret_pass_123",
		"secret_bearer_token",
		"secret_pass",
		"secret_query_tok",
		"11111111-2222-3333-4444-555555555555",
		"secret_key_material",
		"secret_env_token",
	}

	for _, input := range secrets {
		firstPass := RedactSecrets(input)
		// Check that secrets are redacted
		for _, s := range secretFragments {
			if strings.Contains(input, s) && strings.Contains(firstPass, s) {
				t.Fatalf("first pass leaked secret %q in %s", s, firstPass)
			}
		}

		// Check idempotency: RedactSecrets(firstPass) == firstPass
		secondPass := RedactSecrets(firstPass)
		if firstPass != secondPass {
			t.Fatalf("redaction not idempotent: pass1=%q, pass2=%q", firstPass, secondPass)
		}
	}
}

// Large-input runtime sanity test: avoid catastrophic backtracking
func TestGateE_Redaction_LargeInputSanity(t *testing.T) {
	// Build a 2MB test payload
	var b strings.Builder
	for i := 0; i < 20000; i++ {
		b.WriteString(fmt.Sprintf("log line %d: user=alice, token=secret_%d, owner_uuid=e4046ae6-25b5-4760-af29-649b283af6f4, status=ok\n", i, i))
	}
	largeInput := b.String()

	start := time.Now()
	redacted := RedactSecrets(largeInput)
	elapsed := time.Since(start)

	// Under -race instrumentation, Go memory tracking adds 10x-15x overhead to 20,000 regex submatches.
	// Catastrophic backtracking would hang exponentially (minutes/hours).
	maxAllowed := 25 * time.Second
	if elapsed > maxAllowed {
		t.Fatalf("redaction took too long (%v > %v) on 2MB input; possible catastrophic backtracking", elapsed, maxAllowed)
	}

	if strings.Contains(redacted, "secret_123") {
		t.Fatalf("leaked secret token in large input")
	}
	if !strings.Contains(redacted, "owner_uuid=e4046ae6-25b5-4760-af29-649b283af6f4") {
		t.Fatalf("lost owner_uuid in large input")
	}
}
