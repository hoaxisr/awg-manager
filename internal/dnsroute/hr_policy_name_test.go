package dnsroute

import (
	"strings"
	"testing"
)

func TestValidateHRPolicyName(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr string // empty = expect nil
	}{
		{"empty", "", "empty"},
		{"too long", strings.Repeat("a", 33), "too long"},
		{"at max length", strings.Repeat("a", 32), ""},

		{"system Policy0", "Policy0", "reserved for system"},
		{"system Policy12", "Policy12", "reserved for system"},
		{"system Policy999", "Policy999", "reserved for system"},

		{"valid latin", "HydraRoute", ""},
		{"valid lowercase", "streaming", ""},
		{"valid mixed case", "VpnWork", ""},

		{"digit rejected", "Route1", "only latin letters"},
		{"hyphen rejected", "my-policy", "only latin letters"},
		{"underscore rejected", "my_policy", "only latin letters"},
		{"space rejected", "My Policy", "only latin letters"},
		{"dot rejected", "my.policy", "only latin letters"},
		{"cyrillic rejected", "Политика", "only latin letters"},
		{"emoji rejected", "policy🚀", "only latin letters"},

		// "Policy" on its own (no digit) is NOT system — rejected only by the
		// general "must not contain digits" rule if digit, or accepted if plain
		// latin (though confusing). Accept — not our job to ban all collisions.
		{"plain 'Policy' (no digit) accepted", "Policy", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateHRPolicyName(tc.input)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}
