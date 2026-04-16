package dnsroute

import (
	"reflect"
	"testing"
)

func TestSplitDomainsAndSubnets(t *testing.T) {
	tests := []struct {
		name        string
		input       []string
		wantDomains []string
		wantSubnets []string
	}{
		{
			name:        "only domains",
			input:       []string{"google.com", "youtube.com"},
			wantDomains: []string{"google.com", "youtube.com"},
			wantSubnets: nil,
		},
		{
			name:        "only cidrs",
			input:       []string{"10.0.0.0/8", "192.168.1.0/24"},
			wantDomains: nil,
			wantSubnets: []string{"10.0.0.0/8", "192.168.1.0/24"},
		},
		{
			name:        "mixed",
			input:       []string{"google.com", "10.10.0.1/32", "youtube.com", "2001:db8::/32"},
			wantDomains: []string{"google.com", "youtube.com"},
			wantSubnets: []string{"10.10.0.1/32", "2001:db8::/32"},
		},
		{
			name:        "geosite tag stays in domains",
			input:       []string{"geosite:GOOGLE", "google.com"},
			wantDomains: []string{"geosite:GOOGLE", "google.com"},
			wantSubnets: nil,
		},
		{
			name:        "geoip tag goes to subnets",
			input:       []string{"geoip:RU", "5.8.0.0/21"},
			wantDomains: nil,
			wantSubnets: []string{"geoip:RU", "5.8.0.0/21"},
		},
		{
			name:        "bare IP without mask treated as domain",
			input:       []string{"10.0.0.1"},
			wantDomains: []string{"10.0.0.1"},
			wantSubnets: nil,
		},
		{
			name:        "empty",
			input:       nil,
			wantDomains: nil,
			wantSubnets: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotDomains, gotSubnets := splitDomainsAndSubnets(tt.input)
			if !reflect.DeepEqual(gotDomains, tt.wantDomains) {
				t.Errorf("domains: got %v, want %v", gotDomains, tt.wantDomains)
			}
			if !reflect.DeepEqual(gotSubnets, tt.wantSubnets) {
				t.Errorf("subnets: got %v, want %v", gotSubnets, tt.wantSubnets)
			}
		})
	}
}
