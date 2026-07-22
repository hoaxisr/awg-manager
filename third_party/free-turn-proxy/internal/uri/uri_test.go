package uri

import (
	"reflect"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
	}{
		{
			name: "full",
			cfg: &Config{
				Version:        currentVersion,
				Provider:       "vk",
				Peer:           "127.0.0.1:56000",
				Transport:      "udp",
				Mode:           "tcp",
				Bond:           true,
				ObfProfile:     "rtpopus",
				ObfKey:         "d823fa",
				N:              16,
				StreamsPerCred: 8,
				ClientID:       "abc123",
				Listen:         "127.0.0.1:9000",
				DNSMode:        "doh",
				DNSServers:     "1.1.1.1,8.8.8.8",
				ManualCaptcha:  true,
				Comment:        "MyServer",
			},
		},
		{
			name: "minimal",
			cfg: &Config{
				Version:  currentVersion,
				Provider: "vk",
				Peer:     "1.2.3.4:56000",
			},
		},
		{
			name: "unicode comment",
			cfg: &Config{
				Version:  currentVersion,
				Provider: "vk",
				Peer:     "1.2.3.4:56000",
				Mode:     "udp",
				Comment:  "Сервер РФ",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.cfg.String())
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.cfg) {
				t.Errorf("round-trip mismatch\n got = %+v\nwant = %+v", got, tt.cfg)
			}
		})
	}
}

func TestObfNoneOmitted(t *testing.T) {
	cfg := &Config{
		Version:    currentVersion,
		Provider:   "vk",
		Peer:       "1.2.3.4:56000",
		ObfProfile: "none",
		ObfKey:     "ignored",
	}
	got, err := Parse(cfg.String())
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got.ObfProfile != "" || got.ObfKey != "" {
		t.Errorf("obf none must be omitted, got profile=%q key=%q", got.ObfProfile, got.ObfKey)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"bad scheme", "http://vk"},
		{"empty payload", "freeturn://"},
		{"bad base64", "freeturn://!!!notbase64!!!"},
		{"bad json", "freeturn://Zm9vYmFy"}, // base64("foobar")
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse(tt.input); err == nil {
				t.Errorf("Parse(%q) expected error, got nil", tt.input)
			}
		})
	}
}
