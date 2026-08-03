package config

import (
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestValidateAWG3(t *testing.T) {
	tests := []struct {
		name    string
		obf     storage.AWGObfuscation
		wantErr string
	}{
		{
			name: "no header protection — S values are irrelevant",
			obf:  storage.AWGObfuscation{S1: 0, S2: 0, S3: 0, S4: 0},
		},
		{
			name: "header protection with S1-S4 at the nonce size",
			obf: storage.AWGObfuscation{
				HeaderProtectionKey: "ceaFRw/bcsvnr9I5fmZqPY1HTuUhhdkYjSxgUH0ixRc=",
				S1:                  12, S2: 12, S3: 12, S4: 12,
			},
		},
		{
			name: "header protection with no S values at all — the silent-dead-tunnel case",
			obf: storage.AWGObfuscation{
				HeaderProtectionKey: "ceaFRw/bcsvnr9I5fmZqPY1HTuUhhdkYjSxgUH0ixRc=",
			},
			wantErr: "S1 = 0",
		},
		{
			name: "header protection with only S4 too small",
			obf: storage.AWGObfuscation{
				HeaderProtectionKey: "ceaFRw/bcsvnr9I5fmZqPY1HTuUhhdkYjSxgUH0ixRc=",
				S1:                  87, S2: 118, S3: 36, S4: 11,
			},
			wantErr: "S4 = 11",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAWG3(&tt.obf)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

// awg showconf prints "0" for every unset AWG 3.0 range; re-importing that
// output must not turn an AWG 2.0 tunnel into an awg3-looking one.
func TestParseIgnoresZeroAWG3Ranges(t *testing.T) {
	conf := `[Interface]
PrivateKey = GF+8XleAkOGCOSOX0yRC5duoeI0fY12VSTtZ1sJ3uXk=
Address = 10.0.0.2/32
ContentPaddingAddition = 0
RekeyAfterTime = 0
RekeyTimeout = 0
RejectAfterTime = 0
KeepaliveTimeout = 0
MaxHandshakeAttempts = 0

[Peer]
PublicKey = t/y0HtImYhv3EIA6gWX4pvKdHVSBFVbSfX1ldOMlXl4=
AllowedIPs = 0.0.0.0/0
Endpoint = 192.0.2.1:51820
`
	tunnel, err := Parse(conf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	iface := tunnel.Interface
	if iface.ContentPaddingAddition != "" || iface.RekeyAfterTime != "" ||
		iface.RekeyTimeout != "" || iface.RejectAfterTime != "" ||
		iface.KeepaliveTimeout != "" || iface.MaxHandshakeAttempts != "" {
		t.Fatalf("zero ranges must parse as unset, got %+v", iface.AWGObfuscation)
	}

	ranged, err := Parse(strings.Replace(conf, "ContentPaddingAddition = 0", "ContentPaddingAddition = 0-80", 1))
	if err != nil {
		t.Fatalf("parse ranged: %v", err)
	}
	if ranged.Interface.ContentPaddingAddition != "0-80" {
		t.Fatalf("range starting at zero must survive, got %q", ranged.Interface.ContentPaddingAddition)
	}
}
